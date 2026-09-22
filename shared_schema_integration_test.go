package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/embedded"
	riverkit "github.com/open-rails/helpers/river"
	openrailsembed "github.com/open-rails/openrails/embed"
)

func TestAllPublicInitializerOrderAndConcurrentReplay(t *testing.T) {
	// Each component goes first in a fresh database. Rotate the remaining
	// initializers, then overlap repeated calls on the same one-connection pool.
	names := []string{"app", "auth", "billing", "river"}
	for first := range names {
		t.Run(names[first]+"_first", func(t *testing.T) {
			admin := newBlogTestDatabase(t)
			pool, _ := newBlogTestOwnerPool(t, admin, 1)
			cfg := Config{AppSchema: "public", AuthSchema: "public", BillingSchema: "public", RiverSchema: "public"}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			initializers := []func(context.Context) error{
				func(ctx context.Context) error { return applyMigrations(ctx, pool, cfg) },
				func(ctx context.Context) error {
					return embedded.ApplyMigrations(ctx, pool, "public", embedded.MigrationOptions{River: embedded.RiverFromHost()})
				},
				func(ctx context.Context) error { return initializeBilling(ctx, cfg, pool) },
				func(ctx context.Context) error { return riverkit.ApplyMigrations(ctx, pool, cfg.RiverSchema) },
			}
			for i := range names {
				if err := initializers[(first+i)%len(names)](ctx); err != nil {
					t.Fatalf("initialize %s: %v", names[(first+i)%len(names)], err)
				}
			}
			assertAllPublicStorage(t, pool)
			before := migrationLedgerSnapshot(t, pool)
			results := make(chan error, 8)
			var wg sync.WaitGroup
			for range 2 {
				for _, initialize := range initializers {
					wg.Go(func() { results <- initialize(ctx) })
				}
			}
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Errorf("concurrent initializer: %v", err)
				}
			}
			if after := migrationLedgerSnapshot(t, pool); after != before {
				t.Fatal("repeated initialization rewrote migration history")
			}
			assertAllPublicStorage(t, pool)
			if err := pool.Ping(ctx); err != nil {
				t.Fatal("initializer closed host pool:", err)
			}
		})
	}
}

func TestAllPublicManagedLibraryInitializers(t *testing.T) {
	for _, authFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("auth_first_%v", authFirst), func(t *testing.T) {
			admin := newBlogTestDatabase(t)
			pool, _ := newBlogTestOwnerPool(t, admin, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			cfg := Config{AppSchema: "public"}
			auth := func() error { return embedded.ApplyMigrations(ctx, pool, "public") }
			billing := func() error {
				return openrailsembed.ApplyMigrations(ctx, pool, openrailsembed.MigrationOptions{Schema: "public"})
			}
			initializers := []func() error{billing, auth}
			if authFirst {
				initializers = []func() error{auth, billing}
			}
			for _, initialize := range initializers {
				if err := initialize(); err != nil {
					t.Fatal(err)
				}
			}
			if err := applyMigrations(ctx, pool, cfg); err != nil {
				t.Fatal(err)
			}
			before := migrationLedgerSnapshot(t, pool)
			for _, initialize := range initializers {
				if err := initialize(); err != nil {
					t.Fatal(err)
				}
			}
			if after := migrationLedgerSnapshot(t, pool); after != before {
				t.Fatal("managed initializers rewrote migration history")
			}
			assertAllPublicStorage(t, pool)
		})
	}
}

func migrationLedgerSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(t.Context(), "SELECT jsonb_agg(m ORDER BY id)::text FROM public.migrations m").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestAllPublicCLICommands(t *testing.T) {
	admin := newBlogTestDatabase(t)
	pool, dsn := newBlogTestOwnerPool(t, admin, 1)
	for name, value := range map[string]string{"DATABASE_URL": dsn, "AUTH_SCHEMA": "public", "APP_SCHEMA": "public", "BILLING_SCHEMA": "public", "RIVER_SCHEMA": "public", "PUBLIC_URL": "http://localhost:3000", "AUTH_ISSUER": "http://localhost:3000", "AUTH_AUDIENCE": "openrails-demo", "STRIPE_SECRET_KEY": ""} {
		t.Setenv(name, value)
	}
	for range 2 {
		if err := run(t.Context(), []string{"migrate"}, io.Discard); err != nil {
			t.Fatal("CLI migrate:", err)
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	service, err := newAuth(t.Context(), cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	app, err := newApp(pool, service, nil, cfg, newChannels(pool, service, nil, cfg))
	if err != nil {
		t.Fatal(err)
	}
	server := newBlogTestServer(t, app)
	owner := registerBlogTestUser(t, server, service, "cliowner")
	operatorServer := &blogTestServer{url: server.url, client: newBlogTestHTTPClient(t, "127.0.0.5")}
	operator := registerBlogTestUser(t, operatorServer, service, "clioperator")
	post := createBlogTestPost(t, server, owner.token, map[string]any{"slug": "cli-private", "title": "CLI protected", "body": "Private", "visibility": "private"})
	blogTestRequest(t, server, http.MethodGet, blogTestPath(post.ID), operator.token, nil, http.StatusNotFound)
	if err := run(t.Context(), []string{"admin", "grant", "--user-id", operator.id}, io.Discard); err != nil {
		t.Fatal("CLI grant:", err)
	}
	assertBlogTestReadable(t, server, post, operator.token, true)
	if err := run(t.Context(), []string{"admin", "revoke", "--user-id", operator.id}, io.Discard); err != nil {
		t.Fatal("CLI revoke:", err)
	}
	blogTestRequest(t, server, http.MethodGet, blogTestPath(post.ID), operator.token, nil, http.StatusNotFound)
	assertAllPublicStorage(t, pool)
}

func TestBillingConstructorPreservesHostPoolOnFailure(t *testing.T) {
	admin := newBlogTestDatabase(t)
	pool, dsn := newBlogTestOwnerPool(t, admin, 1)
	cfg := Config{DatabaseURL: dsn, PublicURL: "http://localhost:3000", AppSchema: "public", AuthSchema: "public", BillingSchema: "public", RiverSchema: "public", StripeSecretKey: "sk_test_cleanup", StripeAccountID: "acct_cleanup", StripeWebhookSecret: "whsec_cleanup"}
	if err := initializeDatabase(t.Context(), cfg, pool); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	billing, err := newBilling(canceled, cfg, pool, nil, billingOptions{StripeTransport: &blogTestStripe{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected constructor failure: %v", err)
	}
	if billing != nil {
		t.Fatal("failed constructor returned a runtime")
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("constructor closed borrowed pool: %v", err)
	}
	var queued int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM public.river_job").Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("failed initialization started jobs: %d %v", queued, err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatal("failed initialization closed host pool:", err)
	}
}

func TestAllPublicConcurrentFreshInitializers(t *testing.T) {
	for _, connections := range []int32{1, 4} {
		t.Run(fmt.Sprintf("pool_%d", connections), func(t *testing.T) {
			admin := newBlogTestDatabase(t)
			pool, _ := newBlogTestOwnerPool(t, admin, connections)
			cfg := Config{AppSchema: "public", AuthSchema: "public", BillingSchema: "public", RiverSchema: "public"}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			initializers := []func(context.Context) error{
				func(ctx context.Context) error { return applyMigrations(ctx, pool, cfg) },
				func(ctx context.Context) error {
					return embedded.ApplyMigrations(ctx, pool, "public", embedded.MigrationOptions{River: embedded.RiverFromHost()})
				},
				func(ctx context.Context) error { return initializeBilling(ctx, cfg, pool) },
				func(ctx context.Context) error { return riverkit.ApplyMigrations(ctx, pool, cfg.RiverSchema) },
			}
			start := make(chan struct{})
			results := make(chan error, len(initializers))
			for _, initialize := range initializers {
				go func() { <-start; results <- initialize(ctx) }()
			}
			close(start)
			for range initializers {
				if err := <-results; err != nil {
					t.Errorf("fresh concurrent initialization: %v", err)
				}
			}
			if t.Failed() {
				return
			}
			assertAllPublicStorage(t, pool)
			before := migrationLedgerSnapshot(t, pool)
			if err := initializeDatabase(ctx, cfg, pool); err != nil {
				t.Fatal(err)
			}
			if after := migrationLedgerSnapshot(t, pool); after != before {
				t.Fatal("replay changed fresh concurrent migration records")
			}
		})
	}
}
