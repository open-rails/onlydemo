package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/contentkit/media/worker"
)

// runMediaWorker runs ContentKit's media worker with this app's kinds, spec
// choice and hooks until ctx ends: image renditions, slots, video encodes and
// placement of multipart uploads. MEDIA_WORKER_* and MEDIA_HOST_* tune it
// (worker.Config.TuningFromEnv).
func runMediaWorker(ctx context.Context) error {
	cfg, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	w, err := newMediaWorker(ctx, cfg, pool, func(wc *worker.Config) error { return wc.TuningFromEnv() })
	if err != nil {
		return err
	}
	return w.Run(ctx)
}

func newMediaWorker(ctx context.Context, cfg Config, pool *pgxpool.Pool, tune func(*worker.Config) error) (*worker.Worker, error) {
	kinds, err := media.NewRegistry(mediaKinds...)
	if err != nil {
		return nil, err
	}
	s3cfg := mediaStoreConfig(cfg.Media)
	probe, err := mediaS3.New(s3cfg)
	if err != nil {
		return nil, err
	}
	if s3cfg.Capabilities, err = media.Probe(ctx, probe, "_probe/"); err != nil {
		return nil, fmt.Errorf("probe media bucket %s: %w", cfg.Media.S3Bucket, err)
	}
	store, err := mediaS3.New(s3cfg)
	if err != nil {
		return nil, err
	}
	wc := worker.Config{Pool: pool, Store: store, Kinds: kinds, Specs: mediaSpecs, HostSchema: cfg.RiverSchema, Logger: slog.Default(),
		Hooks: media.Hooks{Failed: mediaFailed, SlotEncoded: slotEncoded(pool, pgx.Identifier{appSchema(cfg), "media_slots"}.Sanitize()), PublicRemoved: purgeCDN}}
	if err := tune(&wc); err != nil {
		return nil, err
	}
	return worker.New(ctx, wc)
}
