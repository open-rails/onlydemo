package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/riverqueue/river"
)

const blogTestStripeWebhookSecret = "whsec_demo_integration_only"

func TestPostPurchasesIntegration(t *testing.T) {
	for _, test := range []struct {
		name, billingSchema, riverSchema, authSchema, appSchema string
		maxConns                                                int32
	}{
		{name: "default_schema_single_connection", maxConns: 1},
		{name: "custom_schemas", billingSchema: "demo_billing", riverSchema: "demo_jobs"},
		{name: "all_public_single_connection", billingSchema: "public", riverSchema: "public", authSchema: "public", appSchema: "public", maxConns: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runPostPurchasesIntegration(t, test.billingSchema, test.riverSchema, test.maxConns, test.authSchema, test.appSchema)
		})
	}
}

func runPostPurchasesIntegration(t *testing.T, billingSchema, riverSchema string, maxConns int32, schemas ...string) {
	migrationPool := newBlogTestDatabase(t)
	config := Config{
		AuthIssuer: "http://localhost:3000", AuthAudience: "openrails-demo",
		PublicURL:       "http://localhost:3000",
		StripeSecretKey: "sk_test_demo_integration", StripeAccountID: "acct_demo_test",
		StripeWebhookSecret: blogTestStripeWebhookSecret,
		BillingSchema:       billingSchema, RiverSchema: riverSchema,
	}
	if len(schemas) == 2 {
		config.AuthSchema, config.AppSchema = schemas[0], schemas[1]
	}
	table := pgx.Identifier{appSchema(config), "blog_posts"}.Sanitize()
	pool, databaseURL := newBlogTestOwnerPool(t, migrationPool, maxConns)
	config.DatabaseURL = databaseURL
	if err := initializeDatabase(t.Context(), config, pool); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	assertBlogTestOwnerRole(t, pool)
	if config.AppSchema == "public" {
		if err := initializeDatabase(t.Context(), config, pool); err != nil {
			t.Fatalf("repeat public initialization: %v", err)
		}
		assertAllPublicStorage(t, pool)
	}
	migrationPool.Close()
	service, err := newAuth(t.Context(), config, pool)
	if err != nil {
		t.Fatalf("initialize AuthKit: %v", err)
	}
	t.Cleanup(func() {
		service.Close()
		if err := pool.Ping(context.Background()); err != nil {
			t.Errorf("services closed the borrowed host pool: %v", err)
		}
	})
	stripe := &blogTestStripe{}
	billing, err := newBilling(t.Context(), config, pool, service, billingOptions{StripeTransport: stripe})
	if err != nil {
		t.Fatalf("initialize OpenRails: %v", err)
	}
	t.Cleanup(func() {
		if err := billing.Close(context.Background()); err != nil {
			t.Errorf("close OpenRails: %v", err)
		}
	})
	channels := newChannels(pool, service, billing, config)
	jobs, err := newJobs(t.Context(), pool, config, service, billing, channels)
	if err != nil {
		t.Fatalf("compose jobs: %v", err)
	}
	t.Cleanup(func() {
		if err := stopJobs(context.Background(), jobs); err != nil {
			t.Errorf("stop host jobs: %v", err)
		}
	})
	jobEvents, unsubscribe := jobs.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()
	if err := service.runtime.Start(t.Context()); err != nil {
		t.Fatalf("start AuthKit: %v", err)
	}
	if err := jobs.Start(t.Context()); err != nil {
		t.Fatalf("start host River: %v", err)
	}
	fiberApp, err := newApp(pool, service, billing, config, channels)
	if err != nil {
		t.Fatalf("mount purchase API: %v", err)
	}
	app := newBlogTestServer(t, fiberApp)
	alice := registerBlogTestUser(t, app, service, "seller")
	bobApp := &blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.3")}
	bob := registerBlogTestUser(t, bobApp, service, "buyer")
	charlieApp := &blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.4")}
	charlie := registerBlogTestUser(t, charlieApp, service, "otherbuyer")
	for _, input := range []map[string]any{
		{"slug": "below-minimum", "title": "Invalid", "body": "Invalid", "price_cents": 49},
		{"slug": "above-maximum", "title": "Invalid", "body": "Invalid", "price_cents": 100_000_000},
		{"slug": "public-paid", "title": "Invalid", "body": "Invalid", "price_cents": 100, "visibility": "public"},
	} {
		input["channel_id"] = alice.channelID
		blogTestRequest(t, app, http.MethodPost, "/api/v1/posts", alice.token, input, http.StatusBadRequest)
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
	listed := blogTestRequest(t, app, http.MethodGet, "/api/v1/posts", "", nil, http.StatusOK)
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
	if checkout.Status != "requires_action" || checkout.Amount == nil || *checkout.Amount != 4_990_000 || checkout.Currency == nil || !strings.EqualFold(*checkout.Currency, "USD") || checkout.URL == nil {
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
	blogTestRequest(t, app, http.MethodGet, "/api/v1/checkouts/"+checkout.ID, charlie.token, nil, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodGet, "/api/v1/checkouts/"+checkout.ID, bob.token, nil, http.StatusOK)
	// Redirect query strings are presentation only; visiting either grants nothing.
	blogTestRequest(t, app, http.MethodGet, "/?checkout=success", bob.token, nil, http.StatusOK)
	blogTestRequest(t, app, http.MethodGet, "/?checkout=canceled", bob.token, nil, http.StatusOK)
	assertBlogTestReadable(t, app, paid, bob.token, false)

	settled := first.event(t, "evt_demo_paid", "checkout.session.completed", "complete", "paid")
	blogTestStripeWebhook(t, app, settled, "whsec_wrong_signature", http.StatusUnauthorized)
	assertBlogTestReadable(t, app, paid, bob.token, false)
	blogTestStripeWebhook(t, app, first.event(t, "evt_demo_unpaid", "checkout.session.completed", "complete", "unpaid"), blogTestStripeWebhookSecret, http.StatusOK)
	assertBlogTestReadable(t, app, paid, bob.token, false)

	// A second customer's same retry key stays scoped to that customer. Expiry
	// is accepted by the real webhook dispatcher without granting access.
	otherCheckout := blogTestCheckout(t, app, paid.ID, charlie.token, "purchase-once", nil)
	if otherCheckout.ID == checkout.ID {
		t.Fatal("checkout retry key crossed customer boundary")
	}
	otherSession := stripe.latestSession(t)
	blogTestStripeWebhook(t, app, otherSession.event(t, "evt_demo_expired", "checkout.session.expired", "expired", "unpaid"), blogTestStripeWebhookSecret, http.StatusOK)
	assertBlogTestReadable(t, app, paid, charlie.token, false)

	blogTestStripeWebhook(t, app, settled, blogTestStripeWebhookSecret, http.StatusOK)
	assertBlogTestReadable(t, app, paid, bob.token, true)
	assertBlogTestReadable(t, app, paid, charlie.token, false)
	assertBlogTestReadable(t, app, paid, "", false)
	blogTestRequest(t, app, http.MethodPatch, blogTestPath(paid.ID), bob.token, map[string]any{"title": "Purchased edit"}, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodDelete, blogTestPath(paid.ID), bob.token, nil, http.StatusNotFound)
	blogTestRequest(t, app, http.MethodPost, checkoutPath, bob.token, nil, http.StatusConflict, http.Header{"Idempotency-Key": {"already-bought"}})

	// Replayed Stripe delivery is idempotent. Grant terms remain permanent.
	blogTestStripeWebhook(t, app, settled, blogTestStripeWebhookSecret, http.StatusOK)
	page, err := billing.client.ProductAccess.List(t.Context(), &openrails.ProductAccessListParams{CustomerID: bob.id})
	if err != nil || len(page.Data) != 1 || page.Data[0].EndsAt != nil {
		t.Fatalf("expected one permanent purchase grant: page=%+v, err=%v", page, err)
	}
	assertCustomerBillingSurface(t, app, bob.id, bob.token, charlie.token, page.Data[0].ProductID)

	for _, price := range []int{799, 0} {
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(paid.ID), alice.token, map[string]any{"price_cents": price}, http.StatusOK)
		assertBlogTestReadable(t, app, paid, bob.token, true)
		assertBlogTestReadable(t, app, paid, alice.token, true)
		if price == 799 {
			newPriceCheckout := blogTestCheckout(t, app, paid.ID, charlie.token, "updated-price", nil)
			if newPriceCheckout.Amount == nil || *newPriceCheckout.Amount != 7_990_000 || stripe.latestSession(t).amount != 799 {
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
	t.Run("creator catalogs and moderator pricing", func(t *testing.T) {
		// A buyer is also an independent author. Supplied owner/catalog fields
		// cannot move their new product into the original seller's catalog.
		alicePost := blogTestStoredPost(t, pool, paid.ID, table)
		aliceClient, err := billing.client.ForCatalogOwner(alice.channelID)
		if err != nil {
			t.Fatal(err)
		}
		aliceCatalog, err := aliceClient.EnsureOwnCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		otherPost := createBlogTestPost(t, bobApp, bob.token, map[string]any{
			"slug": "second-author", "title": "Another author's work", "body": "Second purchased body",
			"visibility": "private", "price_cents": 699, "author_id": alice.id,
			"catalog_id": aliceCatalog.ID.String(), "openrails_product_id": alicePost.ProductID,
		})
		bobPost := blogTestStoredPost(t, pool, otherPost.ID, table)
		if bobPost.AuthorID != bob.id {
			t.Fatal("request fields changed the authenticated post owner")
		}
		bobClient, err := billing.client.ForCatalogOwner(bob.channelID)
		if err != nil {
			t.Fatal(err)
		}
		bobCatalog, err := bobClient.EnsureOwnCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if aliceCatalog.ID == bobCatalog.ID || bobCatalog.OwnerSubject == nil || *bobCatalog.OwnerSubject != bob.channelID {
			t.Fatal("authors must have distinct OpenRails-owned catalogs")
		}
		aliceProduct, bobProduct := alicePost.ProductID, bobPost.ProductID
		if _, err := bobClient.Products.Retrieve(t.Context(), aliceProduct); !errors.Is(err, openrails.ErrNotFound) {
			t.Fatalf("another author could address the seller's product: %v", err)
		}
		if _, _, err := billing.EnsurePostOffer(t.Context(), bob.channelID, alicePost.BillingKey, "Foreign author", 999); err == nil {
			t.Fatal("author billing path accepted another author's billing key")
		}
		if _, _, err := billing.EnsurePostOfferAsAdmin(t.Context(), bob.channelID, alicePost.BillingKey, "Wrong catalog", 999); err == nil {
			t.Fatal("administrator path silently reassigned a product to another catalog")
		}
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(otherPost.ID), alice.token, map[string]any{"price_cents": 999}, http.StatusNotFound)

		moderatorApp := &blogTestServer{url: app.url, client: newBlogTestHTTPClient(t, "127.0.0.5")}
		moderator := registerBlogTestUser(t, moderatorApp, service, "pricemoderator")
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(otherPost.ID), moderator.token, map[string]any{"price_cents": 1299}, http.StatusNotFound)
		if err := service.grantAdmin(t.Context(), moderator.id); err != nil {
			t.Fatal(err)
		}
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(otherPost.ID), moderator.token,
			map[string]any{"price_cents": 1299, "title": "Moderated title", "author_id": moderator.id}, http.StatusOK)
		after := blogTestStoredPost(t, pool, otherPost.ID, table)
		product, err := bobClient.Products.Retrieve(t.Context(), bobProduct)
		if err != nil {
			t.Fatal(err)
		}
		if after.Title != "Moderated title" || after.PriceCents == nil || *after.PriceCents != 1299 || after.AuthorID != bob.id || after.ProductID != bobPost.ProductID || product.CatalogID != bobCatalog.ID.String() || product.DisplayName != bobPost.Title {
			t.Fatal("moderator edit failed to preserve the product owner/title snapshot while updating the post and price")
		}
		if err := service.revokeAdmin(t.Context(), moderator.id); err != nil {
			t.Fatal(err)
		}
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(otherPost.ID), moderator.token, map[string]any{"price_cents": 1599}, http.StatusNotFound)
		catalogs, err := billing.client.ListCatalogs(t.Context(), openrails.PageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, owned := range catalogs {
			if owned.OwnerSubject != nil && *owned.OwnerSubject == moderator.id {
				t.Fatal("moderation created a catalog for the moderator")
			}
		}
		checkout := blogTestCheckout(t, app, otherPost.ID, charlie.token, "second-author-purchase", nil)
		if checkout.Amount == nil || *checkout.Amount != 12_990_000 {
			t.Fatal("checkout did not use the authorized moderator price")
		}
		session := stripe.latestSession(t)
		blogTestStripeWebhook(t, app, session.event(t, "evt_second_author_paid", "checkout.session.completed", "complete", "paid"), blogTestStripeWebhookSecret, http.StatusOK)
		assertBlogTestReadable(t, app, otherPost, charlie.token, true)
		assertBlogTestReadable(t, app, otherPost, alice.token, false)
		assertBlogTestReadable(t, app, otherPost, bob.token, true)
		blogTestRequest(t, app, http.MethodPatch, blogTestPath(otherPost.ID), charlie.token, map[string]any{"price_cents": 50}, http.StatusNotFound)
		assertBlogTestReadable(t, app, paid, bob.token, true)
	})
	t.Run("concurrent content edit survives completed billing work", func(t *testing.T) {
		before := blogTestStoredPost(t, pool, paid.ID, table)
		productID := before.ProductID
		productBefore, err := billing.client.Products.Retrieve(t.Context(), productID)
		if err != nil {
			t.Fatal(err)
		}
		// Simulate an editor committing after the offer has been prepared. The
		// callback borrows the exact same pool, including the MaxConns=1 case.
		// A held host transaction would stall; a missing CAS would lose this edit.
		observer := postOfferCallbackBilling{postBilling: billing, after: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, "UPDATE "+table+" SET title=$1, updated_at=now() WHERE id=$2", "Concurrent content edit", paid.ID)
			return err
		}}
		fiberApp, err := newApp(pool, service, observer, config, channels)
		if err != nil {
			t.Fatal(err)
		}
		concurrentApp := newBlogTestServer(t, fiberApp)
		blogTestRequest(t, concurrentApp, http.MethodPatch, blogTestPath(paid.ID), alice.token,
			map[string]any{"title": "Stale pricing edit", "price_cents": 1099}, http.StatusConflict)
		after := blogTestStoredPost(t, pool, paid.ID, table)
		if after.Title != "Concurrent content edit" || after.PriceCents != nil || after.PriceID != before.PriceID || after.ProductID != before.ProductID {
			t.Fatal("a stale pricing edit overwrote the committed content revision")
		}
		productAfter, err := billing.client.Products.Retrieve(t.Context(), productID)
		if err != nil {
			t.Fatal(err)
		}
		if productAfter.DisplayName != productBefore.DisplayName || productAfter.DisplayName == "Stale pricing edit" {
			t.Fatal("the rejected blog edit changed the existing billing product label")
		}
		blogTestRequest(t, concurrentApp, http.MethodPost, checkoutPath, charlie.token, nil, http.StatusNotFound,
			http.Header{"Idempotency-Key": {"after-conflicting-price"}})
	})
	schema := config.BillingSchema
	if schema == "" {
		schema = "billing"
	}
	var secrets int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{schema, "merchant_secrets"}.Sanitize()).Scan(&secrets); err != nil || secrets != 0 {
		t.Fatalf("host provider credentials were persisted: rows=%d err=%v", secrets, err)
	}
	t.Run("feed checks one bounded page", func(t *testing.T) {
		for i := 0; i < 4; i++ {
			createBlogTestPost(t, app, alice.token, map[string]any{"slug": fmt.Sprintf("page-%d", i), "title": "Paged offer", "body": "Purchased content", "visibility": "private", "price_cents": 99})
		}
		checked := make(chan []string, 2)
		observer := postAccessCallbackBilling{postBilling: billing, check: func(ids []string) { checked <- append([]string(nil), ids...) }}
		fiberApp, err := newApp(pool, service, observer, config, channels)
		if err != nil {
			t.Fatal(err)
		}
		pagedApp := newBlogTestServer(t, fiberApp)
		cursor := ""
		seen := map[int64]bool{}
		for range 2 {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, pagedApp.url+"/api/v1/posts?limit=2&before="+cursor, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+bob.token)
			resp, err := pagedApp.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var page []blogPost
			err = json.NewDecoder(resp.Body).Decode(&page)
			resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK || len(page) != 2 {
				t.Fatalf("page failed: status=%d posts=%d error=%v", resp.StatusCode, len(page), err)
			}
			for _, post := range page {
				if seen[post.ID] || post.CanRead || post.Body != "" {
					t.Fatal("feed repeated a row or exposed purchased content")
				}
				seen[post.ID] = true
			}
			select {
			case ids := <-checked:
				if len(ids) != 2 {
					t.Fatalf("access checked %d products for a 2-row page", len(ids))
				}
			default:
				t.Fatal("feed did not perform a bounded batch access check")
			}
			cursor = resp.Header.Get("X-Next-Cursor")
			if cursor == "" {
				t.Fatal("missing next-page cursor")
			}
		}
		blogTestRequest(t, pagedApp, http.MethodGet, "/api/v1/posts?limit=101", bob.token, nil, http.StatusBadRequest)
		blogTestRequest(t, pagedApp, http.MethodGet, "/api/v1/posts?before=invalid", bob.token, nil, http.StatusBadRequest)
	})
	// Observe both libraries before the restart proof below. River closes its
	// event subscription when stopped; buffered events cannot prove a new fleet.
	jobCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var authJob, billingJob bool
	for !authJob || !billingJob {
		select {
		case event := <-jobEvents:
			if event == nil {
				t.Fatal("host job subscription stopped")
			}
			authJob = authJob || event.Job.Kind == "authkit_cleanup_expired_auth_state"
			billingJob = billingJob || strings.HasPrefix(event.Job.Kind, "openrails.")
		case <-jobCtx.Done():
			t.Fatalf("shared job fleet did not execute both libraries: authkit=%v, openrails=%v", authJob, billingJob)
		}
	}
	t.Run("durable channel deletion serializes catalog writes", func(t *testing.T) {
		var team channel
		decodeBlogTestJSON(t, blogTestRequest(t, app, http.MethodPost, "/api/v1/channels", alice.token, map[string]any{"slug": "retiring-publisher", "name": "Retiring publisher"}, http.StatusCreated), &team)
		firstPost := createBlogTestPost(t, app, alice.token, map[string]any{"channel_id": team.ID, "slug": "retirement-purchase", "title": "Purchased before retirement", "body": "Purchased channel body", "price_cents": 99})
		storedFirst := blogTestStoredPost(t, pool, firstPost.ID, table)
		blogTestCheckout(t, app, firstPost.ID, bob.token, "retirement-purchase", nil)
		purchase := stripe.latestSession(t)
		blogTestStripeWebhook(t, app, purchase.event(t, "evt_retirement_paid", "checkout.session.completed", "complete", "paid"), blogTestStripeWebhookSecret, http.StatusOK)
		assertBlogTestReadable(t, app, firstPost, bob.token, true)
		if err := jobs.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		// The enqueue client remains usable while processing is stopped. A
		// committed delete must survive until this same host fleet restarts.
		ready, resume := make(chan struct{}, 1), make(chan struct{})
		observer := postOfferCallbackBilling{postBilling: billing, after: func(ctx context.Context) error {
			ready <- struct{}{}
			select {
			case <-resume:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
		writerFiber, err := newApp(pool, service, observer, config, channels)
		if err != nil {
			t.Fatal(err)
		}
		writerApp := newBlogTestServer(t, writerFiber)
		type result struct {
			status int
			body   []byte
			err    error
		}
		request := func(method, path string, body []byte) <-chan result {
			out := make(chan result, 1)
			go func() {
				req, err := http.NewRequestWithContext(t.Context(), method, writerApp.url+path, bytes.NewReader(body))
				if err != nil {
					out <- result{err: err}
					return
				}
				req.Header.Set("Authorization", "Bearer "+alice.token)
				req.Header.Set("Content-Type", "application/json")
				resp, err := writerApp.client.Do(req)
				if err != nil {
					out <- result{err: err}
					return
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				out <- result{status: resp.StatusCode, body: raw, err: err}
			}()
			return out
		}
		body, _ := json.Marshal(map[string]any{"channel_id": team.ID, "slug": "last-channel-offer", "title": "Accepted before deletion", "body": "Last content", "price_cents": 199})
		writing := request(http.MethodPost, "/api/v1/posts", body)
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			t.Fatal("catalog write did not reach its guarded boundary")
		}
		deleting := request(http.MethodDelete, "/api/v1/channels/"+team.ID, nil)
		wait, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		for {
			var blocked bool
			err = pool.QueryRow(wait, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_database())::oid AND objid=hashtext($1)::oid)`, "demo-channel:"+team.ID).Scan(&blocked)
			if err != nil {
				cancel()
				close(resume)
				t.Fatal("deletion did not wait for the accepted catalog write:", err)
			}
			if blocked {
				break
			}
			select {
			case <-wait.Done():
				cancel()
				close(resume)
				t.Fatal("channel deletion was not serialized")
			case <-time.After(10 * time.Millisecond):
			}
		}
		cancel()
		close(resume)
		written, deleted := <-writing, <-deleting
		if written.err != nil || written.status != http.StatusCreated {
			t.Fatalf("last write: %+v body=%s", written, written.body)
		}
		if deleted.err != nil || deleted.status != http.StatusAccepted {
			t.Fatalf("queued deletion: %+v body=%s", deleted, deleted.body)
		}
		var lastPost blogPost
		decodeBlogTestJSON(t, written.body, &lastPost)
		storedLast := blogTestStoredPost(t, pool, lastPost.ID, table)
		var queued int
		jobSchema := config.RiverSchema
		if jobSchema == "" {
			jobSchema = "public"
		}
		if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{jobSchema, "river_job"}.Sanitize()+` WHERE kind='demo_delete_channel' AND args->>'channel_id'=$1 AND state<>'completed'`, team.ID).Scan(&queued); err != nil || queued != 1 {
			t.Fatalf("durable deletion queue rows=%d err=%v", queued, err)
		}
		blogTestRequest(t, app, http.MethodPost, "/api/v1/posts", alice.token, map[string]any{"channel_id": team.ID, "slug": "too-late", "title": "Too late", "body": "Rejected", "price_cents": 299}, http.StatusNotFound)
		if err = jobs.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		deadline, stop := context.WithTimeout(t.Context(), 20*time.Second)
		defer stop()
		for {
			var exists bool
			if err = pool.QueryRow(deadline, `SELECT EXISTS(SELECT 1 FROM `+channels.table+` WHERE id=$1)`, team.ID).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if !exists {
				break
			}
			select {
			case <-deadline.Done():
				t.Fatal("restarted worker did not finish channel cleanup")
			case <-time.After(25 * time.Millisecond):
			}
		}
		for _, id := range []string{storedFirst.ProductID, storedLast.ProductID} {
			product, err := billing.client.Products.Retrieve(t.Context(), id)
			if err != nil || !product.Archived {
				t.Fatalf("channel product was not archived: product=%+v err=%v", product, err)
			}
		}
		if owned, err := billing.HasPostAccess(t.Context(), bob.id, storedFirst.ProductID); err != nil || !owned {
			t.Fatalf("cleanup changed financial purchase history: access=%v err=%v", owned, err)
		}
		blogTestRequest(t, app, http.MethodGet, blogTestPath(firstPost.ID), bob.token, nil, http.StatusNotFound)
	})
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	if len(stripe.catalogWrites) != 0 {
		t.Errorf("engine catalog unexpectedly wrote Stripe objects: %v", stripe.catalogWrites)
	}
	for _, version := range stripe.versions {
		if version == "" {
			t.Error("OpenRails did not pin Stripe-Version above the transport seam")
		}
	}
}

type postOfferCallbackBilling struct {
	postBilling
	after func(context.Context) error
}

func (b postOfferCallbackBilling) EnsurePostOffer(ctx context.Context, author, key, title string, price int64) (string, string, error) {
	productID, priceID, err := b.postBilling.EnsurePostOffer(ctx, author, key, title, price)
	if err == nil {
		err = b.after(ctx)
	}
	return productID, priceID, err
}

func blogTestStoredPost(t *testing.T, pool *pgxpool.Pool, id int64, tables ...string) blogPost {
	t.Helper()
	table := blogPostsTable
	if len(tables) == 1 {
		table = tables[0]
	}
	post, err := scanBlogPost(pool.QueryRow(t.Context(), "SELECT "+postColumns+" FROM "+table+" WHERE id=$1", id))
	if err != nil {
		t.Fatal(err)
	}
	return post
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

// Only the Stripe wire transport is substituted. Catalog creation, checkout,
// signature verification, payment recording and access grants all execute in
// the real embedded OpenRails engine against an isolated PostgreSQL database.
type blogTestStripe struct {
	mu            sync.Mutex
	customers     int
	sessions      []blogTestStripeSession
	versions      []string
	catalogWrites []string
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
	case "POST /v1/products", "POST /v1/prices":
		s.catalogWrites = append(s.catalogWrites, req.URL.Path)
		return nil, fmt.Errorf("engine catalog must stay local: %s", req.URL.Path)
	case "POST /v1/customers":
		s.customers++
		body = map[string]any{"id": fmt.Sprintf("cus_demo_%d", s.customers), "object": "customer"}
	case "POST /v1/checkout/sessions":
		id := fmt.Sprintf("cs_test_demo_%d", len(s.sessions)+1)
		if req.PostForm.Get("line_items[0][price]") != "" {
			return nil, fmt.Errorf("checkout must use inline terms, not a mirrored Stripe price")
		}
		amount, err := strconv.ParseInt(req.PostForm.Get("line_items[0][price_data][unit_amount]"), 10, 64)
		if err != nil || amount <= 0 || req.PostForm.Get("line_items[0][price_data][currency]") != "usd" || req.PostForm.Get("line_items[0][price_data][product_data][name]") == "" {
			return nil, fmt.Errorf("checkout omitted accepted inline price terms: %v", req.PostForm)
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, app.url+"/billing/v1/merchants/openrails-demo/webhooks/stripe/acct_demo_test", bytes.NewReader(payload))
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

// The test harness creates an isolated normal database owner. Application
// initialization and runtime then share that one login without provisioning roles.
func newBlogTestOwnerPool(t *testing.T, pool *pgxpool.Pool, maxConns int32) (*pgxpool.Pool, string) {
	t.Helper()
	role := "demo_app_test_" + strings.ToLower(rand.Text())
	quotedRole := pgx.Identifier{role}.Sanitize()
	password := rand.Text()
	adminConfig := pool.Config().ConnConfig.Copy()
	var statement string
	if err := pool.QueryRow(t.Context(), "SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', $1::text, $2::text)", role, password).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		admin, err := pgx.ConnectConfig(ctx, adminConfig)
		if err != nil {
			t.Errorf("connect to remove isolated role: %v", err)
			return
		}
		defer admin.Close(ctx)
		var exists bool
		if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
			t.Errorf("check isolated application role: %v", err)
			return
		}
		if !exists {
			return
		}
		if _, err := admin.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{adminConfig.Database}.Sanitize()+" OWNER TO CURRENT_USER"); err != nil {
			t.Errorf("return owned test database to fixture administrator: %v", err)
			return
		}
		if _, err := admin.Exec(ctx, "DROP OWNED BY "+quotedRole+"; DROP ROLE "+quotedRole); err != nil {
			t.Errorf("remove isolated application role: %v", err)
		}
	})
	if _, err := pool.Exec(t.Context(), "ALTER DATABASE "+pgx.Identifier{adminConfig.Database}.Sanitize()+" OWNER TO "+quotedRole); err != nil {
		t.Fatal(err)
	}
	runtimeConfig := pool.Config()
	runtimeConfig.ConnConfig.User = role
	runtimeConfig.ConnConfig.Password = password
	if maxConns > 0 {
		runtimeConfig.MaxConns = maxConns
	}
	runtimePool, err := pgxpool.NewWithConfig(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimePool.Close)
	conn := runtimeConfig.ConnConfig
	dsn := &url.URL{Scheme: "postgres", Host: net.JoinHostPort(conn.Host, strconv.Itoa(int(conn.Port))), Path: "/" + conn.Database, User: url.UserPassword(role, password)}
	query := dsn.Query()
	if conn.TLSConfig == nil {
		query.Set("sslmode", "disable")
	} else {
		query.Set("sslmode", "require")
	}
	dsn.RawQuery = query.Encode()
	return runtimePool, dsn.String()
}

func assertBlogTestOwnerRole(t *testing.T, runtimePool *pgxpool.Pool) {
	t.Helper()
	var privileged bool
	if err := runtimePool.QueryRow(t.Context(), `SELECT rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole FROM pg_roles WHERE rolname = current_user`).Scan(&privileged); err != nil || privileged {
		t.Fatalf("runtime must use a normal application login: privileged=%v err=%v", privileged, err)
	}
	var memberships int
	if err := runtimePool.QueryRow(t.Context(), `SELECT count(*) FROM pg_auth_members WHERE member = (SELECT oid FROM pg_roles WHERE rolname = current_user)`).Scan(&memberships); err != nil || memberships != 0 {
		t.Fatalf("runtime login must have no role memberships: memberships=%d err=%v", memberships, err)
	}
	var ownsMigrations bool
	if err := runtimePool.QueryRow(t.Context(), `SELECT pg_get_userbyid(relowner) = current_user FROM pg_class WHERE oid = 'public.migrations'::regclass`).Scan(&ownsMigrations); err != nil || !ownsMigrations {
		t.Fatalf("the same application login must own its initialized storage: owner=%v err=%v", ownsMigrations, err)
	}
}

func assertAllPublicStorage(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{"blog_posts", "users", "products", "prices", "payments", "river_job"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil || !exists {
			t.Fatalf("public table %s missing: %v", table, err)
		}
	}
	var escaped int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_namespace WHERE nspname IN ('demo','profiles','billing','openrails')`).Scan(&escaped); err != nil || escaped != 0 {
		t.Fatalf("default schemas leaked: %d %v", escaped, err)
	}
	var searchPath string
	if err := pool.QueryRow(t.Context(), "SHOW search_path").Scan(&searchPath); err != nil || searchPath != `"$user", public` {
		t.Fatalf("shared pool search_path changed: %q %v", searchPath, err)
	}
}

type postAccessCallbackBilling struct {
	postBilling
	check func([]string)
}

func (b postAccessCallbackBilling) CheckPostAccess(ctx context.Context, userID string, productIDs []string) (map[string]bool, error) {
	b.check(productIDs)
	return b.postBilling.CheckPostAccess(ctx, userID, productIDs)
}
