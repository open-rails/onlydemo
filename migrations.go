package main

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/migratekit"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

// initializeDatabase applies the app's and ContentKit's schemas and the host
// fleet's River tables. AuthKit and OpenRails migrate themselves in New.
func initializeDatabase(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	ctx, cancelMigration := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelMigration()
	if err := applyMigrations(ctx, pool, cfg); err != nil {
		return fmt.Errorf("application migrations: %w", err)
	}
	if err := applyContentMigrations(ctx, pool, cfg); err != nil {
		return fmt.Errorf("ContentKit migrations: %w", err)
	}
	if err := riverkit.ApplyMigrations(ctx, pool, riverSchema(cfg)); err != nil {
		return fmt.Errorf("host River migrations: %w", err)
	}
	return nil
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	migrations, err := migratekit.LoadFromFS(migrationFiles, "migrations/postgres")
	if err != nil {
		return err
	}

	migrator, err := migratekit.NewPostgresFromPGXPool(pool, "onlydemo")
	if err != nil {
		return err
	}
	defer migrator.Close()

	return migrator.WithSchema(appSchema(cfg)).ApplyMigrations(ctx, migrations)
}
