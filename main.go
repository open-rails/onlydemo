package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
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
	poolConfig.ConnConfig.Tracer = queryTracer{}
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
	channels *channelAPI
	posts    *postAPI
	media    *mediaService
	jobs     *river.Client[pgx.Tx]
	app      *fiber.App
	closers  []func()
}

// Close stops jobs before the services they use.
func (s *server) Close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
}

func startServer(ctx context.Context, config Config, pool *pgxpool.Pool, opts billingOptions) (_ *server, err error) {
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
	s.channels = newChannels(pool, s.auth, s.billing, config)
	if s.posts, err = newPosts(s.channels, config); err != nil {
		return nil, err
	}
	if s.media, err = newMedia(ctx, config, pool, s.auth, s.billing, s.channels, s.posts); err != nil {
		return nil, fmt.Errorf("initialize media: %w", err)
	}
	if s.jobs, err = newJobs(ctx, pool, config, s.auth, s.billing, s.channels, s.posts, s.media); err != nil {
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
	if err = s.billing.requireReady(ctx); err != nil {
		return nil, err
	}
	if err = s.jobs.Start(ctx); err != nil {
		return nil, fmt.Errorf("start application jobs: %w", err)
	}
	if s.app, err = newApp(pool, s.auth, s.billing, config, s.channels, s.posts, s.media); err != nil {
		return nil, fmt.Errorf("create application: %w", err)
	}
	return s, nil
}

func newApp(pool *pgxpool.Pool, authService *appAuth, billing *billingService, cfg Config, channels *channelAPI, posts *postAPI, media *mediaService) (*fiber.App, error) {
	app := fiber.New()
	app.Use(countQueries)
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
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "unavailable",
				"error":  "database is unavailable",
			})
		}
		if err := billing.Ready(pingContext); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unavailable", "error": "billing workers are unavailable"})
		}

		return c.JSON(fiber.Map{
			"status":   "ok",
			"database": "ok",
		})
	})

	optional := authkitfiber.Optional(authService.client)
	required := authkitfiber.Required(authService.client)
	channels.mount(app, required)
	app.Get("/api/v1/channels", optional, channels.list)
	app.Get("/api/v1/channels/:id", optional, channels.publicGet)
	app.Put("/api/v1/channels/:id/membership", required, channels.setMembership)
	app.Post("/api/v1/channels/:id/join", required, channels.join)
	app.Post("/api/v1/channels/:id/leave", required, channels.leave)
	app.Post("/api/v1/channels/:id/subscribe", required, func(c fiber.Ctx) error { return channels.subscribe(c, returnOrigin(c, cfg.PublicURL)) })
	app.Get("/api/v1/channels/:id/members", required, channels.members)
	app.Post("/api/v1/channels/:id/members", required, channels.members)
	app.Delete("/api/v1/channels/:id/members/:user_id", required, channels.members)
	app.Get("/api/v1/me", required, posts.me)
	app.Get("/api/v1/me/channels", required, channels.mine)
	app.Get("/api/v1/config", publicConfiguration(billing, media, cfg.TrustedCountryHeader))
	app.Get("/api/v1/posts", optional, posts.list)
	app.Post("/api/v1/posts", required, posts.create)
	app.Get("/api/v1/posts/:code", optional, posts.get)
	app.Patch("/api/v1/posts/:code", required, posts.update)
	app.Delete("/api/v1/posts/:code", required, posts.delete)
	app.Post("/api/v1/posts/:code/checkout", required, func(c fiber.Ctx) error {
		return posts.checkout(c, returnOrigin(c, cfg.PublicURL))
	})
	media.mount(app, optional)
	// Site admins work on customers' billing through the merchant API.
	if err := billing.Mount(app, authService.client); err != nil {
		return nil, err
	}

	// Register AuthKit endpoints on Fiber so they appear in its route table.
	if err := authkitfiber.Mount(app, authService.client); err != nil {
		return nil, err
	}
	urls, err := newURLRouter(posts, cfg)
	if err != nil {
		return nil, err
	}
	mountFrontend(app, urls, cfg.PublicURL, posts)
	return app, nil
}
