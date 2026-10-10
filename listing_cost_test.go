package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
)

// inRequest counts an AuthKit or OpenRails read made while serving a request
// (background jobs are not counted).
func inRequest(ctx context.Context, n *atomic.Int64) {
	if ctx.Value(queryCountKey{}) != nil {
		n.Add(1)
	}
}

type countingReads struct {
	entitlementReads
	n *atomic.Int64
}

func (r countingReads) CheckEntitlements(ctx context.Context, customer billing.CustomerID, params billing.CheckEntitlementsParams, o ...openrails.RequestOption) (*billing.EntitlementCheck, error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.CheckEntitlements(ctx, customer, params, o...)
}
func (r countingReads) ListCustomerEntitlements(ctx context.Context, customer billing.CustomerID, params billing.CustomerEntitlementListParams, o ...openrails.RequestOption) (*billing.ListPage[billing.CustomerEntitlement], error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.ListCustomerEntitlements(ctx, customer, params, o...)
}
func (r countingReads) ListOffers(ctx context.Context, params billing.OfferListParams, o ...openrails.RequestOption) (billing.OfferPages, error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.ListOffers(ctx, params, o...)
}
func (r countingReads) CheckProductAccess(ctx context.Context, customer billing.CustomerID, params billing.CheckProductAccessParams, o ...openrails.RequestOption) (map[string]bool, error) {
	inRequest(ctx, r.n)
	return r.entitlementReads.CheckProductAccess(ctx, customer, params, o...)
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
func (h *mediaHarness) testListingCost(t *testing.T, purchasedPost testPost, buyer peer) {
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
	paidPost := func(ch, title string) string {
		code := lister.call("POST", "/api/v1/posts", map[string]any{"channel_id": ch, "title": title, "body": "b", "access_policy": "ppv",
			"price": map[string]any{"unit_amount": "990000", "currency": "USD"}}, "", 201)["code"].(string)
		eventually(t, title+" offer", func() bool {
			return len(lister.call("GET", "/api/v1/posts/"+code, nil, "", 200)["offers"].([]any)) > 0
		})
		return code
	}
	first := channel("cost-a")
	firstPaid := paidPost(first, "cost-a-paid")

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
		lister.call("POST", "/api/v1/posts", map[string]any{"channel_id": first, "title": fmt.Sprintf("cost public %d", i), "body": "b"}, "", 201)
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
	if !slices.ContainsFunc(library.Purchased, func(p post) bool { return p.ID == purchasedPost.id && p.Code == purchasedPost.code && p.Purchased && p.CanRead }) {
		t.Fatalf("library %+v lacks purchased post %s", library.Purchased, purchasedPost)
	}

	// A media read resolves the post and the viewer's grants (plus, when
	// they hold any, the account's liveness) once.
	for _, p := range []peer{buyer, fan, lister} {
		c, _ := h.cost(p, "/api/v1/contentkit/media/post/"+purchasedPost.id)
		if c.auth > 2 || c.billing > 1 {
			t.Errorf("%s media read: %+v", p.name, c)
		}
	}

	// A deleted account's still-valid token carries no channel authority.
	helper := h.register("helper", "127.0.0.14")
	lister.call("POST", "/api/v1/channels/"+first+"/members", map[string]any{"username": "helper", "role": "editor"}, "", 201)
	rights := func() (bool, bool, bool) {
		ch := helper.call("GET", "/api/v1/channels/"+first, nil, "", 200)
		post := helper.call("GET", "/api/v1/posts/"+firstPaid, nil, "", 200)
		return ch["can_edit"] == true, post["can_read"] == true, h.read(helper, kindPost, post["id"].(string), false).res.Access == media.AccessFull
	}
	if edit, read, media := rights(); !edit || !read || !media {
		t.Fatalf("editor rights before deletion: edit %t read %t media %t", edit, read, media)
	}
	if _, err := h.srv.auth.client.DeleteUsers(h.ctx, iam.SystemIdentity(), []string{helper.id}); err != nil {
		t.Fatal(err)
	}
	if edit, read, media := rights(); edit || read || media {
		t.Fatalf("deleted editor kept rights: edit %t read %t media %t", edit, read, media)
	}
}
