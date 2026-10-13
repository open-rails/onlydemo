package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	"github.com/open-rails/contentkit"
	"github.com/open-rails/contentkit/access"
	"github.com/riverqueue/river"
)

func openDatabase(ctx context.Context) (Config, *pgxpool.Pool, error) {
	config, err := loadConfig()
	if err != nil {
		return Config{}, nil, fmt.Errorf("load configuration: %w", err)
	}

	poolConfig, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		return Config{}, nil, fmt.Errorf("parse database URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
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

// migrate creates or upgrades every schema: each library's New migrates its own.
func migrate(ctx context.Context) error {
	config, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	s, err := compose(ctx, config, pool, billingOptions{})
	if err != nil {
		return err
	}
	s.Close()
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
	srv, err := startServer(ctx, config, pool, billingOptions{})
	if err != nil {
		return err
	}
	defer srv.Close()
	go func() {
		<-ctx.Done()
		if err := srv.app.ShutdownWithTimeout(10 * time.Second); err != nil {
			log.Printf("shutdown HTTP: %v", err)
		}
	}()
	log.Printf("API listening on http://localhost:%d", config.Port)
	return srv.app.Listen(fmt.Sprintf(":%d", config.Port))
}

// server is the composed application: libraries, background jobs and HTTP.
type server struct {
	auth     *appAuth
	billing  *billingService
	content  *contentkit.Engine
	ckRoutes http.Handler
	jobs     *river.Client[pgx.Tx]
	notices  *notifications
	app      *fiber.App
	closers  []func()
}

// Close stops jobs before the services they use.
func (s *server) Close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
}

// compose builds the libraries, each migrating its schema in New, and the
// app's services over them.
func compose(ctx context.Context, config Config, pool *pgxpool.Pool, opts billingOptions) (_ *server, err error) {
	s := &server{}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	if s.auth, err = newAuth(ctx, config, pool); err != nil {
		return nil, fmt.Errorf("initialize authkit: %w", err)
	}
	s.closers = append(s.closers, s.auth.Close)
	if s.billing, err = newBilling(ctx, config, pool, s.auth, opts); err != nil {
		return nil, fmt.Errorf("initialize OpenRails: %w", err)
	}
	s.closers = append(s.closers, func() { _ = s.billing.Close(context.Background()) })
	if s.content, err = newContentKit(ctx, config, pool, s.auth, s.billing); err != nil {
		return nil, fmt.Errorf("initialize ContentKit: %w", err)
	}
	s.closers = append(s.closers, func() { _ = s.content.Close(context.Background()) })
	s.auth.content = s.content
	routes, err := s.content.Routes(contentkit.Routes{Prefix: contentkitPrefix, Groups: contentkit.AllRoutes})
	if err != nil {
		return nil, err
	}
	s.ckRoutes = routes[0].Handler
	return s, nil
}

func startServer(ctx context.Context, config Config, pool *pgxpool.Pool, opts billingOptions) (_ *server, err error) {
	s, err := compose(ctx, config, pool, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	if s.notices, err = newNotifications(ctx, config, s.billing, s.auth, s.content); err != nil {
		return nil, fmt.Errorf("initialize notifications: %w", err)
	}
	if s.jobs, err = newJobs(ctx, pool, config, s.auth, s.billing, s.content, s.notices); err != nil {
		return nil, fmt.Errorf("compose application jobs: %w", err)
	}
	s.closers = append(s.closers, func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := stopJobs(stopCtx, s.jobs); err != nil {
			log.Printf("close background jobs: %v", err)
		}
	})
	if err = s.auth.client.Start(ctx, authkit.WithRiverClient(s.jobs)); err != nil {
		return nil, fmt.Errorf("start AuthKit: %w", err)
	}
	if err = s.billing.Start(ctx, s.jobs); err != nil {
		return nil, fmt.Errorf("start OpenRails: %w", err)
	}
	if err = s.content.Start(ctx, contentkit.WithRiverClient(s.jobs)); err != nil {
		return nil, fmt.Errorf("start ContentKit: %w", err)
	}
	if err = s.billing.requireReady(ctx); err != nil {
		return nil, err
	}
	if err = s.jobs.Start(ctx); err != nil {
		return nil, fmt.Errorf("start application jobs: %w", err)
	}
	if s.app, err = newApp(pool, s, config); err != nil {
		return nil, fmt.Errorf("create application: %w", err)
	}
	return s, nil
}

func newApp(pool *pgxpool.Pool, s *server, cfg Config) (*fiber.App, error) {
	app := fiber.New()
	// One entitlement read per request: the paywall gate memoizes it.
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(access.WithMemo(c.Context()))
		return c.Next()
	})

	app.Get("/dev/routes", homepage(app))

	app.Get("/health", func(c fiber.Ctx) error {
		pingContext, cancel := context.WithTimeout(c.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingContext); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unavailable", "error": "database is unavailable"})
		}
		if err := s.billing.Ready(pingContext); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unavailable", "error": "billing workers are unavailable"})
		}
		if err := s.content.Ready(pingContext); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unavailable", "error": "content workers are unavailable"})
		}
		return c.JSON(fiber.Map{"status": "ok", "database": "ok"})
	})

	app.Get("/api/v1/config", publicConfiguration(cfg.TrustedCountryHeader))
	// ContentKit authenticates its own requests through AuthKit.
	app.All(contentkitPrefix+"/v1/*", adaptor.HTTPHandler(s.ckRoutes))
	// Site admins work on customers' billing through the merchant API.
	if err := s.billing.Mount(app, s.auth.client.Authenticator()); err != nil {
		return nil, err
	}
	// Register AuthKit endpoints on Fiber so they appear in its route table.
	if err := authkitfiber.Mount(app, s.auth.client); err != nil {
		return nil, err
	}
	mountFrontend(app)
	return app, nil
}
