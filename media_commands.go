package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/contentkit/media/workqueue"
)

// withMediaJobs runs fn with media's maintenance jobs on the app database.
func withMediaJobs(ctx context.Context, fn func(Config, *pgxpool.Pool, *media.Jobs) error) error {
	cfg, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	kinds, err := newRegistry(cfg.Media, media.Hooks{})
	if err != nil {
		return err
	}
	store, err := openMediaStore(ctx, cfg.Media)
	if err != nil {
		return err
	}
	queue, err := workqueue.New(pool, kinds, mediaWorkerSchema)
	if err != nil {
		return err
	}
	journal, err := media.NewPGJournal(pool, contentSchema(cfg), queue)
	if err != nil {
		return err
	}
	jobs, err := media.NewJobs(media.JobsConfig{Store: store, Registry: kinds, Locker: media.PGLocker(pool), Journal: journal, Processes: queue})
	if err != nil {
		return err
	}
	return fn(cfg, pool, jobs)
}

// reprocessMedia asks the media worker to redo every item's stale outputs
// after a preset changed.
func reprocessMedia(ctx context.Context, out io.Writer) error {
	return withMediaJobs(ctx, func(_ Config, _ *pgxpool.Pool, jobs *media.Jobs) error {
		for _, kind := range []string{kindPost, kindChannel, kindUser} {
			n, err := jobs.Regenerate(ctx, kind, "")
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %d items queued for reprocessing\n", kind, n)
		}
		return nil
	})
}

// sweepOrphanMedia reports post and channel folders whose row is gone (or
// whose id is not a content id), untouched for grace; del removes them.
func sweepOrphanMedia(ctx context.Context, out io.Writer, grace time.Duration, del bool) error {
	return withMediaJobs(ctx, func(cfg Config, pool *pgxpool.Pool, jobs *media.Jobs) error {
		for _, k := range []struct{ kind, table string }{{kindPost, "posts"}, {kindChannel, "channels"}} {
			table := pgx.Identifier{appSchema(cfg), k.table}.Sanitize()
			exists := func(ctx context.Context, ids []string) (map[string]bool, error) {
				rows, err := pool.Query(ctx, `SELECT id::text FROM `+table+` WHERE id::text=ANY($1)`, ids)
				if err != nil {
					return nil, err
				}
				found, err := pgx.CollectRows(rows, pgx.RowTo[string])
				have := make(map[string]bool, len(found))
				for _, id := range found {
					have[id] = true
				}
				return have, err
			}
			rep, err := jobs.SweepOrphans(ctx, media.OrphanSweep{Kind: k.kind, Exists: exists, Grace: grace, Delete: del})
			if err != nil {
				return err
			}
			for _, o := range rep.Orphans {
				fmt.Fprintf(out, "%s %d objects, newest %s, deleted=%t\n", o.Prefix, o.Objects, o.Newest.Format(time.RFC3339), o.Deleted)
			}
			fmt.Fprintf(out, "%s: %d folders, %d orphans\n", k.kind, rep.Folders, len(rep.Orphans))
		}
		return nil
	})
}
