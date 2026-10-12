package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/auth"
	"github.com/open-rails/contentkit/access"
	ckopenrails "github.com/open-rails/contentkit/adapters/openrails"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/openrails"
	openrailsfiber "github.com/open-rails/openrails/adapters/fiber"
	"github.com/open-rails/openrails/billing"
	"github.com/riverqueue/river"
)

type billingService struct {
	engine *openrails.Engine
	client *openrails.Client
	// reads are the viewer lookups behind every page (the client; tests count them).
	reads entitlementReads
	// gate decides paywalls from one entitlement read per request.
	gate *access.Gate
	// psps are the declared PSPs every offer price is sold through.
	psps []string
	// membershipHours is the renewal period of new membership prices.
	membershipHours int
}

type billingOptions struct {
	// Test replaces provider transports and the clock, and Sandbox the
	// providers' sandbox endpoints, only in local integration tests.
	Test    func(*openrails.Deps)
	Sandbox *openrails.ProviderSandboxConfig
}

// newBilling is deliberately sandbox-only: OpenRails runs in sandbox posture
// and each declared PSP must carry test credentials. New creates or upgrades
// OpenRails' tables.
func newBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, auth *appAuth, options billingOptions) (*billingService, error) {
	if pool == nil {
		return nil, errors.New("billing requires the host PostgreSQL pool")
	}
	if cfg.MembershipHours < 1 {
		return nil, errors.New("membership period must be at least 1 hour")
	}
	deps := openrails.Deps{Postgres: pool, UserInfo: auth.client.UserInfo()}
	merchant := cfg.Merchant
	merchant.Issuer = cfg.AuthIssuer
	if options.Test != nil {
		options.Test(&deps)
	}
	engine, err := openrails.New(ctx, openrails.Config{
		Database:             openrails.DatabaseConfig{Schema: billingSchema(cfg), RiverSchema: riverSchema(cfg)},
		TestMode:             openrails.Sandbox,
		ProviderWriteMode:    openrails.ProviderWritesFull,
		Merchants:            map[string]openrails.MerchantDeclaration{"onlydemo": merchant},
		PublicBillingBaseURL: cfg.PublicURL + "/billing",
		ReturnOrigins:        cfg.ReturnOrigins,
		ProviderSandbox:      options.Sandbox,
	}, deps)
	if err != nil {
		return nil, fmt.Errorf("start OpenRails: %w", err)
	}
	client, err := engine.MerchantBySlug("onlydemo")
	if err != nil {
		_ = engine.Close(ctx)
		return nil, err
	}
	b := &billingService{engine: engine, client: client, reads: client, psps: slices.Sorted(maps.Keys(cfg.Merchant.PSPs)), membershipHours: cfg.MembershipHours}
	if b.gate, err = access.NewGate(access.GateConfig{Entitlements: ckopenrails.Entitlements(liveReads{b}), Tenant: tenant,
		MemberKinds: []string{kindChannel}, OwnedKinds: []string{kindPost}}); err != nil {
		return nil, err
	}
	// This assignment precedes shared-fleet startup and HTTP publication.
	auth.billing = b
	return b, nil
}

func billingSchema(cfg Config) string {
	if schema := strings.TrimSpace(cfg.BillingSchema); schema != "" {
		return schema
	}
	return "billing"
}

type entitlementReads interface {
	ListProductAccess(context.Context, billing.ProductAccessListParams, ...openrails.RequestOption) (*billing.ListPage[billing.ProductAccessGrant], error)
	CheckEntitlements(context.Context, billing.CheckEntitlementsParams, ...openrails.RequestOption) (*billing.EntitlementCheck, error)
	ListEntitlements(context.Context, billing.EntitlementListParams, ...openrails.RequestOption) (*billing.ListPage[billing.CustomerEntitlement], error)
	ListProducts(context.Context, billing.ProductListParams, ...openrails.RequestOption) (*billing.ListPage[billing.Product], error)
}

// liveReads preserves the request's shared entitlement read and test observer.
type liveReads struct{ b *billingService }

func (r liveReads) CheckEntitlements(ctx context.Context, p billing.CheckEntitlementsParams, o ...openrails.RequestOption) (*billing.EntitlementCheck, error) {
	return r.b.reads.CheckEntitlements(ctx, p, o...)
}
func (b *billingService) RiverJobs() riverkit.Contribution { return b.engine.RiverJobs() }

// Start binds OpenRails to the host's River fleet.
func (b *billingService) Start(ctx context.Context, fleet *river.Client[pgx.Tx]) error {
	return b.engine.Start(ctx, openrails.WithRiverClient(fleet))
}

// Mount serves billing at /billing/v1: the storefront, a customer's billing
// management (paying sessions the app mints, cards, history) and the merchant
// API for site admins. Customers start no checkout here: the app's purchase
// endpoints apply its content rules first.
func (b *billingService) Mount(app *fiber.App, authenticator auth.Authenticator) error {
	// Only the host may create purchases after checking its content policy.
	app.Post("/billing/v1/me/orders", func(c fiber.Ctx) error { return fiber.ErrNotFound })
	return openrailsfiber.Mount(app, b.engine, openrails.Routes{
		Auth: authenticator, Prefix: "/billing",
		RouteGroups: openrails.RouteGroups{Admin: true},
	})
}

func (b *billingService) Close(ctx context.Context) error {
	return b.engine.Close(ctx)
}

func (b *billingService) Ready(ctx context.Context) error {
	return b.engine.Ready(ctx)
}

// requireReady refuses to serve unless OpenRails is ready. Call it after
// River composition, which Ready also checks.
func (b *billingService) requireReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.engine.Ready(ctx); err != nil {
		return fmt.Errorf("OpenRails is not ready; refusing to serve: %w", err)
	}
	return nil
}
