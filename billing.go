package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/openrails"
	openrailsfiber "github.com/open-rails/openrails/adapters/fiber"
	openrailsconfig "github.com/open-rails/openrails/config"
	openrailsembed "github.com/open-rails/openrails/embed"
	"github.com/open-rails/openrails/pkg/billingauth"
)

const (
	billingMerchantSlug       = "openrails-demo"
	minPostPriceCents   int64 = 50
	maxPostPriceCents   int64 = 99_999_999
)

type postCheckout struct {
	UserID, ProductID, PriceID, IdempotencyKey string
	SuccessURL, CancelURL                      string
}

type billingService struct {
	runtime *openrailsembed.Runtime
	client  *openrails.Client
}

func initializeBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	return openrailsembed.ApplyMigrations(ctx, pool, openrailsembed.MigrationOptions{
		Schema: cfg.BillingSchema,
		River:  openrailsembed.RiverFromHost(),
	})
}

type billingOptions struct {
	// StripeTransport replaces provider HTTP only in local integration tests.
	// It is independent of sandbox credentials; nil uses the real Stripe sandbox.
	StripeTransport http.RoundTripper
}

// newBilling is deliberately sandbox-only. Startup requires the host-owned
// Stripe account, test credential and webhook configuration.
func newBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, options billingOptions) (_ *billingService, err error) {
	if cfg.StripeSecretKey == "" {
		return nil, errors.New("billing requires STRIPE_SECRET_KEY")
	}
	if !strings.HasPrefix(cfg.StripeSecretKey, "sk_test_") && !strings.HasPrefix(cfg.StripeSecretKey, "rk_test_") {
		return nil, errors.New("this demo requires a Stripe sk_test_ or rk_test_ sandbox key")
	}
	if !strings.HasPrefix(cfg.StripeAccountID, "acct_") || !strings.HasPrefix(cfg.StripeWebhookSecret, "whsec_") {
		return nil, errors.New("billing requires STRIPE_ACCOUNT_ID and STRIPE_WEBHOOK_SECRET")
	}
	if pool == nil {
		return nil, errors.New("billing requires the host PostgreSQL pool")
	}
	if auth == nil {
		return nil, errors.New("billing customer routes require AuthKit")
	}
	identity, err := billingauth.NewIntegration(billingauth.IntegrationOptions{Verifier: auth.runtime.Verifier(), Customer: billingauth.SubjectCustomerID})
	if err != nil {
		return nil, err
	}
	opts := openrailsembed.Options{
		Auth: identity,
		HTTP: &openrailsembed.HTTPConfig{CustomerRoutes: []openrailsembed.CustomerRoutesConfig{{
			Merchant: billingMerchantSlug, Scope: openrailsembed.CustomerBillingManagement,
		}}},
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
			TestMode:            openrailsconfig.CredentialPostureSandbox,
			ProviderWriteMode:   openrailsconfig.ProviderWriteModeFull,
			AllowCatalogUpdates: true,
			DB:                  &openrailsconfig.DBConfig{URL: cfg.DatabaseURL, Schema: cfg.BillingSchema},
		},
		PGXPool:         pool,
		River:           openrailsembed.RiverFromHost(),
		StripeTransport: options.StripeTransport,
	}

	runtime, err := openrailsembed.New(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("start OpenRails: %w", err)
	}
	defer func() {
		if err != nil {
			_ = runtime.Close(context.Background())
		}
	}()
	client, err := runtime.Client()
	if err != nil {
		return nil, err
	}
	return &billingService{runtime: runtime, client: client}, nil
}

func (b *billingService) RiverJobs() riverkit.Contribution { return b.runtime.RiverJobs() }

func (b *billingService) Mount(router fiber.Router) error {
	routes, err := openrailsfiber.Routes(b.runtime)
	if err != nil {
		return err
	}
	return routes.Mount(router)
}

func (b *billingService) Close(ctx context.Context) error {
	return b.runtime.Close(ctx)
}

func (b *billingService) Ready(ctx context.Context) error {
	return b.runtime.Ready(ctx)
}

// The handler verifies channel permissions through AuthKit before selecting
// that channel's catalog. Post authorship does not confer catalog authority.
func (b *billingService) EnsurePostOffer(ctx context.Context, catalogOwnerID, billingKey, title string, priceCents int64) (string, string, error) {
	client, err := b.client.ForCatalogOwner(catalogOwnerID)
	if err != nil {
		return "", "", err
	}
	return ensurePostOffer(ctx, client, "", nil, billingKey, title, priceCents)
}

// The blog handler calls this path only after AuthKit grants moderation access.
// It uses administrator authority explicitly while preserving the channel's catalog.
func (b *billingService) EnsurePostOfferAsAdmin(ctx context.Context, ownerID, billingKey, title string, priceCents int64) (string, string, error) {
	catalog, err := b.client.EnsureCatalogForOwner(ctx, ownerID)
	if err != nil {
		return "", "", err
	}
	return ensurePostOffer(ctx, b.client, catalog.ID.String(), []string{"stripe"}, billingKey, title, priceCents)
}

// ArchiveChannelCatalog preserves purchase history while retiring a channel's
// offers. Exact owner lookup does not manufacture an empty catalog.
func (b *billingService) ArchiveChannelCatalog(ctx context.Context, channelID string) error {
	const pageSize = 100
	catalog, err := b.client.GetCatalogForOwner(ctx, channelID)
	if errors.Is(err, openrails.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find channel catalog: %w", err)
	}
	active, archived := false, true
	for {
		products, err := b.client.Products.List(ctx, &openrails.ProductListParams{
			PageOptions: openrails.PageOptions{Limit: pageSize}, CatalogID: catalog.ID.String(), Archived: &active,
		})
		if err != nil {
			return fmt.Errorf("list channel offers: %w", err)
		}
		if len(products.Items) == 0 {
			return nil
		}
		for _, product := range products.Items {
			if _, err := b.client.Products.Update(ctx, product.ID, &openrails.ProductUpdateParams{Archived: &archived}); err != nil {
				return fmt.Errorf("archive channel offer: %w", err)
			}
		}
		// Repeat the first active page: archived products leave this set.
	}
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

func (b *billingService) CreateCheckout(ctx context.Context, request postCheckout) (*openrails.CheckoutSession, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 200 {
		return nil, errors.New("Idempotency-Key must contain between 1 and 200 characters")
	}
	// Namespace the caller's retry key by authenticated buyer and product. A
	// second buyer cannot collide with or retrieve someone else's checkout.
	digest := sha256.Sum256([]byte(request.UserID + "\x00" + request.ProductID + "\x00" + request.IdempotencyKey))
	return b.client.CreateCheckoutSession(ctx, openrails.CreateCheckoutSessionRequest{
		Customer:       openrails.CheckoutCustomerIdentity{ID: request.UserID},
		PriceID:        request.PriceID,
		PaymentOptions: openrails.CheckoutPaymentOptions{Rail: "stripe"},
		IdempotencyKey: "post-" + hex.EncodeToString(digest[:]),
		Metadata:       map[string]string{"product_id": request.ProductID},
		SuccessURL:     request.SuccessURL,
		CancelURL:      request.CancelURL,
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
