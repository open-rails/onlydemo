package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/contentkit/media/gateway"
	"github.com/open-rails/contentkit/media/layout"
	"github.com/open-rails/contentkit/media/token"
)

// TestMediaDefaults: media-defaults renders the avatar default into the
// bucket, and the gateway, given the value it prints, serves it to a person
// who never uploaded an avatar.
func TestMediaDefaults(t *testing.T) {
	endpoint, access, secret := os.Getenv("DEMO_TEST_S3_ENDPOINT"), os.Getenv("DEMO_TEST_S3_ACCESS_KEY"), os.Getenv("DEMO_TEST_S3_SECRET_KEY")
	if endpoint == "" {
		t.Skip("DEMO_TEST_S3_ENDPOINT is required")
	}
	ctx := t.Context()
	bucket := "demo-defaults-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	h := &harness{t: t, s3: testS3Client(endpoint, access, secret), bucket: bucket}
	if _, err := h.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.removeBucket)

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	tokenKey := "k1:" + base64.StdEncoding.EncodeToString(key)
	cfg := Config{Media: mediaConfig{S3Endpoint: endpoint, S3PublicEndpoint: endpoint, S3Bucket: bucket, S3Region: "us-east-1",
		S3AccessKeyID: access, S3SecretKey: secret, URL: "http://media.test", Delivery: media.DeliverURL, TokenKey: tokenKey}}
	value, err := publishMediaDefaults(ctx, cfg)
	if err != nil || !strings.Contains(value, tenant+"/user:") {
		t.Fatalf("media-defaults: %q %v", value, err)
	}
	if again, err := publishMediaDefaults(ctx, cfg); err != nil || again != value {
		t.Fatalf("a rerun: %q %v, want %q", again, err, value)
	}

	defaults, err := layout.ParseDefaults(value)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := token.ParseRing(tokenKey, "")
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(gateway.Config{Endpoint: endpoint, AccessKeyID: access, SecretAccessKey: secret, Ring: ring,
		Hosts: map[string][]string{"media.test": {tenant}}, Buckets: map[string]string{tenant: bucket}, Defaults: defaults})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	for _, width := range []string{"64", "512"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/"+tenant+"/user/"+uuid.NewString()+"/public/avatar-"+width+".webp", nil)
		req.Host = "media.test"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/webp" {
			t.Fatalf("a person without an avatar, %spx: %d %s", width, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}
