package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	helpersauth "github.com/open-rails/authkit/auth"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/authkit/verify"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/contenturl"
	"github.com/open-rails/contentkit/media"
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
	// urls holds each post's content code and slug (from its title).
	urls *contenturl.Store
}

func newPosts(channels *channelAPI, cfg Config) (*postAPI, error) {
	urls, err := contenturl.New(contenturl.Options{Pool: channels.pool, Schema: contentSchema(cfg), Tenant: tenant})
	if err != nil {
		return nil, err
	}
	return &postAPI{pool: channels.pool, auth: channels.auth, billing: channels.billing, channels: channels, urls: urls,
		table: pgx.Identifier{appSchema(cfg), "posts"}.Sanitize()}, nil
}

type post struct {
	ID          string `json:"id"`
	AuthorID    string `json:"author_id"`
	ChannelID   string `json:"channel_id"`
	ChannelSlug string `json:"channel_slug"`
	ChannelName string `json:"channel_name"`
	// Code and Slug make the post's URL, /p/{code}/{slug}; the slug follows
	// the title, and a stale one redirects.
	Code          string `json:"code"`
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Body          string `json:"body,omitempty"`
	AccessPolicy  string `json:"access_policy"`
	OfferStatus   string `json:"offer_status"`
	OfferRevision int64  `json:"-"`
	// State is "draft" (the composer's), "publishing" (media still
	// processing: its editors alone see it) or "published".
	State string `json:"state"`
	// Deleted posts stay readable to their buyers alone.
	Deleted bool `json:"-"`
	// MediaReadiness is a publishing post's media processing, for its editors.
	MediaReadiness     *media.Readiness `json:"media_readiness,omitempty"`
	CanRead            bool             `json:"can_read"`
	CanEdit            bool             `json:"can_edit"`
	Purchased          bool             `json:"purchased"`
	SubscriptionActive bool             `json:"has_membership"`
	ChannelAvatar      *publicPreset    `json:"channel_avatar,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
	Offers             []offer          `json:"offers"`
}
type postInput struct {
	ChannelID    *string     `json:"channel_id"`
	Title        *string     `json:"title"`
	Body         *string     `json:"body"`
	AccessPolicy *string     `json:"access_policy"`
	Price        *offerPrice `json:"price"`
	// Draft creates an empty draft; DraftID publishes that draft.
	Draft   bool    `json:"draft"`
	DraftID *string `json:"draft_id"`
}

const postColumns = `id::text,author_id::text,channel_id::text,title,body,access_policy,offer_status,offer_revision,state,deleted_at IS NOT NULL,created_at,updated_at`

// Post states.
const (
	stateDraft      = "draft"
	statePublishing = "publishing"
	statePublished  = "published"
)

// published excludes drafts, which only their author sees (through the
// composer), and publishing posts, which only their editors see.
const published = ` AND state='published'`

func scanPost(row interface{ Scan(...any) error }) (post, error) {
	var p post
	err := row.Scan(&p.ID, &p.AuthorID, &p.ChannelID, &p.Title, &p.Body, &p.AccessPolicy, &p.OfferStatus, &p.OfferRevision, &p.State, &p.Deleted, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (p post) draft() bool { return p.State == stateDraft }

var errNoPost = errors.New("post not found")

// postID resolves the route's post code (any Crockford spelling) to the
// post's id; anything else names no post.
func (api *postAPI) postID(c fiber.Ctx) (string, error) {
	code, err := contenturl.ParseCode(c.Params("code"))
	if err != nil {
		return "", errNoPost
	}
	link, err := api.urls.Resolve(c.Context(), code)
	if errors.Is(err, contenturl.ErrNotFound) || err == nil && link.ContentKind != kindPost {
		return "", errNoPost
	}
	return link.ContentID, err
}

// parsePostID parses a post id: a canonical UUIDv7 (ContentKit's content id).
func parsePostID(raw string) (string, error) {
	if contentref.ValidateID(raw) != nil {
		return "", errNoPost
	}
	return raw, nil
}

// putURL registers the post's code, and its slug from the title, in tx.
func (api *postAPI) putURL(ctx context.Context, tx pgx.Tx, p *post) error {
	links, err := api.urls.WithTx(tx).Put(ctx, contenturl.Entry{ContentRef: postRef(p.ID), Title: p.Title})
	if err == nil {
		p.Code, p.Slug = string(links[0].Code), links[0].Slug
	}
	return err
}

// links fills posts' codes and slugs in one read.
func (api *postAPI) links(ctx context.Context, posts []post) error {
	refs := make([]contentref.ContentRef, len(posts))
	for i, p := range posts {
		refs[i] = postRef(p.ID)
	}
	links, err := api.urls.Links(ctx, refs)
	for i := range posts {
		l := links[refs[i].Key()]
		posts[i].Code, posts[i].Slug = string(l.Code), l.Slug
	}
	return err
}

// viewer is the signed-in user AuthKit's gate verified, or "".
func viewer(c fiber.Ctx) string {
	if id, ok := verify.IdentityFromContext(c.Context()); ok && id.SubjectKind == helpersauth.SubjectUser {
		return id.Subject
	}
	return ""
}

// decorate fills posts' channel, access and (withOffers) purchase offers for
// the viewer from one page load and one offer lookup.
func (api *postAPI) decorate(c fiber.Ctx, posts []post, withOffers bool) error {
	ctx, user := c.Context(), viewer(c)
	channels, keys := make([]string, len(posts)), []string{}
	for i, p := range posts {
		channels[i] = p.ChannelID
		if user != "" {
			keys = append(keys, postResource(p.ID), membershipResource(p.ChannelID))
		}
	}
	pg, err := api.channels.loadPage(ctx, user, channels, keys)
	if err != nil {
		return err
	}
	items := make([]access.Item, len(posts))
	for i, p := range posts {
		items[i] = access.Item{Ref: postRef(p.ID), Rule: postRule(p)}
	}
	readable, _, err := api.billing.gate.Settle(ctx, actorFor(user), pg.filter, items)
	if err != nil {
		return err
	}
	if err = api.links(ctx, posts); err != nil {
		return err
	}
	for i := range posts {
		if posts[i].State == statePublishing {
			if posts[i].MediaReadiness, err = api.media.readiness(ctx, posts[i].ID); err != nil {
				return err
			}
		}
	}
	avatars, err := api.media.publicImages(ctx, kindChannel, channels, avatarImage)
	if err != nil {
		return err
	}
	selling := []string{}
	for i := range posts {
		p := &posts[i]
		g := pg.channels[p.ChannelID]
		p.ChannelSlug, p.ChannelName = g.Slug, g.Name
		p.Purchased = pg.access[postResource(p.ID)]
		p.SubscriptionActive = pg.access[membershipResource(p.ChannelID)]
		// Channel grants include the viewer's site-wide (root) grants.
		p.CanEdit = pg.can(p.ChannelID, channelEditPermission) || pg.can(p.ChannelID, postEditPermission)
		p.CanRead = readable[i] || pg.can(p.ChannelID, channelReadPermission) || pg.can(p.ChannelID, postReadPermission)
		if p.Deleted {
			p.CanEdit, p.CanRead = false, p.Purchased
		}
		p.ChannelAvatar = &avatars[p.ChannelID][0]
		if !p.CanRead {
			p.Body = ""
		}
		if withOffers && !p.Deleted && paidPolicy(p.AccessPolicy) && p.OfferStatus == "active" {
			selling = append(selling, postResource(p.ID))
		}
	}
	offers, err := api.billing.offers(ctx, offerPermanent, selling, 100)
	if err != nil {
		return err
	}
	for i := range posts {
		if posts[i].Offers = offers[postResource(posts[i].ID)]; posts[i].Offers == nil {
			posts[i].Offers = []offer{}
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
	// UUIDv7 ids sort by creation, so the last id is the cursor.
	before := c.Query("before")
	if before != "" {
		if _, err := parsePostID(before); err != nil {
			return clientError(c, 400, "invalid cursor")
		}
	}
	var err error
	channel := c.Query("channel_id")
	if channel != "" {
		if _, err := channelID(channel); err != nil {
			return clientError(c, 400, "invalid channel_id")
		}
	}
	// A channel's editors also see its publishing posts there.
	editor := false
	if channel != "" {
		if editor, err = api.editsChannel(c.Context(), viewer(c), channel); err != nil {
			return billingUnavailable(c)
		}
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE deleted_at IS NULL AND (state='published' OR $4 AND state='publishing') AND ($1::text='' OR channel_id::text=$1) AND ($2::text='' OR id<$2::uuid) AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL) ORDER BY id DESC LIMIT $3`, channel, before, limit+1, editor)
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
		c.Set("X-Next-Cursor", posts[len(posts)-1].ID)
	}
	// Feed access is one bounded grant lookup; paid posts carry their offers
	// so cards can show prices.
	if err = api.decorate(c, posts, true); err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts)
}

// get serves a post by its code; a deleted one only to its buyers (show).
func (api *postAPI) get(c fiber.Ctx) error {
	id, err := api.postID(c)
	if err != nil {
		return readError(c, err)
	}
	return api.show(c, api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`+shown+` AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
}

// shown is what the post page loads: a publishing post is then 404 for
// anyone but its editors (show).
const shown = ` AND state IN ('published','publishing')`

func (api *postAPI) show(c fiber.Ctx, row pgx.Row) error {
	p, err := scanPost(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	if p.State == statePublishing {
		editor, err := api.editsChannel(c.Context(), viewer(c), p.ChannelID)
		if err != nil {
			return billingUnavailable(c)
		}
		if !editor {
			return clientError(c, 404, "post not found")
		}
	}
	posts := []post{p}
	if err = api.decorate(c, posts, true); err != nil {
		return billingUnavailable(c)
	}
	if posts[0].Deleted && !posts[0].Purchased {
		return clientError(c, 404, "post not found")
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts[0])
}

const needsText = "Add a title or some text, or attach an image or video."

// validatePost checks a post. Text is optional when it has media.
func validatePost(p post, hasMedia bool) error {
	if !hasMedia && strings.TrimSpace(p.Title) == "" && strings.TrimSpace(p.Body) == "" {
		return errors.New(needsText)
	}
	if len(p.Title) > 300 || len(p.Body) > 1_000_000 {
		return errors.New("post is too large")
	}
	switch p.AccessPolicy {
	case "public", "membership", "members_ppv", "ppv":
		return nil
	}
	return errors.New("invalid access_policy")
}

const noMembership = "create a channel membership before publishing membership posts"

// create publishes a post. With draft it instead makes an empty draft, whose
// id is the media folder the composer uploads to; draft_id publishes it.
func (api *postAPI) create(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	var in postInput
	if err := bindJSON(c, &in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.ChannelID == nil {
		return clientError(c, 400, "channel_id is required")
	}
	id, err := channelID(*in.ChannelID)
	if err != nil {
		return clientError(c, 400, "invalid channel_id")
	}
	if in.Draft {
		return api.createDraft(c, id)
	}
	p := post{AuthorID: viewer(c), ChannelID: id, Title: deref(in.Title), Body: deref(in.Body), AccessPolicy: "public", OfferStatus: "none"}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	hasMedia := false
	if in.DraftID != nil {
		if *in.DraftID, err = parsePostID(*in.DraftID); err != nil {
			return clientError(c, 404, "draft not found")
		}
		if hasMedia, err = api.media.hasMedia(c.Context(), *in.DraftID); err != nil {
			return clientError(c, 500, "media store error")
		}
	}
	if err = validatePost(p, hasMedia); err != nil {
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
		if in.DraftID != nil {
			// A draft whose media is still processing (or failed) is
			// publishing: the media worker's ItemReady makes it live. The row
			// is locked before readiness is read, so an ItemReady landing
			// after the read waits and then finds it publishing.
			if err = tx.QueryRow(c.Context(), `SELECT 1 FROM `+api.table+` WHERE id=$1 AND channel_id=$2 AND author_id=$3 AND state='draft' AND deleted_at IS NULL FOR UPDATE`, *in.DraftID, id, p.AuthorID).Scan(new(int)); err != nil {
				return err
			}
			state := statePublished
			if r, err := api.media.readiness(c.Context(), *in.DraftID); err != nil {
				return err
			} else if !r.Ready() {
				state = statePublishing
			}
			p, err = scanPost(tx.QueryRow(c.Context(), `UPDATE `+api.table+` SET title=$1,body=$2,access_policy=$3,offer_status=$4,offer_revision=$5,state=$7,published_at=CASE WHEN $7='published' THEN NOW() END,created_at=NOW(),updated_at=NOW() WHERE id=$6 RETURNING `+postColumns, p.Title, p.Body, p.AccessPolicy, p.OfferStatus, p.OfferRevision, *in.DraftID, state))
		} else {
			p, err = scanPost(tx.QueryRow(c.Context(), `INSERT INTO `+api.table+`(author_id,channel_id,title,body,access_policy,offer_status,offer_revision,state,published_at) VALUES($1,$2,$3,$4,$5,$6,$7,'published',NOW()) RETURNING `+postColumns, p.AuthorID, id, p.Title, p.Body, p.AccessPolicy, p.OfferStatus, p.OfferRevision))
			if err == nil {
				err = api.media.createPost(c.Context(), p.ID)
			}
		}
		if err == nil {
			err = api.putURL(c.Context(), tx, &p)
		}
		if err == nil && p.State == statePublished {
			err = api.media.exposeTx(c.Context(), tx, p.ID)
		}
		if err != nil || job == nil {
			return err
		}
		job.PostID = p.ID
		_, err = api.jobs.InsertTx(c.Context(), tx, *job, nil)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "draft not found")
	}
	if err != nil {
		return writeError(c, err)
	}
	return api.settled(c, 201, p.ID, job)
}

func (api *postAPI) createDraft(c fiber.Ctx, channel string) error {
	allowed, err := api.channels.allowed(c.Context(), viewer(c), channel, channelCreatePermission)
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed {
		return clientError(c, 404, "channel not found")
	}
	var id, code string
	err = pgx.BeginFunc(c.Context(), api.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(c.Context(), `INSERT INTO `+api.table+`(author_id,channel_id,title,body) SELECT $1,$2,'','' WHERE EXISTS(SELECT 1 FROM `+api.channels.table+` WHERE id=$2 AND deleted_at IS NULL) RETURNING id::text`, viewer(c), channel).Scan(&id)
		if err != nil {
			return err
		}
		p := post{ID: id}
		if err = api.putURL(c.Context(), tx, &p); err != nil {
			return err
		}
		code = p.Code
		return api.media.createPost(c.Context(), id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return writeError(c, err)
	}
	return c.Status(201).JSON(fiber.Map{"id": id, "code": code, "channel_id": channel, "draft": true})
}

func (api *postAPI) update(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := api.postID(c)
	if err != nil {
		return readError(c, err)
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
	if p.draft() {
		return clientError(c, 404, "post not found")
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
	revision, wasPaid, title := p.UpdatedAt, paidPolicy(p.AccessPolicy), p.Title
	if in.Title != nil {
		p.Title = *in.Title
	}
	if in.Body != nil {
		p.Body = *in.Body
	}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	hasMedia, err := api.media.hasMedia(c.Context(), id)
	if err != nil {
		return clientError(c, 500, "media store error")
	}
	if err = validatePost(p, hasMedia); err != nil {
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
		p, err = scanPost(tx.QueryRow(c.Context(), `UPDATE `+api.table+` SET title=$1,body=$2,access_policy=$3,offer_status=$4,offer_revision=$5,updated_at=NOW() WHERE id=$6 AND updated_at=$7 AND deleted_at IS NULL RETURNING `+postColumns, p.Title, p.Body, p.AccessPolicy, p.OfferStatus, p.OfferRevision, id, revision))
		if err == nil && p.Title != title {
			err = api.putURL(c.Context(), tx, &p)
		}
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
	id, err := api.postID(c)
	if err != nil {
		return readError(c, err)
	}
	p, err := api.live(c.Context(), id)
	if err != nil {
		return readError(c, err)
	}
	if p.draft() {
		return api.discardDraft(c, p)
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
		var offered int64
		err := tx.QueryRow(c.Context(), `UPDATE `+api.table+` SET deleted_at=NOW() WHERE id=$1 AND deleted_at IS NULL RETURNING offer_revision`, id).Scan(&offered)
		if err != nil {
			return err
		}
		if _, err = api.jobs.InsertTx(c.Context(), tx, postArchiveArgs{PostID: id}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}); err != nil {
			return err
		}
		// A post never offered for sale has no buyers: its media goes. One
		// once offered keeps it for its buyers; only its public preview goes.
		if offered == 0 {
			return api.media.deletePostsTx(c.Context(), tx, p.ChannelID, id)
		}
		return api.media.exposeTx(c.Context(), tx, id)
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

// discardDraft removes an unpublished draft and its media folder (uploads
// landing later are deleted by ContentKit's late-upload pass).
func (api *postAPI) discardDraft(c fiber.Ctx, p post) error {
	if p.AuthorID != viewer(c) {
		if admin, err := api.canModerate(c, viewer(c), postDeletePermission); err != nil {
			return billingUnavailable(c)
		} else if !admin {
			return clientError(c, 404, "post not found")
		}
	}
	err := pgx.BeginFunc(c.Context(), api.pool, func(tx pgx.Tx) error {
		return api.deleteDraftsTx(c.Context(), tx, `id=$1`, p.ID)
	})
	if err != nil {
		return databaseError(c, err)
	}
	return c.SendStatus(204)
}

// deleteDraftsTx deletes the drafts matching where, with their media.
func (api *postAPI) deleteDraftsTx(ctx context.Context, tx pgx.Tx, where string, args ...any) error {
	rows, err := tx.Query(ctx, `DELETE FROM `+api.table+` WHERE state='draft' AND `+where+` RETURNING id::text,channel_id::text`, args...)
	if err != nil {
		return err
	}
	byChannel := map[string][]string{}
	for rows.Next() {
		var id string
		var channel string
		if err = rows.Scan(&id, &channel); err != nil {
			rows.Close()
			return err
		}
		byChannel[channel] = append(byChannel[channel], id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for channel, ids := range byChannel {
		if err = api.media.deletePostsTx(ctx, tx, channel, ids...); err != nil {
			return err
		}
	}
	return nil
}

// editsChannel reports whether user edits posts in channel (its owners and
// editors, site admins): they alone see its publishing posts.
func (api *postAPI) editsChannel(ctx context.Context, user, channel string) (bool, error) {
	if user == "" {
		return false, nil
	}
	for _, perm := range []iam.Perm{channelEditPermission, channelCreatePermission} {
		if ok, err := api.channels.allowed(ctx, user, channel, perm); err != nil || ok {
			return ok, err
		}
	}
	return api.auth.can(ctx, user, iam.RootGroup(), postEditPermission)
}

// onLiveChannel is a post (or draft), deleted ones included, on a live channel.
func (api *postAPI) onLiveChannel(ctx context.Context, id string) (post, error) {
	return scanPost(api.pool.QueryRow(ctx, `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
}
func (api *postAPI) live(ctx context.Context, id string) (post, error) {
	return scanPost(api.pool.QueryRow(ctx, `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL`, id))
}
func readError(c fiber.Ctx, err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNoPost) {
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
func (api *postAPI) settled(c fiber.Ctx, status int, id string, job *postOfferArgs) error {
	if job != nil {
		inline(c.Context(), "post offer sync", func(ctx context.Context) error { return api.syncOffer(ctx, *job) })
	}
	p, err := scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if err != nil {
		return databaseError(c, err)
	}
	p.CanRead, p.CanEdit, p.Offers = true, true, []offer{}
	if p.State == statePublishing {
		if p.MediaReadiness, err = api.media.readiness(c.Context(), p.ID); err != nil {
			return clientError(c, 500, "media store error")
		}
	}
	avatars, err := api.media.publicImages(c.Context(), kindChannel, []string{p.ChannelID}, avatarImage)
	if err != nil {
		return clientError(c, 500, "media store error")
	}
	p.ChannelAvatar = &avatars[p.ChannelID][0]
	posts := []post{p}
	if err = api.links(c.Context(), posts); err != nil {
		return databaseError(c, err)
	}
	p = posts[0]
	_ = api.pool.QueryRow(c.Context(), `SELECT slug,name FROM `+api.channels.table+` WHERE id=$1`, p.ChannelID).Scan(&p.ChannelSlug, &p.ChannelName)
	if paidPolicy(p.AccessPolicy) && p.OfferStatus == "active" {
		if offers, e := api.billing.offers(c.Context(), offerPermanent, []string{postResource(p.ID)}, 100); e == nil && offers[postResource(p.ID)] != nil {
			p.Offers = offers[postResource(p.ID)]
		}
	}
	return c.Status(status).JSON(p)
}

var errJobsUnavailable = errors.New("background workers are unavailable")

func writeError(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, errJobsUnavailable):
		return clientError(c, 503, err.Error())
	case errors.Is(err, media.ErrFolderNotEmpty):
		log.Printf("new post refused: %v", err)
		return clientError(c, 500, "media store error")
	}
	return databaseError(c, err)
}
func (api *postAPI) canModerate(c fiber.Ctx, user string, permission iam.Perm) (bool, error) {
	return api.auth.can(c.Context(), user, iam.RootGroup(), permission)
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
