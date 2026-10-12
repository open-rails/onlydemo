package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	riverkit "github.com/open-rails/helpers/river"
)

// initializeDatabase applies the host fleet's River tables. The app keeps no
// tables: AuthKit, OpenRails and ContentKit migrate their own in New.
func initializeDatabase(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := riverkit.ApplyMigrations(ctx, pool, riverSchema(cfg)); err != nil {
		return fmt.Errorf("host River migrations: %w", err)
	}
	return nil
}
