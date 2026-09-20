package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/openrails"
	openrailsconfig "github.com/open-rails/openrails/config"
	openrailsembed "github.com/open-rails/openrails/embed"
	"github.com/open-rails/openrails/permissions"
	"github.com/open-rails/openrails/pkg/billingauth"
	"github.com/open-rails/openrails/pkg/merchant"
)

const (
	billingMerchantSlug       = "openrails-demo"
	billingWebhookPath        = "/billing/v1/merchants/" + billingMerchantSlug + "/webhooks/stripe"
	minPostPriceCents   int64 = 50
	maxPostPriceCents   int64 = 99_999_999
)

// postBilling keeps payment confirmation and access grants in OpenRails. The
// demo stores only the product/price IDs that connect a post to its offer.
type postBilling interface {
	EnsurePostOffer(context.Context, string, string, int64) (string, string, error)
	HasPostAccess(context.Context, string, string) (bool, error)
	ListProductAccess(context.Context, string) (map[string]bool, error)
	CreateCheckout(context.Context, string, string, string, string) (*openrails.CheckoutSession, error)
	GetCheckout(context.Context, string, string) (*openrails.CheckoutSession, error)
}

type billingService struct {
	runtime   *openrailsembed.Runtime
	client    *openrails.Client
	pool      *pgxpool.Pool
	webhook   http.Handler
	publicURL string
}

func initializeBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, jobs *appJobs) error {
	return openrailsembed.ApplyMigrations(ctx, pool, openrailsembed.MigrationOptions{
		Schema: cfg.BillingSchema,
		River:  jobs.ownership(),
	})
}

// newBilling is deliberately sandbox-only. A missing Stripe key leaves selling
// disabled; supplying a key requires the complete persistent billing config.
// transport is the supported OpenRails fake-Stripe seam for integration tests.
func newBilling(ctx context.Context, cfg Config, jobs *appJobs, transport ...http.RoundTripper) (*billingService, error) {
	if cfg.StripeSecretKey == "" {
		return nil, nil
	}
	if !strings.HasPrefix(cfg.StripeSecretKey, "sk_test_") && !strings.HasPrefix(cfg.StripeSecretKey, "rk_test_") {
		return nil, errors.New("this demo requires a Stripe sk_test_ or rk_test_ sandbox key")
	}
	if !strings.HasPrefix(cfg.StripeAccountID, "acct_") || !strings.HasPrefix(cfg.StripeWebhookSecret, "whsec_") {
		return nil, errors.New("billing requires STRIPE_ACCOUNT_ID and STRIPE_WEBHOOK_SECRET")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.BillingEncryptionKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("BILLING_ENCRYPTION_KEY must be base64 of 32 random bytes")
	}
	if cfg.BillingDatabaseURL == "" {
		return nil, errors.New("BILLING_DATABASE_URL is required when billing is enabled")
	}
	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil || publicURL.Host == "" || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.RawQuery != "" || publicURL.Fragment != "" || publicURL.User != nil {
		return nil, errors.New("PUBLIC_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if len(transport) > 1 {
		return nil, errors.New("newBilling accepts at most one Stripe transport")
	}
	pool, err := pgxpool.New(ctx, cfg.BillingDatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open billing database: %w", err)
	}
	opts := openrailsembed.Options{
		Config: &openrailsconfig.Config{
			Env:               "development",
			TestMode:          openrailsconfig.CredentialPostureSandbox,
			ProviderWriteMode: openrailsconfig.ProviderWriteModeFull,
			MerchantSource:    openrailsconfig.MerchantSourceAPI,
			SecretBackend:     openrailsconfig.SecretBackendDB,
			APIURL:            strings.TrimRight(cfg.PublicURL, "/") + "/billing",
			DB:                &openrailsconfig.DBConfig{URL: cfg.BillingDatabaseURL, Schema: cfg.BillingSchema},
			Encryption:        &openrailsconfig.EncryptionConfig{MasterKey: cfg.BillingEncryptionKey},
		},
		PGXPool: pool,
		River:   jobs.ownership(),
	}
	if len(transport) == 1 {
		opts.StripeTransport = transport[0]
	}
	runtime, err := openrailsembed.New(ctx, opts)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("start OpenRails: %w", err)
	}
	billing := &billingService{runtime: runtime, pool: pool, publicURL: strings.TrimRight(cfg.PublicURL, "/")}
	complete := false
	defer func() {
		if !complete {
			_ = billing.Close(context.Background())
		}
	}()
	merchantID, err := runtime.UpsertMerchantConfig(ctx, billingMerchantSlug, openrailsembed.MerchantConfig{DisplayName: "OpenRails Blog Demo"})
	if err != nil {
		return nil, fmt.Errorf("bind billing merchant: %w", err)
	}
	if err := configureStripe(ctx, runtime, merchantID, cfg); err != nil {
		return nil, err
	}
	billing.client, err = runtime.Client()
	if err != nil {
		return nil, err
	}
	billing.webhook, err = runtime.Handler(openrailsembed.MountOptions{
		MountPrefix:    "/billing",
		RouteSets:      []openrailsembed.RouteSet{openrailsembed.RouteSetWebhooks},
		ProviderRoutes: &openrailsembed.ProviderRoutes{Webhooks: true},
	})
	if err != nil {
		return nil, err
	}
	complete = true
	return billing, nil
}

func (b *billingService) Close(ctx context.Context) error {
	err := b.runtime.Close(ctx)
	b.pool.Close()
	return err
}

func (b *billingService) WebhookHandler() http.Handler { return b.webhook }

func (b *billingService) Ready(ctx context.Context) error {
	return b.runtime.Ready(ctx)
}

func (b *billingService) EnsurePostOffer(ctx context.Context, billingKey, title string, priceCents int64) (string, string, error) {
	id, err := uuid.Parse(billingKey)
	if err != nil || id == uuid.Nil || id.String() != billingKey {
		return "", "", errors.New("invalid post billing key")
	}
	if priceCents < minPostPriceCents || priceCents > maxPostPriceCents {
		return "", "", errors.New("post price must be between 50 and 99999999 USD cents")
	}
	key := "post-" + billingKey
	product, err := b.client.GetProductByKey(ctx, key)
	if errors.Is(err, openrails.ErrNotFound) {
		product, err = b.client.CreateProduct(ctx, openrails.CreateProductRequest{Key: key, DisplayName: title})
		if errors.Is(err, openrails.ErrConflict) {
			product, err = b.client.GetProductByKey(ctx, key)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("ensure post product: %w", err)
	}
	// Each amount has an immutable offer. Leave previous offers intact: a local
	// post transaction can still roll back after this external catalog write.
	// Only the price selected by the post row is exposed by our checkout route.
	price, err := b.client.CreatePrice(ctx, openrails.CreatePriceRequest{
		ProductID:           product.ID,
		Key:                 key + "-usd-" + strconv.FormatInt(priceCents, 10),
		UnitAmount:          priceCents * 10_000, // OpenRails fiat amounts are micros.
		Currency:            "USD",
		AccessDurationHours: nil,
		AutoRenew:           false,
		PSPs:                []string{"stripe"},
	})
	if err != nil {
		return "", "", fmt.Errorf("ensure post price: %w", err)
	}
	if state, ok := price.Providers["stripe"]; !ok || state.Status != openrails.ProviderStatusLinked {
		return "", "", errors.New("post price is not linked to Stripe")
	}
	return product.ID.String(), price.ID.String(), nil
}

func (b *billingService) HasPostAccess(ctx context.Context, userID, productID string) (bool, error) {
	customer, err := openrails.ParseCustomerID(userID)
	if err != nil {
		return false, err
	}
	product, err := openrails.ParseProductID(productID)
	if err != nil {
		return false, err
	}
	return b.client.HasProductAccess(ctx, customer, product)
}

func (b *billingService) ListProductAccess(ctx context.Context, userID string) (map[string]bool, error) {
	customer, err := openrails.ParseCustomerID(userID)
	if err != nil {
		return nil, err
	}
	grants, err := b.client.ListProductAccess(ctx, customer)
	if err != nil {
		return nil, err
	}
	products := make(map[string]bool, len(grants))
	for _, grant := range grants {
		products[grant.ProductID.String()] = true
	}
	return products, nil
}

func (b *billingService) CreateCheckout(ctx context.Context, userID, productID, priceID, idempotencyKey string) (*openrails.CheckoutSession, error) {
	customer, err := openrails.ParseCustomerID(userID)
	if err != nil {
		return nil, err
	}
	product, err := openrails.ParseProductID(productID)
	if err != nil {
		return nil, err
	}
	price, err := openrails.ParsePriceID(priceID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 200 {
		return nil, errors.New("Idempotency-Key must contain between 1 and 200 characters")
	}
	// Namespace the caller's retry key by authenticated buyer and product. A
	// second buyer cannot collide with or retrieve someone else's checkout.
	digest := sha256.Sum256([]byte(userID + "\x00" + productID + "\x00" + idempotencyKey))
	return b.client.CreateCheckoutSession(ctx, openrails.CreateCheckoutSessionRequest{
		Customer:       openrails.CheckoutCustomerIdentity{ID: customer},
		PriceID:        price,
		Mode:           "one_off",
		Payment:        openrails.CheckoutPayment{Rail: "stripe"},
		IdempotencyKey: "post-" + hex.EncodeToString(digest[:]),
		Metadata:       map[string]string{"product_id": product.String()},
		SuccessURL:     b.publicURL + "/?checkout=success",
		CancelURL:      b.publicURL + "/?checkout=canceled",
	})
}

func (b *billingService) GetCheckout(ctx context.Context, userID, checkoutID string) (*openrails.CheckoutSession, error) {
	customer, err := openrails.ParseCustomerID(userID)
	if err != nil {
		return nil, err
	}
	checkout, err := openrails.ParseCheckoutSessionID(checkoutID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid checkout ID", openrails.ErrNotFound)
	}
	result, err := b.client.GetCheckoutSession(ctx, customer, checkout)
	if errors.Is(err, openrails.ErrDenied) {
		return nil, openrails.ErrNotFound
	}
	return result, err
}

// The public embedded client does not expose payment-provider configuration.
// Bootstrap through OpenRails' supported HTTP surface in-process, then discard
// it. This privileged handler is never registered on the demo's HTTP server.
func configureStripe(ctx context.Context, runtime *openrailsembed.Runtime, id merchant.ID, cfg Config) error {
	handler, err := runtime.Handler(openrailsembed.MountOptions{
		MountPrefix: "/billing",
		RouteSets:   []openrailsembed.RouteSet{openrailsembed.RouteSetPaymentProviders},
		Gate:        stripeBootstrapGate{id: id},
	})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"account_id": cfg.StripeAccountID,
		"credentials": map[string]string{
			"secret_key":             cfg.StripeSecretKey,
			"webhook_signing_secret": cfg.StripeWebhookSecret,
		},
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://in-process/billing/v1/merchant/payment-providers/stripe", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response := &bootstrapResponse{header: make(http.Header), status: http.StatusOK}
	handler.ServeHTTP(response, request)
	if response.status < 200 || response.status >= 300 {
		// Keep credential-bearing bootstrap responses out of application logs.
		return fmt.Errorf("configure Stripe through OpenRails: HTTP %d", response.status)
	}
	return nil
}

type stripeBootstrapGate struct{ id merchant.ID }

func (g stripeBootstrapGate) Authorize(_ context.Context, r *http.Request, permission string) (billingauth.Principal, error) {
	if r.Method != http.MethodPut || r.URL.Path != "/billing/v1/merchant/payment-providers/stripe" || permission != permissions.MerchantPaymentProvidersUpdate {
		return billingauth.Principal{}, billingauth.GateError{Status: http.StatusForbidden, Message: "bootstrap operation not permitted"}
	}
	return billingauth.Principal{MerchantID: g.id, Permissions: []string{permissions.MerchantPaymentProvidersUpdate}}, nil
}

type bootstrapResponse struct {
	header http.Header
	status int
}

func (r *bootstrapResponse) Header() http.Header         { return r.header }
func (r *bootstrapResponse) WriteHeader(status int)      { r.status = status }
func (r *bootstrapResponse) Write(p []byte) (int, error) { return len(p), nil }
