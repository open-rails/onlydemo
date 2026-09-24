package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-rails/authkit"
	"github.com/open-rails/openrails"
)

// countingAuth and countingReads count the AuthKit and OpenRails reads made
// while serving a request (background jobs are not counted).
type countingAuth struct {
	authkit.Client
	n *atomic.Int64
}

func inRequest(ctx context.Context, n *atomic.Int64) {
	if ctx.Value(queryCountKey{}) != nil {
		n.Add(1)
	}
}

func (a countingAuth) GroupInstanceForSlug(ctx context.Context, g authkit.GroupRef) (authkit.GroupInstance, error) {
	inRequest(ctx, a.n)
	return a.Client.GroupInstanceForSlug(ctx, g)
}
func (a countingAuth) GroupInstancesByIDs(ctx context.Context, ids []string) (map[string]authkit.GroupInstance, error) {
	inRequest(ctx, a.n)
	return a.Client.GroupInstancesByIDs(ctx, ids)
}
func (a countingAuth) GroupInstanceByID(ctx context.Context, id string) (authkit.GroupInstance, error) {
	inRequest(ctx, a.n)
	return a.Client.GroupInstanceByID(ctx, id)
}
func (a countingAuth) ListSubjectGroups(ctx context.Context, s authkit.Subject) ([]authkit.SubjectGroupMembership, error) {
	inRequest(ctx, a.n)
	return a.Client.ListSubjectGroups(ctx, s)
}
func (a countingAuth) Can(ctx context.Context, s authkit.Subject, g authkit.GroupRef, p authkit.Perm) (bool, error) {
	inRequest(ctx, a.n)
	return a.Client.Can(ctx, s, g, p)
}
func (a countingAuth) CanOnGroup(ctx context.Context, s authkit.Subject, id string, p authkit.Perm) (bool, error) {
	inRequest(ctx, a.n)
	return a.Client.CanOnGroup(ctx, s, id, p)
}
func (a countingAuth) EffectivePermissionsForGroups(ctx context.Context, s authkit.Subject, ids []string) (map[string][]authkit.Perm, error) {
	inRequest(ctx, a.n)
	return a.Client.EffectivePermissionsForGroups(ctx, s, ids)
}
func (a countingAuth) ListEffectivePermissions(ctx context.Context, s authkit.Subject, g authkit.GroupRef) ([]authkit.Perm, error) {
	inRequest(ctx, a.n)
	return a.Client.ListEffectivePermissions(ctx, s, g)
}

type countingReads struct {
	entitlementReads
	n *atomic.Int64
}

func (r countingReads) CheckEntitlements(ctx context.Context, user string, keys []string, at time.Time, o ...openrails.RequestOption) (map[string]bool, error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.CheckEntitlements(ctx, user, keys, at, o...)
}
func (r countingReads) ListEntitlements(ctx context.Context, user string, at time.Time, o ...openrails.RequestOption) ([]openrails.EntitlementRecord, error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.ListEntitlements(ctx, user, at, o...)
}
func (r countingReads) ListOffersForEntitlements(ctx context.Context, keys []string, params openrails.OfferListParams, o ...openrails.RequestOption) (map[string]openrails.OfferList, error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.ListOffersForEntitlements(ctx, keys, params, o...)
}

type cost struct{ queries, auth, billing int64 }

// cost serves one GET as p and reports what it took.
func (h *mediaHarness) cost(p peer, path string) (cost, []byte) {
	h.t.Helper()
	h.calls.auth.Store(0)
	h.calls.billing.Store(0)
	res, raw := p.do("GET", path, nil, "")
	if res.StatusCode != 200 {
		h.t.Fatalf("%s GET %s: %d %s", p.name, path, res.StatusCode, raw)
	}
	var c cost
	if _, err := fmt.Sscanf(res.Header.Get("Server-Timing"), `sql;desc="%d queries"`, &c.queries); err != nil {
		h.t.Fatalf("Server-Timing %q: %v", res.Header.Get("Server-Timing"), err)
	}
	c.auth, c.billing = h.calls.auth.Load(), h.calls.billing.Load()
	return c, raw
}

// testListingCost grows the site by channels and posts and requires every
// listing to cost exactly what it did before. It also proves the library
// holds a purchase older than the newest 50 posts.
func (h *mediaHarness) testListingCost(t *testing.T, purchasedPost int64, buyer peer) {
	lister := h.register("lister", "127.0.0.11")
	fan := h.register("follower", "127.0.0.12")
	anon := peer{h: h, name: "anonymous", client: h.client("127.0.0.13")}
	membership := map[string]any{"enabled": true, "price": map[string]any{"unit_amount": "2000000", "currency": "USD"}}
	channel := func(slug string) string {
		id := lister.call("POST", "/api/v1/channels", map[string]any{"slug": slug, "name": slug}, "", 201)["id"].(string)
		lister.call("PUT", "/api/v1/channels/"+id+"/membership", membership, "", 200)
		eventually(t, slug+" membership offer", func() bool {
			return lister.call("GET", "/api/v1/channels/"+id, nil, "", 200)["membership"].(map[string]any)["offer"] != nil
		})
		return id
	}
	paidPost := func(ch, slug string) {
		id := lister.call("POST", "/api/v1/posts", map[string]any{"channel_id": ch, "slug": slug, "title": slug, "body": "b", "access_policy": "ppv",
			"price": map[string]any{"unit_amount": "990000", "currency": "USD"}}, "", 201)["id"].(float64)
		eventually(t, slug+" offer", func() bool {
			return len(lister.call("GET", fmt.Sprintf("/api/v1/posts/%.0f", id), nil, "", 200)["offers"].([]any)) > 0
		})
	}
	first := channel("cost-a")
	paidPost(first, "cost-a-paid")

	pages := []struct {
		p    peer
		path string
	}{
		{anon, "/api/v1/posts?limit=50"}, {fan, "/api/v1/posts?limit=50"}, {buyer, "/api/v1/posts?limit=50"},
		{anon, "/api/v1/channels"}, {fan, "/api/v1/channels"}, {lister, "/api/v1/channels"},
		{lister, "/api/v1/me/channels"}, {lister, "/api/v1/me"}, {buyer, "/api/v1/me"},
	}
	before := make([]cost, len(pages))
	for i, pg := range pages {
		before[i], _ = h.cost(pg.p, pg.path)
	}

	// 51 newer posts push the purchase out of the newest 50.
	for i := range 51 {
		lister.call("POST", "/api/v1/posts", map[string]any{"channel_id": first, "slug": fmt.Sprintf("cost-public-%d", i), "title": "t", "body": "b"}, "", 201)
	}
	for _, slug := range []string{"cost-b", "cost-c", "cost-d"} {
		paidPost(channel(slug), slug+"-paid")
	}

	for i, pg := range pages {
		after, _ := h.cost(pg.p, pg.path)
		t.Logf("%s GET %s: %+v", pg.p.name, pg.path, after)
		if after != before[i] {
			t.Errorf("%s GET %s cost %+v with more items, %+v before", pg.p.name, pg.path, after, before[i])
		}
		if pg.p.name != "anonymous" && (after.auth > 3 || after.billing > 4) {
			t.Errorf("%s GET %s: %+v", pg.p.name, pg.path, after)
		}
	}

	var library struct {
		Purchased []post `json:"purchased_posts"`
	}
	_, raw := h.cost(buyer, "/api/v1/me")
	if err := json.Unmarshal(raw, &library); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(library.Purchased, func(p post) bool { return p.ID == purchasedPost && p.Purchased && p.CanRead }) {
		t.Fatalf("library %+v lacks purchased post %d", library.Purchased, purchasedPost)
	}

	// A media read resolves the post and the viewer's grants once.
	for _, p := range []peer{buyer, fan, lister} {
		c, _ := h.cost(p, fmt.Sprintf("/api/v1/media/post/%d?variant=large,blurred", purchasedPost))
		if c.auth != 1 || c.billing > 1 {
			t.Errorf("%s media read: %+v", p.name, c)
		}
	}
}
