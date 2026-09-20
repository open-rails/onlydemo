package main

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/migratekit"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := migratekit.LoadFromFS(migrationFiles, "migrations/postgres")
	if err != nil {
		return err
	}

	migrator, err := migratekit.NewPostgresFromPGXPool(pool, "openrails-demo")
	if err != nil {
		return err
	}
	defer migrator.Close()

	return migrator.WithSchema("demo").ApplyMigrations(ctx, migrations)
}
