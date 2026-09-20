package main

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	authkitmigrations "github.com/open-rails/authkit/migrations/postgres"
	"github.com/open-rails/migratekit"
	openrailsmigrations "github.com/open-rails/openrails/migrations/postgres"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	authMigrations, err := migratekit.LoadFromFS(authkitmigrations.FS)
	if err != nil {
		return err
	}
	authMigrator, err := migratekit.NewPostgresFromPGXPool(pool, "authkit")
	if err != nil {
		return err
	}
	defer authMigrator.Close()
	if err := authMigrator.WithSchema("profiles").ApplyMigrations(ctx, authMigrations); err != nil {
		return err
	}

	// OpenRails grants its runtime role access to existing AuthKit and River
	// tables, so those independently owned schemas must be migrated first.
	riverMigrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{})
	if err != nil {
		return fmt.Errorf("create River migrator: %w", err)
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate River: %w", err)
	}
	billingMigrations, err := migratekit.LoadFromFS(openrailsmigrations.FS)
	if err != nil {
		return err
	}
	billingMigrator, err := migratekit.NewPostgresFromPGXPool(pool, "openrails")
	if err != nil {
		return err
	}
	defer billingMigrator.Close()
	if err := billingMigrator.WithSchema("openrails").ApplyMigrations(ctx, billingMigrations); err != nil {
		return fmt.Errorf("migrate OpenRails: %w", err)
	}

	migrations, err := migratekit.LoadFromFS(migrationFiles, "migrations/postgres")
	if err != nil {
		return err
	}

	migrator, err := migratekit.NewPostgresFromPGXPool(pool, "openrails-demo")
	if err != nil {
		return err
	}
	defer migrator.Close()

	if err := migrator.ApplyMigrations(ctx, migrations); err != nil {
		return err
	}
	// OpenRails' prospective River grants cover new public tables. The billing
	// role needs no access to this application's private content.
	_, err = pool.Exec(ctx, `REVOKE ALL ON TABLE public.blog_posts FROM openrails_app;
		REVOKE ALL ON SEQUENCE public.blog_posts_id_seq FROM openrails_app`)
	return err
}
