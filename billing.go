package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
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
	// This assignment precedes shared-fleet startup and HTTP publication.
	auth.billing = client
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
