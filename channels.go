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
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	riverkit "github.com/open-rails/helpers/river"
	"github.com/riverqueue/river"
)

type channelAPI struct {
	pool         *pgxpool.Pool
	auth         *appAuth
	billing      postBilling
	table, posts string
	jobs         *river.Client[pgx.Tx]
	locks        chan struct{}
}

type channel struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func newChannels(pool *pgxpool.Pool, auth *appAuth, billing postBilling, cfg Config) *channelAPI {
	return &channelAPI{pool: pool, auth: auth, billing: billing, table: pgx.Identifier{appSchema(cfg), "channels"}.Sanitize(), posts: pgx.Identifier{appSchema(cfg), "blog_posts"}.Sanitize(), locks: make(chan struct{}, 4)}
}

func (api *channelAPI) mount(app fiber.Router, required fiber.Handler) {
	app.Post("/api/channels", required, api.create)
	app.Get("/api/channels/:id", required, api.get)
	app.Delete("/api/channels/:id", required, api.delete)
}

func (api *channelAPI) create(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
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
	ref := authkit.GroupRef{Persona: channelPersona, Instance: input.Slug}
	group, err := api.auth.client.GroupInstanceForSlug(c.Context(), ref)
	if errors.Is(err, authkit.ErrGroupNotFound) {
		id, createErr := api.auth.client.CreatePermissionGroup(c.Context(), authkit.CreatePermissionGroupRequest{Persona: channelPersona, InstanceSlug: input.Slug, DisplayName: input.Name, OwnerSubjectID: user.UserID})
		if createErr != nil {
			return clientError(c, http.StatusConflict, "channel slug is unavailable")
		}
		group = authkit.GroupInstance{ID: id, Persona: channelPersona, InstanceSlug: input.Slug, DisplayName: input.Name}
	} else if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	} else {
		// Retry can finish a group created before an application DB failure,
		// but never adopt another publisher's existing group.
		allowed, err := api.auth.client.CanOnGroup(c.Context(), authkit.UserSubject(user.UserID), group.ID, "channel:settings:manage")
		if err != nil {
			return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
		}
		if !allowed {
			return clientError(c, http.StatusConflict, "channel slug is unavailable")
		}
	}
	release, err := api.lock(c.Context(), group.ID)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	// A concurrent accepted deletion may have removed the group while this
	// request waited. Never recreate an application row for a deleted group.
	group, err = api.auth.client.GroupInstanceByID(c.Context(), group.ID)
	if err != nil {
		return clientError(c, http.StatusConflict, "channel is no longer available")
	}
	_, err = api.pool.Exec(c.Context(), `INSERT INTO `+api.table+`(id,created_by) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, group.ID, user.UserID)
	if err != nil {
		return databaseError(c, err)
	}
	if active, err := api.active(c.Context(), group.ID); err != nil {
		return databaseError(c, err)
	} else if !active {
		return clientError(c, http.StatusConflict, "channel is being deleted")
	}
	return c.Status(http.StatusCreated).JSON(channel{ID: group.ID, Slug: group.InstanceSlug, Name: group.DisplayName})
}

func (api *channelAPI) get(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid channel id")
	}
	allowed, err := api.allowed(c.Context(), user.UserID, id, channelReadPermission)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	if !allowed {
		return clientError(c, http.StatusNotFound, "channel not found")
	}
	group, err := api.auth.client.GroupInstanceByID(c.Context(), id)
	if err != nil {
		return clientError(c, http.StatusNotFound, "channel not found")
	}
	return c.JSON(channel{ID: group.ID, Slug: group.InstanceSlug, Name: group.DisplayName})
}

func (api *channelAPI) active(ctx context.Context, id string) (bool, error) {
	var active bool
	err := api.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+api.table+` WHERE id=$1 AND deleting_at IS NULL)`, id).Scan(&active)
	return active, err
}

func (api *channelAPI) allowed(ctx context.Context, userID, channelID string, permission authkit.Perm) (bool, error) {
	if userID == "" {
		return false, nil
	}
	active, err := api.active(ctx, channelID)
	if err != nil || !active {
		return false, err
	}
	return api.auth.client.CanOnGroup(ctx, authkit.UserSubject(userID), channelID, permission)
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
		if cfg.Queues == nil {
			cfg.Queues = map[string]river.QueueConfig{}
		}
		cfg.Queues["default"] = river.QueueConfig{MaxWorkers: 2}
		return nil
	}, func(_ context.Context, binding riverkit.Binding) error { api.jobs = binding.Client; return nil }, func() error { api.jobs = nil; return nil })
}

func (api *channelAPI) delete(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
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
	allowed, err := api.auth.client.CanOnGroup(c.Context(), authkit.UserSubject(user.UserID), id, "channel:settings:manage")
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
	result, err := tx.Exec(c.Context(), `UPDATE `+api.table+` SET deleting_at=COALESCE(deleting_at,NOW()) WHERE id=$1`, id)
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
	return c.Status(http.StatusAccepted).JSON(fiber.Map{"id": id, "status": "deleting"})
}

func (api *channelAPI) finishDeletion(ctx context.Context, id string) error {
	release, err := api.lock(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	var deleting bool
	err = api.pool.QueryRow(ctx, `SELECT deleting_at IS NOT NULL FROM `+api.table+` WHERE id=$1`, id).Scan(&deleting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !deleting {
		return errors.New("channel deletion was not accepted")
	}
	if api.billing != nil {
		if err = api.billing.ArchiveChannelCatalog(ctx, id); err != nil {
			return fmt.Errorf("archive channel catalog: %w", err)
		}
	}
	if _, err = api.pool.Exec(ctx, `DELETE FROM `+api.posts+` WHERE channel_id=$1`, id); err != nil {
		return err
	}
	if err = api.auth.client.DeleteGroupInstanceByID(ctx, id, authkit.DeletePermissionGroupOptions{}); err != nil && !errors.Is(err, authkit.ErrGroupNotFound) {
		return err
	}
	_, err = api.pool.Exec(ctx, `DELETE FROM `+api.table+` WHERE id=$1 AND deleting_at IS NOT NULL`, id)
	return err
}
