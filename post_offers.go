package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/riverqueue/river"
)

// Post rows commit with their catalog job; the request then tries the same
// idempotent sync inline and the job finishes it if that attempt fails.
const inlineCatalogTimeout = 5 * time.Second

func paidPolicy(policy string) bool { return policy == "ppv" || policy == "members_ppv" }

// Price nil retires the offer of a post that is no longer sold separately.
type postOfferArgs struct {
	PostID   int64       `json:"post_id"`
	Revision int64       `json:"revision"`
	Price    *offerPrice `json:"price,omitempty"`
}

func (postOfferArgs) Kind() string { return "demo_sync_post_offer" }

type postArchiveArgs struct {
	PostID int64 `json:"post_id"`
}

func (postArchiveArgs) Kind() string { return "demo_archive_post" }

// Abandoned composers (a closed tab) leave drafts; the sweep deletes them.
const draftLifetime = 24 * time.Hour

type postDraftSweepArgs struct{}

func (postDraftSweepArgs) Kind() string { return "demo_sweep_post_drafts" }

type postDraftSweepWorker struct {
	river.WorkerDefaults[postDraftSweepArgs]
	api *postAPI
}

func (w *postDraftSweepWorker) Work(ctx context.Context, _ *river.Job[postDraftSweepArgs]) error {
	return pgx.BeginFunc(ctx, w.api.pool, func(tx pgx.Tx) error {
		return w.api.deleteDraftsTx(ctx, tx, `id IN (SELECT id FROM `+w.api.table+` WHERE published_at IS NULL AND created_at < $1 LIMIT 500)`, time.Now().Add(-draftLifetime))
	})
}

type postOfferWorker struct {
	river.WorkerDefaults[postOfferArgs]
	api *postAPI
}

func (w *postOfferWorker) Work(ctx context.Context, job *river.Job[postOfferArgs]) error {
	err := w.api.locked(ctx, job.Args.PostID, func() error { return w.api.syncOffer(ctx, job.Args) })
	if err != nil && job.Attempt >= job.MaxAttempts {
		_, _ = w.api.pool.Exec(ctx, `UPDATE `+w.api.table+` SET offer_status='failed' WHERE id=$1 AND offer_revision=$2 AND offer_status='pending'`, job.Args.PostID, job.Args.Revision)
	}
	return err
}

type postArchiveWorker struct {
	river.WorkerDefaults[postArchiveArgs]
	api *postAPI
}

func (w *postArchiveWorker) Work(ctx context.Context, job *river.Job[postArchiveArgs]) error {
	return w.api.locked(ctx, job.Args.PostID, func() error { return w.api.archivePost(ctx, job.Args.PostID) })
}

func (api *postAPI) RiverJobs() riverkit.Contribution {
	return riverkit.NewContribution("demo-posts", func(_ context.Context, cfg *river.Config) error {
		if cfg.Workers == nil {
			cfg.Workers = river.NewWorkers()
		}
		river.AddWorker(cfg.Workers, &postOfferWorker{api: api})
		river.AddWorker(cfg.Workers, &postArchiveWorker{api: api})
		river.AddWorker(cfg.Workers, &postDraftSweepWorker{api: api})
		cfg.PeriodicJobs = append(cfg.PeriodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return postDraftSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
		return nil
	}, func(_ context.Context, binding riverkit.Binding) error { api.jobs = binding.Client; return nil }, func() error { api.jobs = nil; return nil })
}

// locked serializes catalog writes with post edits and channel cleanup.
func (api *postAPI) locked(ctx context.Context, id int64, fn func() error) error {
	var channel string
	err := api.pool.QueryRow(ctx, `SELECT channel_id::text FROM `+api.table+` WHERE id=$1`, id).Scan(&channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	release, err := api.channels.lock(ctx, channel)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// syncOffer requires the channel lock. Catalog.Apply is keyed by the post
// resource, so repeating it after an inline success changes nothing.
func (api *postAPI) syncOffer(ctx context.Context, args postOfferArgs) error {
	var channel, key, title, status string
	var revision int64
	var live bool
	err := api.pool.QueryRow(ctx, `SELECT p.channel_id::text,p.billing_key::text,p.title,p.offer_status,p.offer_revision,p.deleted_at IS NULL AND ch.deleted_at IS NULL FROM `+api.table+` p JOIN `+api.channels.table+` ch ON ch.id=p.channel_id WHERE p.id=$1`, args.PostID).Scan(&channel, &key, &title, &status, &revision, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Deletion archives the product; a newer revision has its own job.
	if !live || revision != args.Revision {
		return nil
	}
	// A post that became free keeps its buyers' access; nothing is refunded.
	if args.Price == nil {
		return api.billing.archiveResource(ctx, postResource(key))
	}
	if status == "active" {
		return nil
	}
	if err = api.billing.setOffer(ctx, channel, postResource(key), title, args.Price, false, false); err != nil {
		return err
	}
	_, err = api.pool.Exec(ctx, `UPDATE `+api.table+` SET offer_status='active' WHERE id=$1 AND offer_revision=$2`, args.PostID, args.Revision)
	return err
}

func (api *postAPI) archivePost(ctx context.Context, id int64) error {
	var key string
	var deletedAt *time.Time
	err := api.pool.QueryRow(ctx, `SELECT billing_key::text,deleted_at FROM `+api.table+` WHERE id=$1`, id).Scan(&key, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if deletedAt == nil {
		return errors.New("post deletion was not accepted")
	}
	return api.billing.archivePostProduct(ctx, postResource(key), *deletedAt)
}

// inline runs a committed job's work in the request, bounded; failure is left
// to the queued job.
func inline(ctx context.Context, name string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inlineCatalogTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		log.Printf("%s deferred to background job: %v", name, err)
	}
}
