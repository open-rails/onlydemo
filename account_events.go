package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/iam"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type accountEventArgs struct{}

func (accountEventArgs) Kind() string { return "onlydemo_account_events" }
func (accountEventArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "onlydemo_account_events", MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}
}

type accountEventWorker struct {
	river.WorkerDefaults[accountEventArgs]
	auth   *appAuth
	pool   *pgxpool.Pool
	schema string
}

func (w *accountEventWorker) Work(ctx context.Context, _ *river.Job[accountEventArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	table := pgx.Identifier{w.schema, "onlydemo_auth_cursor"}.Sanitize()
	var cursor string
	if err = tx.QueryRow(ctx, "SELECT cursor FROM "+table+" WHERE name='account-deletion' FOR UPDATE").Scan(&cursor); err != nil {
		return err
	}
	page, err := w.auth.client.ListEvents(ctx, iam.EventQuery{After: cursor, Limit: 100})
	if err != nil {
		return fmt.Errorf("account deletion event reader (expired cursors require a state resync): %w", err)
	}
	for _, event := range page.Events {
		if err = w.auth.onEvent(ctx, event); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE "+table+" SET cursor=$1 WHERE name='account-deletion'", page.NextCursor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func accountEvents(auth *appAuth, pool *pgxpool.Pool, cfg Config) riverkit.Contribution {
	return riverkit.NewContribution("onlydemo-account-events", func(_ context.Context, c *river.Config) error {
		if c.Workers == nil {
			c.Workers = river.NewWorkers()
		}
		if c.Queues == nil {
			c.Queues = map[string]river.QueueConfig{}
		}
		c.Queues["onlydemo_account_events"] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(c.Workers, &accountEventWorker{auth: auth, pool: pool, schema: riverSchema(cfg)})
		c.PeriodicJobs = append(c.PeriodicJobs, riverkit.NewPeriodicJob(c, 10*time.Second, func() (river.JobArgs, *river.InsertOpts) { return accountEventArgs{}, nil }, &river.PeriodicJobOpts{ID: "onlydemo-account-events", RunOnStart: true}))
		return nil
	}, nil, nil)
}
