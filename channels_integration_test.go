package main

import (
	"image/color"
	"testing"

	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/openrails/billing"
)

// TestChannelsOnContentKit: a channel is a ContentKit group governed by its
// AuthKit group. Its membership is a ContentKit group offer that ContentKit
// syncs to OpenRails; a free one is joined through ContentKit; the app's
// members-only posts open to the group's key; the channel's images and posts'
// media upload through ContentKit under AuthKit's channel roles.
func TestChannelsOnContentKit(t *testing.T) {
	h := newHarness(t, harnessOptions{minio: true})
	ctx := h.ctx
	owner := h.register("owner", "127.0.0.2")
	fan := h.register("fanatic", "127.0.0.3")
	editor := h.register("editor", "127.0.0.4")
	// ContentKit admits only staff to group writes until creators may make
	// their own channels (ContentKit lane 120-user-groups).
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

	public := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": id, "title": "Hello", "body": "Everyone reads this", "access_policy": "public"}, "", 201)
	members := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": id, "title": "Members", "body": "Members read this", "access_policy": "membership"}, "", 201)
	editor.call("POST", "/api/v1/posts", map[string]any{"channel_id": id, "title": "By the editor", "body": "Hi", "access_policy": "public"}, "", 201)
	fan.call("POST", "/api/v1/posts", map[string]any{"channel_id": id, "title": "Not mine", "body": "No"}, "", 404)
	if p := fan.call("GET", "/api/v1/posts/"+public["id"].(string), nil, "", 200); p["can_read"] != true || p["channel_name"] != "Studio" {
		t.Fatalf("public post: %v", p)
	}
	membersPath := "/api/v1/posts/" + members["id"].(string)
	reads := func(p peer) bool {
		got := p.call("GET", membersPath, nil, "", 200)
		if got["can_read"] == true != (got["body"] != nil) {
			t.Fatalf("%s: body served %v with can_read %v", p.name, got["body"], got["can_read"])
		}
		return got["can_read"] == true
	}
	if reads(fan) || !reads(editor) || !reads(owner) {
		t.Fatal("members-only post opened to the wrong people")
	}

	// A free membership: ContentKit grants the group's key on join.
	offer := "/contentkit/v1/groups/" + id + "/offer"
	join := "/contentkit/v1/groups/" + id + "/join"
	owner.call("PUT", offer, map[string]any{"amount": "0"}, "", 200)
	if j := fan.call("POST", join, nil, "", 200); j["member"] != true {
		t.Fatalf("join: %v", j)
	}
	if !reads(fan) {
		t.Fatal("joining did not open the members-only post")
	}
	fan.call("DELETE", join, nil, "", 200)
	if reads(fan) {
		t.Fatal("leaving kept the members-only post open")
	}

	// A paid membership: ContentKit sells the group key at a renewing price.
	key, err := access.GroupKey(tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	product := func() billing.Product {
		page, err := h.srv.billing.client.ListProducts(ctx, billing.ProductListParams{Keys: []string{key.String()}})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("products for %s: %v %v", key, page, err)
		}
		return page.Items[0]
	}
	owner.call("PUT", offer, map[string]any{"amount": "4.99", "renew_hours": 720}, "", 200)
	eventually(t, "the membership price", func() bool {
		p := product()
		for _, price := range p.Prices {
			if !price.Archived && price.UnitAmount == 4_990_000 && price.BillingIntervalHours != nil && *price.BillingIntervalHours == 720 {
				return !p.Archived && len(p.Entitlements) == 1 && p.Entitlements[0] == key.String()
			}
		}
		return false
	})
	fan.call("POST", join, nil, "", 409)
	owner.call("DELETE", offer, nil, "", 200)
	eventually(t, "the withdrawn membership", func() bool { return product().Archived })

	// Images: the channel's owners hold channel:media:write, its editors do not.
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
	// Post media: the channel's editors upload, its readers do not.
	image := testPNG(t, 64, 64, color.RGBA{0, 0, 200, 255})
	fan.presign(kindPost, members["id"].(string), "images/one.png", image, "image/png", 403)
	editor.put(kindPost, members["id"].(string), "images/one.png", image, "image/png")

	// Deleting the channel deletes its group and retires its AuthKit group.
	fan.call("DELETE", "/api/v1/channels/"+id, nil, "", 403)
	owner.call("DELETE", "/api/v1/channels/"+id, nil, "", 200)
	fan.call("GET", contentkitPrefix+"/v1/groups/"+id, nil, "", 404)
	fan.call("GET", "/api/v1/posts/"+public["id"].(string), nil, "", 404)
	editor.call("POST", "/api/v1/posts", map[string]any{"channel_id": id, "title": "Too late", "body": "Gone"}, "", 404)
}
