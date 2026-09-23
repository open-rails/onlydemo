package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	authkit "github.com/open-rails/authkit"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
)

type blogAPI struct {
	pool     *pgxpool.Pool
	auth     *appAuth
	billing  *billingService
	table    string
	channels *channelAPI
}

type blogPost struct {
	ID         int64     `json:"id"`
	AuthorID   string    `json:"author_id"`
	ChannelID  string    `json:"channel_id"`
	Slug       string    `json:"slug"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	Visibility string    `json:"visibility"`
	PriceCents *int64    `json:"price_cents"`
	Currency   string    `json:"currency"`
	CanRead    bool      `json:"can_read"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ProductID  string    `json:"-"`
	PriceID    string    `json:"-"`
	BillingKey string    `json:"-"`
}

type blogPostInput struct {
	ChannelID  *string `json:"channel_id"`
	Slug       *string `json:"slug"`
	Title      *string `json:"title"`
	Body       *string `json:"body"`
	Visibility *string `json:"visibility"`
	// Zero removes the listing. Omission leaves an existing listing unchanged.
	PriceCents *int64 `json:"price_cents"`
}

const postColumns = `id, author_id::text, channel_id::text, billing_key::text, slug, title, body, visibility, price_cents,
	COALESCE(openrails_product_id, ''), COALESCE(openrails_price_id, ''), created_at, updated_at`

// list returns one bounded page. Access checks cover only that page's products;
// a buyer's complete purchase history is never loaded to render the feed.
func (api *blogAPI) list(c fiber.Ctx) error {
	userID, admin, err := api.readAccess(c)
	if err != nil {
		return err
	}
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return clientError(c, http.StatusBadRequest, "limit must be between 1 and 100")
		}
	}
	var before int64
	if raw := c.Query("before"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			return clientError(c, http.StatusBadRequest, "invalid post cursor")
		}
	}
	rows, err := api.pool.Query(c.Context(), `SELECT `+postColumns+` FROM `+api.table+`
  WHERE (visibility = 'public' OR price_cents IS NOT NULL OR $1 <> '' OR $2)
   AND EXISTS(SELECT 1 FROM `+api.channels.table+` AS channel WHERE channel.id=channel_id AND channel.deleted_at IS NULL)
   AND ($3::bigint = 0 OR id < $3)
  ORDER BY id DESC LIMIT $4`, userID, admin, before, limit+1)
	if err != nil {
		return databaseError(c, err)
	}
	posts := make([]blogPost, 0, limit+1)
	for rows.Next() {
		post, scanErr := scanBlogPost(rows)
		if scanErr != nil {
			rows.Close()
			return databaseError(c, scanErr)
		}
		posts = append(posts, post)
	}
	rowsErr := rows.Err()
	rows.Close() // Release the host connection before OpenRails borrows it.
	if rowsErr != nil {
		return databaseError(c, rowsErr)
	}
	if len(posts) > limit {
		posts = posts[:limit]
		c.Set("X-Next-Cursor", strconv.FormatInt(posts[len(posts)-1].ID, 10))
	}
	products := make([]string, 0, len(posts))
	channelAccess := make(map[string]bool)
	for _, post := range posts {
		if userID != "" && !admin && post.Visibility != "public" {
			if _, checked := channelAccess[post.ChannelID]; !checked {
				allowed, err := api.channels.allowed(c.Context(), userID, post.ChannelID, channelReadPermission)
				if err != nil {
					return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
				}
				channelAccess[post.ChannelID] = allowed
			}
			if !channelAccess[post.ChannelID] && post.ProductID != "" {
				products = append(products, post.ProductID)
			}
		}
	}
	access := map[string]bool{}
	if len(products) != 0 {
		access, err = api.billing.CheckPostAccess(c.Context(), userID, products)
		if err != nil {
			return billingUnavailable(c)
		}
	}
	visible := make([]blogPost, 0, len(posts))
	for _, post := range posts {
		readable := post.Visibility == "public" || channelAccess[post.ChannelID] || admin || access[post.ProductID]
		if !readable && post.PriceCents == nil {
			continue
		}
		post.setReadable(readable)
		visible = append(visible, post)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(visible)
}

func (api *blogAPI) get(c fiber.Ctx) error {
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}
	userID, admin, err := api.readAccess(c)
	if err != nil {
		return err
	}
	post, err := scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id = $1 AND EXISTS(SELECT 1 FROM `+api.channels.table+` AS channel WHERE channel.id=channel_id AND channel.deleted_at IS NULL)`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	canRead := post.Visibility == "public" || admin
	if !canRead && userID != "" {
		canRead, err = api.channels.allowed(c.Context(), userID, post.ChannelID, channelReadPermission)
		if err != nil {
			return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
		}
	}
	if !canRead && userID != "" && post.ProductID != "" {
		canRead, err = api.billing.HasPostAccess(c.Context(), userID, post.ProductID)
		if err != nil {
			return billingUnavailable(c)
		}
	}
	if !canRead && post.PriceCents == nil {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	post.setReadable(canRead)
	c.Set("Cache-Control", "no-store")
	return c.JSON(post)
}

func (api *blogAPI) create(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	var input blogPostInput
	if err := c.Bind().Body(&input); err != nil {
		return clientError(c, http.StatusBadRequest, "invalid JSON body")
	}
	if input.Slug == nil || input.Title == nil || input.Body == nil {
		return clientError(c, http.StatusBadRequest, "slug, title, and body are required")
	}
	if input.ChannelID == nil {
		return clientError(c, http.StatusBadRequest, "channel_id is required")
	}
	channelID, err := channelID(*input.ChannelID)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid channel id")
	}
	release, err := api.channels.lock(c.Context(), channelID)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.channels.allowed(c.Context(), user.UserID, channelID, channelCreatePermission)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	if !allowed {
		return clientError(c, http.StatusNotFound, "channel not found")
	}
	post := blogPost{AuthorID: user.UserID, ChannelID: channelID, Slug: *input.Slug, Title: *input.Title, Body: *input.Body, Visibility: "private", PriceCents: input.PriceCents}
	if input.Visibility != nil {
		post.Visibility = *input.Visibility
	}
	if err := validatePost(&post); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}
	// Publish the post after its offer exists. Holding an application
	// transaction while billing borrows the same pool can exhaust that pool.
	post.BillingKey = uuid.NewString()
	if post.PriceCents != nil {
		post.ProductID, post.PriceID, err = api.billing.EnsurePostOffer(c.Context(), post.ChannelID, post.BillingKey, post.Title, *post.PriceCents)
		if err != nil {
			return billingUnavailable(c)
		}
	}
	post, err = scanBlogPost(api.pool.QueryRow(c.Context(), `INSERT INTO `+api.table+`
        (author_id, billing_key, slug, title, body, visibility, price_cents, openrails_product_id, openrails_price_id, channel_id)
        VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10) RETURNING `+postColumns,
		post.AuthorID, post.BillingKey, post.Slug, post.Title, post.Body, post.Visibility, post.PriceCents, post.ProductID, post.PriceID, post.ChannelID))
	if err != nil {
		return databaseError(c, err)
	}
	post.setReadable(true)
	return c.Status(http.StatusCreated).JSON(post)
}

func (api *blogAPI) update(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}
	var input blogPostInput
	if err := c.Bind().Body(&input); err != nil {
		return clientError(c, http.StatusBadRequest, "invalid JSON body")
	}
	if input.ChannelID != nil {
		return clientError(c, http.StatusBadRequest, "channel_id is fixed when a post is created")
	}
	admin, err := api.canModerate(c, user.UserID, postEditPermission)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	// Use an optimistic revision check after billing work so simultaneous
	// edits cannot overwrite each other or hold a shared-pool connection idle.
	post, err := scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+`
        FROM `+api.table+` WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	release, err := api.channels.lock(c.Context(), post.ChannelID)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	active, err := api.channels.active(c.Context(), post.ChannelID)
	if err != nil {
		return databaseError(c, err)
	}
	allowed, err := api.channels.allowed(c.Context(), user.UserID, post.ChannelID, channelEditPermission)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	if !active || (!allowed && !admin) {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	revision := post.UpdatedAt
	if input.Slug != nil {
		post.Slug = *input.Slug
	}
	if input.Title != nil {
		post.Title = *input.Title
	}
	if input.Body != nil {
		post.Body = *input.Body
	}
	if input.Visibility != nil {
		post.Visibility = *input.Visibility
	}
	oldPrice := post.PriceCents
	if input.PriceCents != nil {
		post.PriceCents = input.PriceCents
	}
	if err := validatePost(&post); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}
	if post.PriceCents != nil && (oldPrice == nil || *oldPrice != *post.PriceCents) {
		if allowed {
			post.ProductID, post.PriceID, err = api.billing.EnsurePostOffer(c.Context(), post.ChannelID, post.BillingKey, post.Title, *post.PriceCents)
		} else {
			// The row lookup above requires AuthKit's moderation grant here.
			post.ProductID, post.PriceID, err = api.billing.EnsurePostOfferAsAdmin(c.Context(), post.ChannelID, post.BillingKey, post.Title, *post.PriceCents)
		}
		if err != nil {
			return billingUnavailable(c)
		}
	}
	post, err = scanBlogPost(api.pool.QueryRow(c.Context(), `UPDATE `+api.table+`
		SET slug=$1, title=$2, body=$3, visibility=$4, price_cents=$5,
			openrails_product_id=NULLIF($6,''), openrails_price_id=NULLIF($7,''), updated_at=NOW()
		WHERE id=$8 AND channel_id=$9 AND updated_at=$10
		AND EXISTS(SELECT 1 FROM `+api.channels.table+` AS channel WHERE channel.id=channel_id AND channel.deleted_at IS NULL) RETURNING `+postColumns,
		post.Slug, post.Title, post.Body, post.Visibility, post.PriceCents, post.ProductID, post.PriceID, id, post.ChannelID, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, http.StatusConflict, "post changed while editing; fetch it again and retry")
	}
	if err != nil {
		return databaseError(c, err)
	}
	post.setReadable(true)
	return c.JSON(post)
}

func (api *blogAPI) delete(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}
	admin, err := api.canModerate(c, user.UserID, postDeletePermission)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	var channel string
	err = api.pool.QueryRow(c.Context(), `SELECT channel_id::text FROM `+api.table+` WHERE id=$1`, id).Scan(&channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	release, err := api.channels.lock(c.Context(), channel)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	active, err := api.channels.active(c.Context(), channel)
	if err != nil {
		return databaseError(c, err)
	}
	allowed, err := api.channels.allowed(c.Context(), user.UserID, channel, channelRemovePermission)
	if err != nil {
		return clientError(c, http.StatusServiceUnavailable, "permission service is unavailable")
	}
	if !active || (!allowed && !admin) {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	result, err := api.pool.Exec(c.Context(), `DELETE FROM `+api.table+` WHERE id=$1 AND channel_id=$2`, id, channel)
	if err != nil {
		return databaseError(c, err)
	}
	if result.RowsAffected() == 0 {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	return c.SendStatus(http.StatusNoContent)
}

// AuthKit owns live role membership. Account bans take effect at token refresh;
// an already-issued access token remains valid until it expires.
func (api *blogAPI) canModerate(c fiber.Ctx, userID, permission string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	return api.auth.client.Can(c.Context(), authkit.UserSubject(userID), authkit.RootGroup(), authkit.Perm(permission))
}

func (api *blogAPI) readAccess(c fiber.Ctx) (string, bool, error) {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return "", false, nil
	}
	admin, err := api.canModerate(c, user.UserID, postReadPermission)
	if err != nil {
		return "", false, fiber.NewError(http.StatusServiceUnavailable, "permission service is unavailable")
	}
	return user.UserID, admin, nil
}

func (p *blogPost) setReadable(allowed bool) {
	p.CanRead = allowed
	p.Currency = "USD"
	if !allowed {
		p.Body = ""
	}
}

func scanBlogPost(row interface{ Scan(...any) error }) (blogPost, error) {
	var post blogPost
	err := row.Scan(&post.ID, &post.AuthorID, &post.ChannelID, &post.BillingKey, &post.Slug, &post.Title, &post.Body, &post.Visibility,
		&post.PriceCents, &post.ProductID, &post.PriceID, &post.CreatedAt, &post.UpdatedAt)
	return post, err
}

func postID(c fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid post id")
	}
	return id, nil
}

func validatePost(post *blogPost) error {
	if err := validatePostFields(post.Slug, post.Title, post.Body, post.Visibility); err != nil {
		return err
	}
	if post.PriceCents != nil && *post.PriceCents == 0 {
		post.PriceCents = nil
	}
	if post.PriceCents != nil {
		if post.Visibility != "private" {
			return errors.New("only private posts can be listed for sale")
		}
		if *post.PriceCents < minPostPriceCents || *post.PriceCents > maxPostPriceCents {
			return errors.New("price_cents must be between 50 and 99999999 USD cents, or zero to remove the listing")
		}
	}
	return nil
}

func validatePostFields(slug, title, body, visibility string) error {
	if strings.TrimSpace(slug) == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return errors.New("slug, title, and body cannot be empty")
	}
	if visibility != "public" && visibility != "private" {
		return errors.New("visibility must be public or private")
	}
	return nil
}

func clientError(c fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": message})
}

func databaseError(c fiber.Ctx, err error) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "database error"})
}

func billingUnavailable(c fiber.Ctx) error {
	return clientError(c, http.StatusServiceUnavailable, "billing service is unavailable")
}
