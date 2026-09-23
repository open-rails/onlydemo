package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	"github.com/open-rails/openrails"
)

type blogAPI struct {
	pool     *pgxpool.Pool
	auth     *appAuth
	billing  *billingService
	table    string
	channels *channelAPI
}
type blogPost struct {
	ID                 int64                    `json:"id"`
	AuthorID           string                   `json:"author_id"`
	ChannelID          string                   `json:"channel_id"`
	Slug               string                   `json:"slug"`
	Title              string                   `json:"title"`
	Body               string                   `json:"body,omitempty"`
	AccessPolicy       string                   `json:"access_policy"`
	CanRead            bool                     `json:"can_read"`
	CanEdit            bool                     `json:"can_edit"`
	Purchased          bool                     `json:"purchased"`
	SubscriptionActive bool                     `json:"has_membership"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	BillingKey         string                   `json:"-"`
	Offers             []openrails.CatalogOffer `json:"offers"`
}
type blogPostInput struct {
	ChannelID    *string     `json:"channel_id"`
	Slug         *string     `json:"slug"`
	Title        *string     `json:"title"`
	Body         *string     `json:"body"`
	AccessPolicy *string     `json:"access_policy"`
	Price        *offerPrice `json:"price"`
}

const postColumns = `id,author_id::text,channel_id::text,billing_key::text,slug,title,body,access_policy,created_at,updated_at`

func scanBlogPost(row interface{ Scan(...any) error }) (blogPost, error) {
	var p blogPost
	err := row.Scan(&p.ID, &p.AuthorID, &p.ChannelID, &p.BillingKey, &p.Slug, &p.Title, &p.Body, &p.AccessPolicy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}
func postID(c fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid post id")
	}
	return id, nil
}
func viewer(c fiber.Ctx) string {
	if cl, ok := authkitfiber.UserClaims(c); ok {
		return cl.UserID
	}
	return ""
}
func (api *blogAPI) decorate(c fiber.Ctx, posts []blogPost, withOffers bool) error {
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
	publishing := map[string]bool{}
	editing := map[string]bool{}
	for i := range posts {
		p := &posts[i]
		if _, ok := publishing[p.ChannelID]; !ok {
			publishing[p.ChannelID], err = api.channels.allowed(c.Context(), user, p.ChannelID, channelReadPermission)
			if err != nil {
				return err
			}
			editing[p.ChannelID], err = api.channels.allowed(c.Context(), user, p.ChannelID, channelEditPermission)
			if err != nil {
				return err
			}
		}
		p.Purchased = access[postResource(p.BillingKey)]
		p.SubscriptionActive = access[membershipResource(p.ChannelID)]
		p.CanEdit = editing[p.ChannelID] || editAdmin
		p.CanRead = p.AccessPolicy == "public" || publishing[p.ChannelID] || admin || p.Purchased || (p.AccessPolicy == "membership" && p.SubscriptionActive)
		if !p.CanRead {
			p.Body = ""
		}
		p.Offers = []openrails.CatalogOffer{}
		if withOffers && (p.AccessPolicy == "ppv" || p.AccessPolicy == "members_ppv") {
			p.Offers, err = api.billing.offers(c.Context(), postResource(p.BillingKey), false)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (api *blogAPI) list(c fiber.Ctx) error {
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
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE ($1::text='' OR channel_id::text=$1) AND ($2::bigint=0 OR id<$2) AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL) ORDER BY id DESC LIMIT $3`, channel, before, limit+1)
	if err != nil {
		return databaseError(c, err)
	}
	posts := []blogPost{}
	for rows.Next() {
		p, e := scanBlogPost(rows)
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
	// Feed access is one bounded grant lookup; offer discovery is only on detail.
	if err = api.decorate(c, posts, false); err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts)
}
func (api *blogAPI) get(c fiber.Ctx) error {
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	p, err := scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	posts := []blogPost{p}
	if err = api.decorate(c, posts, true); err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(posts[0])
}
func validatePost(p blogPost) error {
	if strings.TrimSpace(p.Slug) == "" || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Body) == "" {
		return errors.New("slug, title and body are required")
	}
	if len(p.Title) > 300 || len(p.Body) > 1_000_000 || len(p.Slug) > 120 {
		return errors.New("post is too large")
	}
	switch p.AccessPolicy {
	case "public", "membership", "members_ppv", "ppv":
		return nil
	}
	return errors.New("invalid access_policy")
}
func (api *blogAPI) create(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	var in blogPostInput
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
	p := blogPost{AuthorID: viewer(c), ChannelID: id, BillingKey: uuid.NewString(), Slug: *in.Slug, Title: *in.Title, Body: *in.Body, AccessPolicy: "public"}
	if in.AccessPolicy != nil {
		p.AccessPolicy = *in.AccessPolicy
	}
	if err = validatePost(p); err != nil {
		return clientError(c, 400, err.Error())
	}
	if p.AccessPolicy == "ppv" || p.AccessPolicy == "members_ppv" {
		if err = api.billing.setOffer(c.Context(), id, postResource(p.BillingKey), p.Title, in.Price, false, false); err != nil {
			return clientError(c, 400, "offer could not be saved")
		}
	}
	p, err = scanBlogPost(api.pool.QueryRow(c.Context(), `INSERT INTO `+api.table+`(author_id,channel_id,billing_key,slug,title,body,access_policy) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+postColumns, p.AuthorID, id, p.BillingKey, p.Slug, p.Title, p.Body, p.AccessPolicy))
	if err != nil {
		return databaseError(c, err)
	}
	p.CanRead, p.CanEdit = true, true
	p.Offers, _ = api.billing.offers(c.Context(), postResource(p.BillingKey), false)
	return c.Status(201).JSON(p)
}
func (api *blogAPI) update(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	var in blogPostInput
	if err = bindJSON(c, &in); err != nil {
		return clientError(c, 400, "invalid JSON")
	}
	if in.ChannelID != nil {
		return clientError(c, 400, "channel_id is immutable")
	}
	p, err := scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
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
	p, err = scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	revision := p.UpdatedAt
	if in.Slug != nil {
		p.Slug = *in.Slug
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
	if p.AccessPolicy == "ppv" || p.AccessPolicy == "members_ppv" {
		if in.Price != nil {
			if err = api.billing.setOffer(c.Context(), p.ChannelID, postResource(p.BillingKey), p.Title, in.Price, false, false); err != nil {
				return clientError(c, 400, "offer could not be saved")
			}
		} else {
			offers, e := api.billing.offers(c.Context(), postResource(p.BillingKey), false)
			if e != nil {
				return billingUnavailable(c)
			}
			if len(offers) == 0 {
				return clientError(c, 400, "a purchase offer is required")
			}
		}
	} else if err = api.billing.archiveResource(c.Context(), postResource(p.BillingKey)); err != nil {
		return billingUnavailable(c)
	}
	p, err = scanBlogPost(api.pool.QueryRow(c.Context(), `UPDATE `+api.table+` SET slug=$1,title=$2,body=$3,access_policy=$4,updated_at=NOW() WHERE id=$5 AND updated_at=$6 RETURNING `+postColumns, p.Slug, p.Title, p.Body, p.AccessPolicy, id, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 409, "post changed; reload and retry")
	}
	if err != nil {
		return databaseError(c, err)
	}
	p.CanRead, p.CanEdit = true, true
	p.Offers, _ = api.billing.offers(c.Context(), postResource(p.BillingKey), false)
	return c.JSON(p)
}
func (api *blogAPI) delete(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	p, err := scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
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
	if err = api.billing.archiveResource(c.Context(), postResource(p.BillingKey)); err != nil {
		return billingUnavailable(c)
	}
	_, err = api.pool.Exec(c.Context(), `DELETE FROM `+api.table+` WHERE id=$1`, id)
	if err != nil {
		return databaseError(c, err)
	}
	return c.SendStatus(204)
}
func (api *blogAPI) canModerate(c fiber.Ctx, user, permission string) (bool, error) {
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
