package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	billingMerchantSlug       = "onlydemo"
	minPostPriceCents   int64 = 50
	maxPostPriceCents   int64 = 99_999_999
)

type billingService struct {
	runtime *openrailsembed.Runtime
	client  *openrails.Client
	// psps are the enabled PSPs every offer price declares.
	psps         []string
	postDeletion postDeletionPolicy
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

// providerDeclarations maps the operator's BILLING_PSPS onto OpenRails PSP
// declarations. Every listed provider requires its sandbox credentials.
func providerDeclarations(cfg Config) (map[string]openrailsembed.PSPConfig, error) {
	if len(cfg.BillingPSPs) == 0 {
		return nil, errors.New("BILLING_PSPS must list at least one provider")
	}
	psps := map[string]openrailsembed.PSPConfig{}
	for _, name := range cfg.BillingPSPs {
		switch name {
		case "stripe":
			if !strings.HasPrefix(cfg.StripeSecretKey, "sk_test_") && !strings.HasPrefix(cfg.StripeSecretKey, "rk_test_") {
				return nil, errors.New("stripe requires a STRIPE_SECRET_KEY sandbox key (sk_test_ or rk_test_)")
			}
			if !strings.HasPrefix(cfg.StripeAccountID, "acct_") || !strings.HasPrefix(cfg.StripeWebhookSecret, "whsec_") {
				return nil, errors.New("stripe requires STRIPE_ACCOUNT_ID and STRIPE_WEBHOOK_SECRET")
			}
			if !strings.HasPrefix(cfg.StripePublishableKey, "pk_test_") {
				return nil, errors.New("stripe requires a STRIPE_PUBLISHABLE_KEY pk_test_ key")
			}
			psps["stripe"] = openrailsembed.PSPConfig{"stripe": {
				AccountID: cfg.StripeAccountID,
				Secrets: map[string]string{
					"secret_key":             cfg.StripeSecretKey,
					"webhook_signing_secret": cfg.StripeWebhookSecret,
				},
			}}
		case "nmi":
			for env, value := range map[string]string{"NMI_ACCOUNT_ID": cfg.NMIAccountID, "NMI_SANDBOX_SECURITY_KEY": cfg.NMISandboxSecurityKey, "NMI_TOKENIZATION_KEY": cfg.NMITokenizationKey, "NMI_WEBHOOK_SIGNING_SECRET": cfg.NMIWebhookSecret} {
				if strings.TrimSpace(value) == "" {
					return nil, fmt.Errorf("nmi requires %s", env)
				}
			}
			settings := map[string]any{"endpoint_deployment": "gateway", "tokenization_key": cfg.NMITokenizationKey}
			if cfg.NMITokenizationURL != "" {
				settings["tokenization_url"] = cfg.NMITokenizationURL
			}
			psps["nmi"] = openrailsembed.PSPConfig{"nmi": {
				AccountID: cfg.NMIAccountID,
				Secrets: map[string]string{
					"security_key":           cfg.NMISandboxSecurityKey,
					"webhook_signing_secret": cfg.NMIWebhookSecret,
				},
				Settings: settings,
			}}
		default:
			return nil, fmt.Errorf("unsupported billing provider %q", name)
		}
	}
	return psps, nil
}

// newBilling is deliberately sandbox-only: OpenRails runs in sandbox posture
// and each declared provider must carry test credentials.
func newBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, options billingOptions) (_ *billingService, err error) {
	psps, err := providerDeclarations(cfg)
	if err != nil {
		return nil, fmt.Errorf("billing: %w", err)
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
			DisplayName: "OnlyDemo",
			PSPs:        psps,
		}},
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
	return &billingService{runtime: runtime, client: client, psps: cfg.BillingPSPs, postDeletion: cfg.PostDeletion}, nil
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

// requireReady refuses to serve unless every declared PSP's sandbox posture
// was verified. Call it after River composition, which Ready also checks.
func (b *billingService) requireReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.runtime.Ready(ctx); err != nil {
		return fmt.Errorf("OpenRails is not ready; refusing to serve (a disarmed PSP means its credentials were not verified as sandbox): %w", err)
	}
	return nil
}
