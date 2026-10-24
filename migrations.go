package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	riverkit "github.com/open-rails/helpers/river"
)

// initializeDatabase applies the host fleet and its destination-owned account
// event cursor. AuthKit, OpenRails and ContentKit migrate their own in New.
func initializeDatabase(ctx context.Context, cfg Config, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := riverkit.ApplyMigrations(ctx, pool, riverSchema(cfg)); err != nil {
		return fmt.Errorf("host River migrations: %w", err)
	}
	jobs := pgx.Identifier{riverSchema(cfg), "river_job"}.Sanitize()
	var running bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+jobs+" WHERE kind='onlydemo_notification_poll' AND state='running')").Scan(&running); err != nil {
		return err
	}
	if running {
		return fmt.Errorf("stop the old notification worker before upgrading")
	}
	if _, err := pool.Exec(ctx, "UPDATE "+jobs+" SET state='cancelled',finalized_at=now() WHERE kind='onlydemo_notification_poll' AND state IN ('available','pending','retryable','scheduled')"); err != nil {
		return err
	}
	table := pgx.Identifier{riverSchema(cfg), "onlydemo_auth_cursor"}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (name text PRIMARY KEY, cursor text NOT NULL DEFAULT '')"); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, "INSERT INTO "+table+" (name) VALUES ('account-deletion') ON CONFLICT DO NOTHING")
	return err
}
