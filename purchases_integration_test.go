package main

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
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/openrails"
)

const blogTestStripeWebhookSecret = "whsec_demo_integration_only"

func TestPostPurchasesIntegration(t *testing.T) {
	pool := newBlogTestDatabase(t)
	if err := applyMigrations(t.Context(), pool); err != nil {
		t.Fatalf("migrate purchase test database: %v", err)
	}
	config := Config{
		AuthIssuer: "http://localhost:3000", AuthAudience: "openrails-demo",
		PublicURL: "http://localhost:3000", BillingDatabaseURL: blogTestBillingDSN(t, pool),
		StripeSecretKey: "sk_test_demo_integration", StripeAccountID: "acct_demo_test",
		StripeWebhookSecret:  blogTestStripeWebhookSecret,
		BillingEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)),
	}
	service, err := newAuth(config, pool)
	if err != nil {
		t.Fatalf("initialize AuthKit: %v", err)
	}
	t.Cleanup(func() { service.Close() })
	stripe := &blogTestStripe{}
	billing, err := newBilling(t.Context(), config, stripe)
	if err != nil {
		t.Fatalf("initialize OpenRails: %v", err)
	}
	t.Cleanup(func() {
		if err := billing.Close(context.Background()); err != nil {
			t.Errorf("close OpenRails: %v", err)
		}
	})
	fiberApp, err := newApp(pool, service, billing)
	if err != nil {
		t.Fatalf("mount purchase API: %v", err)
	}
	app := newBlogTestServer(t, fiberApp)
	alice := registerBlogTestUser(t, app, pool, "seller")
	bobApp := &blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.3")}
	bob := registerBlogTestUser(t, bobApp, pool, "buyer")
	charlieApp := &blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.4")}
	charlie := registerBlogTestUser(t, charlieApp, pool, "otherbuyer")
	for _, input := range []map[string]any{
		{"slug": "below-minimum", "title": "Invalid", "body": "Invalid", "price_cents": 49},
		{"slug": "above-maximum", "title": "Invalid", "body": "Invalid", "price_cents": 100_000_000},
		{"slug": "public-paid", "title": "Invalid", "body": "Invalid", "price_cents": 100, "visibility": "public"},
	} {
		blogTestRequest(t, app, http.MethodPost, "/api/posts", alice.token, input, http.StatusBadRequest)
	}
	paid := createBlogTestPost(t, app, alice.token, map[string]any{
		"slug": "paid-post", "title": "Article for sale", "body": "The complete purchased article",
		"visibility": "private", "price_cents": 499,
	})
	checkoutPath := blogTestPath(paid.ID) + "/checkout"
	for _, token := range []string{"", bob.token, charlie.token} {
		assertBlogTestReadable(t, app, paid, token, false)
	}
	assertBlogTestReadable(t, app, paid, alice.token, true)
	// Discovery lists the offer but cannot leak its protected body.
	listed := blogTestRequest(t, app, http.MethodGet, "/api/posts", "", nil, http.StatusOK)
	if bytes.Contains(listed, []byte(paid.Body)) || !bytes.Contains(listed, []byte(`"price_cents":499`)) {
		t.Fatalf("sale listing leaked content or omitted the offer: %s", listed)
	}
	blogTestRequest(t, app, http.MethodPatch, blogTestPath(paid.ID), bob.token, map[string]any{"price_cents": 50}, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodPost, checkoutPath, "", nil, http.StatusUnauthorized)
	blogTestRequest(t, app, http.MethodPost, checkoutPath, alice.token, nil, http.StatusConflict, http.Header{"Idempotency-Key": {"self-purchase"}})
	blogTestRequest(t, app, http.MethodPost, checkoutPath, bob.token, nil, http.StatusBadRequest)

	checkout := blogTestCheckout(t, app, paid.ID, bob.token, "purchase-once", map[string]any{
		"customer_id": charlie.id, "price_cents": 1, "currency": "EUR", "payment_status": "paid",
	})
	if checkout.Status != "requires_action" || checkout.Amount != 4_990_000 || !strings.EqualFold(checkout.Currency, "USD") || checkout.URL == nil {
		t.Fatalf("checkout did not use server-owned USD terms: %+v", checkout)
	}
	first := stripe.latestSession(t)
	if first.form.Get("mode") != "payment" || first.amount != 499 || first.form.Get("metadata[user_id]") != bob.id {
		t.Fatalf("Stripe checkout did not bind buyer and one-time price: %v", first.form)
	}
	retried := blogTestCheckout(t, app, paid.ID, bob.token, "purchase-once", nil)
	if retried.ID != checkout.ID || stripe.latestSession(t).id != first.id {
		t.Fatal("repeated checkout key created a second payment attempt")
	}
	blogTestRequest(t, app, http.MethodGet, "/api/checkouts/"+checkout.ID.String(), charlie.token, nil, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodGet, "/api/checkouts/"+checkout.ID.String(), bob.token, nil, http.StatusOK)
	// Redirect query strings are presentation only; visiting either grants nothing.
	blogTestRequest(t, app, http.MethodGet, "/?checkout=success", bob.token, nil, http.StatusOK)
	blogTestRequest(t, app, http.MethodGet, "/?checkout=canceled", bob.token, nil, http.StatusOK)
	assertBlogTestReadable(t, app, paid, bob.token, false)

	settled := first.event(t, "evt_demo_paid", "checkout.session.completed", "complete", "paid")
	blogTestStripeWebhook(t, app, settled, "whsec_wrong_signature", http.StatusUnauthorized)
	assertBlogTestReadable(t, app, paid, bob.token, false)
	blogTestStripeWebhook(t, app, first.event(t, "evt_demo_unpaid", "checkout.session.completed", "complete", "unpaid"), blogTestStripeWebhookSecret, http.StatusOK)
	assertBlogTestWebhookRecorded(t, pool, "evt_demo_unpaid")
	assertBlogTestReadable(t, app, paid, bob.token, false)

	// A second customer's same retry key stays scoped to that customer. Expiry
	// is accepted by the real webhook dispatcher without granting access.
	otherCheckout := blogTestCheckout(t, app, paid.ID, charlie.token, "purchase-once", nil)
	if otherCheckout.ID == checkout.ID {
		t.Fatal("checkout retry key crossed customer boundary")
	}
	otherSession := stripe.latestSession(t)
	blogTestStripeWebhook(t, app, otherSession.event(t, "evt_demo_expired", "checkout.session.expired", "expired", "unpaid"), blogTestStripeWebhookSecret, http.StatusOK)
	assertBlogTestWebhookRecorded(t, pool, "evt_demo_expired")
	assertBlogTestReadable(t, app, paid, charlie.token, false)

	blogTestStripeWebhook(t, app, settled, blogTestStripeWebhookSecret, http.StatusOK)
	assertBlogTestWebhookRecorded(t, pool, "evt_demo_paid")
	assertBlogTestReadable(t, app, paid, bob.token, true)
	assertBlogTestReadable(t, app, paid, charlie.token, false)
	assertBlogTestReadable(t, app, paid, "", false)
	blogTestRequest(t, app, http.MethodPatch, blogTestPath(paid.ID), bob.token, map[string]any{"title": "Purchased edit"}, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodDelete, blogTestPath(paid.ID), bob.token, nil, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodPost, checkoutPath, bob.token, nil, http.StatusConflict, http.Header{"Idempotency-Key": {"already-bought"}})

	// Replayed Stripe delivery is idempotent. Grant terms remain permanent.
	blogTestStripeWebhook(t, app, settled, blogTestStripeWebhookSecret, http.StatusOK)
	var grantCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM openrails.grants WHERE customer_id=$1 AND kind='ownership' AND event='grant' AND ends_at IS NULL`, bob.id).Scan(&grantCount); err != nil || grantCount != 1 {
		t.Fatalf("permanent purchase grant count=%d, err=%v", grantCount, err)
	}
	for _, price := range []int{799, 0} {
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(paid.ID), alice.token, map[string]any{"price_cents": price}, http.StatusOK)
		assertBlogTestReadable(t, app, paid, bob.token, true)
		assertBlogTestReadable(t, app, paid, alice.token, true)
		if price == 799 {
			newPriceCheckout := blogTestCheckout(t, app, paid.ID, charlie.token, "updated-price", nil)
			if newPriceCheckout.Amount != 7_990_000 || stripe.latestSession(t).amount != 799 {
				t.Fatal("new checkout did not use the author's changed price")
			}
		}
	}
	blogTestRequest(t, app, http.MethodGet, blogTestPath(paid.ID), charlie.token, nil, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodPost, checkoutPath, charlie.token, nil, http.StatusNotFound, http.Header{"Idempotency-Key": {"after-delisting"}})
	assertBlogPostIDs(t, app, bob.token, paid.ID)
	assertBlogPostIDs(t, app, alice.token, paid.ID)
	assertBlogPostIDs(t, app, charlie.token)
	assertBlogPostIDs(t, app, "")
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	for _, version := range stripe.versions {
		if version == "" {
			t.Error("OpenRails did not pin Stripe-Version above the transport seam")
		}
	}
}

func blogTestCheckout(t *testing.T, app *blogTestServer, postID int64, token, key string, input any) openrails.CheckoutSession {
	t.Helper()
	body := blogTestRequest(t, app, http.MethodPost, blogTestPath(postID)+"/checkout", token, input, http.StatusCreated, http.Header{"Idempotency-Key": {key}})
	var checkout openrails.CheckoutSession
	decodeBlogTestJSON(t, body, &checkout)
	return checkout
}

func assertBlogTestReadable(t *testing.T, app *blogTestServer, post blogPost, token string, expected bool) {
	t.Helper()
	body := blogTestRequest(t, app, http.MethodGet, blogTestPath(post.ID), token, nil, http.StatusOK)
	var result map[string]any
	decodeBlogTestJSON(t, body, &result)
	if result["can_read"] != expected {
		t.Fatalf("can_read=%v, want %v: %s", result["can_read"], expected, body)
	}
	if expected {
		if result["body"] != post.Body {
			t.Fatalf("readable post body=%v, want %q", result["body"], post.Body)
		}
	} else if _, present := result["body"]; present {
		t.Fatalf("protected body was included in unpaid response: %s", body)
	}
}

func assertBlogTestWebhookRecorded(t *testing.T, pool *pgxpool.Pool, eventID string) {
	t.Helper()
	var processed bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM openrails.webhook_events WHERE event_id=$1)`, eventID).Scan(&processed); err != nil {
		t.Fatalf("check webhook completion: %v", err)
	}
	if !processed {
		t.Fatalf("Stripe event %s was not durably processed", eventID)
	}
}

// Only the Stripe wire transport is substituted. Catalog creation, checkout,
// signature verification, payment recording and access grants all execute in
// the real embedded OpenRails engine against an isolated PostgreSQL database.
type blogTestStripe struct {
	mu        sync.Mutex
	products  int
	customers int
	prices    map[string]int64
	sessions  []blogTestStripeSession
	versions  []string
}

type blogTestStripeSession struct {
	id       string
	form     url.Values
	amount   int64
	customer string
}

func (s *blogTestStripe) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.URL.Host != "api.stripe.com" {
		return nil, fmt.Errorf("unexpected Stripe request host %q", req.URL.Host)
	}
	s.versions = append(s.versions, req.Header.Get("Stripe-Version"))
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	var body any
	switch req.Method + " " + req.URL.Path {
	case "GET /v1/account":
		body = map[string]any{"id": "acct_demo_test", "object": "account", "charges_enabled": true, "details_submitted": true, "country": "US", "default_currency": "usd"}
	case "GET /v1/balance":
		body = map[string]any{"object": "balance", "available": []any{}, "pending": []any{}, "livemode": false}
	case "GET /v1/products/search", "GET /v1/customers/search":
		body = map[string]any{"object": "search_result", "data": []any{}, "has_more": false}
	case "GET /v1/prices", "GET /v1/subscriptions", "GET /v1/webhook_endpoints":
		body = map[string]any{"object": "list", "data": []any{}, "has_more": false}
	case "POST /v1/products":
		s.products++
		body = map[string]any{"id": fmt.Sprintf("prod_demo_%d", s.products), "object": "product"}
	case "POST /v1/prices":
		if s.prices == nil {
			s.prices = make(map[string]int64)
		}
		amount, err := strconv.ParseInt(req.PostForm.Get("unit_amount"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("Stripe price amount: %w", err)
		}
		id := fmt.Sprintf("price_demo_%d", len(s.prices)+1)
		s.prices[id] = amount
		body = map[string]any{"id": id, "object": "price", "unit_amount": amount, "currency": "usd", "product": req.PostForm.Get("product")}
	case "POST /v1/customers":
		s.customers++
		body = map[string]any{"id": fmt.Sprintf("cus_demo_%d", s.customers), "object": "customer"}
	case "POST /v1/checkout/sessions":
		id := fmt.Sprintf("cs_test_demo_%d", len(s.sessions)+1)
		priceID := req.PostForm.Get("line_items[0][price]")
		amount, ok := s.prices[priceID]
		if !ok {
			return nil, fmt.Errorf("checkout used unknown Stripe price %q", priceID)
		}
		s.sessions = append(s.sessions, blogTestStripeSession{id: id, form: req.PostForm, amount: amount, customer: req.PostForm.Get("customer")})
		body = map[string]any{"id": id, "object": "checkout.session", "status": "open", "payment_status": "unpaid", "url": "https://checkout.stripe.com/c/pay/" + id}
	case "POST /v1/webhook_endpoints":
		body = map[string]any{"id": "we_demo_test", "object": "webhook_endpoint", "status": "enabled", "url": req.PostForm.Get("url"), "api_version": req.PostForm.Get("api_version"), "enabled_events": req.PostForm["enabled_events[]"], "metadata": map[string]string{"openrails_managed": "true"}, "secret": blogTestStripeWebhookSecret}
	default:
		return nil, fmt.Errorf("unexpected Stripe request %s %s", req.Method, req.URL.Path)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: req}, nil
}

func (s *blogTestStripe) latestSession(t *testing.T) blogTestStripeSession {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) == 0 {
		t.Fatal("checkout never reached the Stripe transport")
	}
	return s.sessions[len(s.sessions)-1]
}

func (s blogTestStripeSession) event(t *testing.T, eventID, eventType, status, paymentStatus string) []byte {
	t.Helper()
	metadata := make(map[string]string)
	for key, values := range s.form {
		if strings.HasPrefix(key, "metadata[") && strings.HasSuffix(key, "]") {
			metadata[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = values[0]
		}
	}
	body, err := json.Marshal(map[string]any{
		"id": eventID, "object": "event", "type": eventType, "livemode": false, "created": time.Now().Unix(),
		"data": map[string]any{"object": map[string]any{
			"id": s.id, "object": "checkout.session", "mode": "payment", "status": status,
			"payment_status": paymentStatus, "customer": s.customer, "payment_intent": "pi_" + s.id,
			"amount_total": s.amount, "currency": "usd", "metadata": metadata,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func blogTestStripeWebhook(t *testing.T, app *blogTestServer, payload []byte, secret string, expectedStatus int) {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(payload)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, app.url+"/billing/v1/merchants/openrails-demo/webhooks/stripe", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t="+timestamp+",v1="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := app.client.Do(req)
	if err != nil {
		t.Fatalf("deliver Stripe webhook: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != expectedStatus {
		t.Fatalf("Stripe webhook returned %d, want %d: %s", resp.StatusCode, expectedStatus, body)
	}
}

// Run OpenRails with a non-superuser LOGIN role inheriting the upstream
// openrails_app grants; never weaken RLS or alter a shared cluster role.
func blogTestBillingDSN(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	role := "demo_billing_test_" + strings.ToLower(rand.Text())
	password := rand.Text()
	if _, err := pool.Exec(t.Context(), "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create isolated billing role: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := pool.Exec(ctx, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Errorf("drop isolated billing role: %v", err)
		}
	})
	if _, err := pool.Exec(t.Context(), "GRANT openrails_app TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatalf("grant billing RLS role: %v", err)
	}
	config := pool.Config().ConnConfig
	dsn := &url.URL{Scheme: "postgres", Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + config.Database, User: url.UserPassword(role, password)}
	query := dsn.Query()
	if config.TLSConfig == nil {
		query.Set("sslmode", "disable")
	} else {
		query.Set("sslmode", "require")
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}
