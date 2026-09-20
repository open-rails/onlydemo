package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	"github.com/open-rails/authkit/authhttp"
)

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("load configuration: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), config.DatabaseURL)
	if err != nil {
		log.Fatalf("create database pool: %v", err)
	}
	defer pool.Close()

	pingContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	migrationContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := applyMigrations(migrationContext, pool); err != nil {
		log.Fatalf("apply database migrations: %v", err)
	}
	if config.MigrationsOnly {
		return
	}

	authService, err := newAuth(config, pool)
	if err != nil {
		log.Fatalf("initialize authkit: %v", err)
	}
	defer authService.Close()

	app, err := newApp(pool, authService)
	if err != nil {
		log.Fatalf("mount authkit: %v", err)
	}
	log.Printf("API listening on http://localhost:%d", config.Port)
	if err := app.Listen(fmt.Sprintf(":%d", config.Port)); err != nil {
		log.Printf("server stopped: %v", err)
	}
}

func newApp(pool *pgxpool.Pool, authService *authhttp.Service) (*fiber.App, error) {
	app := fiber.New()
	blogAPI := &blogAPI{pool: pool}

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

	// Register AuthKit endpoints on Fiber so they appear in its route table.
	if err := authkitfiber.Mount(app, authService); err != nil {
		return nil, err
	}
	return app, nil
}
