package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/jackc/pgx/v5/pgxpool"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	pool, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	pingContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	migrationContext, stopMigration := context.WithTimeout(ctx, 5*time.Minute)
	err = initializeDatabase(migrationContext, config, pool)
	stopMigration()
	if err != nil {
		return err
	}
	if config.MigrationsOnly {
		return nil
	}

	jobs := newJobs(pool, config)

	authService, err := newAuth(ctx, config, pool)
	if err != nil {
		return fmt.Errorf("initialize authkit: %w", err)
	}
	defer authService.Close()
	jobs.auth = authService

	if config.AdminOnly {
		if config.AdminRevoke {
			if err := authService.revokeAdmin(ctx, config.AdminUserID); err != nil {
				return err
			}
			log.Printf("Revoked AuthKit admin role from %s", config.AdminUserID)
			return nil
		}
		if err := authService.grantAdmin(ctx, config.AdminUserID); err != nil {
			return err
		}
		log.Printf("Granted AuthKit admin role to %s", config.AdminUserID)
		return nil
	}

	billing, err := newBilling(ctx, config, jobs)
	if err != nil {
		return fmt.Errorf("initialize OpenRails: %w", err)
	}
	var postsBilling postBilling
	if billing != nil {
		postsBilling = billing
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := billing.Close(closeCtx); err != nil {
				log.Printf("close OpenRails: %v", err)
			}
		}()
	} else {
		log.Print("Stripe is not configured; post sales are disabled")
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := jobs.close(stopCtx); err != nil {
			log.Printf("close background jobs: %v", err)
		}
	}()
	if err := jobs.start(ctx); err != nil {
		return fmt.Errorf("start application jobs: %w", err)
	}

	app, err := newApp(pool, authService, postsBilling)
	if err != nil {
		return fmt.Errorf("create application: %w", err)
	}
	go func() {
		<-ctx.Done()
		if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
			log.Printf("shutdown HTTP: %v", err)
		}
	}()
	log.Printf("API listening on http://localhost:%d", config.Port)
	return app.Listen(fmt.Sprintf(":%d", config.Port))
}

func newApp(pool *pgxpool.Pool, authService *appAuth, billing postBilling) (*fiber.App, error) {
	app := fiber.New()
	blogAPI := &blogAPI{pool: pool, auth: authService, billing: billing}

	app.Get("/", homepage(app))

	app.Get("/health", func(c fiber.Ctx) error {
		pingContext, cancel := context.WithTimeout(c.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingContext); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "unavailable",
				"error":  "database is unavailable",
			})
		}
		if readiness, ok := billing.(interface{ Ready(context.Context) error }); ok {
			if err := readiness.Ready(pingContext); err != nil {
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"status": "unavailable", "error": "billing workers are unavailable",
				})
			}
		}

		return c.JSON(fiber.Map{
			"status":   "ok",
			"database": "ok",
		})
	})

	optional := authkitfiber.Optional(authService.Verifier())
	required := authkitfiber.Required(authService.Verifier())
	app.Get("/api/posts", optional, blogAPI.list)
	app.Post("/api/posts", required, blogAPI.create)
	app.Get("/api/posts/:id", optional, blogAPI.get)
	app.Patch("/api/posts/:id", required, blogAPI.update)
	app.Delete("/api/posts/:id", required, blogAPI.delete)
	app.Post("/api/posts/:id/checkout", required, blogAPI.checkout)
	app.Get("/api/checkouts/:id", required, blogAPI.getCheckout)
	if hooks, ok := billing.(interface{ WebhookHandler() http.Handler }); ok {
		app.Post(billingWebhookPath, adaptor.HTTPHandler(hooks.WebhookHandler()))
	}

	// Register AuthKit endpoints on Fiber so they appear in its route table.
	if err := authkitfiber.Mount(app, authService.Service); err != nil {
		return nil, err
	}
	return app, nil
}
