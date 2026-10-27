package main

// Integration harness on real services: PostgreSQL (app, AuthKit, OpenRails,
// ContentKit and River, each test in schemas of its own), OpenRails's nmimock
// gateway on loopback and an S3 bucket.
//
//	DEMO_TEST_DATABASE_URL   PostgreSQL with PGroonga; each test's schemas are dropped after it
//	DEMO_TEST_S3_ENDPOINT    e.g. http://127.0.0.1:9000 (MinIO); a bucket is created and emptied.
//	                         Unset: an in-process S3 serves the media store's probe only.
//	DEMO_TEST_S3_ACCESS_KEY, DEMO_TEST_S3_SECRET_KEY

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/jonboulle/clockwork"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/openrailstest/nmimock"
)

const (
	testPassword   = "Demo-test-password-42!"
	nmiTestAccount = "demo-nmi-test"
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	cfg    Config
	pool   *pgxpool.Pool
	srv    *server
	base   string
	nmi    *nmimock.Mock
	clock  *clockwork.FakeClock // nil: real time
	s3     *s3.Client
	bucket string
}

type harnessOptions struct {
	// clock drives OpenRails and the gateway from a fake clock in the past.
	clock bool
	// minio needs DEMO_TEST_S3_ENDPOINT: real uploads land in its bucket.
	minio             bool
	providerWriteMode string
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	dbURL := os.Getenv("DEMO_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("DEMO_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	h := &harness{t: t, ctx: ctx}
	now := time.Now
	if o.clock {
		// Engine time starts in the past so everything it schedules is
		// already due on River's wall clock; the test steps it.
		h.clock = clockwork.NewFakeClockAt(time.Now().UTC().AddDate(-4, 0, 0).Truncate(time.Second))
		now = h.clock.Now
	}
	h.nmi = nmimock.New(nmimock.Options{Clock: now})
	t.Cleanup(h.nmi.Close)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	h.cfg = Config{AuthTenantID: iam.NewTenantID(), AuthSchema: "od_auth_" + suffix, BillingSchema: "od_bill_" + suffix,
		RiverSchema: "od_river_" + suffix, ContentSchema: "od_ck_" + suffix, AuthKeysPath: "", AuthAudience: "demo-test",
		Merchant: testMerchant(openrails.NMIPSP{Environment: openrails.EnvironmentSandbox, GatewayID: nmiTestAccount, SecurityKey: "nmimock-security-key",
			WebhookSigningSecret: "nmimock-webhook-secret", TokenizationKey: "nmimock-tokenization"}.PSPConfig())}
	h.cfg.ProviderWriteMode = o.providerWriteMode
	if h.cfg.ProviderWriteMode == "" {
		h.cfg.ProviderWriteMode = openrails.ProviderWritesFull
	}
	h.pool = testPool(t, ctx, dbURL, h.cfg)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + listener.Addr().String()
	h.cfg.DatabaseURL, h.cfg.PublicURL, h.cfg.ReturnOrigins, h.cfg.AuthIssuer = h.pool.Config().ConnString(), h.base, []string{h.base}, h.base

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	endpoint, access, secret := os.Getenv("DEMO_TEST_S3_ENDPOINT"), os.Getenv("DEMO_TEST_S3_ACCESS_KEY"), os.Getenv("DEMO_TEST_S3_SECRET_KEY")
	switch {
	case o.minio && endpoint == "":
		t.Skip("DEMO_TEST_S3_ENDPOINT is required")
	case !o.minio:
		fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
		t.Cleanup(fake.Close)
		endpoint, access, secret = fake.URL, "demo-test", "demo-test-secret"
	}
	h.bucket = "demo-" + suffix
	h.s3 = testS3Client(endpoint, access, secret)
	if _, err = h.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &h.bucket}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.removeBucket)
	h.cfg.Media = mediaConfig{S3Endpoint: endpoint, S3PublicEndpoint: endpoint, S3Bucket: h.bucket, S3Region: "us-east-1",
		S3AccessKeyID: access, S3SecretKey: secret, URL: "http://127.0.0.1:9", Delivery: media.DeliverCookie, CookieDomain: "localhost",
		TokenKey: "k1:" + base64.StdEncoding.EncodeToString(key)}

	if err = initializeDatabase(ctx, h.cfg, h.pool); err != nil {
		t.Fatal(err)
	}
	opts := billingOptions{Sandbox: &openrails.ProviderSandboxConfig{NMIGatewayURL: h.nmi.URL()}}
	if h.clock != nil {
		opts.Test = func(d *openrails.Deps) { d.Clock = h.clock }
	}
	if h.srv, err = startServer(ctx, h.cfg, h.pool, opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.srv.Close)
	go func() { _ = h.srv.app.Listener(listener) }()
	t.Cleanup(func() { _ = h.srv.app.Shutdown() })
	return h
}

// testS3Client administers the test bucket outside the media store.
func testS3Client(endpoint, access, secret string) *s3.Client {
	return s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(access, secret, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
}

// testPool connects to the test database and drops cfg's schemas afterwards.
func testPool(t *testing.T, ctx context.Context, url string, cfg Config) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, s := range []string{cfg.AuthSchema, cfg.BillingSchema, cfg.RiverSchema, cfg.ContentSchema, mediaWorkerSchema(cfg)} {
			if _, err := pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{s}.Sanitize()+" CASCADE"); err != nil {
				t.Errorf("drop schema %s: %v", s, err)
			}
		}
		pool.Close()
	})
	return pool
}

func (h *harness) removeBucket() {
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

// testMerchant is the demo merchant selling through psp, keyed by its rail.
func testMerchant(psp openrails.PSPConfig) openrails.MerchantDeclaration {
	return openrails.MerchantDeclaration{Slug: "onlydemo", DisplayName: "OnlyDemo", PSPs: map[string]openrails.PSPConfig{string(psp.Rail): psp}}
}

func (h *harness) client(ip string) *http.Client {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type peer struct {
	h      *harness
	name   string
	id     string
	token  string
	client *http.Client
}

// register signs a new person up and in, each from its own loopback address.
func (h *harness) register(name, ip string) peer {
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

func (h *harness) anonymous(ip string) peer {
	return peer{h: h, name: "anonymous", client: h.client(ip)}
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
	if key == "" && method == "POST" && strings.HasSuffix(path, "/pay") {
		key = uuid.NewString()
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

func ref(kind, id string) map[string]string { return map[string]string{"kind": kind, "id": id} }

// postRef is a post version's media item.
func postRef(id, version string) map[string]string {
	return map[string]string{"kind": kindPost, "id": id, "version": version}
}

// presign asks ContentKit for an upload plan.
func (p peer) presign(kind, id, path string, data []byte, typ string, status int) map[string]any {
	p.h.t.Helper()
	return p.presignRef(ref(kind, id), path, data, typ, status)
}

func (p peer) presignVersion(id, version, path string, data []byte, typ string, status int) map[string]any {
	p.h.t.Helper()
	return p.presignRef(postRef(id, version), path, data, typ, status)
}

func (p peer) presignRef(item map[string]string, path string, data []byte, typ string, status int) map[string]any {
	p.h.t.Helper()
	sum := sha256.Sum256(data)
	return p.call("POST", contentkitPrefix+"/v1/media/upload/presign", map[string]any{"ref": item,
		"path": path, "type": typ, "size": len(data), "sha256": hex.EncodeToString(sum[:])}, "", status)
}

func (p peer) put(kind, id, path string, data []byte, typ string) string {
	p.h.t.Helper()
	return p.putRef(ref(kind, id), path, data, typ)
}

func (p peer) putVersion(id, version, path string, data []byte, typ string) string {
	p.h.t.Helper()
	return p.putRef(postRef(id, version), path, data, typ)
}

// putRef plays the browser SDK: presign, a checksum-bound PUT straight to
// the bucket, then the commit. It returns the committed path.
func (p peer) putRef(item map[string]string, path string, data []byte, typ string) string {
	p.h.t.Helper()
	plan := p.presignRef(item, path, data, typ, 200)
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
	committed := plan["path"].(string)
	p.call("POST", contentkitPrefix+"/v1/media/upload/commit", map[string]any{"ref": item,
		"ops": []media.Op{{Op: media.OpPut, Path: committed, Blob: plan["blob"].(string)}}, "operation_id": uuid.NewString()}, "", 200)
	return committed
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
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// post is a published post of channel, made through ContentKit's catalog:
// its content id and its one (hidden) version's id.
func (p peer) post(channel, title, body string, rule map[string]any) (string, string) {
	p.h.t.Helper()
	in := map[string]any{"kind": kindPost, "channel_id": channel, "versions": []map[string]any{{"languages": []string{"en"}, "text": map[string]any{"en": map[string]string{"title": title, "body": body}}}}}
	if rule != nil {
		in["access"] = rule
	}
	created := p.call("POST", contentkitPrefix+"/v1/contents", in, "", 201)
	id := created["id"].(string)
	versions, _ := created["versions"].([]any)
	if len(versions) != 1 {
		p.h.t.Fatalf("post %s has versions %v", id, versions)
	}
	p.call("POST", contentkitPrefix+"/v1/contents/"+id+"/publish", map[string]any{}, "", 200)
	eventually(p.h.t, "the post's durable publication", func() bool {
		post := p.call("GET", contentkitPrefix+"/v1/contents/"+id, nil, "", 200)
		versions, _ := post["versions"].([]any)
		return len(versions) == 1 && versions[0].(map[string]any)["live"] == true
	})
	return id, versions[0].(map[string]any)["id"].(string)
}

// opens reports whether p opens content id's first version.
func (p peer) opens(id string) bool {
	p.h.t.Helper()
	c := p.call("GET", contentkitPrefix+"/v1/contents/"+id, nil, "", 200)
	versions, _ := c["versions"].([]any)
	if len(versions) == 0 {
		p.h.t.Fatalf("%s sees no version of %s: %v", p.name, id, c)
	}
	d, _ := versions[0].(map[string]any)["decision"].(map[string]any)
	return d["allowed"] == true
}

// channel creates p's channel through ContentKit, which provisions its
// AuthKit group with p as owner.
func (p peer) channel(slug, name string) string {
	p.h.t.Helper()
	res, raw := p.do("POST", contentkitPrefix+"/v1/channels", map[string]any{"slug": slug,
		"names": []map[string]string{{"language": "en", "kind": "name", "name": name}}}, "")
	var created struct{ ID string }
	if res.StatusCode != 201 || json.Unmarshal(raw, &created) != nil || created.ID == "" {
		p.h.t.Fatalf("%s: create channel %s = %d: %s", p.name, slug, res.StatusCode, raw)
	}
	return created.ID
}

// contentPrice waits for content id's paywall to offer key on sale and
// returns the offer's OpenRails price, as the browser's Paywall reads it.
func (p peer) contentPrice(id string) string {
	post := p.call("GET", contentkitPrefix+"/v1/contents/"+id, nil, "", 200)
	versions, _ := post["versions"].([]any)
	if len(versions) != 1 {
		p.h.t.Fatalf("post has no single edition: %v", post)
	}
	return p.productPrice("onlydemo:content:" + id + "/" + versions[0].(map[string]any)["id"].(string))
}
func (p peer) groupPrice(id string) string { return p.productPrice("onlydemo:group:" + id) }
func (p peer) productPrice(key string) string {
	p.h.t.Helper()
	product := p.call("GET", "/api/v1/products?key="+key, nil, "", 200)
	prices, _ := product["prices"].([]any)
	if len(prices) != 1 {
		p.h.t.Fatalf("product has no single current price: %v", product)
	}
	return prices[0].(map[string]any)["id"].(string)
}
func (p peer) removePost(id string) {
	p.h.t.Helper()
	post := p.call("GET", contentkitPrefix+"/v1/contents/"+id, nil, "", 200)
	for _, version := range post["versions"].([]any) {
		p.call("DELETE", contentkitPrefix+"/v1/contents/"+id+"/versions/"+version.(map[string]any)["id"].(string), nil, "", 204)
	}
}

// body is content id's text body as p reads it; "" when withheld.
func (p peer) body(id string) string {
	p.h.t.Helper()
	b, _ := p.call("GET", contentkitPrefix+"/v1/contents/"+id, nil, "", 200)["body"].(string)
	return b
}
