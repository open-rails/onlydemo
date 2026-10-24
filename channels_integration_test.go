package main

import (
	"encoding/json"
	"image/color"
	"slices"
	"testing"

	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/openrails/billing"
)

// TestChannelsOnContentKit covers entity ownership and editorial permissions,
// native OpenRails catalog edits/free grants, protected media, and deletion.
func TestChannelsOnContentKit(t *testing.T) {
	h := newHarness(t, harnessOptions{minio: true})
	ctx := h.ctx
	owner := h.register("owner", "127.0.0.2")
	fan := h.register("fanatic", "127.0.0.3")
	editor := h.register("editor", "127.0.0.4")
	anon := h.anonymous("127.0.0.5")

	model := fan.call("GET", contentkitPrefix+"/v1/kinds", nil, "", 200)
	if post := model["kinds"].(map[string]any)[kindPost].(map[string]any); post["presentation"] != "post" {
		t.Fatalf("post kind: %v", post)
	}
	if ch := model["entity_kinds"].(map[string]any)[kindChannel].(map[string]any); ch["owner"] != "user" || ch["auth_scope"] != true {
		t.Fatalf("channel group kind: %v", ch)
	}

	id := owner.channel("studio", "Studio")
	g := fan.call("GET", contentkitPrefix+"/v1/entities/"+id, nil, "", 200)
	if g["name"] != "Studio" || g["owner_user_id"] != owner.id || g["auth_scope"] != id {
		t.Fatalf("channel group: %v", g)
	}
	eventually(t, "the channel's AuthKit group", func() bool {
		roles, err := h.srv.auth.client.GroupRoles(ctx, iam.GroupByID(id), []iam.Subject{iam.UserSubject(owner.id)})
		return err == nil && roles[iam.UserSubject(owner.id)] == channelOwnerRole
	})
	fan.call("POST", contentkitPrefix+"/v1/entities", []map[string]any{{"kind": kindChannel, "slug": "studio",
		"names": []map[string]string{{"language": "en", "kind": "name", "name": "Again"}}}}, "", 409)
	if _, err := h.srv.auth.client.SetGroupRole(ctx, iam.UserIdentity(owner.id), iam.GroupByID(id), iam.UserSubject(editor.id), channelEditorRole); err != nil {
		t.Fatal(err)
	}

	// A members-only post needs no offer on its channel: it stays locked,
	// selling nothing, until the channel sells a membership (here a free one).
	members, membersVersion := owner.post(id, "Members", "Members read this", map[string]any{"level": "group", "group_id": id})
	if fan.opens(members) || anon.opens(members) || fan.body(members) != "" {
		t.Fatal("a members-only post in a channel selling no membership is open to non-members")
	}
	offer := "/api/v1/products/entities/" + id
	join := "/api/v1/memberships/" + id
	fan.call("POST", join, nil, "", 404)
	fan.call("PUT", offer, map[string]any{"amount": "0"}, "", 403)
	editor.call("PUT", offer, map[string]any{"amount": "0"}, "", 403)

	// Comments follow the post's access: the owner writes, non-members read
	// none and are refused.
	thread := contentkitPrefix + "/v1/contents/" + members + "/comments"
	owner.call("POST", thread, map[string]any{"body": "welcome, members"}, "", 201)
	for _, p := range []peer{fan, anon} {
		if got := p.comments(members); len(got) != 0 {
			t.Fatalf("%s reads a members-only post's comments: %v", p.name, got)
		}
	}
	if s := fan.call("GET", contentkitPrefix+"/v1/contents/"+members+"/can-comment", nil, "", 200); s["locked"] != true || s["can_comment"] == true {
		t.Fatalf("a non-member's standing: %v", s)
	}
	if r := fan.call("POST", thread, map[string]any{"body": "let me in"}, "", 403); r["code"] != "comments_locked" {
		t.Fatalf("a non-member's comment: %v", r)
	}

	// The owner manages the offer from the channel; the paywall offers it to
	// everyone else.
	owner.call("PUT", offer, map[string]any{"amount": "0"}, "", 200)
	public, _ := owner.post(id, "Hello", "Everyone reads this", nil)
	paid, _ := owner.post(id, "Paid", "Buyers read this", map[string]any{"level": "ppv"})
	editor.post(id, "By the editor", "Hi", nil)
	fan.call("POST", contentkitPrefix+"/v1/contents", map[string]any{"kind": kindPost, "owner_group_id": id,
		"text": map[string]any{"en": map[string]string{"title": "Not mine"}}}, "", 403)
	for _, c := range []struct {
		who  peer
		post string
		want bool
	}{{anon, public, true}, {anon, members, false}, {fan, members, false}, {owner, members, true}, {editor, members, true}, {fan, paid, false}} {
		if got := c.who.opens(c.post); got != c.want {
			t.Fatalf("%s opens %s: %v, want %v", c.who.name, c.post, got, c.want)
		}
	}
	if c := editor.call("GET", contentkitPrefix+"/v1/contents/"+members, nil, "", 200); c["editor"] != true {
		t.Fatalf("the channel's editor does not edit its post: %v", c)
	}
	// A locked post's body is withheld; its description is the teaser.
	if b := fan.body(members); b != "" {
		t.Fatalf("a locked post served its body: %q", b)
	}
	if c := fan.call("GET", contentkitPrefix+"/v1/contents/"+members, nil, "", 200); c["owner_group_id"] != id || c["title"] != "Members" {
		t.Fatalf("members-only post: %v", c)
	}
	feed := anon.call("GET", contentkitPrefix+"/v1/contents?kind=post&mode=lock&owner_group="+id, nil, "", 200)
	locked := map[string]bool{}
	for _, card := range feed["contents"].([]any) {
		card := card.(map[string]any)
		locked[card["id"].(string)] = card["access"].(map[string]any)["allowed"] != true
		if owner, _ := card["owner"].(map[string]any); owner["id"] != id || owner["name"] != "Studio" {
			t.Fatalf("card owner: %v", card["owner"])
		}
	}
	if len(locked) != 4 || locked[public] || !locked[members] || !locked[paid] {
		t.Fatalf("feed: %v", locked)
	}

	// A free membership: the host grants the native OpenRails product.
	fan.call("POST", join, nil, "", 204)
	if !fan.opens(members) || fan.opens(paid) || fan.body(members) != "Members read this" {
		t.Fatal("joining did not open the members-only post alone")
	}
	fan.call("POST", thread, map[string]any{"body": "glad to be here"}, "", 201)
	if got := fan.comments(members); !slices.Equal(got, []string{"glad to be here", "welcome, members"}) {
		t.Fatalf("a member reads the comments %v", got)
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
	fan.call("DELETE", join, nil, "", 204)
	if fan.opens(members) {
		t.Fatal("leaving kept the members-only post open")
	}

	// Native OpenRails products grant the stable ContentKit access keys.
	groupKey, _ := access.EntityKey(tenant, id)
	contentKey, _ := access.ContentKey(tenant, paid)
	product := func(key access.Key) billing.Product {
		page, err := h.srv.billing.client.ListProducts(ctx, billing.ProductListParams{Keys: []string{key.String()}})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("products for %s: %v %v", key, page, err)
		}
		return page.Items[0]
	}
	owner.call("PUT", offer, map[string]any{"amount": "4.99", "renew_hours": 720}, "", 200)
	fan.groupPrice(id)
	if a := fan.call("GET", contentkitPrefix+"/v1/entities/"+id, nil, "", 200)["access"].(map[string]any); a["key"] != groupKey.String() ||
		a["member"] != false {
		t.Fatalf("group access: %v", a)
	}
	if p := product(groupKey); p.Archived || len(p.Entitlements) != 1 || p.Entitlements[0] != groupKey.String() {
		t.Fatalf("membership product: %+v", p)
	}
	fan.call("POST", join, nil, "", 409)
	owner.call("PUT", "/api/v1/products/contents/"+paid, map[string]any{"amount": "2.50"}, "", 200)
	fan.contentPrice(paid)
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
	owner.removePost(public)
	anon.call("GET", contentkitPrefix+"/v1/contents/"+public, nil, "", 404)

	// Deleting the channel deletes its group and retires its AuthKit group.
	fan.call("DELETE", contentkitPrefix+"/v1/entities/"+id, nil, "", 403)
	owner.call("DELETE", contentkitPrefix+"/v1/entities/"+id, nil, "", 200)
	fan.call("GET", contentkitPrefix+"/v1/entities/"+id, nil, "", 404)
	eventually(t, "the channel's AuthKit group retired", func() bool {
		g, err := h.srv.auth.client.Group(ctx, iam.GroupByID(id))
		return err != nil || g.DeletedAt != nil
	})
}

// comments are the bodies of content id's top-level comments, newest first.
func (p peer) comments(id string) []string {
	p.h.t.Helper()
	res, raw := p.do("GET", contentkitPrefix+"/v1/contents/"+id+"/comments", nil, "")
	var list []struct{ Body string }
	if res.StatusCode != 200 || json.Unmarshal(raw, &list) != nil {
		p.h.t.Fatalf("%s: comments on %s = %d: %s", p.name, id, res.StatusCode, raw)
	}
	out := []string{}
	for _, c := range list {
		out = append(out, c.Body)
	}
	return out
}
