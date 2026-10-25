package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/open-rails/openrails"
	"github.com/riverqueue/river"
)

// Compose all contributors before constructing the one host-owned client.
func newJobs(ctx context.Context, pool *pgxpool.Pool, cfg Config, auth *appAuth, billing *billingService, content *contentkit.Engine) (*river.Client[pgx.Tx], error) {
	contributions := []riverkit.Contribution{
		auth.client.RiverJobs(),
		billing.RiverJobs(),
		content.RiverJobs(),
		accountEvents(auth, pool, cfg),
	}
	queues := map[string]river.QueueConfig{openrails.QueueBilling: {MaxWorkers: 4}}
	return riverkit.New(ctx, pool, &river.Config{Schema: riverSchema(cfg), Queues: queues}, contributions...)
}

func stopJobs(ctx context.Context, client *river.Client[pgx.Tx]) error {
	if err := client.Stop(ctx); err != nil {
		// A graceful-stop deadline does not mean workers have stopped. Cancel
		// and join them before closing service and database resources.
		return fmt.Errorf("stop application jobs: %w", errors.Join(err, client.StopAndCancel(context.Background())))
	}
	return nil
}
