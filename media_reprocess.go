package main

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
)

// reprocessMedia re-derives every item's media under the current policy
// (slot widths, cover widths, variant specs): one ProcessJob per post and per
// channel or user with a slot. The app's job workers re-encode what changed
// from the kept originals and drop retired widths.
func reprocessMedia(ctx context.Context, out io.Writer) error {
	cfg, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	q, err := media.NewProcessInserter(pool, cfg.RiverSchema, "")
	if err != nil {
		return err
	}
	schema := appSchema(cfg)
	rows, err := pool.Query(ctx, `SELECT 'post', id::text FROM `+pgx.Identifier{schema, "posts"}.Sanitize()+` WHERE deleted_at IS NULL
		UNION SELECT DISTINCT kind, item_id FROM `+pgx.Identifier{schema, "media_slots"}.Sanitize())
	if err != nil {
		return err
	}
	var kind, id string
	n := 0
	if _, err = pgx.ForEachRow(rows, []any{&kind, &id}, func() error {
		n++
		return q.Enqueue(ctx, media.ProcessJob{Ref: contentref.New(cfg.Media.Tenant, kind, id)})
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "enqueued %d media items for reprocessing\n", n)
	return nil
}
