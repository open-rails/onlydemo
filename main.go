package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	openrailsfiber "github.com/open-rails/openrails/adapters/fiber"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	command, err := parseCommand(args, output)
	if err != nil {
		return err
	}
	if command.kind == "help" {
		return nil
	}
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

	if err := initializeDatabase(ctx, config, pool); err != nil {
		return err
	}
	if command.kind == "migrate" {
		return nil
	}

	jobs := newJobs(pool, config)

	authService, err := newAuth(ctx, config, pool)
	if err != nil {
		return fmt.Errorf("initialize authkit: %w", err)
	}
	defer authService.Close()
	jobs.auth = authService

	if command.kind == "admin" {
		if command.revoke {
			if err := authService.revokeAdmin(ctx, command.userID); err != nil {
				return err
			}
			log.Printf("Revoked AuthKit admin role from %s", command.userID)
			return nil
		}
		if err := authService.grantAdmin(ctx, command.userID); err != nil {
			return err
		}
		log.Printf("Granted AuthKit admin role to %s", command.userID)
		return nil
	}

	billing, err := newBilling(ctx, config, pool, authService)
	if err != nil {
		return fmt.Errorf("initialize OpenRails: %w", err)
	}
	var postsBilling postBilling
	if billing != nil {
		defer billing.Close(context.Background())

		jobs.billing = billing
		postsBilling = billing
	} else {
		log.Print("Stripe is not configured; post sales are disabled")
	}
	channels := newChannels(pool, authService, postsBilling, config)
	jobs.channels = channels
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

	app, err := newApp(pool, authService, postsBilling, config, channels)
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

func newApp(pool *pgxpool.Pool, authService *appAuth, billing postBilling, cfg Config, channels *channelAPI) (*fiber.App, error) {
	app := fiber.New()
	if err := validateDatabaseSchemas(cfg); err != nil {
		return nil, err
	}
	blogAPI := &blogAPI{pool: pool, auth: authService, billing: billing, channels: channels, table: pgx.Identifier{appSchema(cfg), "blog_posts"}.Sanitize()}

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

	optional := authkitfiber.Optional(authService.runtime.Verifier())
	required := authkitfiber.Required(authService.runtime.Verifier())
	channels.mount(app, required)
	app.Get("/api/posts", optional, blogAPI.list)
	app.Post("/api/posts", required, blogAPI.create)
	app.Get("/api/posts/:id", optional, blogAPI.get)
	app.Patch("/api/posts/:id", required, blogAPI.update)
	app.Delete("/api/posts/:id", required, blogAPI.delete)
	app.Post("/api/posts/:id/checkout", required, blogAPI.checkout)
	app.Get("/api/checkouts/:id", required, blogAPI.getCheckout)
	if billing, ok := billing.(*billingService); ok && billing != nil {
		routes, err := openrailsfiber.Routes(billing.runtime)
		if err != nil {
			return nil, err
		}
		if err := routes.Mount(app.Group("/billing")); err != nil {
			return nil, err
		}
	}

	// Register AuthKit endpoints on Fiber so they appear in its route table.
	authRoutes, err := authkitfiber.Routes(authService.runtime)
	if err != nil {
		return nil, err
	}
	if err := authRoutes.Mount(app); err != nil {
		return nil, err
	}
	return app, nil
}
