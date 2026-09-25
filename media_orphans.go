package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
)

// sweepOrphanMedia reports post and channel folders whose row is gone (or
// whose id is not a content id), untouched for grace; del removes them.
func sweepOrphanMedia(ctx context.Context, out io.Writer, grace time.Duration, del bool) error {
	cfg, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	kinds, err := media.NewRegistry(mediaKinds...)
	if err != nil {
		return err
	}
	store, err := mediaS3.New(mediaStoreConfig(cfg.Media))
	if err != nil {
		return err
	}
	jobs, err := media.NewJobs(media.JobsConfig{Store: store, Kinds: kinds, Tenants: []string{cfg.Media.Tenant}})
	if err != nil {
		return err
	}
	for _, k := range []struct{ kind, table string }{{kindPost, "posts"}, {kindChannel, "channels"}} {
		table := pgx.Identifier{appSchema(cfg), k.table}.Sanitize()
		exists := func(ctx context.Context, ids []string) (map[string]bool, error) {
			rows, err := pool.Query(ctx, `SELECT id::text FROM `+table+` WHERE id=ANY($1::uuid[])`, ids)
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
		rep, err := jobs.SweepOrphans(ctx, media.OrphanSweep{Tenant: cfg.Media.Tenant, Kind: k.kind, Exists: exists, Grace: grace, Delete: del})
		if err != nil {
			return err
		}
		for _, o := range rep.Orphans {
			fmt.Fprintf(out, "%s %d objects, newest %s, deleted=%t\n", o.Prefix, o.Objects, o.Newest.Format(time.RFC3339), o.Deleted)
		}
		fmt.Fprintf(out, "%s: %d folders, %d orphans\n", k.kind, rep.Folders, len(rep.Orphans))
	}
	return nil
}
