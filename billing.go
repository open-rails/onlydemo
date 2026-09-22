package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	EnsurePostOffer(context.Context, string, string, string, int64) (string, string, error)
	EnsurePostOfferAsAdmin(context.Context, string, string, string, int64) (string, string, error)
	HasPostAccess(context.Context, string, string) (bool, error)
	ListProductAccess(context.Context, string) (map[string]bool, error)
	CreateCheckout(context.Context, string, string, string, string) (*openrails.CheckoutSession, error)
	GetCheckout(context.Context, string, string) (*openrails.CheckoutSession, error)
}

type billingService struct {
	runtime   *openrailsembed.Runtime
	client    *openrails.Client
	webhook   http.Handler
	publicURL string
}

func initializeBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	return openrailsembed.ApplyMigrations(ctx, pool, openrailsembed.MigrationOptions{
		Schema: cfg.BillingSchema,
		River:  openrailsembed.RiverFromHost(),
	})
}

// newBilling is deliberately sandbox-only. A missing Stripe key leaves selling
// disabled; supplying a key requires the host-owned account and webhook config.
// transport is the supported OpenRails fake-Stripe seam for integration tests.
func newBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, transport ...http.RoundTripper) (*billingService, error) {
	if cfg.StripeSecretKey == "" {
		return nil, nil
	}
	if !strings.HasPrefix(cfg.StripeSecretKey, "sk_test_") && !strings.HasPrefix(cfg.StripeSecretKey, "rk_test_") {
		return nil, errors.New("this demo requires a Stripe sk_test_ or rk_test_ sandbox key")
	}
	if !strings.HasPrefix(cfg.StripeAccountID, "acct_") || !strings.HasPrefix(cfg.StripeWebhookSecret, "whsec_") {
		return nil, errors.New("billing requires STRIPE_ACCOUNT_ID and STRIPE_WEBHOOK_SECRET")
	}
	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil || publicURL.Host == "" || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.RawQuery != "" || publicURL.Fragment != "" || publicURL.User != nil {
		return nil, errors.New("PUBLIC_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if len(transport) > 1 {
		return nil, errors.New("newBilling accepts at most one Stripe transport")
	}
	if pool == nil {
		return nil, errors.New("billing requires the host PostgreSQL pool")
	}
	opts := openrailsembed.Options{
		Config: &openrailsconfig.Config{
			Env:                             "development",
			TestMode:                        openrailsconfig.CredentialPostureSandbox,
			ProviderWriteMode:               openrailsconfig.ProviderWriteModeFull,
			MerchantConfigSource:            openrailsconfig.MerchantConfigSourceManifest,
			NewSubscriptionCollectionPolicy: "engine",
			CatalogSource:                   openrailsconfig.CatalogSourceAPI,
			APIURL:                          strings.TrimRight(cfg.PublicURL, "/") + "/billing",
			DB:                              &openrailsconfig.DBConfig{URL: cfg.DatabaseURL, Schema: cfg.BillingSchema},
		},
		PGXPool: pool,
		River:   openrailsembed.RiverFromHost(),
	}
	if len(transport) == 1 {
		opts.StripeTransport = transport[0]
	}

	runtime, err := openrailsembed.New(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("start OpenRails: %w", err)
	}
	return &billingService{runtime: runtime, publicURL: strings.TrimRight(cfg.PublicURL, "/")}, nil
}

// The caller defers Close immediately after construction, before this fallible
// configuration. The same defer covers initialization failure and normal shutdown.
func (billing *billingService) initialize(ctx context.Context, cfg Config) error {
	runtime := billing.runtime
	_, err := runtime.UpsertMerchantConfig(ctx, billingMerchantSlug, openrailsembed.MerchantConfig{
		DisplayName: "OpenRails Blog Demo",
		PSPs: map[string]openrailsembed.PSPConfig{"stripe": {"stripe": {
			AccountID: cfg.StripeAccountID,
			Secrets: map[string]string{
				"secret_key":             cfg.StripeSecretKey,
				"webhook_signing_secret": cfg.StripeWebhookSecret,
			},
		}}},
	})
	if err != nil {
		return fmt.Errorf("configure billing merchant: %w", err)
	}
	billing.client, err = runtime.Client()
	if err != nil {
		return err
	}
	billing.webhook, err = runtime.Handler(openrailsembed.MountOptions{
		MountPrefix:    "/billing",
		RouteSets:      []openrailsembed.RouteSet{openrailsembed.RouteSetWebhooks},
		ProviderRoutes: &openrailsembed.ProviderRoutes{Webhooks: true},
	})
	if err != nil {
		return err
	}
	return nil
}

func (b *billingService) Close(ctx context.Context) error {
	return b.runtime.Close(ctx)
}

func (b *billingService) WebhookHandler() http.Handler { return b.webhook }

func (b *billingService) Ready(ctx context.Context) error {
	return b.runtime.Ready(ctx)
}

// Author authority comes from the authenticated caller, independently of the
// owner recorded on an existing post.
func (b *billingService) EnsurePostOffer(ctx context.Context, authorID, billingKey, title string, priceCents int64) (string, string, error) {
	client, err := b.runtime.CatalogClient(authorID)
	if err != nil {
		return "", "", err
	}
	catalog, err := client.EnsureOwnCatalog(ctx)
	if err != nil {
		return "", "", err
	}
	return ensurePostOffer(ctx, client, catalog.ID, nil, billingKey, title, priceCents)
}

// The blog handler calls this path only after AuthKit grants moderation access.
// It uses administrator authority explicitly while preserving the author's catalog.
func (b *billingService) EnsurePostOfferAsAdmin(ctx context.Context, ownerID, billingKey, title string, priceCents int64) (string, string, error) {
	catalog, err := b.client.EnsureCatalogForOwner(ctx, ownerID)
	if err != nil {
		return "", "", err
	}
	return ensurePostOffer(ctx, b.client, catalog.ID, []string{"stripe"}, billingKey, title, priceCents)
}

func ensurePostOffer(ctx context.Context, client *openrails.Client, catalogID openrails.CatalogID, providers []string, billingKey, title string, priceCents int64) (string, string, error) {
	id, err := uuid.Parse(billingKey)
	if err != nil || id == uuid.Nil || id.String() != billingKey {
		return "", "", errors.New("invalid post billing key")
	}
	if priceCents < minPostPriceCents || priceCents > maxPostPriceCents {
		return "", "", errors.New("post price must be between 50 and 99999999 USD cents")
	}
	key := "post-" + billingKey
	product, err := client.GetProductByKey(ctx, key)
	if errors.Is(err, openrails.ErrNotFound) {
		product, err = client.CreateProduct(ctx, openrails.CreateProductRequest{Key: key, DisplayName: title, CatalogID: catalogID})
		if errors.Is(err, openrails.ErrConflict) {
			product, err = client.GetProductByKey(ctx, key)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("ensure post product: %w", err)
	}
	if product.CatalogID != catalogID {
		return "", "", errors.New("post product belongs to another catalog")
	}
	// The product label is the first-listing title snapshot. Preparing an offer
	// must not change it before the blog's revision check accepts the edit.
	// Each amount has an immutable offer. Leave previous offers intact: the
	// later post write or revision check can fail after this catalog write.
	// Only the price selected by the post row is exposed by our checkout route.
	price, err := client.CreatePrice(ctx, openrails.CreatePriceRequest{
		ProductID:           product.ID,
		Key:                 key + "-usd-" + strconv.FormatInt(priceCents, 10),
		UnitAmount:          priceCents * 10_000, // OpenRails fiat amounts are micros.
		Currency:            "USD",
		AccessDurationHours: nil,
		AutoRenew:           false,
		PSPs:                providers,
	})
	if err != nil {
		return "", "", fmt.Errorf("ensure post price: %w", err)
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
