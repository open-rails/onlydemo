package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/jackc/pgx/v5/pgxpool"
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

	authService, authMount, err := newAuth(config, pool)
	if err != nil {
		log.Fatalf("initialize authkit: %v", err)
	}
	defer authService.Close()

	app := fiber.New()
	blogAPI := &blogAPI{pool: pool, auth: authService}

	app.Get("/", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"message": "openrails demo api",
		})
	})

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

	app.Get("/api/posts", blogAPI.list)
	app.Post("/api/posts", blogAPI.create)
	app.Get("/api/posts/:id", blogAPI.get)
	app.Patch("/api/posts/:id", blogAPI.update)
	app.Delete("/api/posts/:id", blogAPI.delete)

	// AuthKit owns registration, password login, token refresh, logout, and
	// account-management routes under /api/v1. Its JWKS and browser OIDC
	// routes are mounted at their standard root paths as well.
	app.Use("/api/v1", adaptor.HTTPHandler(authMount))
	app.Use("/.well-known", adaptor.HTTPHandler(authMount))
	app.Use("/oidc", adaptor.HTTPHandler(authMount))

	log.Printf("API listening on http://localhost:%d", config.Port)
	if err := app.Listen(fmt.Sprintf(":%d", config.Port)); err != nil {
		log.Printf("server stopped: %v", err)
	}
}
