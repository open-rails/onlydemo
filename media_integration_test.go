package main

// End-to-end media proof on real services: PostgreSQL (app, AuthKit,
// OpenRails, River), an S3 backend (MinIO in CI), the real ContentKit upload
// and read handlers, the libvips image job, the media-worker video encode
// (ffmpeg) and the media-access binary. Only the Stripe API is replaced, by a
// closed local fake.
//
//	DEMO_TEST_DATABASE_URL   loopback PostgreSQL admin URL; a database is created and dropped
//	DEMO_TEST_S3_ENDPOINT    e.g. http://127.0.0.1:9000; a bucket is created and emptied
//	DEMO_TEST_S3_ACCESS_KEY, DEMO_TEST_S3_SECRET_KEY
//	DEMO_TEST_MEDIA_ACCESS   media-access binary; built from the pinned ContentKit when unset
//	DEMO_TEST_MEDIA_WORKER   media-worker binary; built likewise when unset
//	DEMO_TEST_FFMPEG=1       fail instead of skipping the video proof without ffmpeg (CI)

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/contentkit/media/token"
	"github.com/open-rails/contentkit/media/video"
	"github.com/open-rails/openrails"
	openrailsembed "github.com/open-rails/openrails/embed"
)

const (
	testWebhookSecret = "whsec_media_test_fake_only"
	testPassword      = "Media-test-password-42!"
	testQuota         = 3 << 20
	testFilesPerHour  = 20
)

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
	posts := map[string]int64{}
	for _, policy := range []string{"public", "membership", "ppv", "members_ppv"} {
		body := map[string]any{"channel_id": channelID, "slug": "p-" + strings.ReplaceAll(policy, "_", "-"), "title": policy, "body": "text", "access_policy": policy}
		if paidPolicy(policy) {
			body["price"] = price
		}
		posts[policy] = int64(owner.call("POST", "/api/v1/posts", body, "", 201)["id"].(float64))
	}

	// The creator uploads two images per post; paid posts get a blurred teaser
	// of the first. Order lives in the manifest.
	for policy, id := range posts {
		ref := postRefBody(id)
		a := owner.upload(ref, "", testPNG(t, 640, 480, color.RGBA{200, 40, 40, 255}), "image/png", 200)
		b := owner.upload(ref, "", testPNG(t, 480, 640, color.RGBA{40, 40, 200, 255}), "image/png", 200)
		ops := []media.Op{{Op: media.OpInsert, Name: "a.png", Original: a}, {Op: media.OpInsert, Name: "b.png", Original: b}}
		if policy != "public" {
			ops = append([]media.Op{{Op: media.OpInsert, Name: "teaser", Original: a, Meta: map[string]any{"teaser": true}}}, ops...)
		}
		owner.call("POST", "/api/v1/media/upload/commit", map[string]any{"ref": ref, "ops": ops}, "", 200)
	}
	for _, id := range posts {
		h.waitDerived(owner, id)
	}

	// Entitlements live only in OpenRails: a membership window, and real
	// purchases through checkout and a signed provider webhook.
	membershipKey := membershipResource(channelID)
	if _, err := h.srv.billing.client.GrantEntitlement(ctx, member.id, openrails.GrantEntitlementRequest{Entitlement: membershipKey}); err != nil {
		t.Fatal(err)
	}
	h.purchase(buyer, posts["ppv"])
	loyalMembership, err := h.srv.billing.client.GrantEntitlement(ctx, loyal.id, openrails.GrantEntitlementRequest{Entitlement: membershipKey})
	if err != nil {
		t.Fatal(err)
	}
	h.purchase(loyal, posts["members_ppv"])
	if err := h.srv.billing.client.RevokeEntitlement(ctx, loyal.id, loyalMembership.ID); err != nil {
		t.Fatal(err)
	}

	full, teaser := "full", "teaser"
	want := map[string]map[*peer]string{
		"public":      {&anon: full, &stranger: full, &member: full},
		"membership":  {&anon: teaser, &stranger: teaser, &member: full, &editor: full, &admin: full, &loyal: teaser, &buyer: teaser},
		"ppv":         {&anon: teaser, &stranger: teaser, &member: teaser, &buyer: full, &owner: full},
		"members_ppv": {&stranger: teaser, &member: teaser, &loyal: full, &editor: full},
	}
	for policy, viewers := range want {
		for viewer, level := range viewers {
			t.Run(policy+"/"+viewer.name, func(t *testing.T) {
				h.expectRead(t, *viewer, posts[policy], level)
			})
		}
	}

	t.Run("url delivery", func(t *testing.T) {
		mc := h.cfg.Media
		signing, _ := token.ParseKey(mc.TokenKey)
		reader, err := media.NewReader(media.ReaderOptions{Manifests: h.srv.media.manifests, Kinds: h.srv.media.kinds, Resolver: h.srv.media,
			Delivery: media.Delivery{Mode: media.DeliverURL, BaseURL: mc.URL, SigningKey: signing}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := reader.Read(ctx, h.srv.media.postRef(posts["public"]), actorFor(""), media.ReadOptions{Variants: []string{"large"}})
		if err != nil || res.Access != media.AccessFull || res.Cookie != nil {
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
		ref := postRefBody(posts["public"])
		big := make([]byte, testQuota+1)
		stranger.upload(ref, "", testPNG(t, 8, 8, color.White), "image/png", 403)
		stranger.call("GET", fmt.Sprintf("/api/v1/posts/%d/media", posts["ppv"]), nil, "", 404)
		files := editor.call("GET", fmt.Sprintf("/api/v1/posts/%d/media", posts["ppv"]), nil, "", 200)["files"].([]any)
		if len(files) != 3 || files[0].(map[string]any)["name"] != "teaser" || files[0].(map[string]any)["original"] != files[1].(map[string]any)["original"] {
			t.Fatalf("editor file list %v", files)
		}
		owner.presign(ref, "", big, "image/png", 413, media.CodeQuota)
		admin.presign(ref, "", big, "image/png", 200, "") // site admins are exempt

		// Quota binds at commit too: an original that got past presign (here
		// an exempt upload; in general a lapsed reservation) cannot be committed
		// past the channel's quota, and the refused commit writes nothing.
		orig := admin.upload(ref, "", big, "image/png", 200)
		used, _, err := h.srv.media.limiter.Usage(ctx, h.cfg.Media.Tenant, channelOwner(channelID))
		if err != nil {
			t.Fatal(err)
		}
		over := owner.call("POST", "/api/v1/media/upload/commit", map[string]any{"ref": ref, "ops": []media.Op{{Op: media.OpInsert, Name: "big.png", Original: orig}}}, "", 413)
		if over["code"] != media.CodeQuota {
			t.Fatalf("commit over quota: %v", over)
		}
		after, _, err := h.srv.media.limiter.Usage(ctx, h.cfg.Media.Tenant, channelOwner(channelID))
		if err != nil || after != used {
			t.Fatalf("usage %d after a refused commit, was %d (%v)", after, used, err)
		}
		if files := owner.call("GET", fmt.Sprintf("/api/v1/posts/%d/media", posts["public"]), nil, "", 200)["files"].([]any); len(files) != 2 {
			t.Fatalf("refused commit changed the manifest: %v", files)
		}

		spam := spammer.call("POST", "/api/v1/channels", map[string]any{"slug": "spam", "name": "Spam"}, "", 201)
		sp := spammer.call("POST", "/api/v1/posts", map[string]any{"channel_id": spam["id"], "slug": "spam-post", "title": "s", "body": "s"}, "", 201)
		spamRef := postRefBody(int64(sp["id"].(float64)))
		for i := range testFilesPerHour {
			spammer.presign(spamRef, "", []byte(fmt.Sprintf("file %d", i)), "image/png", 200, "")
		}
		body := []byte("one too many")
		spammer.presign(spamRef, "", body, "image/png", 429, media.CodeRate)
		sum := sha256.Sum256(body)
		h.expectAbsent(t, fmt.Sprintf("%s/post/%d/originals/sha256-%x", h.cfg.Media.Tenant, int64(sp["id"].(float64)), sum))
	})

	t.Run("public slots", func(t *testing.T) {
		chRef := map[string]string{"kind": kindChannel, "id": channelID}
		editor.upload(chRef, "avatar", testPNG(t, 300, 300, color.White), "image/png", 403)
		owner.upload(chRef, "avatar", testPNG(t, 300, 300, color.RGBA{0, 160, 0, 255}), "image/png", 200)
		// Never upscaled: a 300px avatar has the 128 and 256 widths.
		h.waitSlot(t, "channel avatar", h.channelSlot(anon, channelID, "avatar"), "", 128, 256)

		stranger.upload(map[string]string{"kind": media.UserKind, "id": owner.id}, "avatar", testPNG(t, 64, 64, color.White), "image/png", 403)
		// The SDK crops before upload: commit-slot carries the edit (Aspect 1
		// derives the height), so a 400px crop of a 600px image has no 512.
		_, got := stranger.uploadSlot(map[string]string{"kind": media.UserKind, "id": stranger.id}, "avatar", testPNG(t, 600, 600, color.Black),
			map[string]any{"crop": map[string]int{"x": 100, "y": 150, "w": 400}, "rotate": 90})
		if e := got["edit"].(map[string]any); e["crop"].(map[string]any)["h"] != float64(400) || e["rotate"] != float64(90) {
			t.Fatalf("user avatar commit %+v", got)
		}
		h.waitSlot(t, "user avatar", func() any {
			return stranger.call("GET", "/api/v1/me", nil, "", 200)["user"].(map[string]any)["avatar"]
		}, "", 128, 256)
	})

	t.Run("mixed images and videos", func(t *testing.T) {
		body := map[string]any{"channel_id": channelID, "slug": "p-mixed", "title": "mixed", "body": "text", "access_policy": "membership", "price": price}
		id := int64(owner.call("POST", "/api/v1/posts", body, "", 201)["id"].(float64))
		ref := postRefBody(id)

		// ContentKit's per-type caps: images keep their own byte cap.
		big := make([]byte, maxImageBytes+1)
		admin.presign(ref, "", big, "image/png", 413, media.CodeTooLarge)
		admin.presign(ref, "", big, "video/mp4", 200, "")
		if !h.video {
			t.Skip("ffmpeg not installed")
		}

		img := owner.upload(ref, "", testPNG(t, 640, 360, color.RGBA{10, 120, 90, 255}), "image/png", 200)
		clip := owner.upload(ref, "", testVideo(t, 320, 240), "video/mp4", 200)
		commit := func(ops []media.Op, status int) map[string]any {
			return owner.call("POST", "/api/v1/media/upload/commit", map[string]any{"ref": ref, "ops": ops}, "", status)
		}
		if out := commit([]media.Op{{Op: media.OpInsert, Name: "teaser", Original: clip, Meta: map[string]any{"teaser": true}}}, 400); out["code"] != media.CodeInvalid {
			t.Fatalf("video teaser: %v", out)
		}
		var many []media.Op
		for i := range maxPostFiles + 1 {
			many = append(many, media.Op{Op: media.OpInsert, Name: fmt.Sprintf("img%02d.png", i), Original: img})
		}
		if out := commit(many, 409); out["code"] != media.CodeTooManyFiles {
			t.Fatalf("file ceiling: %v", out)
		}
		many = many[:0]
		for i := range maxPostVideos + 1 {
			many = append(many, media.Op{Op: media.OpInsert, Name: fmt.Sprintf("clip%02d.mp4", i), Original: clip})
		}
		if out := commit(many, 409); out["code"] != media.CodeTooManyFiles {
			t.Fatalf("video ceiling: %v", out)
		}
		commit([]media.Op{
			{Op: media.OpInsert, Name: "teaser", Original: img, Meta: map[string]any{"teaser": true}},
			{Op: media.OpInsert, Name: "still.png", Original: img},
			{Op: media.OpInsert, Name: "clip.mp4", Original: clip},
		}, 200)
		h.waitDerived(owner, id)

		// Entitled: the master and media playlists come from the read API, the
		// byte ranges from media-access under the folder cookie.
		r := h.read(member, id)
		v := r.res.Files[2]
		if r.res.Access != media.AccessFull || v.Name != "clip.mp4" || !v.HLS || v.Type != "video/mp4" || v.Duration <= 0 {
			t.Fatalf("member read %+v", r.res)
		}
		base := fmt.Sprintf("/api/v1/media/post/%d/hls/clip.mp4/", id)
		master := h.playlist(member, base+"master.m3u8", 200)
		var variants []string
		for _, line := range strings.Split(master, "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				variants = append(variants, line)
			}
		}
		if len(variants) != 1 || variants[0] != "video/240.m3u8" || !strings.Contains(master, `TYPE=AUDIO`) {
			t.Fatalf("master playlist:\n%s", master)
		}
		rendition := h.playlist(member, base+variants[0], 200)
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
		h.expectRange(t, blob, "", o, n, 403)

		// Videos keep ContentKit's default ladder (up to 2160p): the worker
		// recorded that recipe.
		man, _, err := h.srv.media.manifests.Get(ctx, h.srv.media.postRef(id))
		if err != nil {
			t.Fatal(err)
		}
		if hls := man.Files[2].HLS; hls.Spec != video.Spec(media.DefaultLadder) {
			t.Fatalf("hls spec %s, want the default ladder's %s", hls.Spec, video.Spec(media.DefaultLadder))
		}

		// Downloads: one per quality, saved under the post's name.
		if len(r.res.Downloads) != 1 || r.res.Downloads[0].Key != "clip.mp4-240p" || r.res.Downloads[0].Name != "p-mixed-clip-240p.mp4" {
			t.Fatalf("downloads %+v", r.res.Downloads)
		}
		res, _ := member.do("GET", fmt.Sprintf("/api/v1/media/post/%d/download/clip.mp4-240p", id), nil, "")
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "video/mp4" ||
			!strings.Contains(res.Header.Get("Content-Disposition"), "p-mixed-clip-240p.mp4") {
			t.Fatalf("download: %d %v", res.StatusCode, res.Header)
		}

		// Locked: teaser only, "3 items locked" includes the video, no playlist.
		l := h.read(stranger, id)
		if l.res.Access != media.AccessNone || !l.res.Files[0].Teaser || !l.res.Files[1].Locked || !l.res.Files[2].Locked ||
			l.res.Files[2].Name != "" || len(l.res.Downloads) != 0 {
			t.Fatalf("stranger read %+v", l.res)
		}
		h.playlist(stranger, base+"master.m3u8", 404)
		h.playlist(stranger, base+variants[0], 404)
		if res, _ := stranger.do("GET", fmt.Sprintf("/api/v1/media/post/%d/download/clip.mp4-240p", id), nil, ""); res.StatusCode != 404 {
			t.Fatalf("stranger download %d", res.StatusCode)
		}
	})

	t.Run("aspect-aware ladder", func(t *testing.T) {
		if !h.video {
			t.Skip("ffmpeg not installed")
		}
		body := map[string]any{"channel_id": channelID, "slug": "p-aspects", "title": "aspects", "body": "text", "access_policy": "public"}
		id := int64(owner.call("POST", "/api/v1/posts", body, "", 201)["id"].(float64))
		ref := postRefBody(id)
		// 9:21 and 21:9 get real 720 and 480 rungs (short sides) at their own
		// aspect; 3:1 is outside the default 1:2.4–2.4:1 and fails.
		owner.call("POST", "/api/v1/media/upload/commit", map[string]any{"ref": ref, "ops": []media.Op{
			{Op: media.OpInsert, Name: "tall.mp4", Original: owner.upload(ref, "", testVideo(t, 720, 1680), "video/mp4", 200)},
			{Op: media.OpInsert, Name: "wide.mp4", Original: owner.upload(ref, "", testVideo(t, 1680, 720), "video/mp4", 200)},
			{Op: media.OpInsert, Name: "band.mp4", Original: owner.upload(ref, "", testVideo(t, 480, 160), "video/mp4", 200)},
		}}, "", 200)
		h.waitDerived(owner, id)

		o := h.read(owner, id).res.Files
		if o[0].Width != 720 || o[0].Height != 1680 || !o[0].HLS || o[1].Width != 1680 || o[1].Height != 720 || !o[1].HLS {
			t.Fatalf("owner read %+v", o)
		}
		if o[2].HLS || !strings.Contains(o[2].Failed, "aspect") {
			t.Fatalf("3:1 video %+v", o[2])
		}
		r := h.read(member, id)
		if f := r.res.Files[2]; f.HLS || f.Failed != "" {
			t.Fatalf("reader sees the failure reason: %+v", f)
		}
		man, _, err := h.srv.media.manifests.Get(ctx, h.srv.media.postRef(id))
		if err != nil {
			t.Fatal(err)
		}
		if e := man.Files[2].HLS; e == nil || e.Error == "" || len(e.Video) != 0 {
			t.Fatalf("band hls %+v", e)
		}
		for i, c := range []struct {
			name string
			want [][3]int // rung, w, h
		}{{"tall.mp4", [][3]int{{720, 720, 1680}, {480, 480, 1120}}}, {"wide.mp4", [][3]int{{720, 1680, 720}, {480, 1120, 480}}}} {
			var got [][3]int
			for _, v := range man.Files[i].HLS.Video {
				got = append(got, [3]int{v.Rung, v.Width, v.Height})
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("%s renditions %v, want %v", c.name, got, c.want)
			}
			master := h.playlist(member, fmt.Sprintf("/api/v1/media/post/%d/hls/%s/master.m3u8", id, c.name), 200)
			for _, v := range c.want {
				if !strings.Contains(master, fmt.Sprintf("RESOLUTION=%dx%d", v[1], v[2])) || !strings.Contains(master, fmt.Sprintf("video/%d.m3u8", v[0])) {
					t.Fatalf("%s master lacks %dp %dx%d:\n%s", c.name, v[0], v[1], v[2], master)
				}
				h.playlist(member, fmt.Sprintf("/api/v1/media/post/%d/hls/%s/video/%d.m3u8", id, c.name, v[0]), 200)
			}
		}
		var keys []string
		for _, d := range r.res.Downloads {
			keys = append(keys, d.Key+"="+d.Name)
		}
		if want := "tall.mp4-480p=p-aspects-tall-480p.mp4 tall.mp4-720p=p-aspects-tall-720p.mp4 wide.mp4-480p=p-aspects-wide-480p.mp4 wide.mp4-720p=p-aspects-wide-720p.mp4"; strings.Join(keys, " ") != want {
			t.Fatalf("downloads %v", keys)
		}
	})

	t.Run("image edits", func(t *testing.T) {
		id := posts["public"]
		ref := postRefBody(id)
		edit := map[string]any{"crop": map[string]int{"x": 0, "y": 0, "w": 320, "h": 240}, "rotate": 90}
		stranger.call("POST", "/api/v1/media/upload/commit", map[string]any{"ref": ref, "ops": []any{map[string]any{"op": "edit", "name": "a.png", "edit": edit}}}, "", 403)
		before := h.read(owner, id).res.Files[0]
		owner.call("POST", "/api/v1/media/upload/commit", map[string]any{"ref": ref, "ops": []any{map[string]any{"op": "edit", "name": "a.png", "edit": edit}}}, "", 200)
		// Variants re-derive through crop then rotate; dims stay the source's.
		eventually(t, "edited variant", func() bool {
			f := h.read(owner, id).res.Files[0]
			return f.URL != "" && f.URL != before.URL && f.Width == 240 && f.Height == 320
		})
		f := h.read(owner, id).res.Files[0]
		if f.Edit == nil || f.Edit.Rotate != 90 || f.Edit.Crop.W != 320 || f.Dims == nil || f.Dims.W != 640 || f.Dims.H != 480 {
			t.Fatalf("edited file %+v", f)
		}
		h.expectFetch(t, f.URL, h.read(owner, id).cookie, 200)
		res, raw := owner.do("GET", fmt.Sprintf("/api/v1/media/post/%d?variant=editor", id), nil, "")
		var ed media.ReadResult
		if res.StatusCode != 200 || json.Unmarshal(raw, &ed) != nil || ed.Files[0].Variant != "editor" || ed.Files[0].URL == "" {
			t.Fatalf("editor variant: %d %s", res.StatusCode, raw)
		}
		if f := editor.call("GET", fmt.Sprintf("/api/v1/posts/%d/media", id), nil, "", 200)["files"].([]any)[0].(map[string]any); f["edit"] == nil {
			t.Fatalf("uploader file list lacks the edit: %v", f)
		}
		// Readers with full access get neither the uncropped variant nor the
		// edit and source dims.
		for _, p := range []peer{stranger, anon, member} {
			res, raw := p.do("GET", fmt.Sprintf("/api/v1/media/post/%d?variant=editor", id), nil, "")
			var r media.ReadResult
			if res.StatusCode != 200 || json.Unmarshal(raw, &r) != nil || r.Access != media.AccessFull {
				t.Fatalf("%s editor read: %d %s", p.name, res.StatusCode, raw)
			}
			for _, f := range r.Files {
				if f.URL != "" || f.Variant != "" || f.Edit != nil || f.Dims != nil {
					t.Fatalf("%s got editor data: %+v", p.name, f)
				}
			}
			if f := h.read(p, id).res.Files[0]; f.URL == "" || f.Edit != nil || f.Dims != nil || f.Width != 240 {
				t.Fatalf("%s large read %+v", p.name, f)
			}
		}
	})

	t.Run("channel cover: crop, rotate and re-crop without re-upload", func(t *testing.T) {
		chRef := map[string]string{"kind": kindChannel, "id": channelID}
		cover := h.channelSlot(anon, channelID, "cover")
		owner.upload(chRef, "cover", testPNG(t, 1600, 1600, color.RGBA{200, 120, 0, 255}), "image/png", 200)
		first := h.waitSlot(t, "cover", cover, "", 1500)
		// Narrower than the cover's MinWidth: refused, the served cover stays.
		if res, raw := owner.do("POST", "/api/v1/media/upload/edit-slot", map[string]any{"ref": chRef, "slot": "cover",
			"edit": map[string]any{"crop": map[string]int{"x": 0, "y": 0, "w": 900}}}, ""); res.StatusCode/100 != 4 {
			t.Fatalf("narrow cover edit %d %s", res.StatusCode, raw)
		}
		// Rotated a quarter turn: the crop is in original pixels and its height
		// follows at 1:3, so the output is 1590x530.
		edit := map[string]any{"ref": chRef, "slot": "cover", "edit": map[string]any{"crop": map[string]int{"x": 100, "y": 0, "w": 530}, "rotate": 90}}
		editor.call("POST", "/api/v1/media/upload/edit-slot", edit, "", 403)
		got := owner.call("POST", "/api/v1/media/upload/edit-slot", edit, "", 200)
		if crop := got["edit"].(map[string]any)["crop"].(map[string]any); crop["h"] != float64(1590) {
			t.Fatalf("cover edit %+v", got)
		}
		h.waitSlot(t, "re-cropped cover", cover, first.Version, 1500)
	})

	t.Run("channel avatar from a post image", func(t *testing.T) {
		chRef := map[string]string{"kind": kindChannel, "id": channelID}
		from := postRefBody(posts["public"])
		slot := map[string]any{"ref": chRef, "slot": "avatar", "from": from, "file": "b.png", "edit": map[string]any{"crop": map[string]int{"x": 0, "y": 100, "w": 480}}}
		// Channel editors post but do not manage the channel's look.
		editor.call("POST", "/api/v1/media/upload/commit-slot-from-file", slot, "", 403)
		// Managing a channel is not enough to take another channel's images.
		other := stranger.call("POST", "/api/v1/channels", map[string]any{"slug": "elsewhere", "name": "Elsewhere"}, "", 201)
		stranger.call("POST", "/api/v1/media/upload/commit-slot-from-file",
			map[string]any{"ref": map[string]string{"kind": kindChannel, "id": other["id"].(string)}, "slot": "avatar", "from": from, "file": "b.png"}, "", 403)
		avatar := h.channelSlot(anon, channelID, "avatar")
		before, _ := avatar().(map[string]any)
		got := owner.call("POST", "/api/v1/media/upload/commit-slot-from-file", slot, "", 200)
		// Slot.Aspect 1 derives the crop height.
		if crop := got["edit"].(map[string]any)["crop"].(map[string]any); crop["h"] != float64(480) {
			t.Fatalf("avatar edit %+v", got)
		}
		h.waitSlot(t, "avatar from post image", avatar, before["version"].(string), 128, 256)
	})

	t.Run("post deletion erases its folder and releases quota", func(t *testing.T) {
		id := posts["ppv"]
		used, _, err := h.srv.media.limiter.Usage(ctx, h.cfg.Media.Tenant, channelOwner(channelID))
		if err != nil || used <= 0 {
			t.Fatalf("usage before delete: %d %v", used, err)
		}
		owner.call("DELETE", fmt.Sprintf("/api/v1/posts/%d", id), nil, "", 204)
		buyer.call("GET", fmt.Sprintf("/api/v1/media/post/%d", id), nil, "", 404)
		prefix := fmt.Sprintf("%s/post/%d/", h.cfg.Media.Tenant, id)
		eventually(t, "folder erased", func() bool { return h.count(prefix) == 0 })
		eventually(t, "quota released", func() bool {
			after, _, err := h.srv.media.limiter.Usage(ctx, h.cfg.Media.Tenant, channelOwner(channelID))
			return err == nil && after < used
		})
	})
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
	worker := startMediaAccess(t, endpoint, h.bucket, access, secret, tokenKey)

	h.cfg = Config{MembershipHours: 720, DatabaseURL: pool.Config().ConnString(), PublicURL: h.base, AuthIssuer: h.base, AuthAudience: "demo-media-test",
		PSPs: map[string]openrailsembed.PSPConfig{"stripe": {"stripe": {AccountID: "acct_demo_test",
			Secrets: map[string]string{"secret_key": "sk_test_fake_only", "webhook_signing_secret": testWebhookSecret}}}}, CheckoutPSP: "stripe", AuthKeysPath: "",
		Media: mediaConfig{Tenant: "o", S3Endpoint: endpoint, S3PublicEndpoint: endpoint, S3Bucket: h.bucket, S3Region: "us-east-1",
			S3AccessKeyID: access, S3SecretKey: secret, URL: worker, Delivery: media.DeliverCookie, CookieDomain: "localhost",
			TokenKey: tokenKey, FilesPerHour: testFilesPerHour, BytesPerDay: 1 << 30, ChannelQuota: testQuota}}
	if err = initializeDatabase(ctx, h.cfg, pool); err != nil {
		t.Fatal(err)
	}
	if h.video = hasFFmpeg(t); h.video {
		startMediaWorker(t, h.cfg.DatabaseURL, endpoint, h.bucket, access, secret)
	}
	if h.srv, err = startServer(ctx, h.cfg, pool, billingOptions{Test: func(o *openrailsembed.Options) { o.StripeTransport = h.stripe }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.srv.Close)
	go func() { _ = h.srv.app.Listener(listener) }()
	t.Cleanup(func() { _ = h.srv.app.Shutdown() })
	for _, route := range h.srv.app.GetRoutes(true) {
		if route.Method == "POST" && strings.Contains(route.Path, "webhooks/") {
			h.hook = strings.NewReplacer(":merchant", billingMerchantSlug, ":provider", "stripe", ":account_id", "acct_demo_test").Replace(route.Path)
		}
	}
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
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// startMediaAccess runs the real access worker binary with the host's key.
func startMediaAccess(t *testing.T, endpoint, bucket, access, secret, tokenKey string) string {
	bin := os.Getenv("DEMO_TEST_MEDIA_ACCESS")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "media-access")
		out, err := exec.Command("go", "build", "-o", bin, "github.com/open-rails/contentkit/cmd/media-access").CombinedOutput()
		if err != nil {
			t.Fatalf("build media-access: %v\n%s", err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MEDIA_ACCESS_LISTEN="+addr, "MEDIA_ACCESS_S3_ENDPOINT="+endpoint, "MEDIA_ACCESS_S3_BUCKET="+bucket,
		"MEDIA_ACCESS_S3_ACCESS_KEY_ID="+access, "MEDIA_ACCESS_S3_SECRET_ACCESS_KEY="+secret, "MEDIA_ACCESS_TOKEN_KEY="+tokenKey)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	base := "http://" + addr
	eventually(t, "media-access listening", func() bool {
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

// startMediaWorker runs ContentKit's video encode worker on the test database.
func startMediaWorker(t *testing.T, dbURL, endpoint, bucket, access, secret string) {
	bin := os.Getenv("DEMO_TEST_MEDIA_WORKER")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "media-worker")
		out, err := exec.Command("go", "build", "-o", bin, "github.com/open-rails/contentkit/cmd/media-worker").CombinedOutput()
		if err != nil {
			t.Fatalf("build media-worker: %v\n%s", err, out)
		}
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL, "MEDIA_S3_ENDPOINT="+endpoint, "MEDIA_S3_BUCKET="+bucket,
		"MEDIA_S3_ACCESS_KEY_ID="+access, "MEDIA_S3_SECRET_ACCESS_KEY="+secret, "MEDIA_WORKER_THREADS=2",
		"MEDIA_WORKER_TMP="+t.TempDir(), "MEDIA_WORKER_SHUTDOWN_GRACE=1s")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
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

func (h *mediaHarness) count(prefix string) int {
	out, err := h.s3.ListObjectsV2(h.ctx, &s3.ListObjectsV2Input{Bucket: &h.bucket, Prefix: &prefix})
	if err != nil {
		h.t.Fatal(err)
	}
	return len(out.Contents)
}

func (h *mediaHarness) expectAbsent(t *testing.T, key string) {
	if _, err := h.s3.HeadObject(h.ctx, &s3.HeadObjectInput{Bucket: &h.bucket, Key: aws.String(key)}); err == nil {
		t.Fatalf("%s exists", key)
	}
}

func (h *mediaHarness) client(ip string) *http.Client {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}
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
	p.call("POST", "/auth/v1/register", map[string]any{"identifier": name + "@example.test", "username": name, "password": testPassword}, "", 202)
	login := p.call("POST", "/auth/v1/password/login", map[string]any{"identifier": name + "@example.test", "password": testPassword}, "", 200)
	p.token, _ = login["access_token"].(string)
	user, err := h.srv.auth.client.GetUserByUsername(h.ctx, name)
	if err != nil || p.token == "" {
		h.t.Fatalf("register %s: %v", name, err)
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

func postRefBody(id int64) map[string]string {
	return map[string]string{"kind": kindPost, "id": strconv.FormatInt(id, 10)}
}

// presign asks for an upload plan and checks its status and error code.
func (p peer) presign(ref map[string]string, slot string, data []byte, typ string, status int, code string) map[string]any {
	p.h.t.Helper()
	sum := sha256.Sum256(data)
	out := p.call("POST", "/api/v1/media/upload/presign", map[string]any{"ref": ref, "type": typ, "size": len(data), "sha256": hex.EncodeToString(sum[:]), "slot": slot}, "", status)
	if code != "" && out["code"] != code {
		p.h.t.Fatalf("%s presign code %v, want %s", p.name, out["code"], code)
	}
	return out
}

// upload plays the browser SDK: presign, checksum-bound PUT straight to the
// bucket, and commit-slot (centred crop) for slots. It returns the original's name.
func (p peer) upload(ref map[string]string, slot string, data []byte, typ string, status int) string {
	p.h.t.Helper()
	name, _ := p.put(ref, slot, data, typ, status, nil)
	return name
}

// uploadSlot uploads a slot original and commits it with edit, as the SDK's
// uploadSlot does, returning the commit-slot reply (the slot manifest).
func (p peer) uploadSlot(ref map[string]string, slot string, data []byte, edit map[string]any) (string, map[string]any) {
	p.h.t.Helper()
	return p.put(ref, slot, data, "image/png", 200, edit)
}

func (p peer) put(ref map[string]string, slot string, data []byte, typ string, status int, edit map[string]any) (string, map[string]any) {
	p.h.t.Helper()
	plan := p.presign(ref, slot, data, typ, status, "")
	if status != 200 || plan["exists"] == true {
		name, _ := plan["name"].(string)
		return name, nil
	}
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
	var reply map[string]any
	if slot != "" {
		sum := sha256.Sum256(data)
		body := map[string]any{"ref": ref, "slot": slot, "sha256": hex.EncodeToString(sum[:])}
		if edit != nil {
			body["edit"] = edit
		}
		reply = p.call("POST", "/api/v1/media/upload/commit-slot", body, "", 200)
	}
	return plan["name"].(string), reply
}

func (h *mediaHarness) read2(p peer, kind, id string) media.ReadResult {
	h.t.Helper()
	res, raw := p.do("GET", "/api/v1/media/"+kind+"/"+id+"?variant=editor", nil, "")
	var out media.ReadResult
	if res.StatusCode != 200 || json.Unmarshal(raw, &out) != nil {
		h.t.Fatalf("%s read %s/%s: %d %s", p.name, kind, id, res.StatusCode, raw)
	}
	return out
}

type readResult struct {
	res    media.ReadResult
	cookie string
}

func (h *mediaHarness) read(p peer, id int64) readResult {
	h.t.Helper()
	res, raw := p.do("GET", fmt.Sprintf("/api/v1/media/post/%d?variant=large,blurred", id), nil, "")
	if res.StatusCode != 200 {
		h.t.Fatalf("%s read post %d: %d %s", p.name, id, res.StatusCode, raw)
	}
	var out readResult
	if err := json.Unmarshal(raw, &out.res); err != nil {
		h.t.Fatal(err)
	}
	for _, c := range res.Cookies() {
		if c.Name == media.CookieName {
			out.cookie = c.Value
		}
	}
	return out
}

// waitDerived waits for the image job to fill every image's variant and the
// media-worker to encode every video (or, read by an editor, refuse it).
func (h *mediaHarness) waitDerived(p peer, id int64) {
	eventually(h.t, fmt.Sprintf("variants of post %d", id), func() bool {
		r := h.read(p, id)
		for _, f := range r.res.Files {
			if isVideoType(f.Type) && !f.HLS && f.Failed == "" || !isVideoType(f.Type) && f.URL == "" {
				return false
			}
		}
		return len(r.res.Files) > 0
	})
}

// expectRead checks one viewer's read API answer and what the access worker
// then serves: everything, or only the blurred teaser.
func (h *mediaHarness) expectRead(t *testing.T, p peer, id int64, level string) {
	r := h.read(p, id)
	post := p.call("GET", fmt.Sprintf("/api/v1/posts/%d", id), nil, "", 200)
	if post["can_read"] != (level == "full") {
		t.Fatalf("post can_read %v disagrees with media level %s", post["can_read"], level)
	}
	owner := h.read(h.owner, id)
	switch level {
	case "full":
		if r.res.Access != media.AccessFull || r.cookie == "" {
			t.Fatalf("want full access with a folder cookie, got %s (cookie %t)", r.res.Access, r.cookie != "")
		}
		for _, f := range r.res.Files {
			if f.Locked || strings.Contains(f.URL, "?t=") {
				t.Fatalf("full access file %+v", f)
			}
			h.expectFetch(t, f.URL, r.cookie, 200)
			h.expectFetch(t, f.URL, "", 403)
		}
		// The cookie never opens originals or manifests.
		prefix := strings.TrimSuffix(r.res.Files[0].URL[:strings.Index(r.res.Files[0].URL, "/blobs/")], "/")
		h.expectFetch(t, prefix+"/manifest.json", r.cookie, 404)
		h.expectFetch(t, prefix+"/originals/"+h.original(id), r.cookie, 404)
	case "teaser":
		if r.res.Access != media.AccessNone || r.cookie != "" {
			t.Fatalf("want teaser only, got %s (cookie %t)", r.res.Access, r.cookie != "")
		}
		for i, f := range r.res.Files {
			if f.Teaser {
				if f.Variant != "blurred" || !strings.Contains(f.URL, "?t=") {
					t.Fatalf("teaser %+v", f)
				}
				h.expectFetch(t, f.URL, "", 200)
				continue
			}
			if !f.Locked || f.URL != "" || f.Name != "" {
				t.Fatalf("locked file leaked: %+v", f)
			}
			// The creator's plain URL and folder cookie are no use to this viewer.
			h.expectFetch(t, owner.res.Files[i].URL, "", 403)
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

// expectRange fetches one HLS byte range from media-access.
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

func (h *mediaHarness) original(id int64) string {
	man, _, err := h.srv.media.manifests.Get(h.ctx, h.srv.media.postRef(id))
	if err != nil {
		h.t.Fatal(err)
	}
	return man.Files[0].Original
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

func (h *mediaHarness) channelSlot(p peer, channel, slot string) func() any {
	return func() any { return p.call("GET", "/api/v1/channels/"+channel, nil, "", 200)[slot] }
}

// waitSlot polls an API slot manifest until a version other than prev lists
// exactly widths (the SlotEncoded hook recorded it), then fetches each
// versioned output from the access worker.
func (h *mediaHarness) waitSlot(t *testing.T, what string, get func() any, prev string, widths ...int) media.SlotManifest {
	t.Helper()
	var man media.SlotManifest
	eventually(t, what, func() bool {
		raw, _ := json.Marshal(get())
		man = media.SlotManifest{}
		if json.Unmarshal(raw, &man) != nil || man.Version == "" || man.Version == prev || len(man.Outputs) != len(widths) {
			return false
		}
		for i, o := range man.Outputs {
			if o.W != widths[i] {
				return false
			}
		}
		return true
	})
	for _, o := range man.Outputs {
		if !strings.HasSuffix(o.URL, "?"+media.SlotVersionParam+"="+man.Version) {
			t.Fatalf("%s output %s is not versioned", what, o.URL)
		}
		h.waitPublic(t, o.URL)
	}
	return man
}

func (h *mediaHarness) waitPublic(t *testing.T, u string) {
	if u == "" {
		t.Fatal("no public URL")
	}
	eventually(t, "public slot "+u, func() bool {
		res, err := http.Get(u)
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200 && res.Header.Get("Content-Type") == "image/webp"
	})
}

// purchase buys a post through the app's checkout and settles it with a
// signed provider webhook.
func (h *mediaHarness) purchase(p peer, id int64) {
	path := fmt.Sprintf("/api/v1/posts/%d", id)
	var post map[string]any
	eventually(h.t, "offer of "+path, func() bool {
		post = p.call("GET", path, nil, "", 200)
		return post["offer_status"] == "active" && len(post["offers"].([]any)) > 0
	})
	price := post["offers"].([]any)[0].(map[string]any)["price_id"]
	p.call("POST", path+"/checkout", map[string]any{"price_id": price, "payment": map[string]string{"rail": "stripe"}}, "buy-"+p.name, 201)
	if err := h.stripe.settleLatest(h.ctx, h.base+h.hook); err != nil {
		h.t.Fatal(err)
	}
	eventually(h.t, p.name+" purchase", func() bool { return p.call("GET", path, nil, "", 200)["purchased"] == true })
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
	var data any
	switch req.Method + " " + req.URL.Path {
	case "GET /v1/account":
		data = map[string]any{"id": "acct_demo_test", "object": "account", "charges_enabled": true, "details_submitted": true, "country": "US", "default_currency": "usd"}
	case "GET /v1/balance":
		data = map[string]any{"object": "balance", "available": []any{}, "pending": []any{}, "livemode": false}
	case "GET /v1/customers/search":
		data = map[string]any{"object": "search_result", "data": []any{}, "has_more": false}
	case "GET /v1/subscriptions", "GET /v1/webhook_endpoints":
		data = map[string]any{"object": "list", "data": []any{}, "has_more": false}
	case "POST /v1/customers":
		s.customers++
		data = map[string]any{"id": fmt.Sprintf("cus_media%d", s.customers), "object": "customer"}
	case "POST /v1/checkout/sessions":
		s.sessions = append(s.sessions, req.PostForm)
		data = map[string]any{"id": fmt.Sprintf("cs_media%d", len(s.sessions)), "object": "checkout.session", "status": "open", "payment_status": "unpaid",
			"url": fmt.Sprintf("https://checkout.stripe.com/c/pay/cs_media%d", len(s.sessions))}
	default:
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
