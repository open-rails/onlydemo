package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	riverkit "github.com/open-rails/helpers/river"
	openrailsembed "github.com/open-rails/openrails/embed"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// This app owns one River fleet shared by AuthKit and OpenRails. Every replica
// registers both libraries' workers and schedules before constructing River.
type appJobs struct {
	pool    *pgxpool.Pool
	schema  string
	auth    *appAuth
	billing *billingService
	client  *river.Client[pgx.Tx]
}

func newJobs(pool *pgxpool.Pool, cfg Config) *appJobs {
	schema := strings.TrimSpace(cfg.RiverSchema)
	if schema == "" {
		schema = "public"
	}
	return &appJobs{pool: pool, schema: schema}
}

func (j *appJobs) initialize(ctx context.Context) error {
	// River is host-owned here, so the host runs River's public initializer.
	// Use a separate connection for its advisory lock, including with a one-
	// connection application pool. Libraries use the same database/schema key.
	lock, err := pgx.ConnectConfig(ctx, j.pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lock.Close(cleanup)
	}()
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_lock(hashtext(current_database()), hashtext($1))", "river-migrations:"+j.schema); err != nil {
		return err
	}
	if _, err := j.pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{j.schema}.Sanitize()); err != nil {
		return err
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(j.pool), &rivermigrate.Config{Schema: j.schema})
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

func (j *appJobs) compose(ctx context.Context) error {
	if j.client != nil {
		return errors.New("application River client already constructed")
	}
	if j.auth == nil {
		return errors.New("AuthKit must be constructed before composing jobs")
	}
	contributions := []riverkit.Contribution{j.auth.runtime.RiverJobs()}
	queues := map[string]river.QueueConfig{}
	if j.billing != nil {
		contributions = append(contributions, j.billing.runtime.RiverJobs())
		queues[openrailsembed.QueueBilling] = river.QueueConfig{MaxWorkers: 4}
	}
	var err error
	j.client, err = riverkit.New(ctx, j.pool, &river.Config{Schema: j.schema, Queues: queues}, contributions...)
	return err
}

func (j *appJobs) start(ctx context.Context) error {
	if j.client == nil {
		if err := j.compose(ctx); err != nil {
			return err
		}
	}
	if err := j.auth.runtime.Start(ctx); err != nil {
		return err
	}
	return j.client.Start(ctx)
}

func (j *appJobs) close(ctx context.Context) error {
	if j.client == nil {
		return nil
	}
	if err := j.client.Stop(ctx); err != nil {
		// A graceful-stop deadline does not mean workers have stopped. Cancel
		// their contexts and join them before callers close service/DB resources.
		return fmt.Errorf("stop application jobs: %w", errors.Join(err, j.client.StopAndCancel(context.Background())))
	}
	return nil
}
