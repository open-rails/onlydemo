package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	"github.com/open-rails/authkit/iam"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/riverqueue/river"
)

type channelAPI struct {
	pool         *pgxpool.Pool
	auth         *appAuth
	billing      *billingService
	table, posts string
	jobs         *river.Client[pgx.Tx]
	media        *mediaService
	locks        chan struct{}
}

type channel struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func newChannels(pool *pgxpool.Pool, auth *appAuth, billing *billingService, cfg Config) *channelAPI {
	return &channelAPI{pool: pool, auth: auth, billing: billing, table: pgx.Identifier{appSchema(cfg), "channels"}.Sanitize(), posts: pgx.Identifier{appSchema(cfg), "posts"}.Sanitize(), locks: make(chan struct{}, 4)}
}

func (api *channelAPI) mount(app fiber.Router, required fiber.Handler) {
	app.Post("/api/v1/channels", required, api.create)
	app.Delete("/api/v1/channels/:id", required, api.delete)
}

// create makes a channel: its row and its AuthKit group, owned by the viewer,
// commit together or not at all.
func (api *channelAPI) create(c fiber.Ctx) error {
	user := viewer(c)
	if user == "" {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	var input struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := c.Bind().Body(&input); err != nil {
		return clientError(c, http.StatusBadRequest, "invalid JSON body")
	}
	input.Slug, input.Name = strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.Name)
	if input.Slug == "" || input.Name == "" {
		return clientError(c, http.StatusBadRequest, "slug and name are required")
	}
	if input.Slug == "new" || !channelSlug.MatchString(input.Slug) {
		return clientError(c, http.StatusBadRequest, "channel slug must be lowercase letters, digits, - and _ (and not reserved)")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return databaseError(c, err)
	}
	owner := iam.UserSubject(user)
	err = pgx.BeginFunc(c.Context(), api.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(c.Context(), `INSERT INTO `+api.table+`(id,slug,name,created_by) VALUES($1,$2,$3,$4) ON CONFLICT (slug) DO NOTHING`, id, input.Slug, input.Name, user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errSlugTaken
		}
		_, err = api.auth.client.CreateGroup(c.Context(), iam.NewGroup{ID: id.String(), Persona: channelPersona.Persona, Owner: &owner}, authkit.InTx(tx))
		return err
	})
	if errors.Is(err, errSlugTaken) {
		return clientError(c, http.StatusConflict, "That channel slug is already taken. Choose another.")
	}
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	return c.Status(http.StatusCreated).JSON(channel{ID: id.String(), Slug: input.Slug, Name: input.Name})
}

var errSlugTaken = errors.New("channel slug taken")

func (api *channelAPI) active(ctx context.Context, id string) (bool, error) {
	var active bool
	err := api.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&active)
	return active, err
}

func (api *channelAPI) allowed(ctx context.Context, userID, channelID string, permission iam.Perm) (bool, error) {
	if userID == "" {
		return false, nil
	}
	active, err := api.active(ctx, channelID)
	if err != nil || !active {
		return false, err
	}
	return api.auth.can(ctx, userID, iam.GroupByID(channelID), permission)
}

// name is a channel's display name.
func (api *channelAPI) name(ctx context.Context, id string) (string, error) {
	var name string
	err := api.pool.QueryRow(ctx, `SELECT name FROM `+api.table+` WHERE id=$1`, id).Scan(&name)
	return name, err
}

// A separate session keeps the one-connection application pool available while
// coordinating catalog writes with durable channel cleanup across replicas.
func (api *channelAPI) lock(ctx context.Context, id string) (func(), error) {
	acquire, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case api.locks <- struct{}{}:
	case <-acquire.Done():
		return nil, acquire.Err()
	}
	conn, err := pgx.ConnectConfig(acquire, api.pool.Config().ConnConfig.Copy())
	if err != nil {
		<-api.locks
		return nil, err
	}
	closeConn := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
		<-api.locks
	}
	if _, err = conn.Exec(acquire, `SELECT pg_advisory_lock(hashtext(current_database()),hashtext($1))`, "demo-channel:"+id); err != nil {
		closeConn()
		return nil, err
	}
	return closeConn, nil
}

func channelID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return "", errors.New("invalid channel id")
	}
	return raw, nil
}

type channelDeleteArgs struct {
	ChannelID string `json:"channel_id"`
}

func (channelDeleteArgs) Kind() string { return "demo_delete_channel" }

type channelDeleteWorker struct {
	river.WorkerDefaults[channelDeleteArgs]
	api *channelAPI
}

func (w *channelDeleteWorker) Work(ctx context.Context, job *river.Job[channelDeleteArgs]) error {
	return w.api.finishDeletion(ctx, job.Args.ChannelID)
}

func (api *channelAPI) RiverJobs() riverkit.Contribution {
	return riverkit.NewContribution("demo-channels", func(_ context.Context, cfg *river.Config) error {
		if cfg.Workers == nil {
			cfg.Workers = river.NewWorkers()
		}
		river.AddWorker(cfg.Workers, &channelDeleteWorker{api: api})
		river.AddWorker(cfg.Workers, &membershipSyncWorker{api: api})
		if cfg.Queues == nil {
			cfg.Queues = map[string]river.QueueConfig{}
		}
		cfg.Queues["default"] = river.QueueConfig{MaxWorkers: 2}
		return nil
	}, func(_ context.Context, binding riverkit.Binding) error { api.jobs = binding.Client; return nil }, func() error { api.jobs = nil; return nil })
}

func (api *channelAPI) delete(c fiber.Ctx) error {
	user := viewer(c)
	if user == "" {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid channel id")
	}
	release, err := api.lock(c.Context(), id)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.allowed(c.Context(), user, id, channelSettingsManage)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	if !allowed {
		return clientError(c, http.StatusNotFound, "channel not found")
	}
	if api.jobs == nil {
		return clientError(c, http.StatusServiceUnavailable, "background workers are unavailable")
	}
	tx, err := api.pool.Begin(c.Context())
	if err != nil {
		return databaseError(c, err)
	}
	defer tx.Rollback(c.Context())
	result, err := tx.Exec(c.Context(), `UPDATE `+api.table+` SET deleted_at=COALESCE(deleted_at,NOW()) WHERE id=$1`, id)
	if err != nil {
		return databaseError(c, err)
	}
	if result.RowsAffected() == 0 {
		return clientError(c, http.StatusNotFound, "channel not found")
	}
	if _, err = api.jobs.InsertTx(c.Context(), tx, channelDeleteArgs{ChannelID: id}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}); err != nil {
		return databaseError(c, err)
	}
	if err = tx.Commit(c.Context()); err != nil {
		return databaseError(c, err)
	}
	// Hide the channel durably before retiring its authority. Success means
	// its last owner can immediately delete their account; a failure is retried
	// by the already committed job without reopening visibility.
	if err = api.retireGroup(c.Context(), id); err != nil {
		return clientError(c, http.StatusServiceUnavailable, "channel retirement is pending")
	}
	return c.Status(http.StatusAccepted).JSON(fiber.Map{"id": id, "status": "deleted"})
}

// retireGroup soft-deletes the channel's group: its roles grant nothing and
// its last owner can delete their account. The row's deleted_at starts the
// one retention clock.
func (api *channelAPI) retireGroup(ctx context.Context, id string) error {
	err := api.auth.client.DeleteGroup(ctx, iam.GroupByID(id))
	if errors.Is(err, iam.ErrGroupNotFound) {
		return nil // A prior due purge may have removed the group before a retry.
	}
	return err
}

func (api *channelAPI) finishDeletion(ctx context.Context, id string) error {
	release, err := api.lock(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	var deleted bool
	err = api.pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM `+api.table+` WHERE id=$1`, id).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !deleted {
		return errors.New("channel deletion was not accepted")
	}
	if err = api.retireGroup(ctx, id); err != nil {
		return err
	}
	rows, err := api.pool.Query(ctx, `SELECT id::text FROM `+api.posts+` WHERE channel_id=$1`, id)
	if err != nil {
		return err
	}
	posts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if err = api.billing.archiveChannel(ctx, id, posts); err != nil {
		return fmt.Errorf("archive channel products: %w", err)
	}
	var seconds float64
	if err = api.pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM deleted_at + INTERVAL '30 days' - clock_timestamp())::double precision FROM `+api.table+` WHERE id=$1`, id).Scan(&seconds); err != nil {
		return err
	}
	if seconds > 0 {
		return river.JobSnooze(max(time.Millisecond, time.Duration(seconds*float64(time.Second))))
	}
	// Post and channel folders are erased with the rows; soft-deleted posts
	// were erased when they were deleted, and a repeated erasure is harmless.
	err = pgx.BeginFunc(ctx, api.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM `+api.posts+` WHERE channel_id=$1 RETURNING id::text`, id)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if err = api.media.deletePostsTx(ctx, tx, id, ids...); err != nil {
			return err
		}
		return api.media.deleteChannelTx(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	if err = api.auth.client.PurgeGroup(ctx, iam.GroupByID(id)); err != nil && !errors.Is(err, iam.ErrGroupNotFound) {
		return err
	}
	_, err = api.pool.Exec(ctx, `DELETE FROM `+api.table+` WHERE id=$1 AND deleted_at <= clock_timestamp()-INTERVAL '30 days'`, id)
	return err
}
