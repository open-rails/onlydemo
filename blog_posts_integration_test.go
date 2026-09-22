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

const blogPostsTable = "demo.blog_posts"

// TEST_DATABASE_URL selects a PostgreSQL server where the test user can create
// databases. Every run creates and removes its own database; the database named
// in the connection string and its contents are never reset.
func TestBlogPostsIntegration(t *testing.T) {
	migrationPool := newBlogTestDatabase(t)
	config := Config{AuthIssuer: "http://localhost:3000", AuthAudience: "openrails-demo"}
	pool, databaseURL := newBlogTestOwnerPool(t, migrationPool, 0)
	config.DatabaseURL = databaseURL
	for pass := 1; pass <= 2; pass++ {
		if err := initializeDatabase(t.Context(), config, pool); err != nil {
			t.Fatalf("apply embedded migrations pass %d: %v", pass, err)
		}
	}
	assertBlogTestOwnerRole(t, pool)
	migrationPool.Close()
	var placed bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('demo.blog_posts') IS NOT NULL AND to_regclass('public.blog_posts') IS NULL`).Scan(&placed); err != nil || !placed {
		t.Fatalf("application namespace placement = %v, err=%v", placed, err)
	}
	service, err := newAuth(t.Context(), config, pool)
	if err != nil {
		t.Fatalf("initialize AuthKit: %v", err)
	}
	t.Cleanup(func() {
		service.Close()
		if err := pool.Ping(context.Background()); err != nil {
			t.Errorf("AuthKit closed the borrowed host pool: %v", err)
		}
	})
	jobs := newJobs(pool, config)
	jobs.auth = service
	t.Cleanup(func() {
		if err := jobs.close(context.Background()); err != nil {
			t.Errorf("stop AuthKit-only host jobs: %v", err)
		}
	})
	if err := jobs.start(t.Context()); err != nil {
		t.Fatalf("start AuthKit-only host jobs: %v", err)
	}
	fiberApp, err := newApp(pool, service, nil, config)
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

	alice := registerBlogTestUser(t, app, service, "writeralice")
	// A second real peer gets its own registration rate-limit bucket.
	bobApp := blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.3")}
	bob := registerBlogTestUser(t, &bobApp, service, "writerbob")
	adminApp := blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.4")}
	admin := registerBlogTestUser(t, &adminApp, service, "moderator")
	if err := service.grantAdmin(t.Context(), admin.id); err != nil {
		t.Fatalf("grant AuthKit admin role: %v", err)
	}

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
	assertBlogPostIDs(t, app, admin.token, private.ID, public.ID, bobPrivate.ID, bobPublic.ID)

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
	t.Run("AuthKit admin permissions", func(t *testing.T) {
		moderated := createBlogTestPost(t, app, alice.token, map[string]any{
			"slug": "admin-moderation", "title": "Moderated draft", "body": "Private moderation target",
		})
		// The token predates the grant: authority comes from AuthKit's live
		// permission system rather than a role claim copied into a JWT.
		blogTestRequest(t, app, http.MethodGet, blogTestPath(moderated.ID), admin.token, nil, http.StatusOK)
		body := blogTestRequest(t, app, http.MethodPatch, blogTestPath(moderated.ID), admin.token,
			map[string]any{"title": "Moderated title", "owner_id": admin.id}, http.StatusOK)
		var edited blogPost
		decodeBlogTestJSON(t, body, &edited)
		if edited.Title != "Moderated title" {
			t.Fatalf("admin edit returned title %q", edited.Title)
		}
		assertBlogPostOwner(t, pool, moderated.ID, alice.id)
		if err := service.revokeAdmin(t.Context(), admin.id); err != nil {
			t.Fatalf("revoke AuthKit admin role: %v", err)
		}
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(moderated.ID), admin.token,
			map[string]any{"title": "Revoked admin edit"}, http.StatusNotFound)
		blogTestRequest(t, app, http.MethodDelete, blogTestPath(moderated.ID), admin.token, nil, http.StatusNotFound)
		blogTestRequest(t, app, http.MethodGet, blogTestPath(moderated.ID), admin.token, nil, http.StatusNotFound)
		if err := service.grantAdmin(t.Context(), admin.id); err != nil {
			t.Fatalf("restore AuthKit admin role: %v", err)
		}
		blogTestRequest(t, app, http.MethodDelete, blogTestPath(moderated.ID), admin.token, nil, http.StatusNoContent)
		blogTestRequest(t, app, http.MethodGet, blogTestPath(moderated.ID), alice.token, nil, http.StatusNotFound)

		if err := service.client.BanUser(t.Context(), admin.id, nil, nil, admin.id); err != nil {
			t.Fatalf("ban moderator: %v", err)
		}
		// A valid token retains ordinary author access, but no elevated authority.
		blogTestRequest(t, app, http.MethodGet, blogTestPath(private.ID), admin.token, nil, http.StatusNotFound)
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(private.ID), admin.token,
			map[string]any{"title": "Banned moderator edit"}, http.StatusNotFound)
		blogTestRequest(t, app, http.MethodDelete, blogTestPath(private.ID), admin.token, nil, http.StatusNotFound)
		own := createBlogTestPost(t, app, admin.token, map[string]any{
			"slug": "moderator-own-draft", "title": "Own draft", "body": "Ordinary author access",
		})
		blogTestRequest(t, app, http.MethodDelete, blogTestPath(own.ID), admin.token, nil, http.StatusNoContent)
		if err := service.client.UnbanUser(t.Context(), admin.id); err != nil {
			t.Fatalf("unban moderator: %v", err)
		}
	})
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

	t.Run("account bans apply on refresh", func(t *testing.T) {
		if err := service.client.BanUser(t.Context(), bob.id, nil, nil, admin.id); err != nil {
			t.Fatalf("ban user: %v", err)
		}
		// Both Optional reads and Required writes accept an already-issued token.
		// Account status is checked when obtaining another token, not per request.
		blogTestRequest(t, app, http.MethodGet, blogTestPath(bobPublic.ID), bob.token, nil, http.StatusOK)
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(bobPublic.ID), bob.token,
			map[string]any{"title": "Existing token is still valid"}, http.StatusOK)
		blogTestRequest(t, app, http.MethodPost, "/api/v1/token", "", map[string]any{
			"grant_type": "refresh_token", "refresh_token": bob.refreshToken,
		}, http.StatusUnauthorized)
	})
	// Await maintenance while the test context and host fleet are still alive.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		var completed bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.river_job WHERE kind = 'authkit_cleanup_expired_auth_state' AND state = 'completed')`).Scan(&completed); err != nil {
			t.Fatalf("AuthKit maintenance did not complete: %v", err)
		}
		if completed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("AuthKit maintenance did not complete before deadline")
		case <-time.After(100 * time.Millisecond):
		}
	}
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	id           string
	token        string
	refreshToken string
}

func registerBlogTestUser(t *testing.T, app *blogTestServer, auth *appAuth, username string) blogTestUser {
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
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	decodeBlogTestJSON(t, body, &login)
	if login.AccessToken == "" {
		t.Fatal("password login returned no access token")
	}
	if login.RefreshToken == "" {
		t.Fatal("password login returned no refresh token")
	}
	user := blogTestUser{token: login.AccessToken, refreshToken: login.RefreshToken}
	registered, err := auth.client.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatalf("find registered user: %v", err)
	}
	user.id = registered.ID
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
	if err := pool.QueryRow(t.Context(), "SELECT owner_id::text FROM "+blogPostsTable+" WHERE id = $1", postID).Scan(&owner); err != nil {
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

func blogTestRequest(t *testing.T, app *blogTestServer, method, path, token string, input any, status int, headers ...http.Header) []byte {
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
	for _, header := range headers {
		for name, values := range header {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
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
