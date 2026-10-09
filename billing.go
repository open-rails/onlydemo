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
	"github.com/open-rails/contentkit/access"
	ckopenrails "github.com/open-rails/contentkit/adapters/openrails"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/openrails"
	openrailsfiber "github.com/open-rails/openrails/adapters/fiber"
	"github.com/open-rails/openrails/billing"
	"github.com/riverqueue/river"
)

type billingService struct {
	client *openrails.Client
	// reads are the viewer lookups behind every page (the client; tests count them).
	reads entitlementReads
	// gate decides paywalls from one entitlement read per request.
	gate *access.Gate
	// psps are the declared PSPs every offer price is sold through.
	psps         []string
	postDeletion postDeletionPolicy
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
	deps := openrails.Deps{Postgres: pool}
	if options.Test != nil {
		options.Test(&deps)
	}
	client, err := openrails.New(ctx, openrails.Config{
		Database:             openrails.DatabaseConfig{Schema: billingSchema(cfg), RiverSchema: riverSchema(cfg)},
		TestMode:             openrails.Sandbox,
		ProviderWriteMode:    openrails.ProviderWritesFull,
		Merchant:             cfg.Merchant,
		PublicBillingBaseURL: cfg.PublicURL + "/billing",
		ReturnOrigins:        cfg.ReturnOrigins,
		ProviderSandbox:      options.Sandbox,
	}, deps)
	if err != nil {
		return nil, fmt.Errorf("start OpenRails: %w", err)
	}
	b := &billingService{client: client, reads: client, psps: slices.Sorted(maps.Keys(cfg.Merchant.PSPs)), postDeletion: cfg.PostDeletion, membershipHours: cfg.MembershipHours}
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
	CheckEntitlements(context.Context, billing.CustomerID, billing.CheckEntitlementsParams, ...openrails.RequestOption) (*billing.EntitlementCheck, error)
	ListCustomerEntitlements(context.Context, billing.CustomerID, billing.CustomerEntitlementListParams, ...openrails.RequestOption) (*billing.ListPage[billing.CustomerEntitlement], error)
	ListOffers(context.Context, billing.OfferListParams, ...openrails.RequestOption) (billing.OfferPages, error)
	CheckProductAccess(context.Context, billing.CustomerID, billing.CheckProductAccessParams, ...openrails.RequestOption) (map[string]bool, error)
}

// liveReads reads entitlements through b.reads at call time.
type liveReads struct{ b *billingService }

func (r liveReads) CheckEntitlements(ctx context.Context, c billing.CustomerID, p billing.CheckEntitlementsParams, o ...openrails.RequestOption) (*billing.EntitlementCheck, error) {
	return r.b.reads.CheckEntitlements(ctx, c, p, o...)
}

func (b *billingService) RiverJobs() riverkit.Contribution { return b.client.RiverJobs() }

// Start binds OpenRails to the host's River fleet.
func (b *billingService) Start(ctx context.Context, fleet *river.Client[pgx.Tx]) error {
	return b.client.Start(ctx, openrails.WithRiverClient(fleet))
}

// Mount serves billing at /billing/v1: the storefront, a customer's billing
// management (paying sessions the app mints, cards, history) and the merchant
// API for site admins. Customers start no checkout here: the app's purchase
// endpoints apply its content rules first.
func (b *billingService) Mount(app *fiber.App, auth openrails.Auth) error {
	return openrailsfiber.Mount(app, b.client, openrails.Routes{
		Auth:       auth,
		Prefix:     "/billing",
		Storefront: true,
		Customers:  openrails.CustomerBillingManagement,
		Merchant:   true,
		Guards: openrails.Guards{
			openrails.StaffReads:  customersRead,
			openrails.StaffWrites: customersUpdate,
		},
	})
}

func (b *billingService) Close(ctx context.Context) error {
	return b.client.Close(ctx)
}

func (b *billingService) Ready(ctx context.Context) error {
	return b.client.Ready(ctx)
}

// requireReady refuses to serve unless OpenRails is ready. Call it after
// River composition, which Ready also checks.
func (b *billingService) requireReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.client.Ready(ctx); err != nil {
		return fmt.Errorf("OpenRails is not ready; refusing to serve: %w", err)
	}
	return nil
}
