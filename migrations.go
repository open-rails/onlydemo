package main

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
	authkitmigrations "github.com/open-rails/authkit/migrations/postgres"
	"github.com/open-rails/migratekit"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	authMigrations, err := migratekit.LoadFromFS(authkitmigrations.FS)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS profiles`); err != nil {
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

	migrations, err := migratekit.LoadFromFS(migrationFiles, "migrations/postgres")
	if err != nil {
		return err
	}

	migrator, err := migratekit.NewPostgresFromPGXPool(pool, "openrails-demo")
	if err != nil {
		return err
	}
	defer migrator.Close()

	return migrator.ApplyMigrations(ctx, migrations)
}
