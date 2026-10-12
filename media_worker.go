package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit/media/worker"
)

// runMediaWorker runs ContentKit's media worker with the engine's registry
// until ctx ends: upload placement, image renditions and video encodes.
// Readiness and purges go back to the host fleet's River schema, where the
// engine's media jobs run. MEDIA_WORKER_* tune it (worker.Config.TuningFromEnv).
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
	kinds, err := workerRegistry(cfg)
	if err != nil {
		return nil, err
	}
	store, err := openMediaStore(ctx, cfg.Media)
	if err != nil {
		return nil, err
	}
	wc := worker.Config{Pool: pool, Store: store, Kinds: kinds, ContentSchema: contentSchema(cfg), HostSchema: riverSchema(cfg), Logger: slog.Default()}
	if err := tune(&wc); err != nil {
		return nil, err
	}
	wc.Schema = mediaWorkerSchema(cfg)
	return worker.New(ctx, wc)
}
