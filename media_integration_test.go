package main

// End-to-end proof on real services: PostgreSQL (app, AuthKit, OpenRails,
// River, ContentKit), an S3 backend (MinIO in CI), the real ContentKit upload
// and read handlers, the app's media worker (libvips images, ffmpeg video),
// the media-gateway binary and the app's content URL router. Only the Stripe
// API is replaced, by a closed local fake.
//
//	DEMO_TEST_DATABASE_URL   loopback PostgreSQL admin URL; a database is created and dropped
//	DEMO_TEST_S3_ENDPOINT    e.g. http://127.0.0.1:9000; a bucket is created and emptied
//	DEMO_TEST_S3_ACCESS_KEY, DEMO_TEST_S3_SECRET_KEY
//	DEMO_TEST_MEDIA_GATEWAY  media-gateway binary; built from the pinned ContentKit when unset
//	DEMO_TEST_FFMPEG=1       fail instead of skipping without ffmpeg (CI)

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/contenturl"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/contentkit/media/token"
	"github.com/open-rails/contentkit/media/worker"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
)

const (
	testWebhookSecret = "whsec_media_test_fake_only"
	testPassword      = "Media-test-password-42!"
	testQuota         = 3 << 20
	testFilesPerHour  = 40
)

// testPost is a post's id (its media folder) and code (its URLs).
type testPost struct{ id, code string }

func (p testPost) path() string { return "/api/v1/posts/" + p.code }

func TestMediaEndToEnd(t *testing.T) {
	h := newMediaHarness(t)
	ctx := h.ctx

	owner := h.register("owner", "127.0.0.2")
	h.owner = owner
	editor := h.register("editor", "127.0.0.3")
	member := h.register("member", "127.0.0.4")
	buyer := h.register("buyer", "127.0.0.5")
	loyal := h.register("loyal", "127.0.0.6") // members_ppv buyer
	stranger := h.register("stranger", "127.0.0.7")
	admin := h.register("admin", "127.0.0.8")
	spammer := h.register("spammer", "127.0.0.9")
	anon := peer{h: h, name: "anonymous", client: h.client("127.0.0.10")}
	if err := h.srv.auth.grantAdmin(ctx, admin.id); err != nil {
		t.Fatal(err)
	}

	ch := owner.call("POST", "/api/v1/channels", map[string]any{"slug": "studio", "name": "Studio"}, "", 201)
	channelID := ch["id"].(string)
	owner.call("POST", "/api/v1/channels/"+channelID+"/members", map[string]any{"username": "editor", "role": "editor"}, "", 201)
	owner.call("PUT", "/api/v1/channels/"+channelID+"/membership", map[string]any{"enabled": true, "price": nil}, "", 200)

	price := map[string]any{"unit_amount": "4990000", "currency": "USD"}
	newPost := func(p peer, body map[string]any) testPost {
		t.Helper()
		got := p.call("POST", "/api/v1/posts", body, "", 201)
		return testPost{got["id"].(string), got["code"].(string)}
	}
	posts := map[string]testPost{}
	for _, policy := range []string{"public", "membership", "ppv", "members_ppv"} {
		body := map[string]any{"channel_id": channelID, "title": policy, "body": "text", "access_policy": policy}
		if paidPolicy(policy) {
			body["price"] = price
		}
		posts[policy] = newPost(owner, body)
	}

	// The creator uploads two images per post. The first, blurred, is the
	// post's public preview.
	for _, p := range posts {
		ref := postRefBody(p.id)
		owner.put(ref, "images/a.png", testPNG(t, 640, 480, color.RGBA{200, 40, 40, 255}), "image/png")
		owner.put(ref, "images/b.png", testPNG(t, 480, 640, color.RGBA{40, 40, 200, 255}), "image/png")
	}
	for _, p := range posts {
		h.waitReady(owner, kindPost, p.id)
	}

	// Entitlements live only in OpenRails: a free membership, and real
	// purchases through checkout and a signed provider webhook. The loyal
	// buyer bought a members-only post while a member, then left.
	member.call("POST", "/api/v1/channels/"+channelID+"/join", nil, "", 200)
	h.purchase(buyer, posts["ppv"])
	loyal.call("POST", "/api/v1/channels/"+channelID+"/join", nil, "", 200)
	h.purchase(loyal, posts["members_ppv"])
	loyal.call("POST", "/api/v1/channels/"+channelID+"/leave", nil, "", 200)

	full, preview := "full", "preview"
	want := map[string]map[*peer]string{
		"public":      {&anon: full, &stranger: full, &member: full},
		"membership":  {&anon: preview, &stranger: preview, &member: full, &editor: full, &admin: full, &loyal: preview, &buyer: preview},
		"ppv":         {&anon: preview, &stranger: preview, &member: preview, &buyer: full, &owner: full},
		"members_ppv": {&stranger: preview, &member: preview, &loyal: full, &editor: full},
	}
	for policy, viewers := range want {
		for viewer, level := range viewers {
			t.Run(policy+"/"+viewer.name, func(t *testing.T) {
				h.expectRead(t, *viewer, posts[policy], level)
			})
		}
	}

	t.Run("url delivery", func(t *testing.T) {
		signing, _ := token.ParseKey(h.cfg.Media.TokenKey)
		reader, err := media.NewReader(media.ReaderOptions{Manifests: h.srv.media.manifests,
			Delivery: media.Delivery{Mode: media.DeliverURL, SigningKey: signing}})
		if err != nil {
			t.Fatal(err)
		}
		actor := actorFor("")
		actor.IP = "127.0.0.1"
		res, err := reader.Read(ctx, postRef(posts["public"].id), actor, media.ReadOptions{})
		if err != nil || res.Access != media.AccessFull || res.Cookie != nil || len(res.Files) != 2 {
			t.Fatalf("url read: %+v %v", res, err)
		}
		for _, f := range res.Files {
			if !strings.Contains(f.URL, "?t=") {
				t.Fatalf("url mode returned %q", f.URL)
			}
			h.expectFetch(t, f.URL, "", 200)
		}
	})

	t.Run("upload authorization and limits", func(t *testing.T) {
		ref := postRefBody(posts["public"].id)
		big := make([]byte, testQuota+1)
		stranger.presign(ref, "images/x.png", testPNG(t, 8, 8, color.White), "image/png", 403, media.CodeForbidden)
		stranger.call("GET", "/api/v1/contentkit/media/post/"+posts["ppv"].id+"?editor", nil, "", 200) // a viewer, not an editor
		if r := h.read(stranger, kindPost, posts["ppv"].id, true).res; slices.ContainsFunc(r.Files, func(f media.FileInfo) bool { return f.Upload }) {
			t.Fatalf("a stranger's editor read lists uploads: %+v", r.Files)
		}
		owner.presign(ref, "images/big.png", big, "image/png", 413, media.CodeQuota)
		admin.presign(ref, "images/big.png", big, "image/png", 200, "") // site admins are exempt

		// Quota binds at commit too: an upload that got past presign (here an
		// exempt one) cannot be committed past the channel's quota, and the
		// refused commit writes nothing.
		_, blob := admin.upload(ref, "images/big.png", big, "image/png", 200)
		used, _, err := h.srv.media.limiter.Usage(ctx, tenant, channelOwner(channelID))
		if err != nil {
			t.Fatal(err)
		}
		over := owner.commit(ref, []media.Op{{Op: media.OpPut, Path: "images/big.png", Blob: blob}}, 413)
		if over["code"] != media.CodeQuota {
			t.Fatalf("commit over quota: %v", over)
		}
		after, _, err := h.srv.media.limiter.Usage(ctx, tenant, channelOwner(channelID))
		if err != nil || after != used {
			t.Fatalf("usage %d after a refused commit, was %d (%v)", after, used, err)
		}
		if n := len(uploads(h.read(owner, kindPost, posts["public"].id, true).res)); n != 2 {
			t.Fatalf("refused commit changed the item: %d uploads", n)
		}

		spam := spammer.call("POST", "/api/v1/channels", map[string]any{"slug": "spam", "name": "Spam"}, "", 201)
		sp := newPost(spammer, map[string]any{"channel_id": spam["id"], "title": "s", "body": "s"})
		spamRef := postRefBody(sp.id)
		for i := range testFilesPerHour {
			spammer.presign(spamRef, fmt.Sprintf("images/%d.png", i), []byte(fmt.Sprintf("file %d", i)), "image/png", 200, "")
		}
		spammer.presign(spamRef, "images/last.png", []byte("one too many"), "image/png", 429, media.CodeRate)
	})

	t.Run("public images", func(t *testing.T) {
		chRef := map[string]string{"kind": kindChannel, "id": channelID}
		// Channel editors post but do not manage the channel's look.
		editor.presign(chRef, "avatar.png", testPNG(t, 300, 300, color.White), "image/png", 403, media.CodeForbidden)
		owner.put(chRef, "avatar.png", testPNG(t, 300, 300, color.RGBA{0, 160, 0, 255}), "image/png")
		// The API lists the preset's published renditions once the worker
		// renders them; every width is served (a width past the image renders
		// at its width) under a generation-suffixed name.
		channelImage := func(preset string) func() any {
			return func() any { return anon.call("GET", "/api/v1/channels/"+channelID, nil, "", 200)[preset] }
		}
		avatar := h.published(t, channelImage("avatar"), nil)
		if len(avatar) != 2 || !strings.Contains(avatar[0], "/v1/"+tenant+"/channel/"+channelID+"/public/avatar-128-") {
			t.Fatalf("channel avatar %v", avatar)
		}
		for _, u := range avatar {
			h.waitPublic(t, u)
		}

		stranger.presign(map[string]string{"kind": kindUser, "id": owner.id}, "avatar.png", testPNG(t, 64, 64, color.White), "image/png", 403, media.CodeForbidden)
		// The SDK crops before upload: the put carries the edit, whose height
		// the 1:1 preset fits.
		reply := stranger.putEdited(map[string]string{"kind": kindUser, "id": stranger.id}, "avatar.png", testPNG(t, 600, 600, color.Black),
			&media.Edit{Crop: &media.Crop{X: 100, Y: 150, W: 400, H: 1}, Rotate: 90})
		if e := uploadAt(reply.Files, "avatar.png").Edit; e == nil || e.Crop.H != 400 || e.Rotate != 90 {
			t.Fatalf("user avatar commit %+v", reply)
		}
		for _, u := range h.published(t, func() any { return stranger.call("GET", "/api/v1/me", nil, "", 200)["user"].(map[string]any)["avatar"] }, nil) {
			h.waitPublic(t, u)
		}
	})

	t.Run("mixed images and videos", func(t *testing.T) {
		p := newPost(owner, map[string]any{"channel_id": channelID, "title": "mixed", "body": "text", "access_policy": "membership"})
		ref := postRefBody(p.id)

		// Each upload path keeps its own byte cap.
		big := make([]byte, maxImageBytes+1)
		admin.presign(ref, "images/big.png", big, "image/png", 413, media.CodeTooLarge)
		admin.presign(ref, "videos/big.mp4", big, "video/mp4", 200, "")
		admin.presign(ref, "images/clip.mp4", big, "video/mp4", 415, media.CodeType)
		if !h.video {
			t.Skip("ffmpeg not installed")
		}

		owner.put(ref, "images/still.png", testPNG(t, 640, 360, color.RGBA{10, 120, 90, 255}), "image/png")
		owner.put(ref, "videos/clip.mp4", testVideo(t, 320, 240), "video/mp4")
		h.waitReady(owner, kindPost, p.id)
		blobs := map[string]string{}
		for _, f := range uploads(h.read(owner, kindPost, p.id, true).res) {
			blobs[f.Path] = h.blob(p.id, f.Path)
		}
		// An upload path's Max: one put past it refuses the commit.
		ceiling := func(dir, ext string, max int, blob string) {
			var ops []media.Op
			for i := range max {
				ops = append(ops, media.Op{Op: media.OpPut, Path: fmt.Sprintf("%s/n%02d.%s", dir, i, ext), Blob: blob})
			}
			if out := owner.commit(ref, ops, 409); out["code"] != media.CodeTooManyFiles {
				t.Fatalf("%s ceiling: %v", dir, out)
			}
		}
		ceiling("images", "png", maxPostImages, blobs["images/still.png"])
		ceiling("videos", "mp4", maxPostVideos, blobs["videos/clip.mp4"])

		// Entitled: the post shows its images, then its videos. The master and
		// media playlists come from the read API, the byte ranges from
		// media-gateway under the item cookie.
		r := h.read(member, kindPost, p.id, false)
		if r.res.Access != media.AccessFull || !slices.Equal(r.res.HLS, []string{"hls/clip/"}) || r.res.Files[0].Path != "large/still.webp" {
			t.Fatalf("member read %+v", r.res)
		}
		base := "/api/v1/contentkit/media/post/" + p.id + "/hls/hls/clip/"
		master := h.playlist(member, base+"master.m3u8", 200)
		var variants []string
		for _, line := range strings.Split(master, "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				variants = append(variants, line)
			}
		}
		if len(variants) == 0 || !strings.Contains(master, "RESOLUTION=320x240") {
			t.Fatalf("master playlist:\n%s", master)
		}
		// Variant URIs are relative to the master, as hls.js resolves them.
		masterURL, _ := url.Parse(base + "master.m3u8")
		variant, _ := url.Parse(variants[0])
		track := masterURL.ResolveReference(variant).Path
		rendition := h.playlist(member, track, 200)
		blob, first := "", ""
		for _, line := range strings.Split(rendition, "\n") {
			if rng, ok := strings.CutPrefix(line, "#EXT-X-BYTERANGE:"); ok && first == "" {
				first = rng
			} else if strings.HasPrefix(line, "http") {
				blob = line
			}
		}
		if blob == "" || first == "" || !strings.HasPrefix(blob, h.cfg.Media.URL) || strings.Contains(blob, "?t=") {
			t.Fatalf("media playlist:\n%s", rendition)
		}
		length, offset, _ := strings.Cut(first, "@")
		n, _ := strconv.ParseInt(length, 10, 64)
		o, _ := strconv.ParseInt(offset, 10, 64)
		h.expectRange(t, blob, r.cookie, o, n, 206)
		h.expectRange(t, blob, "", o, n, 404)

		// Locked: the image's blurred preview, then locked files; no playlist.
		l := h.read(stranger, kindPost, p.id, false)
		if l.res.Access != media.AccessNone || len(l.res.Previews) != 1 || l.res.HLS != nil ||
			slices.ContainsFunc(l.res.Files, func(f media.FileInfo) bool { return !f.Locked || f.URL != "" }) {
			t.Fatalf("stranger read %+v", l.res)
		}
		h.playlist(stranger, base+"master.m3u8", 404)
		h.playlist(stranger, track, 404)
	})

	t.Run("image edits", func(t *testing.T) {
		p := posts["public"]
		ref := postRefBody(p.id)
		edit := &media.Edit{Crop: &media.Crop{W: 320, H: 240}, Rotate: 90}
		stranger.commit(ref, []media.Op{{Op: media.OpEdit, Path: "images/a.png", Edit: edit}}, 403)
		before := h.read(owner, kindPost, p.id, false).res.Files[0]
		owner.commit(ref, []media.Op{{Op: media.OpEdit, Path: "images/a.png", Edit: edit}}, 200)
		// Renditions re-derive through crop then rotate.
		eventually(t, "edited rendition", func() bool {
			f := h.read(owner, kindPost, p.id, false).res.Files[0]
			return f.URL != before.URL && f.W == 240 && f.H == 320
		})
		ed := h.read(owner, kindPost, p.id, true)
		up := uploadAt(ed.res.Files, "images/a.png")
		if up.Edit == nil || up.Edit.Rotate != 90 || up.Edit.Crop.W != 320 || up.W != 640 || up.H != 480 {
			t.Fatalf("edited upload %+v", up)
		}
		eventually(t, "editor view", func() bool {
			return uploadAt(h.read(owner, kindPost, p.id, true).res.Files, "images/a.png").EditorURL != ""
		})
		h.expectFetch(t, h.read(owner, kindPost, p.id, false).res.Files[0].URL, ed.cookie, 200)
		// Readers with full access get neither the uploads nor their edits.
		for _, v := range []peer{stranger, anon, member} {
			r := h.read(v, kindPost, p.id, true).res
			if r.Access != media.AccessFull || len(uploads(r)) != 0 || slices.ContainsFunc(r.Files, func(f media.FileInfo) bool { return f.Edit != nil || f.EditorURL != "" }) {
				t.Fatalf("%s got editor data: %+v", v.name, r.Files)
			}
		}
	})

	t.Run("channel cover: crop, rotate and re-crop without re-upload", func(t *testing.T) {
		chRef := map[string]string{"kind": kindChannel, "id": channelID}
		coverImage := func() any { return anon.call("GET", "/api/v1/channels/"+channelID, nil, "", 200)["cover"] }
		if before := h.publicURLs(coverImage()); len(before) != 0 {
			t.Fatalf("cover listed before upload: %v", before)
		}
		owner.put(chRef, "cover.png", testPNG(t, 1600, 1600, color.RGBA{200, 120, 0, 255}), "image/png")
		cover := h.published(t, coverImage, nil)
		if len(cover) != 2 {
			t.Fatalf("cover preset %v", cover)
		}
		widest := cover[len(cover)-1]
		h.waitPublic(t, widest)
		first := publicSum(widest)
		// Narrower than the cover's MinWidth: refused, the served cover stays.
		if out := owner.commit(chRef, []media.Op{{Op: media.OpEdit, Path: "cover.png", Edit: &media.Edit{Crop: &media.Crop{W: 500, H: 1}}}}, 422); out["code"] != media.CodeImageTooSmall {
			t.Fatalf("narrow cover edit %v", out)
		}
		// Rotated a quarter turn: the crop is in source pixels and its height
		// follows at 1:3, so the output is 1590x530.
		edit := []media.Op{{Op: media.OpEdit, Path: "cover.png", Edit: &media.Edit{Crop: &media.Crop{X: 100, W: 530, H: 1}, Rotate: 90}}}
		editor.commit(chRef, edit, 403)
		var reply media.CommitReply
		decode(t, owner.commit(chRef, edit, 200), &reply)
		if crop := uploadAt(reply.Files, "cover.png").Edit.Crop; crop.H != 1590 {
			t.Fatalf("cover edit %+v", crop)
		}
		recropped := h.published(t, coverImage, cover)
		if s := publicSum(recropped[len(recropped)-1]); s == "" || s == first {
			t.Fatalf("re-cropped cover %v serves %q", recropped, s)
		}
	})

	t.Run("composer drafts: publish with media, discard leaves nothing", func(t *testing.T) {
		listed := func(id string) bool {
			res, raw := anon.do("GET", "/api/v1/posts?channel_id="+channelID+"&limit=50", nil, "")
			var list []post
			if res.StatusCode != 200 || json.Unmarshal(raw, &list) != nil {
				t.Fatalf("list %d: %s", res.StatusCode, raw)
			}
			return slices.ContainsFunc(list, func(p post) bool { return p.ID == id })
		}
		draft := func() testPost { return newPost(owner, map[string]any{"channel_id": channelID, "draft": true}) }

		d := draft()
		ref := postRefBody(d.id)
		owner.put(ref, "images/a.png", testPNG(t, 400, 300, color.RGBA{1, 2, 3, 255}), "image/png")
		// Only its author uploads to a draft.
		editor.presign(ref, "images/b.png", testPNG(t, 10, 10, color.RGBA{9, 9, 9, 255}), "image/png", 403, media.CodeForbidden)
		h.waitReady(owner, kindPost, d.id)
		stranger.call("GET", "/api/v1/contentkit/media/post/"+d.id, nil, "", 404)
		editor.call("GET", "/api/v1/contentkit/media/post/"+d.id, nil, "", 404)
		owner.call("GET", d.path(), nil, "", 404)
		owner.call("PATCH", d.path(), map[string]any{"title": "x"}, "", 404)
		if listed(d.id) {
			t.Fatal("draft listed")
		}
		editor.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "draft_id": d.id, "title": "t", "body": "b"}, "", 404)
		pub := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "draft_id": d.id, "title": "Composed", "body": "b", "access_policy": "membership"}, "", 201)
		if pub["id"] != d.id || pub["code"] != d.code || pub["slug"] != "composed" || pub["state"] != statePublished || !listed(d.id) {
			t.Fatalf("published draft %v", pub)
		}
		// Publishing exposes the preview.
		eventually(t, "published draft's preview", func() bool {
			r := h.read(stranger, kindPost, d.id, false).res
			return r.Access == media.AccessNone && len(r.Previews) == 1 && publicSum(r.Previews[0]) != ""
		})

		gone := draft()
		gref := postRefBody(gone.id)
		owner.put(gref, "images/g.png", testPNG(t, 300, 300, color.RGBA{4, 5, 6, 255}), "image/png")
		owner.upload(gref, "images/h.png", testPNG(t, 200, 200, color.RGBA{7, 8, 9, 255}), "image/png", 200) // uploaded, never committed
		editor.call("DELETE", gone.path(), nil, "", 404)
		owner.call("DELETE", gone.path(), nil, "", 204)
		var rows int
		if err := h.srv.posts.pool.QueryRow(ctx, `SELECT count(*) FROM `+h.srv.posts.table+` WHERE id=$1`, gone.id).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("discarded draft row: %d %v", rows, err)
		}
		var leftover []string
		func() {
			defer func() {
				if t.Failed() {
					t.Logf("left in the discarded draft's folder: %v", leftover)
				}
			}()
			eventually(t, "discarded draft folder erased", func() bool {
				prefix := fmt.Sprintf("%s/post/%s/", tenant, gone.id)
				leftover = h.keys(prefix)
				return h.erased(prefix)
			})
		}()

		stale := draft()
		if _, err := h.srv.posts.pool.Exec(ctx, `UPDATE `+h.srv.posts.table+` SET created_at=NOW()-INTERVAL '25 hours' WHERE id=$1`, stale.id); err != nil {
			t.Fatal(err)
		}
		fresh := draft()
		if err := (&postDraftSweepWorker{api: h.srv.posts}).Work(ctx, nil); err != nil {
			t.Fatal(err)
		}
		var left []string
		rs, _ := h.srv.posts.pool.Query(ctx, `SELECT id::text FROM `+h.srv.posts.table+` WHERE id=ANY($1::uuid[])`, []string{stale.id, fresh.id})
		for rs.Next() {
			var v string
			_ = rs.Scan(&v)
			left = append(left, v)
		}
		rs.Close()
		if !slices.Equal(left, []string{fresh.id}) {
			t.Fatalf("sweep left %v, want only the fresh draft %s", left, fresh.id)
		}
	})

	t.Run("publishing: editors only until processed; a failed file holds it back", func(t *testing.T) {
		if !h.video {
			t.Skip("the media worker needs ffmpeg")
		}
		d := newPost(owner, map[string]any{"channel_id": channelID, "draft": true})
		ref := postRefBody(d.id)
		owner.put(ref, "videos/clip.mp4", testVideo(t, 320, 240), "video/mp4")
		owner.put(ref, "videos/bad.mp4", []byte("not a video at all"), "video/mp4")
		pub := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "draft_id": d.id, "title": "Held", "body": "b"}, "", 201)
		if pub["state"] != statePublishing {
			t.Fatalf("published with media processing: %v", pub)
		}
		hidden := func() {
			t.Helper()
			for _, p := range []peer{stranger, member, anon} {
				p.call("GET", d.path(), nil, "", 404)
				p.call("GET", "/api/v1/contentkit/media/post/"+d.id, nil, "", 404)
			}
			res, raw := anon.do("GET", "/api/v1/posts?channel_id="+channelID+"&limit=50", nil, "")
			if res.StatusCode != 200 || strings.Contains(string(raw), d.id) {
				t.Fatalf("a publishing post listed: %d", res.StatusCode)
			}
		}
		hidden()
		if got := editor.call("GET", d.path(), nil, "", 200); got["state"] != statePublishing {
			t.Fatalf("the channel editor's view %v", got)
		}
		eventually(t, "the broken file failed", func() bool {
			r, _ := owner.call("GET", d.path(), nil, "", 200)["media_readiness"].(map[string]any)
			failed, _ := r["failed"].([]any)
			return r["state"] == media.StateFailed && len(failed) == 1 && failed[0] == "videos/bad.mp4"
		})
		hidden()
		owner.commit(ref, []media.Op{{Op: media.OpRemove, Path: "videos/bad.mp4"}}, 200)
		eventually(t, "published once processed", func() bool {
			res, _ := anon.do("GET", d.path(), nil, "")
			return res.StatusCode == 200
		})
		eventually(t, "the item exposed", func() bool {
			man, _, err := h.srv.media.manifests.Get(ctx, postRef(d.id))
			return err == nil && !man.Hidden
		})
	})

	t.Run("content urls", func(t *testing.T) {
		h.testContentURLs(t, owner, anon, channelID)
	})

	t.Run("listings cost the same for any number of items", func(t *testing.T) {
		h.testListingCost(t, posts["ppv"], buyer)
	})

	t.Run("deleting a sold post archives it: its buyer keeps it, nobody is refunded", func(t *testing.T) {
		p := posts["ppv"]
		folder := fmt.Sprintf("%s/post/%s/", tenant, p.id)
		preview := h.read(stranger, kindPost, p.id, false).res.Previews[0]
		price := map[string]any{"price_id": stranger.call("GET", p.path(), nil, "", 200)["offers"].([]any)[0].(map[string]any)["price_id"]}
		owner.call("DELETE", p.path(), nil, "", 204)

		product, err := h.srv.billing.client.GetProductByKey(ctx, postResource(p.id))
		if err != nil || !product.Archived {
			t.Fatalf("product after deletion: %+v %v", product, err)
		}
		if offers, err := h.srv.billing.offers(ctx, billing.OfferPermanent, []string{postResource(p.id)}, 10); err != nil || len(offers) != 0 {
			t.Fatalf("a deleted post is still offered: %v %v", offers, err)
		}
		customer, _ := customerID(buyer.id)
		payments, err := h.srv.billing.client.ListPayments(ctx, billing.PaymentListParams{CustomerID: customer, PageRequest: billing.PageRequest{Limit: 50}})
		if err != nil {
			t.Fatal(err)
		}
		bought := false
		for _, pay := range payments.Items {
			if pay.Kind != billing.PaymentCharge || pay.Status != billing.PaymentSucceeded || pay.AmountRefunded != 0 {
				t.Fatalf("the buyer was refunded: %+v", pay)
			}
			bought = bought || pay.Product != nil && pay.Product.Key == postResource(p.id)
		}
		if !bought {
			t.Fatalf("no purchase of the post among %+v", payments.Items)
		}
		if refunds := h.stripe.requested("POST /v1/refunds"); refunds != 0 {
			t.Fatalf("%d provider refunds", refunds)
		}

		// The buyer still opens the post, its media and its library entry.
		got := buyer.call("GET", p.path(), nil, "", 200)
		if got["can_read"] != true || got["purchased"] != true || got["can_edit"] != false || got["body"] != "text" || len(got["offers"].([]any)) != 0 {
			t.Fatalf("the buyer's deleted post %v", got)
		}
		r := h.read(buyer, kindPost, p.id, false)
		if r.res.Access != media.AccessFull || r.cookie == "" || len(r.res.Files) != 2 {
			t.Fatalf("the buyer's media %s (cookie %t): %+v", r.res.Access, r.cookie != "", r.res.Files)
		}
		for _, f := range r.res.Files {
			h.expectFetch(t, f.URL, r.cookie, 200)
		}
		var library struct {
			Purchased []post `json:"purchased_posts"`
		}
		decode(t, buyer.call("GET", "/api/v1/me", nil, "", 200), &library)
		if !slices.ContainsFunc(library.Purchased, func(l post) bool { return l.ID == p.id && l.CanRead }) {
			t.Fatalf("library lost the deleted post: %+v", library.Purchased)
		}
		buyer.call("POST", p.path()+"/checkout", price, "", 404)

		// Everyone else: gone from the post, its media, feeds and its link.
		for _, v := range []peer{owner, editor, admin, member, stranger, anon} {
			v.call("GET", p.path(), nil, "", 404)
			v.call("GET", "/api/v1/contentkit/media/post/"+p.id, nil, "", 404)
			for _, feed := range []string{"/api/v1/posts?limit=50", "/api/v1/posts?channel_id=" + channelID + "&limit=50"} {
				if slices.ContainsFunc(v.callList(feed), func(l any) bool { return l.(map[string]any)["id"] == p.id }) {
					t.Fatalf("%s's %s lists the deleted post", v.name, feed)
				}
			}
		}
		stranger.call("POST", p.path()+"/checkout", price, "", 404)
		if res, _ := anon.page("/p/" + p.code + "/ppv"); res.StatusCode != 410 {
			t.Fatalf("deleted post page %d", res.StatusCode)
		}
		eventually(t, "public preview withdrawn", func() bool { return publicSum(preview) == "" })
		if h.erased(folder) {
			t.Fatal("a sold post's folder was erased")
		}
	})

	t.Run("deleting a never-offered post erases its folder and releases quota", func(t *testing.T) {
		p := posts["public"]
		used, _, err := h.srv.media.limiter.Usage(ctx, tenant, channelOwner(channelID))
		if err != nil || used <= 0 {
			t.Fatalf("usage before delete: %d %v", used, err)
		}
		owner.call("DELETE", p.path(), nil, "", 204)
		anon.call("GET", p.path(), nil, "", 404)
		prefix := fmt.Sprintf("%s/post/%s/", tenant, p.id)
		eventually(t, "folder erased", func() bool { return h.erased(prefix) })
		eventually(t, "quota released", func() bool {
			after, _, err := h.srv.media.limiter.Usage(ctx, tenant, channelOwner(channelID))
			return err == nil && after < used
		})
	})
}

// testContentURLs proves /p/{CODE}/{slug}: the API's links, the router's
// canonical redirects, the page's canonical tags, removal and the backfill.
func (h *mediaHarness) testContentURLs(t *testing.T, owner, anon peer, channelID string) {
	created := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "title": "Hello, World!", "body": "b"}, "", 201)
	code, id := created["code"].(string), created["id"].(string)
	if c, err := contenturl.ParseCode(code); err != nil || string(c) != code || created["slug"] != "hello-world" {
		t.Fatalf("created post link %v", created)
	}
	page := "/p/" + code + "/hello-world"

	// The canonical page: the app shell with its canonical URL and title.
	res, body := anon.page(page)
	if res.StatusCode != 200 || res.Header.Get("Link") != "<"+h.base+page+`>; rel="canonical"` ||
		!strings.Contains(body, `<link rel="canonical" href="`+h.base+page+`" />`) ||
		!strings.Contains(body, `<meta property="og:url" content="`+h.base+page+`" />`) ||
		!strings.Contains(body, `<meta property="og:title" content="Hello, World!" />`) || !strings.Contains(body, `<div id="root">`) {
		t.Fatalf("canonical page %d %v:\n%s", res.StatusCode, res.Header, body)
	}
	// The slug is decoration: a missing or stale slug, another spelling of
	// the code and a trailing slash redirect, keeping the query.
	redirects := map[string]string{
		"/p/" + code:                                 page,
		"/p/" + code + "/old-title":                  page,
		"/p/" + strings.ToLower(code) + "/x?ref=feed": page + "?ref=feed",
		page + "/":                                   page,
	}
	for from, to := range redirects {
		if res, _ := anon.page(from); res.StatusCode != 301 || res.Header.Get("Location") != to {
			t.Fatalf("GET %s = %d → %q, want 301 → %s", from, res.StatusCode, res.Header.Get("Location"), to)
		}
	}
	// The API takes any spelling of the code.
	if got := anon.call("GET", "/api/v1/posts/"+strings.ToLower(code), nil, "", 200); got["id"] != id || got["code"] != code {
		t.Fatalf("post by code %v", got)
	}
	anon.call("GET", "/api/v1/posts/"+id, nil, "", 404) // ids are not URLs
	anon.call("GET", "/api/v1/channels/studio/posts/hello-world", nil, "", 404)

	// A new title moves the slug; the old URL redirects to it.
	renamed := owner.call("PATCH", "/api/v1/posts/"+code, map[string]any{"title": "Goodbye"}, "", 200)
	if renamed["slug"] != "goodbye" || renamed["code"] != code {
		t.Fatalf("renamed %v", renamed)
	}
	if res, _ := anon.page(page); res.StatusCode != 301 || res.Header.Get("Location") != "/p/"+code+"/goodbye" {
		t.Fatalf("old slug %d → %q", res.StatusCode, res.Header.Get("Location"))
	}
	// Listings carry the links.
	var list []post
	decode(t, anon.callList("/api/v1/posts?channel_id="+channelID+"&limit=50"), &list)
	if i := slices.IndexFunc(list, func(p post) bool { return p.ID == id }); i < 0 || list[i].Code != code || list[i].Slug != "goodbye" {
		t.Fatalf("listed link %+v", list)
	}

	// Drafts and unknown codes are the app's 404 page: no redirect, no slug.
	draft := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "draft": true}, "", 201)["code"].(string)
	for _, path := range []string{"/p/" + draft, "/p/ZZZZZZZZZ/x"} {
		if res, body := anon.page(path); res.StatusCode != 200 || strings.Contains(body, "canonical") {
			t.Fatalf("GET %s = %d:\n%s", path, res.StatusCode, body)
		}
	}
	// A deleted post is gone.
	owner.call("DELETE", "/api/v1/posts/"+code, nil, "", 204)
	if res, body := anon.page("/p/" + code + "/goodbye"); res.StatusCode != 410 || !strings.Contains(body, `<div id="root">`) {
		t.Fatalf("deleted post page %d", res.StatusCode)
	}

	// The backfill registers posts that have no code, and changes no code.
	var orphan string
	if err := h.srv.posts.pool.QueryRow(h.ctx, `INSERT INTO `+h.srv.posts.table+`(author_id,channel_id,title,body,state,published_at) VALUES($1,$2,'Imported post','b','published',NOW()) RETURNING id::text`,
		owner.id, channelID).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if n, err := backfillPostURLs(h.ctx, h.cfg, h.srv.posts.pool); err != nil || n == 0 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	links, err := h.srv.posts.urls.Links(h.ctx, []contentref.ContentRef{postRef(orphan), postRef(id)})
	if err != nil || links[postRef(orphan).Key()].Slug != "imported-post" || string(links[postRef(id).Key()].Code) != code {
		t.Fatalf("backfilled links %v %v", links, err)
	}
}

type mediaHarness struct {
	t      *testing.T
	ctx    context.Context
	cfg    Config
	srv    *server
	base   string
	s3     *s3.Client
	bucket string
	stripe *fakeStripe
	hook   string
	owner  peer
	video  bool // ffmpeg present: media-worker runs
	calls  struct{ auth, billing atomic.Int64 }
}

func newMediaHarness(t *testing.T) *mediaHarness {
	dbURL, endpoint := os.Getenv("DEMO_TEST_DATABASE_URL"), os.Getenv("DEMO_TEST_S3_ENDPOINT")
	if dbURL == "" || endpoint == "" {
		t.Skip("DEMO_TEST_DATABASE_URL and DEMO_TEST_S3_ENDPOINT are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	h := &mediaHarness{t: t, ctx: ctx, stripe: &fakeStripe{}}

	pool := testDatabase(t, ctx, dbURL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + listener.Addr().String()

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	tokenKey := "k1:" + base64.StdEncoding.EncodeToString(key)
	h.bucket = "demo-media-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	access, secret := os.Getenv("DEMO_TEST_S3_ACCESS_KEY"), os.Getenv("DEMO_TEST_S3_SECRET_KEY")
	store, err := mediaS3.New(mediaS3.Config{Bucket: h.bucket, Endpoint: endpoint, AccessKeyID: access, SecretAccessKey: secret, UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	h.s3 = store.Client()
	if _, err = h.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &h.bucket}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.removeBucket)
	gateway := startMediaGateway(t, endpoint, h.bucket, access, secret, tokenKey)

	h.cfg = Config{MembershipHours: 720, DatabaseURL: pool.Config().ConnString(), PublicURL: h.base, ReturnOrigins: []string{h.base}, AuthIssuer: h.base, AuthAudience: "demo-media-test",
		Merchant: testMerchant(openrails.StripePSP{AccountID: testStripeAccount, SecretKey: "sk_test_fake_only", WebhookSigningSecret: testWebhookSecret}.PSPConfig()), AuthKeysPath: "",
		Media: mediaConfig{S3Endpoint: endpoint, S3PublicEndpoint: endpoint, S3Bucket: h.bucket, S3Region: "us-east-1",
			S3AccessKeyID: access, S3SecretKey: secret, URL: gateway, Delivery: media.DeliverCookie, CookieDomain: "localhost",
			TokenKey: tokenKey, FilesPerHour: testFilesPerHour, BytesPerDay: 1 << 30, ChannelQuota: testQuota}}
	if err = initializeDatabase(ctx, h.cfg, pool); err != nil {
		t.Fatal(err)
	}
	if h.video = hasFFmpeg(t); !h.video {
		t.Skip("the media worker needs ffmpeg")
	}
	startMediaWorker(t, ctx, h.cfg, pool)
	if h.srv, err = startServer(ctx, h.cfg, pool, billingOptions{Test: func(d *openrails.Deps) { d.StripeTransport = h.stripe }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.srv.Close)
	h.srv.auth.observe = func(ctx context.Context) { inRequest(ctx, &h.calls.auth) }
	h.srv.billing.reads = countingReads{entitlementReads: h.srv.billing.reads, n: &h.calls.billing}
	go func() { _ = h.srv.app.Listener(listener) }()
	t.Cleanup(func() { _ = h.srv.app.Shutdown() })
	h.hook = "/billing/v1/webhooks/stripe/" + testStripeAccount
	return h
}

// testDatabase creates a uniquely named database on the admin connection and
// drops it afterwards.
func testDatabase(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	name := "demo_media_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(cleanup)
	})
	u, _ := url.Parse(adminURL)
	u.Path = "/" + name
	poolConfig, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Tracer = queryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// startMediaGateway runs the real media-gateway binary with the app's key.
func startMediaGateway(t *testing.T, endpoint, bucket, access, secret, tokenKey string) string {
	bin := os.Getenv("DEMO_TEST_MEDIA_GATEWAY")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "media-gateway")
		out, err := exec.Command("go", "build", "-o", bin, "github.com/open-rails/contentkit/cmd/media-gateway").CombinedOutput()
		if err != nil {
			t.Fatalf("build media-gateway: %v\n%s", err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MEDIA_GATEWAY_LISTEN="+addr, "MEDIA_GATEWAY_S3_ENDPOINT="+endpoint, "MEDIA_GATEWAY_S3_BUCKET="+bucket,
		"MEDIA_GATEWAY_S3_ACCESS_KEY_ID="+access, "MEDIA_GATEWAY_S3_SECRET_ACCESS_KEY="+secret, "MEDIA_GATEWAY_TOKEN_KEY="+tokenKey,
		"MEDIA_GATEWAY_HOSTS=127.0.0.1="+tenant, "MEDIA_GATEWAY_CORS_ORIGINS=http://127.0.0.1")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	base := "http://" + addr
	eventually(t, "media-gateway listening", func() bool {
		res, err := http.Get(base + "/healthz")
		if err == nil {
			res.Body.Close()
		}
		return err == nil && res.StatusCode == 200
	})
	return base
}

func hasFFmpeg(t *testing.T) bool {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("DEMO_TEST_FFMPEG") == "1" {
				t.Fatalf("%s is required: %v", tool, err)
			}
			t.Logf("%s not found; the video proof is skipped", tool)
			return false
		}
	}
	return true
}

// startMediaWorker runs the app's media worker (`onlydemo media-worker`) on
// the test database: placement, images and video (H.264 only, for speed).
func startMediaWorker(t *testing.T, ctx context.Context, cfg Config, pool *pgxpool.Pool) {
	w, err := newMediaWorker(ctx, cfg, pool, func(wc *worker.Config) error {
		wc.TempDir, wc.Threads, wc.ShutdownGrace = t.TempDir(), 2, time.Second
		wc.VideoCodecs = []media.Codec{media.CodecH264}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })
}

// testVideo is a 2 s w×h H.264/AAC MP4.
func testVideo(t *testing.T, w, h int) []byte {
	out := filepath.Join(t.TempDir(), "clip.mp4")
	b, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=%dx%d:rate=10:duration=2", w, h),
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-threads", "1", "-c:a", "aac", "-shortest", "-y", out).CombinedOutput()
	if err != nil {
		t.Fatalf("video fixture: %v: %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (h *mediaHarness) removeBucket() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := s3.NewListObjectsV2Paginator(h.s3, &s3.ListObjectsV2Input{Bucket: &h.bucket})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			break
		}
		var ids []types.ObjectIdentifier
		for _, o := range page.Contents {
			ids = append(ids, types.ObjectIdentifier{Key: o.Key})
		}
		if len(ids) > 0 {
			_, _ = h.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &h.bucket, Delete: &types.Delete{Objects: ids}})
		}
	}
	_, _ = h.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &h.bucket})
}

func (h *mediaHarness) keys(prefix string) []string {
	out, err := h.s3.ListObjectsV2(h.ctx, &s3.ListObjectsV2Input{Bucket: &h.bucket, Prefix: &prefix})
	if err != nil {
		h.t.Fatal(err)
	}
	var keys []string
	for _, o := range out.Contents {
		keys = append(keys, *o.Key)
	}
	return keys
}

// erased reports a deleted item's folder: only ContentKit's deletion
// tombstone is left.
func (h *mediaHarness) erased(prefix string) bool {
	return slices.Equal(h.keys(prefix), []string{prefix + "manifest.json"})
}

func (h *mediaHarness) client(ip string) *http.Client {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type peer struct {
	h      *mediaHarness
	name   string
	id     string
	token  string
	client *http.Client
}

func (h *mediaHarness) register(name, ip string) peer {
	p := peer{h: h, name: name, client: h.client(ip)}
	res, raw := p.do("POST", "/auth/v1/register", map[string]any{"identifier": name + "@example.test", "username": name, "password": testPassword}, "")
	if res.StatusCode/100 != 2 {
		h.t.Fatalf("register %s: %d %s", name, res.StatusCode, raw)
	}
	login := p.call("POST", "/auth/v1/password/login", map[string]any{"identifier": name + "@example.test", "password": testPassword}, "", 200)
	tokens, _ := login["token_set"].(map[string]any)
	p.token, _ = tokens["access_token"].(string)
	user, err := h.srv.auth.client.User(h.ctx, iam.UserByUsername(name))
	if err != nil || p.token == "" {
		h.t.Fatalf("register %s: %v %v", name, err, login)
	}
	p.id = user.ID
	return p
}

func (p peer) do(method, path string, body any, key string) (*http.Response, []byte) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			p.h.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(p.h.ctx, method, p.h.base+path, r)
	if err != nil {
		p.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := p.client.Do(req)
	if err != nil {
		p.h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res, raw
}

func (p peer) call(method, path string, body any, key string, status int) map[string]any {
	p.h.t.Helper()
	res, raw := p.do(method, path, body, key)
	if res.StatusCode != status {
		p.h.t.Fatalf("%s: %s %s = %d, want %d: %s", p.name, method, path, res.StatusCode, status, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &out); err != nil {
			p.h.t.Fatal(err)
		}
	}
	return out
}

// callList GETs a JSON array.
func (p peer) callList(path string) []any {
	p.h.t.Helper()
	res, raw := p.do("GET", path, nil, "")
	var out []any
	if res.StatusCode != 200 || json.Unmarshal(raw, &out) != nil {
		p.h.t.Fatalf("%s: GET %s = %d: %s", p.name, path, res.StatusCode, raw)
	}
	return out
}

// page GETs a browser page without following redirects.
func (p peer) page(path string) (*http.Response, string) {
	p.h.t.Helper()
	req, err := http.NewRequestWithContext(p.h.ctx, "GET", p.h.base+path, nil)
	if err != nil {
		p.h.t.Fatal(err)
	}
	res, err := p.client.Do(req)
	if err != nil {
		p.h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res, string(raw)
}

func decode(t *testing.T, v any, into any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(raw, into)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func postRefBody(id string) map[string]string {
	return map[string]string{"kind": kindPost, "id": id}
}

// presign asks for an upload plan and checks its status and error code.
func (p peer) presign(ref map[string]string, path string, data []byte, typ string, status int, code string) map[string]any {
	p.h.t.Helper()
	sum := sha256.Sum256(data)
	out := p.call("POST", "/api/v1/contentkit/media/upload/presign", map[string]any{"ref": ref, "path": path, "type": typ, "size": len(data), "sha256": hex.EncodeToString(sum[:])}, "", status)
	if code != "" && out["code"] != code {
		p.h.t.Fatalf("%s presign code %v, want %s", p.name, out["code"], code)
	}
	return out
}

// upload plays the browser SDK: presign, then a checksum-bound PUT straight
// to the bucket. It returns the path and blob to commit.
func (p peer) upload(ref map[string]string, path string, data []byte, typ string, status int) (string, string) {
	p.h.t.Helper()
	plan := p.presign(ref, path, data, typ, status, "")
	if status != 200 {
		return "", ""
	}
	if plan["exists"] != true {
		put := plan["put"].(map[string]any)
		req, err := http.NewRequestWithContext(p.h.ctx, put["method"].(string), put["url"].(string), bytes.NewReader(data))
		if err != nil {
			p.h.t.Fatal(err)
		}
		for k, v := range put["headers"].(map[string]any) {
			if !strings.EqualFold(k, "Content-Length") && !strings.EqualFold(k, "Host") {
				req.Header.Set(k, v.(string))
			}
		}
		req.ContentLength = int64(len(data))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			p.h.t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			p.h.t.Fatalf("PUT %d: %s", res.StatusCode, raw)
		}
	}
	return plan["path"].(string), plan["blob"].(string)
}

func (p peer) commit(ref map[string]string, ops []media.Op, status int) map[string]any {
	p.h.t.Helper()
	return p.call("POST", "/api/v1/contentkit/media/upload/commit", map[string]any{"ref": ref, "ops": ops, "operation_id": uuid.NewString()}, "", status)
}

// put uploads and commits one file, as the SDK's client.put does.
func (p peer) put(ref map[string]string, path string, data []byte, typ string) *media.CommitReply {
	p.h.t.Helper()
	return p.putEdited(ref, path, data, nil, typ)
}

func (p peer) putEdited(ref map[string]string, path string, data []byte, edit *media.Edit, typ ...string) *media.CommitReply {
	p.h.t.Helper()
	t := "image/png"
	if len(typ) > 0 {
		t = typ[0]
	}
	committed, blob := p.upload(ref, path, data, t, 200)
	var reply media.CommitReply
	decode(p.h.t, p.commit(ref, []media.Op{{Op: media.OpPut, Path: committed, Blob: blob, Edit: edit}}, 200), &reply)
	return &reply
}

// uploads are a read's uploads (editors only).
func uploads(r *media.ReadResult) []media.FileInfo {
	var out []media.FileInfo
	for _, f := range r.Files {
		if f.Upload {
			out = append(out, f)
		}
	}
	return out
}

func uploadAt(files []media.FileInfo, path string) media.FileInfo {
	for _, f := range files {
		if f.Upload && f.Path == path {
			return f
		}
	}
	return media.FileInfo{}
}

type readResult struct {
	res    *media.ReadResult
	cookie string
}

func (h *mediaHarness) read(p peer, kind, id string, editor bool) readResult {
	h.t.Helper()
	path := "/api/v1/contentkit/media/" + kind + "/" + id
	if editor {
		path += "?editor"
	}
	res, raw := p.do("GET", path, nil, "")
	if res.StatusCode != 200 {
		h.t.Fatalf("%s read %s/%s: %d %s", p.name, kind, id, res.StatusCode, raw)
	}
	out := readResult{res: &media.ReadResult{}}
	if err := json.Unmarshal(raw, out.res); err != nil {
		h.t.Fatal(err)
	}
	for _, c := range res.Cookies() {
		if c.Name == media.CookieName {
			out.cookie = c.Value
		}
	}
	return out
}

// blob is an upload's placed blob, from the manifest.
func (h *mediaHarness) blob(id, path string) string {
	man, _, err := h.srv.media.manifests.Get(h.ctx, postRef(id))
	if err != nil {
		h.t.Fatal(err)
	}
	f, ok := man.Get(path)
	if !ok || f.Blob == "" {
		h.t.Fatalf("no placed blob at %s", path)
	}
	return f.Blob
}

// waitReady waits until the worker settled the item: every upload placed and
// processed, or failed.
func (h *mediaHarness) waitReady(p peer, kind, id string) {
	h.t.Helper()
	eventually(h.t, fmt.Sprintf("%s %s processed", kind, id), func() bool {
		r := h.read(p, kind, id, true).res
		return r.State == media.StateReady || r.State == media.StateFailed
	})
}

// expectRead checks one viewer's read API answer and what media-gateway then
// serves: everything, or only the public preview.
func (h *mediaHarness) expectRead(t *testing.T, p peer, post testPost, level string) {
	r := h.read(p, kindPost, post.id, false)
	got := p.call("GET", post.path(), nil, "", 200)
	if got["can_read"] != (level == "full") {
		t.Fatalf("post can_read %v disagrees with media level %s", got["can_read"], level)
	}
	owner := h.read(h.owner, kindPost, post.id, false)
	switch level {
	case "full":
		if r.res.Access != media.AccessFull || r.cookie == "" || len(r.res.Files) != 2 {
			t.Fatalf("want full access with an item cookie, got %s (cookie %t): %+v", r.res.Access, r.cookie != "", r.res.Files)
		}
		for _, f := range r.res.Files {
			if f.Locked || f.Upload || strings.Contains(f.URL, "?t=") || !strings.HasPrefix(f.Path, "large/") {
				t.Fatalf("full access file %+v", f)
			}
			h.expectFetch(t, f.URL, r.cookie, 200)
			h.expectFetch(t, f.URL, "", 404)
		}
		// The cookie opens private/ only: never the manifest.
		item := r.res.Files[0].URL[:strings.Index(r.res.Files[0].URL, "/private/")]
		h.expectFetch(t, item+"/manifest.json", r.cookie, 404)
	case "preview":
		if r.res.Access != media.AccessNone || r.cookie != "" || len(r.res.Previews) != 1 {
			t.Fatalf("want the preview only, got %s (cookie %t): %+v", r.res.Access, r.cookie != "", r.res)
		}
		h.expectFetch(t, r.res.Previews[0], "", 200)
		if publicSum(r.res.Previews[0]) == publicSum(owner.res.Files[0].URL) {
			t.Fatal("the preview is the private rendition")
		}
		for i, f := range r.res.Files {
			if !f.Locked || f.URL != "" {
				t.Fatalf("locked file leaked: %+v", f)
			}
			// The creator's URL without the item cookie is no use to this viewer.
			h.expectFetch(t, owner.res.Files[i].URL, "", 404)
		}
	}
}

// playlist fetches an HLS playlist from the read API.
func (h *mediaHarness) playlist(p peer, path string, status int) string {
	h.t.Helper()
	res, raw := p.do("GET", path, nil, "")
	if res.StatusCode != status {
		h.t.Fatalf("%s GET %s = %d, want %d: %s", p.name, path, res.StatusCode, status, raw)
	}
	if status == 200 && res.Header.Get("Content-Type") != media.HLSContentType {
		h.t.Fatalf("GET %s served %s", path, res.Header.Get("Content-Type"))
	}
	return string(raw)
}

// expectRange fetches one HLS byte range from media-gateway.
func (h *mediaHarness) expectRange(t *testing.T, u, cookie string, offset, length int64, status int) {
	t.Helper()
	req, err := http.NewRequestWithContext(h.ctx, "GET", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: media.CookieName, Value: cookie})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != status || status == 206 && int64(len(body)) != length {
		t.Fatalf("GET %s range %d@%d = %d (%d bytes), want %d", u, length, offset, res.StatusCode, len(body), status)
	}
}

func (h *mediaHarness) expectFetch(t *testing.T, u, cookie string, status int) {
	t.Helper()
	req, err := http.NewRequestWithContext(h.ctx, "GET", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: media.CookieName, Value: cookie})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != status {
		t.Fatalf("GET %s = %d, want %d", u, res.StatusCode, status)
	}
	if status == 200 && res.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("GET %s served %s", u, res.Header.Get("Content-Type"))
	}
}

// publicURLs are an API publicPreset's rendition URLs, narrowest first.
func (h *mediaHarness) publicURLs(v any) []string {
	h.t.Helper()
	var img publicPreset
	decode(h.t, v, &img)
	slices.SortStableFunc(img.Renditions, func(a, b media.PublicRendition) int { return a.W - b.W })
	out := []string{}
	for _, r := range img.Renditions {
		out = append(out, r.URL)
	}
	return out
}

// published waits until get answers a preset with renditions other than
// before, and returns their URLs.
func (h *mediaHarness) published(t *testing.T, get func() any, before []string) []string {
	t.Helper()
	var urls []string
	eventually(t, "published renditions", func() bool {
		urls = h.publicURLs(get())
		return len(urls) > 0 && !slices.Equal(urls, before)
	})
	return urls
}

// publicSum is the digest of a served public file ("" when not served).
func publicSum(u string) string {
	res, err := http.Get(u)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (h *mediaHarness) waitPublic(t *testing.T, u string) {
	if u == "" {
		t.Fatal("no public URL")
	}
	eventually(t, "public file "+u, func() bool {
		res, err := http.Get(u)
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200 && res.Header.Get("Content-Type") == "image/webp"
	})
}

// purchase buys a post through the app's checkout: the app mints the
// session, the buyer pays it on the customer surface (a hosted Stripe page
// without Elements) and a signed provider webhook settles it.
func (h *mediaHarness) purchase(p peer, post testPost) {
	var got map[string]any
	eventually(h.t, "offer of "+post.path(), func() bool {
		got = p.call("GET", post.path(), nil, "", 200)
		return got["offer_status"] == "active" && len(got["offers"].([]any)) > 0
	})
	price := got["offers"].([]any)[0].(map[string]any)["price_id"]
	session := p.call("POST", post.path()+"/checkout", map[string]any{"price_id": price}, "", 201)
	h.pay(p, session["id"].(string))
	if err := h.stripe.settleLatest(h.ctx, h.base+h.hook); err != nil {
		h.t.Fatal(err)
	}
	eventually(h.t, p.name+" purchase", func() bool { return p.call("GET", post.path(), nil, "", 200)["purchased"] == true })
}

// pay pays a minted session with its first payment option, as billing-ui does.
func (h *mediaHarness) pay(p peer, session string) map[string]any {
	path := "/billing/v1/me/checkout-sessions/" + session
	options, _ := p.call("GET", path, nil, "", 200)["options"].([]any)
	if len(options) == 0 {
		h.t.Fatalf("session %s offers no payment option", session)
	}
	option := options[0].(map[string]any)["id"]
	return p.call("POST", path+"/pay", map[string]any{"option_id": option}, "", 200)
}

const testStripeAccount = "acct_demo_test"

// testMerchant is the demo merchant selling through psp, keyed by its rail.
func testMerchant(psp openrails.PSPConfig) openrails.MerchantDeclaration {
	return openrails.MerchantDeclaration{Slug: "onlydemo", DisplayName: "OnlyDemo", PSPs: map[string]openrails.PSPConfig{string(psp.Rail): psp}}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); !ok(); time.Sleep(250 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func testPNG(t *testing.T, w, h int, c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	img.Set(0, 0, color.RGBA{uint8(w), uint8(h), 7, 255}) // distinct bytes per size
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeStripe answers the Stripe calls OpenRails makes for hosted checkout;
// anything else fails. There is no network fallback.
type fakeStripe struct {
	mu        sync.Mutex
	customers int
	sessions  []url.Values
	requests  []string // "METHOD /path"
}

// requested counts the requests made to route ("POST /v1/refunds").
func (s *fakeStripe) requested(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r == route {
			n++
		}
	}
	return n
}

func (s *fakeStripe) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.URL.Host != "api.stripe.com" {
		return nil, fmt.Errorf("unexpected provider host %s", req.URL.Host)
	}
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	s.requests = append(s.requests, req.Method+" "+req.URL.Path)
	var data any
	switch req.Method + " " + req.URL.Path {
	case "GET /v1/account":
		data = map[string]any{"id": "acct_demo_test", "object": "account", "charges_enabled": true, "details_submitted": true, "country": "US", "default_currency": "usd"}
	case "GET /v1/balance":
		data = map[string]any{"object": "balance", "available": []any{}, "pending": []any{}, "livemode": false}
	case "GET /v1/customers/search":
		data = map[string]any{"object": "search_result", "data": []any{}, "has_more": false}
	case "GET /v1/subscriptions", "GET /v1/webhook_endpoints", "GET /v1/charges", "GET /v1/refunds", "GET /v1/invoices", "GET /v1/payment_intents", "GET /v1/events":
		data = map[string]any{"object": "list", "data": []any{}, "has_more": false}
	case "POST /v1/customers":
		s.customers++
		data = map[string]any{"id": fmt.Sprintf("cus_media%d", s.customers), "object": "customer"}
	case "POST /v1/checkout/sessions":
		s.sessions = append(s.sessions, req.PostForm)
		data = map[string]any{"id": fmt.Sprintf("cs_media%d", len(s.sessions)), "object": "checkout.session", "status": "open", "payment_status": "unpaid",
			"url": fmt.Sprintf("https://checkout.stripe.com/c/pay/cs_media%d", len(s.sessions))}
	default:
		// A provider read of a list (refresh windows) finds nothing.
		if req.Method == "GET" && strings.Count(req.URL.Path, "/") == 2 {
			data = map[string]any{"object": "list", "data": []any{}, "has_more": false}
			break
		}
		return nil, fmt.Errorf("unexpected fake provider request %s %s", req.Method, req.URL.Path)
	}
	encoded, err := json.Marshal(data)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: req}, err
}

func (s *fakeStripe) settleLatest(ctx context.Context, hook string) error {
	s.mu.Lock()
	n := len(s.sessions)
	if n == 0 {
		s.mu.Unlock()
		return fmt.Errorf("checkout never reached the fake provider")
	}
	form := s.sessions[n-1]
	s.mu.Unlock()
	metadata := map[string]string{}
	for key, values := range form {
		if strings.HasPrefix(key, "metadata[") {
			metadata[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = values[0]
		}
	}
	amount, _ := strconv.ParseInt(form.Get("line_items[0][price_data][unit_amount]"), 10, 64)
	payload, err := json.Marshal(map[string]any{"id": fmt.Sprintf("evt_media%d", n), "object": "event", "type": "checkout.session.completed", "livemode": false,
		"created": time.Now().Unix(), "data": map[string]any{"object": map[string]any{"id": fmt.Sprintf("cs_media%d", n), "object": "checkout.session",
			"mode": "payment", "status": "complete", "payment_status": "paid", "customer": form.Get("customer"), "payment_intent": fmt.Sprintf("pi_media%d", n),
			"amount_total": amount, "currency": "usd", "metadata": metadata}}})
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", hook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t="+ts+",v1="+hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		raw, _ := io.ReadAll(res.Body)
		return fmt.Errorf("paid webhook returned %d: %s", res.StatusCode, raw)
	}
	return nil
}
