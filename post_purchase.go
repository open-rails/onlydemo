package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	"github.com/open-rails/openrails"
)

func (api *blogAPI) checkout(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}
	key := strings.TrimSpace(c.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		return clientError(c, http.StatusBadRequest, "Idempotency-Key must contain between 1 and 200 characters")
	}
	if api.billing == nil {
		return billingUnavailable(c)
	}
	// Select the current immutable offer before billing borrows the same pool.
	// Later price edits create a new offer; this checkout retains these terms.
	// Request JSON never selects a customer or price.
	post, err := scanBlogPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+`
        FROM `+blogPostsTable+` WHERE id=$1 AND visibility='private' AND price_cents IS NOT NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, http.StatusNotFound, "post is not for sale")
	}
	if err != nil {
		return databaseError(c, err)
	}
	if post.OwnerID == user.UserID {
		return clientError(c, http.StatusConflict, "you already own this post")
	}
	if post.ProductID == "" || post.PriceID == "" {
		return billingUnavailable(c)
	}
	owned, err := api.billing.HasPostAccess(c.Context(), user.UserID, post.ProductID)
	if err != nil {
		return billingUnavailable(c)
	}
	if owned {
		return clientError(c, http.StatusConflict, "you already have access to this post")
	}
	session, err := api.billing.CreateCheckout(c.Context(), user.UserID, post.ProductID, post.PriceID, key)
	if errors.Is(err, openrails.ErrConflict) || errors.Is(err, openrails.ErrIdempotencyKeyReused) {
		return clientError(c, http.StatusConflict, "checkout conflicts with an earlier request")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.Status(http.StatusCreated).JSON(session)
}

func (api *blogAPI) getCheckout(c fiber.Ctx) error {
	user, ok := authkitfiber.UserClaims(c)
	if !ok {
		return clientError(c, http.StatusUnauthorized, "a user access token is required")
	}
	if api.billing == nil {
		return billingUnavailable(c)
	}
	session, err := api.billing.GetCheckout(c.Context(), user.UserID, c.Params("id"))
	if errors.Is(err, openrails.ErrNotFound) || errors.Is(err, openrails.ErrInvalid) || errors.Is(err, openrails.ErrDenied) {
		return clientError(c, http.StatusNotFound, "checkout not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(session)
}
