package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func assertAccountRecovery(t *testing.T, app *blogTestServer, auth *appAuth, pool *pgxpool.Pool, successor blogTestUser) {
	t.Helper()
	peer := &blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.8")}
	user := registerBlogTestUser(t, peer, auth, "recoverableauthor")
	post := createBlogTestPost(t, peer, user.token, map[string]any{"channel_id": user.channelID, "slug": "retained-attribution", "title": "Shared channel content", "body": "The channel owns this content"})
	results, err := auth.client.SoftDeleteUsers(t.Context(), []string{user.id})
	if err != nil || len(results) != 1 || results[0].Err == nil {
		t.Fatalf("sole channel owner deletion was not refused: results=%+v err=%v", results, err)
	}
	members := "/auth/v1/channel/recoverableauthor-channel/members"
	blogTestRequest(t, peer, http.MethodPost, members, user.token, map[string]any{"user_id": successor.id, "role": "owner"}, http.StatusOK)
	blogTestRequest(t, app, http.MethodDelete, members+"/"+user.id, successor.token, nil, http.StatusOK)
	const password = "Local-demo-test-password-42!"
	blogTestRequest(t, peer, http.MethodDelete, "/auth/v1/user", user.token, map[string]any{"password": password}, http.StatusNoContent)
	deleted, err := auth.client.AdminGetUser(t.Context(), user.id)
	if err != nil || deleted == nil || deleted.DeletedAt == nil {
		t.Fatalf("self deletion did not preserve a recoverable identity: user=%+v err=%v", deleted, err)
	}
	assertBlogPostAuthor(t, pool, post.ID, user.id)
	blogTestRequest(t, app, http.MethodGet, blogTestPath(post.ID), successor.token, nil, http.StatusOK)
	blogTestRequest(t, peer, http.MethodGet, blogTestPath(post.ID), user.token, nil, http.StatusNotFound)
	blogTestRequest(t, peer, http.MethodPost, "/auth/v1/token", "", map[string]any{"grant_type": "refresh_token", "refresh_token": user.refreshToken}, http.StatusUnauthorized)
	blogTestRequest(t, peer, http.MethodPost, "/auth/v1/password/login", "", map[string]any{"identifier": "recoverableauthor@example.com", "password": "incorrect"}, http.StatusUnauthorized)
	login := map[string]any{"identifier": "recoverableauthor@example.com", "password": password}
	raw := blogTestRequest(t, peer, http.MethodPost, "/auth/v1/password/login", "", login, http.StatusConflict)
	var proof struct {
		Error struct {
			Metadata struct {
				Recovery struct {
					Token   string    `json:"token"`
					PurgeAt time.Time `json:"purge_at"`
				} `json:"recovery"`
			} `json:"metadata"`
		} `json:"error"`
	}
	decodeBlogTestJSON(t, raw, &proof)
	recovery := proof.Error.Metadata.Recovery
	if recovery.Token == "" || recovery.PurgeAt.Sub(*deleted.DeletedAt) != 30*24*time.Hour || strings.Contains(string(raw), "access_token") {
		t.Fatal("password proof did not return a bounded 30-day recovery confirmation")
	}
	blogTestRequest(t, peer, http.MethodGet, "/api/v1/posts", recovery.Token, nil, http.StatusUnauthorized)
	blogTestRequest(t, peer, http.MethodPost, "/auth/v1/account/recovery/confirm", "", map[string]any{"token": recovery.Token}, http.StatusNoContent)
	blogTestRequest(t, peer, http.MethodPost, "/auth/v1/account/recovery/confirm", "", map[string]any{"token": recovery.Token}, http.StatusUnauthorized)
	var session struct {
		AccessToken string `json:"access_token"`
	}
	decodeBlogTestJSON(t, blogTestRequest(t, peer, http.MethodPost, "/auth/v1/password/login", "", login, http.StatusOK), &session)
	if session.AccessToken == "" {
		t.Fatal("restored account could not obtain a fresh session")
	}
	// Recovery does not undo the explicit ownership transfer or removal.
	blogTestRequest(t, peer, http.MethodPatch, blogTestPath(post.ID), session.AccessToken, map[string]any{"title": "No restored authority"}, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodDelete, blogTestPath(post.ID), successor.token, nil, http.StatusNoContent)
}
