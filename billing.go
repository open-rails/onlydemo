package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/auth"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/openrails"
	openrailsfiber "github.com/open-rails/openrails/adapters/fiber"
	"github.com/riverqueue/river"
)

type billingService struct {
	engine *openrails.Engine
	client *openrails.Client
}

type billingOptions struct {
	// Test replaces provider transports and the clock, and Sandbox the
	// providers' sandbox endpoints, only in local integration tests.
	Test    func(*openrails.Deps)
	Sandbox *openrails.ProviderSandboxConfig
}

// newBilling runs OpenRails over the merchant's non-live PSPs (loadConfig
// refuses a live one). New creates or upgrades OpenRails' tables.
func newBilling(ctx context.Context, cfg Config, pool *pgxpool.Pool, accounts *billingAccounts, options billingOptions) (*billingService, error) {
	if pool == nil {
		return nil, errors.New("billing requires the host PostgreSQL pool")
	}
	deps := openrails.Deps{Postgres: pool, UserInfo: accounts,
		Notifications: &openrails.NotificationChannel{Sink: accounts, Recipient: accounts.recipient}}
	merchant := cfg.Merchant
	merchant.Issuer = cfg.AuthIssuer
	if options.Test != nil {
		options.Test(&deps)
	}
	engine, err := openrails.New(ctx, openrails.Config{
		Database:             openrails.DatabaseConfig{Schema: billingSchema(cfg), RiverSchema: riverSchema(cfg)},
		ProviderWriteMode:    cfg.ProviderWriteMode,
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
	return &billingService{engine: engine, client: client}, nil
}

func billingSchema(cfg Config) string {
	if schema := strings.TrimSpace(cfg.BillingSchema); schema != "" {
		return schema
	}
	return "billing"
}

func (b *billingService) RiverJobs() riverkit.Contribution { return b.engine.RiverJobs() }

// Start binds OpenRails to the host's River fleet.
func (b *billingService) Start(ctx context.Context, fleet *river.Client[pgx.Tx]) error {
	return b.engine.Start(ctx, openrails.WithRiverClient(fleet))
}

// Mount serves billing at /billing/v1: the storefront, a customer's orders
// for native OpenRails prices, billing management and the
// merchant API for site admins.
func (b *billingService) Mount(app *fiber.App, authenticator auth.Authenticator) error {
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
