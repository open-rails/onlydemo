package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST_DATABASE_URL selects a PostgreSQL server where the test user can create
// databases. Every run creates and removes its own database; the database named
// in the connection string and its contents are never reset.
func TestBlogPostsIntegration(t *testing.T) {
	pool := newBlogTestDatabase(t)
	for pass := 1; pass <= 2; pass++ {
		if err := applyMigrations(t.Context(), pool); err != nil {
			t.Fatalf("apply embedded migrations pass %d: %v", pass, err)
		}
	}
	var placed bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('profiles.users') IS NOT NULL AND to_regclass('public.users') IS NULL`).Scan(&placed); err != nil || !placed {
		t.Fatalf("AuthKit namespace placement = %v, err=%v", placed, err)
	}
	var sequenceType string
	if err := pool.QueryRow(t.Context(), `SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='migrations' AND column_name='sequence'`).Scan(&sequenceType); err != nil || sequenceType != "bigint" {
		t.Fatalf("ledger sequence type = %q, err=%v", sequenceType, err)
	}
	service, err := newAuth(Config{
		AuthIssuer:   "http://localhost:3000",
		AuthAudience: "openrails-demo",
	}, pool)
	if err != nil {
		t.Fatalf("initialize AuthKit: %v", err)
	}
	t.Cleanup(func() { service.Close() })
	fiberApp, err := newApp(pool, service)
	if err != nil {
		t.Fatalf("mount AuthKit: %v", err)
	}
	app := newBlogTestServer(t, fiberApp)

	t.Run("homepage route directory", func(t *testing.T) {
		home := string(blogTestRequest(t, app, http.MethodGet, "/", "", nil, http.StatusOK))
		registered := make(map[string]bool)
		for _, route := range fiberApp.GetRoutes(true) {
			registered[route.Method+" "+route.Path] = true
		}
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/"},
			{http.MethodGet, "/health"},
			{http.MethodPost, "/api/posts"},
			{http.MethodDelete, "/api/posts/:id"},
			{http.MethodPost, "/api/v1/register"},
			{http.MethodPost, "/api/v1/password/login"},
			{http.MethodGet, "/api/v1/admin/users"},
			{http.MethodDelete, "/api/v1/user/sessions/:id"},
			{http.MethodGet, "/.well-known/jwks.json"},
			{http.MethodHead, "/.well-known/jwks.json"},
		} {
			if !registered[route.method+" "+route.path] {
				t.Errorf("missing native Fiber route %s %s", route.method, route.path)
			}
			if !strings.Contains(home, fmt.Sprintf(`data-method="%s" data-path="%s"`, route.method, route.path)) {
				t.Errorf("homepage missing %s %s", route.method, route.path)
			}
		}
		for _, path := range []string{"/api/v1/user/2fa", "/api/v1/delegated/token", "/api/v1/device-keys", "/*"} {
			if strings.Contains(home, fmt.Sprintf(`data-path="%s"`, path)) {
				t.Errorf("homepage listed disabled route or middleware catch-all %s", path)
			}
		}
		if !strings.Contains(home, "Application routes") || !strings.Contains(home, "AuthKit routes") {
			t.Error("homepage missing route origins")
		}
		// The directory advertises these paths without bypassing their guards.
		blogTestRequest(t, app, http.MethodGet, "/api/v1/admin/users", "", nil, http.StatusUnauthorized)
		blogTestRequest(t, app, http.MethodDelete, "/api/v1/user/sessions/example-id", "", nil, http.StatusUnauthorized)
	})

	alice := registerBlogTestUser(t, app, pool, "writeralice")
	// A second real peer gets its own registration rate-limit bucket.
	bobApp := blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.3")}
	bob := registerBlogTestUser(t, &bobApp, pool, "writerbob")

	private := createBlogTestPost(t, app, alice.token, map[string]any{
		"slug": "alice-private", "title": "Private draft", "body": "Alice's private body",
		"owner_id": bob.id,
	})
	if private.Visibility != "private" {
		t.Fatalf("omitted visibility = %q, want private", private.Visibility)
	}
	assertBlogPostOwner(t, pool, private.ID, alice.id)

	public := createBlogTestPost(t, app, alice.token, map[string]any{
		"slug": "alice-public", "title": "Public post", "body": "Alice's public body", "visibility": "public",
	})
	bobPrivate := createBlogTestPost(t, app, bob.token, map[string]any{
		"slug": "bob-private", "title": "Bob's draft", "body": "Bob's private body",
	})
	bobPublic := createBlogTestPost(t, app, bob.token, map[string]any{
		"slug": "bob-public", "title": "Bob's public post", "body": "Bob's public body", "visibility": "public",
	})

	assertBlogPostIDs(t, app, "", public.ID, bobPublic.ID)
	assertBlogPostIDs(t, app, alice.token, private.ID, public.ID, bobPublic.ID)
	assertBlogPostIDs(t, app, bob.token, public.ID, bobPrivate.ID, bobPublic.ID)

	for _, post := range []blogPost{public, bobPublic} {
		for _, token := range []string{"", alice.token, bob.token} {
			body := blogTestRequest(t, app, http.MethodGet, blogTestPath(post.ID), token, nil, http.StatusOK)
			var got blogPost
			decodeBlogTestJSON(t, body, &got)
			if got.ID != post.ID || got.Body != post.Body {
				t.Fatalf("public fetch = %+v, want post %d with its public body", got, post.ID)
			}
		}
	}

	for _, tc := range []struct {
		name  string
		post  blogPost
		owner string
		other string
	}{
		{"alice", private, alice.token, bob.token},
		{"bob", bobPrivate, bob.token, alice.token},
	} {
		t.Run(tc.name+" private access", func(t *testing.T) {
			path := blogTestPath(tc.post.ID)
			body := blogTestRequest(t, app, http.MethodGet, path, tc.owner, nil, http.StatusOK)
			var got blogPost
			decodeBlogTestJSON(t, body, &got)
			if got.Body != tc.post.Body {
				t.Fatalf("owner received body %q, want %q", got.Body, tc.post.Body)
			}
			blogTestRequest(t, app, http.MethodGet, path, "", nil, http.StatusNotFound)
			blogTestRequest(t, app, http.MethodGet, path, tc.other, nil, http.StatusNotFound)
			blogTestRequest(t, app, http.MethodPatch, path, tc.other, map[string]any{"title": "Unauthorized edit"}, http.StatusNotFound)
			blogTestRequest(t, app, http.MethodDelete, path, tc.other, nil, http.StatusNotFound)
		})
	}

	// Public visibility permits reading, never changing somebody else's post.
	blogTestRequest(t, app, http.MethodPatch, blogTestPath(public.ID), bob.token, map[string]any{"title": "Unauthorized edit"}, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodDelete, blogTestPath(public.ID), bob.token, nil, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodPost, "/api/posts", "", map[string]any{"slug": "anonymous", "title": "Anonymous", "body": "No owner"}, http.StatusUnauthorized)
	blogTestRequest(t, app, http.MethodPatch, blogTestPath(public.ID), "", map[string]any{"title": "Anonymous edit"}, http.StatusUnauthorized)
	blogTestRequest(t, app, http.MethodDelete, blogTestPath(public.ID), "", nil, http.StatusUnauthorized)

	// Optional authentication must reject an invalid supplied credential, even
	// when the same read would be allowed without an Authorization header.
	blogTestRequest(t, app, http.MethodGet, "/api/posts", "not-a-token", nil, http.StatusUnauthorized)
	blogTestRequest(t, app, http.MethodGet, blogTestPath(public.ID), "not-a-token", nil, http.StatusUnauthorized)

	updatedBody := blogTestRequest(t, app, http.MethodPatch, blogTestPath(private.ID), alice.token, map[string]any{
		"title": "Edited draft", "body": "Edited private body", "owner_id": bob.id,
	}, http.StatusOK)
	var updated blogPost
	decodeBlogTestJSON(t, updatedBody, &updated)
	if updated.Title != "Edited draft" || updated.Body != "Edited private body" || updated.Visibility != "private" || updated.Slug != private.Slug {
		t.Fatalf("partial update returned unexpected post: %+v", updated)
	}
	assertBlogPostOwner(t, pool, private.ID, alice.id)
	blogTestRequest(t, app, http.MethodGet, blogTestPath(private.ID), bob.token, nil, http.StatusNotFound)

	blogTestRequest(t, app, http.MethodPatch, blogTestPath(private.ID), alice.token, map[string]any{"visibility": "public"}, http.StatusOK)
	blogTestRequest(t, app, http.MethodGet, blogTestPath(private.ID), "", nil, http.StatusOK)
	assertBlogPostIDs(t, app, "", private.ID, public.ID, bobPublic.ID)

	for _, post := range []blogPost{private, public} {
		blogTestRequest(t, app, http.MethodDelete, blogTestPath(post.ID), alice.token, nil, http.StatusNoContent)
		blogTestRequest(t, app, http.MethodGet, blogTestPath(post.ID), alice.token, nil, http.StatusNotFound)
	}
	blogTestRequest(t, app, http.MethodDelete, blogTestPath(bobPrivate.ID), bob.token, nil, http.StatusNoContent)
	assertBlogPostIDs(t, app, bob.token, bobPublic.ID)
}

func newBlogTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	admin, err := pgx.ConnectConfig(t.Context(), config.ConnConfig.Copy())
	if err != nil {
		t.Fatalf("connect to test PostgreSQL server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close test database administrator connection: %v", err)
		}
	})
	databaseName := "openrails_demo_test_" + strings.ToLower(rand.Text())
	quotedName := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+quotedName); err != nil {
		t.Fatalf("create isolated test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+quotedName+" WITH (FORCE)"); err != nil {
			t.Errorf("drop isolated test database %s: %v", databaseName, err)
		}
	})
	config.ConnConfig.Database = databaseName
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("create isolated test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type blogTestUser struct {
	id    string
	token string
}

func registerBlogTestUser(t *testing.T, app *blogTestServer, pool *pgxpool.Pool, username string) blogTestUser {
	t.Helper()
	email := username + "@example.com"
	const password = "Local-demo-test-password-42!"
	body := blogTestRequest(t, app, http.MethodPost, "/api/v1/register", "", map[string]any{
		"identifier": email, "username": username, "password": password,
	}, http.StatusAccepted)
	var registration struct {
		TokenSet struct {
			AccessToken string `json:"access_token"`
		} `json:"token_set"`
	}
	decodeBlogTestJSON(t, body, &registration)
	if registration.TokenSet.AccessToken == "" {
		t.Fatal("registration returned no access token")
	}
	blogTestRequest(t, app, http.MethodGet, "/api/posts", registration.TokenSet.AccessToken, nil, http.StatusOK)

	body = blogTestRequest(t, app, http.MethodPost, "/api/v1/password/login", "", map[string]any{
		"identifier": email, "password": password,
	}, http.StatusOK)
	var login struct {
		AccessToken string `json:"access_token"`
	}
	decodeBlogTestJSON(t, body, &login)
	if login.AccessToken == "" {
		t.Fatal("password login returned no access token")
	}
	user := blogTestUser{token: login.AccessToken}
	if err := pool.QueryRow(t.Context(), "SELECT id::text FROM profiles.users WHERE email = $1", email).Scan(&user.id); err != nil {
		t.Fatalf("find registered user: %v", err)
	}
	return user
}

func createBlogTestPost(t *testing.T, app *blogTestServer, token string, input map[string]any) blogPost {
	t.Helper()
	body := blogTestRequest(t, app, http.MethodPost, "/api/posts", token, input, http.StatusCreated)
	var post blogPost
	decodeBlogTestJSON(t, body, &post)
	if post.ID == 0 {
		t.Fatal("created post has no ID")
	}
	return post
}

func assertBlogPostOwner(t *testing.T, pool *pgxpool.Pool, postID int64, expected string) {
	t.Helper()
	var owner string
	if err := pool.QueryRow(t.Context(), "SELECT owner_id::text FROM blog_posts WHERE id = $1", postID).Scan(&owner); err != nil {
		t.Fatalf("read post owner: %v", err)
	}
	if owner != expected {
		t.Fatalf("post %d owner = %q, want %q", postID, owner, expected)
	}
}

func assertBlogPostIDs(t *testing.T, app *blogTestServer, token string, expected ...int64) {
	t.Helper()
	body := blogTestRequest(t, app, http.MethodGet, "/api/posts", token, nil, http.StatusOK)
	var posts []blogPost
	decodeBlogTestJSON(t, body, &posts)
	actual := make([]int64, 0, len(posts))
	for _, post := range posts {
		actual = append(actual, post.ID)
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("visible posts = %v, want %v", actual, expected)
	}
}

func blogTestPath(id int64) string { return fmt.Sprintf("/api/posts/%d", id) }

func blogTestRequest(t *testing.T, app *blogTestServer, method, path, token string, input any, status int) []byte {
	t.Helper()
	var encoded []byte
	if input != nil {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, app.url+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, resp.StatusCode, status, body)
	}
	return body
}

func decodeBlogTestJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

type blogTestServer struct {
	url    string
	client *http.Client
}

func newBlogTestServer(t *testing.T, app *fiber.App) *blogTestServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for integration requests: %v", err)
	}
	stopped := make(chan error, 1)
	go func() {
		stopped <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true})
	}()
	t.Cleanup(func() {
		if err := app.Shutdown(); err != nil {
			t.Errorf("shut down test API: %v", err)
		}
		if err := <-stopped; err != nil {
			t.Errorf("serve test API: %v", err)
		}
	})
	return &blogTestServer{
		url:    "http://" + listener.Addr().String(),
		client: newBlogTestHTTPClient(t, "127.0.0.2"),
	}
}

func newBlogTestHTTPClient(t *testing.T, peerIP string) *http.Client {
	t.Helper()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(peerIP)}}
	transport := &http.Transport{DialContext: dialer.DialContext}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}
