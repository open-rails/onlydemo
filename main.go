package main

import (
	"context"
	"fmt"
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

func openDatabase(ctx context.Context) (Config, *pgxpool.Pool, error) {
	config, err := loadConfig()
	if err != nil {
		return Config{}, nil, fmt.Errorf("load configuration: %w", err)
	}

	pool, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		return Config{}, nil, fmt.Errorf("create database pool: %w", err)
	}

	pingContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		pool.Close()
		return Config{}, nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := initializeDatabase(ctx, config, pool); err != nil {
		pool.Close()
		return Config{}, nil, err
	}
	return config, pool, nil
}

func migrate(ctx context.Context) error {
	_, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	return nil
}

func withAuth(ctx context.Context, action func(*appAuth) error) error {
	config, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	authService, err := newAuth(ctx, config, pool)
	if err != nil {
		return fmt.Errorf("initialize authkit: %w", err)
	}
	defer authService.Close()
	return action(authService)
}

func grantAdmin(ctx context.Context, userID string) error {
	return withAuth(ctx, func(authService *appAuth) error {
		if err := authService.grantAdmin(ctx, userID); err != nil {
			return err
		}
		log.Printf("Granted AuthKit admin role to %s", userID)
		return nil
	})
}

func revokeAdmin(ctx context.Context, userID string) error {
	return withAuth(ctx, func(authService *appAuth) error {
		if err := authService.revokeAdmin(ctx, userID); err != nil {
			return err
		}
		log.Printf("Revoked AuthKit admin role from %s", userID)
		return nil
	})
}

func serve(ctx context.Context) error {
	config, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	authService, err := newAuth(ctx, config, pool)
	if err != nil {
		return fmt.Errorf("initialize authkit: %w", err)
	}
	defer authService.Close()

	billing, err := newBilling(ctx, config, pool, authService)
	if err != nil {
		return fmt.Errorf("initialize OpenRails: %w", err)
	}
	var postsBilling postBilling
	if billing != nil {
		defer billing.Close(context.Background())

		postsBilling = billing
	} else {
		log.Print("Stripe is not configured; post sales are disabled")
	}
	channels := newChannels(pool, authService, postsBilling, config)
	jobs, err := newJobs(ctx, pool, config, authService, billing, channels)
	if err != nil {
		return fmt.Errorf("compose application jobs: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := stopJobs(stopCtx, jobs); err != nil {
			log.Printf("close background jobs: %v", err)
		}
	}()
	if err := authService.runtime.Start(ctx); err != nil {
		return fmt.Errorf("start AuthKit: %w", err)
	}
	if err := jobs.Start(ctx); err != nil {
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
