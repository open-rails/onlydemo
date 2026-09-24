package main

// End-to-end media proof on real services: PostgreSQL (app, AuthKit,
// OpenRails, River), an S3 backend (MinIO in CI), the real ContentKit upload
// and read handlers, the libvips image job and the media-access binary. Only
// the Stripe API is replaced, by a closed local fake.
//
//	DEMO_TEST_DATABASE_URL   loopback PostgreSQL admin URL; a database is created and dropped
//	DEMO_TEST_S3_ENDPOINT    e.g. http://127.0.0.1:9000; a bucket is created and emptied
//	DEMO_TEST_S3_ACCESS_KEY, DEMO_TEST_S3_SECRET_KEY
//	DEMO_TEST_MEDIA_ACCESS   media-access binary; built from the pinned ContentKit when unset

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
	"github.com/open-rails/openrails"
	openrailsembed "github.com/open-rails/openrails/embed"
)

const (
	testWebhookSecret = "whsec_media_test_fake_only"
	testPassword      = "Media-test-password-42!"
	testQuota         = 3 << 20
	testFilesPerHour  = 16
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
		view := anon.call("GET", "/api/v1/channels/"+channelID, nil, "", 200)
		h.waitPublic(t, view["avatar_url"].(string))

		stranger.upload(map[string]string{"kind": media.UserKind, "id": owner.id}, "avatar", testPNG(t, 64, 64, color.White), "image/png", 403)
		stranger.upload(map[string]string{"kind": media.UserKind, "id": stranger.id}, "avatar", testPNG(t, 400, 400, color.Black), "image/png", 200)
		me := stranger.call("GET", "/api/v1/me", nil, "", 200)
		h.waitPublic(t, me["user"].(map[string]any)["avatar_url"].(string))
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
			Secrets: map[string]string{"secret_key": "sk_test_fake_only", "webhook_signing_secret": testWebhookSecret}}}}, AuthKeysPath: "",
		Media: mediaConfig{Tenant: "o", S3Endpoint: endpoint, S3PublicEndpoint: endpoint, S3Bucket: h.bucket, S3Region: "us-east-1",
			S3AccessKeyID: access, S3SecretKey: secret, URL: worker, Delivery: media.DeliverCookie, CookieDomain: "localhost",
			TokenKey: tokenKey, FilesPerHour: testFilesPerHour, BytesPerDay: 1 << 30, ChannelQuota: testQuota}}
	if err = initializeDatabase(ctx, h.cfg, pool); err != nil {
		t.Fatal(err)
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
// bucket, and commit-slot for slots. It returns the original's name.
func (p peer) upload(ref map[string]string, slot string, data []byte, typ string, status int) string {
	p.h.t.Helper()
	plan := p.presign(ref, slot, data, typ, status, "")
	if status != 200 || plan["exists"] == true {
		name, _ := plan["name"].(string)
		return name
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
	if slot != "" {
		sum := sha256.Sum256(data)
		p.call("POST", "/api/v1/media/upload/commit-slot", map[string]any{"ref": ref, "slot": slot, "sha256": hex.EncodeToString(sum[:])}, "", 204)
	}
	return plan["name"].(string)
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

// waitDerived waits for the image job to fill every file's variant.
func (h *mediaHarness) waitDerived(p peer, id int64) {
	eventually(h.t, fmt.Sprintf("variants of post %d", id), func() bool {
		r := h.read(p, id)
		for _, f := range r.res.Files {
			if f.URL == "" {
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
