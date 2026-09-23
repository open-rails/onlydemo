//go:build smoke

// Run explicitly with scripts/smoke.sh. This manual walkthrough uses real local
// database/auth/billing/HTTP behavior and a closed fake Stripe transport.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const smokeWebhookSecret = "whsec_manual_fake_only"

func main() {
	if err := smoke(); err != nil {
		fmt.Fprintln(os.Stderr, "smoke:", err)
		os.Exit(1)
	}
}

func smoke() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, err := pgxpool.ParseConfig(os.Getenv("SMOKE_DATABASE_URL"))
	if err != nil {
		return err
	}
	ip := net.ParseIP(db.ConnConfig.Host)
	if db.ConnConfig.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("SMOKE_DATABASE_URL must use localhost or a loopback IP")
	}
	admin, err := pgx.ConnectConfig(ctx, db.ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer admin.Close(context.Background())
	name := "demo_smoke_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			fmt.Fprintln(os.Stderr, "remove smoke database", name, err)
		}
	}()
	db.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, db)
	if err != nil {
		return err
	}
	defer pool.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	base := "http://" + listener.Addr().String()
	databaseURL := (&url.URL{Scheme: "postgres", User: url.UserPassword(db.ConnConfig.User, db.ConnConfig.Password), Host: net.JoinHostPort(db.ConnConfig.Host, strconv.Itoa(int(db.ConnConfig.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}).String()
	cfg := Config{DatabaseURL: databaseURL, PublicURL: base, AuthIssuer: base, AuthAudience: "demo-smoke", StripeSecretKey: "sk_test_manual_fake_only", StripeAccountID: "acct_demo_test", StripeWebhookSecret: smokeWebhookSecret}
	if err = initializeDatabase(ctx, cfg, pool); err != nil {
		return err
	}
	auth, err := newAuth(ctx, cfg, pool)
	if err != nil {
		return err
	}
	defer auth.Close()
	stripe := &smokeStripe{}
	billing, err := newBilling(ctx, cfg, pool, auth, billingOptions{StripeTransport: stripe})
	if err != nil {
		return err
	}
	defer billing.Close(context.Background())
	channels := newChannels(pool, auth, billing, cfg)
	jobs, err := newJobs(ctx, pool, cfg, auth, billing, channels)
	if err != nil {
		return err
	}
	defer stopJobs(context.Background(), jobs)
	if err = auth.runtime.Start(ctx); err != nil {
		return err
	}
	if err = jobs.Start(ctx); err != nil {
		return err
	}
	app, err := newApp(pool, auth, billing, cfg, channels)
	if err != nil {
		return err
	}
	defer app.Shutdown()
	go func() { _ = app.Listener(listener) }()
	owner := smokePeer(base, "127.0.0.2")
	buyer := smokePeer(base, "127.0.0.3")
	ownerToken, err := owner.register("smokeowner")
	if err != nil {
		return err
	}
	buyerToken, err := buyer.register("smokebuyer")
	if err != nil {
		return err
	}
	ch, err := owner.call("POST", "/api/v1/channels", ownerToken, map[string]any{"slug": "smoke-channel", "name": "Manual smoke"}, "", 201)
	if err != nil {
		return err
	}
	post, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": ch["id"], "slug": "smoke-post", "title": "Manual purchase", "body": "Paid content", "visibility": "private", "price_cents": 499}, "", 201)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/posts/%.0f", post["id"].(float64))
	preview, err := buyer.call("GET", path, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if preview["can_read"] != false || preview["body"] != nil {
		return fmt.Errorf("unpaid content leaked")
	}
	checkout, err := buyer.call("POST", path+"/checkout", buyerToken, nil, "manual-purchase", 201)
	if err != nil {
		return err
	}
	replay, err := buyer.call("POST", path+"/checkout", buyerToken, nil, "manual-purchase", 201)
	if err != nil {
		return err
	}
	if checkout["id"] != replay["id"] {
		return fmt.Errorf("checkout replay created a new session")
	}
	if err = stripe.settle(ctx, base); err != nil {
		return err
	}
	paid, err := buyer.call("GET", path, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if paid["can_read"] != true || paid["body"] != "Paid content" {
		return fmt.Errorf("signed payment did not grant access")
	}
	if _, err = buyer.call("GET", "/billing/v1/me/payments", buyerToken, nil, "", 200); err != nil {
		return err
	}
	if _, err = buyer.call("POST", "/api/v1/register", "", map[string]any{}, "", 404); err != nil {
		return err
	}
	if _, err = buyer.call("GET", "/.well-known/jwks.json", "", nil, "", 200); err != nil {
		return err
	}
	fmt.Println("Manual smoke passed: native auth, channel/post, checkout replay, signed fake payment, paid access and billing history. Zero real provider requests.")
	return nil
}

type smokeClient struct {
	base   string
	client *http.Client
}

func smokePeer(base, address string) smokeClient {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(address)}}
	return smokeClient{base, &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}}
}
func (s smokeClient) call(method, path, token string, body any, key string, status int) (map[string]any, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, s.base+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != status {
		return nil, fmt.Errorf("%s %s returned%d, want%d: %s", method, path, response.StatusCode, status, raw)
	}
	result := map[string]any{}
	if len(raw) > 0 && raw[0] == '{' {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s smokeClient) register(name string) (string, error) {
	_, err := s.call("POST", "/auth/v1/register", "", map[string]any{"identifier": name + "@example.test", "username": name, "password": "Manual-smoke-password-42!"}, "", 202)
	if err != nil {
		return "", err
	}
	login, err := s.call("POST", "/auth/v1/password/login", "", map[string]any{"identifier": name + "@example.test", "password": "Manual-smoke-password-42!"}, "", 200)
	if err != nil {
		return "", err
	}
	token, _ := login["access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("login returned no access token")
	}
	return token, nil
}

// Every request is handled locally or rejected. There is no network fallback.
type smokeStripe struct {
	mu        sync.Mutex
	form      url.Values
	customers int
}

func (s *smokeStripe) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.URL.Host != "api.stripe.com" {
		return nil, fmt.Errorf("unexpected provider host")
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
		data = map[string]any{"id": fmt.Sprintf("cus_smoke_%d", s.customers), "object": "customer"}
	case "POST /v1/checkout/sessions":
		if s.form != nil {
			return nil, fmt.Errorf("unexpected second provider checkout")
		}
		if req.PostForm.Get("line_items[0][price_data][unit_amount]") != "499" {
			return nil, fmt.Errorf("checkout did not use catalog price")
		}
		s.form = req.PostForm
		data = map[string]any{"id": "cs_smoke", "object": "checkout.session", "status": "open", "payment_status": "unpaid", "url": "https://checkout.stripe.com/c/pay/cs_smoke"}
	default:
		return nil, fmt.Errorf("unexpected fake provider request %s %s", req.Method, req.URL.Path)
	}
	encoded, err := json.Marshal(data)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: req}, err
}
func (s *smokeStripe) settle(ctx context.Context, base string) error {
	s.mu.Lock()
	form := s.form
	s.mu.Unlock()
	if form == nil {
		return fmt.Errorf("checkout never reached fake Stripe")
	}
	metadata := map[string]string{}
	for key, values := range form {
		if strings.HasPrefix(key, "metadata[") {
			metadata[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = values[0]
		}
	}
	payload, err := json.Marshal(map[string]any{"id": "evt_smoke_paid", "object": "event", "type": "checkout.session.completed", "livemode": false, "created": time.Now().Unix(), "data": map[string]any{"object": map[string]any{"id": "cs_smoke", "object": "checkout.session", "mode": "payment", "status": "complete", "payment_status": "paid", "customer": form.Get("customer"), "payment_intent": "pi_smoke", "amount_total": 499, "currency": "usd", "metadata": metadata}}})
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(smokeWebhookSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/billing/v1/merchants/openrails-demo/webhooks/stripe/acct_demo_test", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t="+timestamp+",v1="+hex.EncodeToString(mac.Sum(nil)))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("fake paid webhook returned%d", response.StatusCode)
	}
	return nil
}
