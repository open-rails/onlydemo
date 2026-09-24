package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	// reads are the viewer lookups behind every page (the client; tests count them).
	reads entitlementReads
	// psps are the enabled PSPs every offer price declares.
	psps         []string
	postDeletion postDeletionPolicy
	// membershipHours is the renewal period of new membership prices.
	membershipHours int
}

func initializeBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	return openrailsembed.ApplyMigrations(ctx, pool, openrailsembed.MigrationOptions{
		Schema: cfg.BillingSchema,
		River:  openrailsembed.RiverFromHost(),
	})
}

type billingOptions struct {
	// Test adjusts the runtime options, only in local integration tests
	// (fake provider transports).
	Test func(*openrailsembed.Options)
}

// newBilling is deliberately sandbox-only: OpenRails runs in sandbox posture
// and each declared provider must carry test credentials.
func newBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, options billingOptions) (_ *billingService, err error) {
	if pool == nil {
		return nil, errors.New("billing requires the host PostgreSQL pool")
	}
	if auth == nil {
		return nil, errors.New("billing customer routes require AuthKit")
	}
	if cfg.MembershipHours < 1 {
		return nil, errors.New("membership period must be at least 1 hour")
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
			PSPs:        cfg.PSPs,
		}},
		Config: &openrailsconfig.Config{
			TestMode:            openrailsconfig.CredentialPostureSandbox,
			ProviderWriteMode:   openrailsconfig.ProviderWriteModeFull,
			AllowCatalogUpdates: true,
			ReturnOrigins:       cfg.ReturnOrigins,
			DB:                  &openrailsconfig.DBConfig{URL: cfg.DatabaseURL, Schema: cfg.BillingSchema},
		},
		PGXPool: pool,
		River:   openrailsembed.RiverFromHost(),
	}

	if options.Test != nil {
		options.Test(&opts)
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
	if err := routeCheckoutsTo(ctx, client, cfg.CheckoutPSP); err != nil {
		return nil, fmt.Errorf("route new checkouts to %q: %w", cfg.CheckoutPSP, err)
	}
	// This assignment precedes shared-fleet startup and HTTP publication.
	auth.billing = client
	return &billingService{runtime: runtime, client: client, reads: client, psps: slices.Sorted(maps.Keys(cfg.PSPs)), postDeletion: cfg.PostDeletion, membershipHours: cfg.MembershipHours}, nil
}

type entitlementReads interface {
	CheckEntitlements(ctx context.Context, customerID string, entitlements []string, at time.Time, options ...openrails.RequestOption) (map[string]bool, error)
	ListEntitlements(ctx context.Context, subject string, at time.Time, options ...openrails.RequestOption) ([]openrails.EntitlementRecord, error)
	ListOffersForEntitlements(ctx context.Context, entitlements []string, params openrails.OfferListParams, options ...openrails.RequestOption) (map[string]openrails.OfferList, error)
}

// routeCheckoutsTo makes psp OpenRails's only checkout candidate, so new
// purchases and new cards use it while other PSPs keep their existing work.
func routeCheckoutsTo(ctx context.Context, client *openrails.Client, psp string) error {
	settings, err := client.GetMerchantSettings(ctx)
	if err != nil {
		return err
	}
	rules := []openrails.CheckoutRoutingRule{{Prefer: []string{psp}}}
	if settings.CheckoutRouting != nil && slices.EqualFunc(*settings.CheckoutRouting, rules, func(a, b openrails.CheckoutRoutingRule) bool {
		return a.Match.IsCatchAll() && slices.Equal(a.Prefer, b.Prefer)
	}) {
		return nil
	}
	settings.CheckoutRouting = &rules
	return client.SetMerchantSettings(ctx, *settings)
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
