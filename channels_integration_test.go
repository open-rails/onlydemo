package main

import (
	"image/color"
	"testing"

	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/openrails/billing"
)

// TestChannelsOnContentKit: a channel is a ContentKit group governed by its
// AuthKit group, and its posts are ContentKit content it owns. Its membership
// is a group offer ContentKit syncs to OpenRails; a free one is joined through
// ContentKit and opens the channel's members-only posts and their media; a
// post sells through its own offer; uploads follow AuthKit's channel roles.
func TestChannelsOnContentKit(t *testing.T) {
	h := newHarness(t, harnessOptions{minio: true})
	ctx := h.ctx
	owner := h.register("owner", "127.0.0.2")
	fan := h.register("fanatic", "127.0.0.3")
	editor := h.register("editor", "127.0.0.4")
	anon := h.anonymous("127.0.0.5")
	// ContentKit admits only staff to group and content writes until creators
	// hold channel:* permissions there (ContentKit lane 120-user-groups).
	if err := h.srv.auth.grantAdmin(ctx, owner.id); err != nil {
		t.Fatal(err)
	}

	model := fan.call("GET", contentkitPrefix+"/v1/kinds", nil, "", 200)
	if post := model["kinds"].(map[string]any)[kindPost].(map[string]any); post["presentation"] != "post" {
		t.Fatalf("post kind: %v", post)
	}
	if ch := model["group_kinds"].(map[string]any)[kindChannel].(map[string]any); ch["owner"] != "user" || ch["sellable"] != true || ch["auth_scope"] != true {
		t.Fatalf("channel group kind: %v", ch)
	}

	created := owner.call("POST", "/api/v1/channels", map[string]any{"slug": "Studio", "name": "Studio"}, "", 201)
	id, _ := created["id"].(string)
	if created["kind"] != kindChannel || created["slug"] != "studio" || created["owner_user_id"] != owner.id || created["auth_scope"] != id {
		t.Fatalf("channel group: %v", created)
	}
	if g := fan.call("GET", contentkitPrefix+"/v1/groups/"+id, nil, "", 200); g["name"] != "Studio" {
		t.Fatalf("ContentKit serves %v", g)
	}
	if roles, err := h.srv.auth.client.GroupRoles(ctx, iam.GroupByID(id), []iam.Subject{iam.UserSubject(owner.id)}); err != nil || roles[iam.UserSubject(owner.id)] != channelOwnerRole {
		t.Fatalf("AuthKit owner role: %v %v", roles, err)
	}
	// A taken handle refuses the channel and takes its AuthKit group back.
	owner.call("POST", "/api/v1/channels", map[string]any{"slug": "studio", "name": "Again"}, "", 409)
	if groups, err := h.srv.auth.client.ListGroups(ctx, iam.GroupQuery{Persona: channelPersona.Persona}); err != nil || len(groups.Items) != 1 {
		t.Fatalf("AuthKit channel groups after a refused one: %v %v", groups, err)
	}
	if _, err := h.srv.auth.client.SetGroupRole(ctx, iam.UserIdentity(owner.id), iam.GroupByID(id), iam.UserSubject(editor.id), channelEditorRole); err != nil {
		t.Fatal(err)
	}

	public, _ := owner.post(id, "Hello", "Everyone reads this", nil)
	members, membersVersion := owner.post(id, "Members", "Members read this", map[string]any{"level": "group", "group_id": id})
	paid, _ := owner.post(id, "Paid", "Buyers read this", map[string]any{"level": "ppv"})
	fan.call("POST", contentkitPrefix+"/v1/contents", map[string]any{"kind": kindPost, "owner_group_id": id,
		"text": map[string]any{"en": map[string]string{"title": "Not mine"}}}, "", 403)
	if !anon.opens(public) || anon.opens(members) || fan.opens(members) || !owner.opens(members) || fan.opens(paid) {
		t.Fatal("posts opened to the wrong people")
	}
	if c := fan.call("GET", contentkitPrefix+"/v1/contents/"+members, nil, "", 200); c["owner_group_id"] != id || c["title"] != "Members" {
		t.Fatalf("members-only post: %v", c)
	}
	feed := anon.call("GET", contentkitPrefix+"/v1/contents?kind=post&mode=lock", nil, "", 200)
	locked := map[string]bool{}
	for _, card := range feed["contents"].([]any) {
		card := card.(map[string]any)
		locked[card["id"].(string)] = card["access"].(map[string]any)["allowed"] != true
	}
	if len(locked) != 3 || locked[public] || !locked[members] || !locked[paid] {
		t.Fatalf("feed: %v", locked)
	}

	// A free membership: ContentKit grants the group's key on join.
	offer := contentkitPrefix + "/v1/groups/" + id + "/offer"
	join := contentkitPrefix + "/v1/groups/" + id + "/join"
	owner.call("PUT", offer, map[string]any{"amount": "0"}, "", 200)
	if j := fan.call("POST", join, nil, "", 200); j["member"] != true {
		t.Fatalf("join: %v", j)
	}
	if !fan.opens(members) || fan.opens(paid) {
		t.Fatal("joining did not open the members-only post alone")
	}
	// Its media too: the owner's upload to the post's version opens to members.
	image := testPNG(t, 64, 64, color.RGBA{0, 0, 200, 255})
	owner.putVersion(members, membersVersion, "images/one.png", image, "image/png")
	read := contentkitPrefix + "/v1/media/post/" + members + "/versions/" + membersVersion
	if got := fan.call("GET", read, nil, "", 200); got["access"] != "full" {
		t.Fatalf("a member reads the post's media with access %v", got["access"])
	}
	if got := anon.call("GET", read, nil, "", 200); got["access"] != "none" {
		t.Fatalf("an anonymous reader reads the post's media with access %v", got["access"])
	}
	fan.presignVersion(members, membersVersion, "images/two.png", image, "image/png", 403)
	fan.call("DELETE", join, nil, "", 200)
	if fan.opens(members) {
		t.Fatal("leaving kept the members-only post open")
	}

	// A paid membership and a paid post: ContentKit sells their keys.
	product := func(key access.Key) billing.Product {
		page, err := h.srv.billing.client.ListProducts(ctx, billing.ProductListParams{Keys: []string{key.String()}})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("products for %s: %v %v", key, page, err)
		}
		return page.Items[0]
	}
	groupKey, _ := access.GroupKey(tenant, id)
	owner.call("PUT", offer, map[string]any{"amount": "4.99", "renew_hours": 720}, "", 200)
	owner.offerPrice(id)
	if p := product(groupKey); p.Archived || len(p.Entitlements) != 1 || p.Entitlements[0] != groupKey.String() {
		t.Fatalf("membership product: %+v", p)
	}
	fan.call("POST", join, nil, "", 409)
	contentKey, _ := access.ContentKey(tenant, paid)
	owner.call("PUT", contentkitPrefix+"/v1/contents/"+paid+"/offer", map[string]any{"amount": "2.50"}, "", 200)
	owner.offerPrice(paid)
	if p := product(contentKey); p.DisplayName != "Paid" || len(p.Prices) != 1 || p.Prices[0].UnitAmount != 2_500_000 {
		t.Fatalf("post product: %+v", p)
	}
	owner.call("DELETE", offer, nil, "", 200)
	eventually(t, "the withdrawn membership", func() bool { return product(groupKey).Archived })

	// Channel images: owners hold channel:media:write, editors do not.
	cover := testPNG(t, 900, 300, color.RGBA{200, 40, 40, 255})
	editor.presign(kindChannel, id, "cover/banner.png", cover, "image/png", 403)
	fan.presign(kindChannel, id, "cover/banner.png", cover, "image/png", 403)
	if path := owner.put(kindChannel, id, "cover/banner.png", cover, "image/png"); path == "" {
		t.Fatal("no committed cover")
	}
	if _, err := h.srv.auth.client.SetGroupRole(ctx, iam.UserIdentity(owner.id), iam.GroupByID(id), iam.UserSubject(editor.id), channelOwnerRole); err != nil {
		t.Fatal(err)
	}
	editor.presign(kindChannel, id, "cover/banner.png", cover, "image/png", 200)

	// A deleted post is gone for those who never bought it.
	owner.call("DELETE", contentkitPrefix+"/v1/contents/"+public, nil, "", 204)
	anon.call("GET", contentkitPrefix+"/v1/contents/"+public, nil, "", 404)

	// Deleting the channel deletes its group and retires its AuthKit group.
	fan.call("DELETE", "/api/v1/channels/"+id, nil, "", 403)
	owner.call("DELETE", "/api/v1/channels/"+id, nil, "", 200)
	fan.call("GET", contentkitPrefix+"/v1/groups/"+id, nil, "", 404)
	if g, err := h.srv.auth.client.Group(ctx, iam.GroupByID(id)); err == nil && g.DeletedAt == nil {
		t.Fatalf("AuthKit group still live: %+v", g)
	}
}
