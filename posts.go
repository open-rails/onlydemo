package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	"github.com/open-rails/contentkit/media"
	"github.com/open-rails/contentkit/media/tiered"
	"github.com/open-rails/openrails"
	"github.com/riverqueue/river"
)

type postAPI struct {
	pool     *pgxpool.Pool
	auth     *appAuth
	billing  *billingService
	table    string
	channels *channelAPI
	jobs     *river.Client[pgx.Tx]
	media    *mediaService
}

func newPosts(channels *channelAPI, cfg Config) *postAPI {
	return &postAPI{pool: channels.pool, auth: channels.auth, billing: channels.billing, channels: channels, table: pgx.Identifier{appSchema(cfg), "posts"}.Sanitize()}
}

type post struct {
	ID                 int64                    `json:"id"`
	AuthorID           string                   `json:"author_id"`
	ChannelID          string                   `json:"channel_id"`
	ChannelSlug        string                   `json:"channel_slug"`
	ChannelName        string                   `json:"channel_name"`
	Slug               string                   `json:"slug"`
	Title              string                   `json:"title"`
	Body               string                   `json:"body,omitempty"`
	AccessPolicy       string                   `json:"access_policy"`
	OfferStatus        string                   `json:"offer_status"`
	OfferRevision      int64                    `json:"-"`
	CanRead            bool                     `json:"can_read"`
	CanEdit            bool                     `json:"can_edit"`
	Purchased          bool                     `json:"purchased"`
	SubscriptionActive bool                     `json:"has_membership"`
	ChannelAvatar      *media.SlotManifest      `json:"channel_avatar,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	BillingKey         string                   `json:"-"`
	Offers             []openrails.CatalogOffer `json:"offers"`
}
type postInput struct {
	ChannelID    *string     `json:"channel_id"`
	Slug         *string     `json:"slug"`
	Title        *string     `json:"title"`
	Body         *string     `json:"body"`
	AccessPolicy *string     `json:"access_policy"`
	Price        *offerPrice `json:"price"`
}

const postColumns = `id,author_id::text,channel_id::text,billing_key::text,slug,title,body,access_policy,offer_status,offer_revision,created_at,updated_at`

func scanPost(row interface{ Scan(...any) error }) (post, error) {
	var p post
	err := row.Scan(&p.ID, &p.AuthorID, &p.ChannelID, &p.BillingKey, &p.Slug, &p.Title, &p.Body, &p.AccessPolicy, &p.OfferStatus, &p.OfferRevision, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}
func postID(c fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid post id")
	}
	return id, nil
}
func normalSlug(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func viewer(c fiber.Ctx) string {
	if cl, ok := authkitfiber.UserClaims(c); ok {
		return cl.UserID
	}
	return ""
}
func (api *postAPI) decorate(c fiber.Ctx, posts []post, withOffers bool) error {
	user := viewer(c)
	keys := make([]string, 0, len(posts)*2)
	seen := map[string]bool{}
	for _, p := range posts {
		for _, key := range []string{postResource(p.BillingKey), membershipResource(p.ChannelID)} {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	access, err := api.billing.access(c.Context(), user, keys)
	if err != nil {
		return err
	}
	admin, err := api.canModerate(c, user, postReadPermission)
	if err != nil {
		return err
	}
	editAdmin, err := api.canModerate(c, user, postEditPermission)
	if err != nil {
		return err
	}
	policies := make([]tiered.Policy, len(posts))
	for i, p := range posts {
		policies[i] = postPolicy(p)
	}
	held := tiered.CheckerFunc(func(context.Context, string, []string) (map[string]bool, error) { return access, nil })
	readable, err := tiered.DecideAll(c.Context(), held, actorFor(user), policies)
	if err != nil {
		return err
	}
	channelIDs := make([]string, 0, len(posts))
	for _, p := range posts {
		if !slices.Contains(channelIDs, p.ChannelID) {
			channelIDs = append(channelIDs, p.ChannelID)
		}
	}
	avatars, err := api.media.slots(c.Context(), kindChannel, channelIDs, slotAvatar)
	if err != nil {
		return err
	}
	publishing := map[string]bool{}
	editing := map[string]bool{}
	groups := map[string]authkit.GroupInstance{}
	for i := range posts {
		p := &posts[i]
		if _, ok := publishing[p.ChannelID]; !ok {
			if groups[p.ChannelID], err = api.auth.client.GroupInstanceByID(c.Context(), p.ChannelID); err != nil && !errors.Is(err, authkit.ErrGroupNotFound) {
				return err
			}
			publishing[p.ChannelID], err = api.channels.allowed(c.Context(), user, p.ChannelID, channelReadPermission)
			if err != nil {
				return err
			}
			editing[p.ChannelID], err = api.channels.allowed(c.Context(), user, p.ChannelID, channelEditPermission)
			if err != nil {
				return err
			}
		}
		p.ChannelSlug, p.ChannelName = groups[p.ChannelID].InstanceSlug, groups[p.ChannelID].DisplayName
		p.Purchased = access[postResource(p.BillingKey)]
		p.SubscriptionActive = access[membershipResource(p.ChannelID)]
		p.CanEdit = editing[p.ChannelID] || editAdmin
		p.CanRead = readable[i] || publishing[p.ChannelID] || admin
		p.ChannelAvatar = avatars[slotKey{p.ChannelID, slotAvatar}]
		if !p.CanRead {
			p.Body = ""
		}
		p.Offers = []openrails.CatalogOffer{}
		if withOffers && paidPolicy(p.AccessPolicy) && p.OfferStatus == "active" {
			p.Offers, err = api.billing.offers(c.Context(), postResource(p.BillingKey), false)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (api *postAPI) list(c fiber.Ctx) error {
	limit := 25
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			return clientError(c, 400, "limit must be between 1 and 50")
		}
		limit = n
	}
	before := int64(0)
	if raw := c.Query("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return clientError(c, 400, "invalid cursor")
		}
		before = n
	}
	channel := c.Query("channel_id")
	if channel != "" {
		if _, err := channelID(channel); err != nil {
			return clientError(c, 400, "invalid channel_id")
		}
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE deleted_at IS NULL AND ($1::text='' OR channel_id::text=$1) AND ($2::bigint=0 OR id<$2) AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL) ORDER BY id DESC LIMIT $3`, channel, before, limit+1)
	if err != nil {
		return databaseError(c, err)
	}
	posts := []post{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return databaseError(c, e)
		}
		posts = append(posts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return databaseError(c, err)
	}
	more := len(posts) > limit
	if more {
		posts = posts[:limit]
		c.Set("X-Next-Cursor", strconv.FormatInt(posts[len(posts)-1].ID, 10))
	}
	// Feed access is one bounded grant lookup; paid posts carry their offers
	// so cards can show prices.
	if err = api.decorate(c, posts, true); err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts)
}

// get resolves a stable id; billing return pages use it to find the current URL.
func (api *postAPI) get(c fiber.Ctx) error {
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	return api.show(c, api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
}

// getBySlug serves /c/<channel>/<post>: post slugs are unique within a channel.
func (api *postAPI) getBySlug(c fiber.Ctx) error {
	channel, err := api.channels.resolve(c.Context(), c.Params("channel"))
	if errors.Is(err, authkit.ErrGroupNotFound) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	return api.show(c, api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE channel_id=$1 AND slug=$2 AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, channel, strings.ToLower(c.Params("slug"))))
}
func (api *postAPI) show(c fiber.Ctx, row pgx.Row) error {
	p, err := scanPost(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	posts := []post{p}
	if err = api.decorate(c, posts, true); err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts[0])
}

var postSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Words a future /c/<channel>/<word> page could need.
var reservedPostSlugs = map[string]bool{"new": true, "edit": true, "settings": true, "members": true, "team": true, "posts": true, "about": true, "subscribe": true}

func validatePost(p post) error {
	if strings.TrimSpace(p.Slug) == "" || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Body) == "" {
		return errors.New("slug, title and body are required")
	}
	if len(p.Title) > 300 || len(p.Body) > 1_000_000 || len(p.Slug) > 120 {
		return errors.New("post is too large")
	}
	if !postSlug.MatchString(p.Slug) {
		return errors.New("Post slugs use lowercase letters, numbers and single dashes.")
	}
	if reservedPostSlugs[p.Slug] {
		return errors.New("That post slug is reserved. Choose another.")
	}
	switch p.AccessPolicy {
	case "public", "membership", "members_ppv", "ppv":
		return nil
	}
	return errors.New("invalid access_policy")
}

const noMembership = "create a channel membership before publishing membership posts"

func (api *postAPI) create(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	var in postInput
	if err := bindJSON(c, &in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.ChannelID == nil || in.Slug == nil || in.Title == nil || in.Body == nil {
		return clientError(c, 400, "channel_id, slug, title and body are required")
	}
	id, err := channelID(*in.ChannelID)
	if err != nil {
		return clientError(c, 400, "invalid channel_id")
	}
	p := post{AuthorID: viewer(c), ChannelID: id, BillingKey: uuid.NewString(), Slug: normalSlug(*in.Slug), Title: *in.Title, Body: *in.Body, AccessPolicy: "public", OfferStatus: "none"}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	if err = validatePost(p); err != nil {
		return clientError(c, 400, err.Error())
	}
	var job *postOfferArgs
	if paidPolicy(p.AccessPolicy) {
		if _, err = checkPrice(in.Price, minPostPrice); err != nil {
			return clientError(c, 400, err.Error())
		}
		p.OfferStatus, p.OfferRevision = "pending", 1
		job = &postOfferArgs{Revision: 1, Price: in.Price}
	}
	release, err := api.channels.lock(c.Context(), id)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.channels.allowed(c.Context(), viewer(c), id, channelCreatePermission)
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed {
		return clientError(c, 404, "channel not found")
	}
	if ok, err := api.channels.requireMembership(c.Context(), id, p.AccessPolicy); err != nil || !ok {
		if err != nil {
			return databaseError(c, err)
		}
		return clientError(c, 400, noMembership)
	}
	err = api.inTx(c, func(tx pgx.Tx) error {
		p, err = scanPost(tx.QueryRow(c.Context(), `INSERT INTO `+api.table+`(author_id,channel_id,billing_key,slug,title,body,access_policy,offer_status,offer_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+postColumns, p.AuthorID, id, p.BillingKey, p.Slug, p.Title, p.Body, p.AccessPolicy, p.OfferStatus, p.OfferRevision))
		if err != nil || job == nil {
			return err
		}
		job.PostID = p.ID
		_, err = api.jobs.InsertTx(c.Context(), tx, *job, nil)
		return err
	})
	if err != nil {
		return writeError(c, err)
	}
	return api.settled(c, 201, p.ID, job)
}
func (api *postAPI) update(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	var in postInput
	if err = bindJSON(c, &in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.ChannelID != nil {
		return clientError(c, 400, "channel_id is immutable")
	}
	if in.Price != nil {
		if _, err = checkPrice(in.Price, minPostPrice); err != nil {
			return clientError(c, 400, err.Error())
		}
	}
	p, err := api.live(c.Context(), id)
	if err != nil {
		return readError(c, err)
	}
	release, err := api.channels.lock(c.Context(), p.ChannelID)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.channels.allowed(c.Context(), viewer(c), p.ChannelID, channelEditPermission)
	if err != nil {
		return billingUnavailable(c)
	}
	admin, err := api.canModerate(c, viewer(c), postEditPermission)
	if err != nil {
		return billingUnavailable(c)
	}
	active, err := api.channels.active(c.Context(), p.ChannelID)
	if err != nil {
		return databaseError(c, err)
	}
	if !active || (!allowed && !admin) {
		return clientError(c, 404, "post not found")
	}
	if p, err = api.live(c.Context(), id); err != nil {
		return readError(c, err)
	}
	revision, wasPaid := p.UpdatedAt, paidPolicy(p.AccessPolicy)
	if in.Slug != nil {
		p.Slug = normalSlug(*in.Slug)
	}
	if in.Title != nil {
		p.Title = *in.Title
	}
	if in.Body != nil {
		p.Body = *in.Body
	}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	if err = validatePost(p); err != nil {
		return clientError(c, 400, err.Error())
	}
	if ok, err := api.channels.requireMembership(c.Context(), p.ChannelID, p.AccessPolicy); in.AccessPolicy != nil && (err != nil || !ok) {
		if err != nil {
			return databaseError(c, err)
		}
		return clientError(c, 400, noMembership)
	}
	var job *postOfferArgs
	switch isPaid := paidPolicy(p.AccessPolicy); {
	case isPaid && in.Price != nil:
		p.OfferStatus, job = "pending", &postOfferArgs{Price: in.Price}
	case isPaid && !wasPaid:
		return clientError(c, 400, "a purchase offer is required")
	case !isPaid && wasPaid:
		p.OfferStatus, job = "none", &postOfferArgs{}
	}
	if job != nil {
		p.OfferRevision++
		job.PostID, job.Revision = id, p.OfferRevision
	}
	err = api.inTx(c, func(tx pgx.Tx) error {
		p, err = scanPost(tx.QueryRow(c.Context(), `UPDATE `+api.table+` SET slug=$1,title=$2,body=$3,access_policy=$4,offer_status=$5,offer_revision=$6,updated_at=NOW() WHERE id=$7 AND updated_at=$8 AND deleted_at IS NULL RETURNING `+postColumns, p.Slug, p.Title, p.Body, p.AccessPolicy, p.OfferStatus, p.OfferRevision, id, revision))
		if err != nil || job == nil {
			return err
		}
		_, err = api.jobs.InsertTx(c.Context(), tx, *job, nil)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 409, "post changed; reload and retry")
	}
	if err != nil {
		return writeError(c, err)
	}
	return api.settled(c, 200, id, job)
}
func (api *postAPI) delete(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	p, err := api.live(c.Context(), id)
	if err != nil {
		return readError(c, err)
	}
	release, err := api.channels.lock(c.Context(), p.ChannelID)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.channels.allowed(c.Context(), viewer(c), p.ChannelID, channelRemovePermission)
	if err != nil {
		return billingUnavailable(c)
	}
	admin, err := api.canModerate(c, viewer(c), postDeletePermission)
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed && !admin {
		return clientError(c, 404, "post not found")
	}
	err = api.inTx(c, func(tx pgx.Tx) error {
		result, err := tx.Exec(c.Context(), `UPDATE `+api.table+` SET deleted_at=NOW() WHERE id=$1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if _, err = api.jobs.InsertTx(c.Context(), tx, postArchiveArgs{PostID: id}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}); err != nil {
			return err
		}
		return api.media.deletePostsTx(c.Context(), tx, p.ChannelID, id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return writeError(c, err)
	}
	inline(c.Context(), "post archive", func(ctx context.Context) error { return api.archivePost(ctx, id) })
	return c.SendStatus(204)
}

// visible is a live post on a live channel.
func (api *postAPI) visible(ctx context.Context, id int64) (post, error) {
	return scanPost(api.pool.QueryRow(ctx, `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
}
func (api *postAPI) live(ctx context.Context, id int64) (post, error) {
	return scanPost(api.pool.QueryRow(ctx, `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL`, id))
}
func readError(c fiber.Ctx, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	return databaseError(c, err)
}

// inTx commits the post row together with its catalog job.
func (api *postAPI) inTx(c fiber.Ctx, fn func(pgx.Tx) error) error {
	if api.jobs == nil {
		return errJobsUnavailable
	}
	tx, err := api.pool.Begin(c.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Context())
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(c.Context())
}

// settled runs the committed offer job inline (caller holds the channel lock)
// and answers with the post's resulting offer state.
func (api *postAPI) settled(c fiber.Ctx, status int, id int64, job *postOfferArgs) error {
	if job != nil {
		inline(c.Context(), "post offer sync", func(ctx context.Context) error { return api.syncOffer(ctx, *job) })
	}
	p, err := scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if err != nil {
		return databaseError(c, err)
	}
	p.CanRead, p.CanEdit, p.Offers = true, true, []openrails.CatalogOffer{}
	if avatars, e := api.media.slots(c.Context(), kindChannel, []string{p.ChannelID}, slotAvatar); e == nil {
		p.ChannelAvatar = avatars[slotKey{p.ChannelID, slotAvatar}]
	}
	if g, e := api.auth.client.GroupInstanceByID(c.Context(), p.ChannelID); e == nil {
		p.ChannelSlug, p.ChannelName = g.InstanceSlug, g.DisplayName
	}
	if paidPolicy(p.AccessPolicy) && p.OfferStatus == "active" {
		if offers, e := api.billing.offers(c.Context(), postResource(p.BillingKey), false); e == nil {
			p.Offers = offers
		}
	}
	return c.Status(status).JSON(p)
}

var errJobsUnavailable = errors.New("background workers are unavailable")

func writeError(c fiber.Ctx, err error) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, errJobsUnavailable):
		return clientError(c, 503, err.Error())
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "posts_channel_slug_idx":
		return clientError(c, 409, "That post slug is already used in this channel. Choose another.")
	}
	return databaseError(c, err)
}
func (api *postAPI) canModerate(c fiber.Ctx, user, permission string) (bool, error) {
	if user == "" {
		return false, nil
	}
	return api.auth.client.Can(c.Context(), authkit.UserSubject(user), authkit.RootGroup(), authkit.Perm(permission))
}
func clientError(c fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": message})
}
func databaseError(c fiber.Ctx, err error) error { return clientError(c, 500, "database error") }
func billingUnavailable(c fiber.Ctx) error {
	return clientError(c, http.StatusServiceUnavailable, "billing service is unavailable")
}

// Reject retired commercial fields instead of accidentally treating an old
// private/paid-post request as a new public post.
func bindJSON(c fiber.Ctx, value any) error {
	if len(c.Body()) > 1<<20 {
		return errors.New("request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
