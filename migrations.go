package main

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/embedded"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/migratekit"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

// initializeDatabase uses the same owning pool as the API and shared workers.
// Each library initializes its own private storage through its public API.
func initializeDatabase(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	ctx, cancelMigration := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelMigration()
	if err := applyMigrations(ctx, pool, cfg); err != nil {
		return fmt.Errorf("application migrations: %w", err)
	}
	if err := riverkit.ApplyMigrations(ctx, pool, cfg.RiverSchema); err != nil {
		return fmt.Errorf("host River migrations: %w", err)
	}
	if err := embedded.ApplyMigrations(ctx, pool, cfg.AuthSchema, embedded.MigrationOptions{River: embedded.RiverFromHost()}); err != nil {
		return fmt.Errorf("AuthKit migrations: %w", err)
	}
	return initializeBilling(ctx, cfg, pool)
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	migrations, err := migratekit.LoadFromFS(migrationFiles, "migrations/postgres")
	if err != nil {
		return err
	}

	migrator, err := migratekit.NewPostgresFromPGXPool(pool, "openrails-demo")
	if err != nil {
		return err
	}
	defer migrator.Close()

	return migrator.WithSchema(appSchema(cfg)).ApplyMigrations(ctx, migrations)
}
