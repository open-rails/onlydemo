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
	minPostPriceCents   int64 = 50
	maxPostPriceCents   int64 = 99_999_999
)

// postBilling keeps payment confirmation and access grants in OpenRails. The
// demo stores only the product/price IDs that connect a post to its offer.
type postBilling interface {
	EnsurePostOffer(context.Context, string, string, string, int64) (string, string, error)
	EnsurePostOfferAsAdmin(context.Context, string, string, string, int64) (string, string, error)
	HasPostAccess(context.Context, string, string) (bool, error)
	CheckPostAccess(context.Context, string, []string) (map[string]bool, error)
	CreateCheckout(context.Context, string, string, string, string) (*openrails.CheckoutSession, error)
	GetCheckout(context.Context, string, string) (*openrails.CheckoutSession, error)
}

type billingService struct {
	runtime   *openrailsembed.Runtime
	client    *openrails.Client
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
		HTTP: &openrailsembed.HTTPConfig{},
		Merchant: &openrailsembed.MerchantDeclaration{Slug: billingMerchantSlug, Config: openrailsembed.MerchantConfig{
			DisplayName: "OpenRails Blog Demo",
			PSPs: map[string]openrailsembed.PSPConfig{"stripe": {"stripe": {
				AccountID: cfg.StripeAccountID,
				Secrets: map[string]string{
					"secret_key":             cfg.StripeSecretKey,
					"webhook_signing_secret": cfg.StripeWebhookSecret,
				},
			}}}}},
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
	client, err := runtime.Client()
	if err != nil {
		_ = runtime.Close(context.Background())
		return nil, err
	}
	return &billingService{runtime: runtime, client: client, publicURL: strings.TrimRight(cfg.PublicURL, "/")}, nil
}

func (b *billingService) Close(ctx context.Context) error {
	return b.runtime.Close(ctx)
}

func (b *billingService) Ready(ctx context.Context) error {
	return b.runtime.Ready(ctx)
}

// Author authority comes from the authenticated caller, independently of the
// owner recorded on an existing post.
func (b *billingService) EnsurePostOffer(ctx context.Context, authorID, billingKey, title string, priceCents int64) (string, string, error) {
	client, err := b.client.ForCatalogOwner(authorID)
	if err != nil {
		return "", "", err
	}
	return ensurePostOffer(ctx, client, "", nil, billingKey, title, priceCents)
}

// The blog handler calls this path only after AuthKit grants moderation access.
// It uses administrator authority explicitly while preserving the author's catalog.
func (b *billingService) EnsurePostOfferAsAdmin(ctx context.Context, ownerID, billingKey, title string, priceCents int64) (string, string, error) {
	catalog, err := b.client.EnsureCatalogForOwner(ctx, ownerID)
	if err != nil {
		return "", "", err
	}
	return ensurePostOffer(ctx, b.client, catalog.ID.String(), []string{"stripe"}, billingKey, title, priceCents)
}

func ensurePostOffer(ctx context.Context, client *openrails.Client, catalogID string, providers []string, billingKey, title string, priceCents int64) (string, string, error) {
	id, err := uuid.Parse(billingKey)
	if err != nil || id == uuid.Nil || id.String() != billingKey {
		return "", "", errors.New("invalid post billing key")
	}
	if priceCents < minPostPriceCents || priceCents > maxPostPriceCents {
		return "", "", errors.New("post price must be between 50 and 99999999 USD cents")
	}
	key := "post-" + billingKey
	// The product label is the first-listing title snapshot. Preparing an offer
	// must not change it before the blog's revision check accepts the edit.
	// Each amount has an immutable offer. Leave previous offers intact: the
	// later post write or revision check can fail after this catalog write.
	// Only the price selected by the post row is exposed by our checkout route.
	price, err := client.Prices.Create(ctx, &openrails.PriceCreateParams{
		ProductData:         &openrails.PriceCreateProductDataParams{CatalogID: catalogID, Key: key, DisplayName: title},
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
	return price.ProductID, price.ID, nil
}

func (b *billingService) HasPostAccess(ctx context.Context, userID, productID string) (bool, error) {
	result, err := b.client.ProductAccess.Check(ctx, &openrails.ProductAccessCheckParams{CustomerID: userID, ProductID: productID})
	if err != nil {
		return false, err
	}
	return result.HasAccess, nil
}

func (b *billingService) CheckPostAccess(ctx context.Context, userID string, productIDs []string) (map[string]bool, error) {
	return b.client.ProductAccess.CheckMany(ctx, &openrails.ProductAccessCheckManyParams{CustomerID: userID, ProductIDs: productIDs})
}

func (b *billingService) CreateCheckout(ctx context.Context, userID, productID, priceID, idempotencyKey string) (*openrails.CheckoutSession, error) {
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 200 {
		return nil, errors.New("Idempotency-Key must contain between 1 and 200 characters")
	}
	// Namespace the caller's retry key by authenticated buyer and product. A
	// second buyer cannot collide with or retrieve someone else's checkout.
	digest := sha256.Sum256([]byte(userID + "\x00" + productID + "\x00" + idempotencyKey))
	return b.client.CreateCheckoutSession(ctx, openrails.CreateCheckoutSessionRequest{
		Customer:       openrails.CheckoutCustomerIdentity{ID: userID},
		PriceID:        priceID,
		PaymentOptions: openrails.CheckoutPaymentOptions{Rail: "stripe"},
		IdempotencyKey: "post-" + hex.EncodeToString(digest[:]),
		Metadata:       map[string]string{"product_id": productID},
		SuccessURL:     b.publicURL + "/",
		CancelURL:      b.publicURL + "/",
	})
}

func (b *billingService) GetCheckout(ctx context.Context, userID, checkoutID string) (*openrails.CheckoutSession, error) {
	result, err := b.client.GetCheckoutSession(ctx, userID, checkoutID)
	if errors.Is(err, openrails.ErrInvalid) {
		return nil, fmt.Errorf("%w: invalid checkout ID", openrails.ErrNotFound)
	}
	if errors.Is(err, openrails.ErrDenied) {
		return nil, openrails.ErrNotFound
	}
	return result, err
}
