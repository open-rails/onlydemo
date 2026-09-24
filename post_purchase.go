package main

import (
	"errors"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/open-rails/openrails"
	"net/http"
	"strings"
)

type checkoutInput struct {
	PriceID string                           `json:"price_id"`
	Payment openrails.CheckoutPaymentOptions `json:"payment"`
}

func checkoutBody(c fiber.Ctx) (checkoutInput, string, error) {
	var in checkoutInput
	if len(c.Body()) > 0 {
		if err := bindJSON(c, &in); err != nil {
			return in, "", err
		}
	}
	key := strings.TrimSpace(c.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 || in.PriceID == "" {
		return in, key, errors.New("price_id and Idempotency-Key are required")
	}
	return in, key, nil
}
func checkoutError(c fiber.Ctx, err error) error {
	// A definite decline: the buyer may correct the card or use another one.
	if failure, ok := openrails.PaymentFailureFrom(err); ok {
		return c.Status(http.StatusPaymentRequired).JSON(fiber.Map{"error": fiber.Map{"code": openrails.CodeCardDeclined, "message": failure.Message, "metadata": fiber.Map{"failure": failure}}})
	}
	switch {
	case errors.Is(err, openrails.ErrPaymentMethodStale):
		return clientError(c, http.StatusPaymentRequired, "this saved card can no longer be used; add it again")
	case errors.Is(err, openrails.ErrConflict), errors.Is(err, openrails.ErrIdempotencyKeyReused):
		return clientError(c, 409, "checkout conflicts with an earlier request")
	case errors.Is(err, openrails.ErrInvalid):
		return clientError(c, 400, "invalid checkout request")
	case errors.Is(err, openrails.ErrDenied):
		return clientError(c, 403, "checkout is not permitted")
	case errors.Is(err, openrails.ErrNotFound):
		return clientError(c, 404, "offer not found")
	}
	return billingUnavailable(c)
}

// returnOrigin returns the buyer to the site origin they checked out from;
// OpenRails refuses any origin outside PUBLIC_URL and RETURN_ORIGINS.
func returnOrigin(c fiber.Ctx, publicURL string) string {
	if origin := c.Get("Origin"); origin != "" && origin != "null" {
		return origin
	}
	return publicURL
}

func (api *postAPI) checkout(c fiber.Ctx, publicURL string) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	in, key, err := checkoutBody(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	// Identity-only lookup includes soft-deleted content so accepted exact-key
	// retries can resolve before mutable content/membership admission checks.
	post, err := scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	request := checkoutRequest(viewer(c), postResource(post.BillingKey), key, in.PriceID, in.Payment, openrails.OfferPermanent, publicURL)
	replay, err := api.billing.client.LookupCheckoutSession(c.Context(), request)
	if err == nil {
		if replay.Status == "created" {
			replay, err = api.billing.client.CreateCheckoutSession(c.Context(), request)
			if err != nil {
				return checkoutError(c, err)
			}
		}
		return c.Status(201).JSON(replay)
	}
	if !errors.Is(err, openrails.ErrNotFound) {
		return checkoutError(c, err)
	}
	release, err := api.channels.lock(c.Context(), post.ChannelID)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	post, err = scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL`+published+` AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	if !paidPolicy(post.AccessPolicy) {
		return clientError(c, 409, "post is not sold separately")
	}
	if post.OfferStatus != "active" {
		return clientError(c, 409, "post price is pending")
	}
	editorial, err := api.channels.allowed(c.Context(), viewer(c), post.ChannelID, channelReadPermission)
	if err != nil {
		return billingUnavailable(c)
	}
	if editorial {
		return clientError(c, 409, "you already have channel access to this post")
	}
	if post.AccessPolicy == "members_ppv" {
		access, e := api.billing.access(c.Context(), viewer(c), []string{membershipResource(post.ChannelID)})
		if e != nil {
			return billingUnavailable(c)
		}
		if !access[membershipResource(post.ChannelID)] {
			return clientError(c, 403, "active channel membership is required to buy this post")
		}
	}
	result, err := api.billing.client.CreateCheckoutSession(c.Context(), request)
	if err != nil {
		return checkoutError(c, err)
	}
	c.Set("Cache-Control", "no-store")
	return c.Status(201).JSON(result)
}
func (api *channelAPI) subscribe(c fiber.Ctx, publicURL string) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	in, key, err := checkoutBody(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
	}
	request := checkoutRequest(viewer(c), membershipResource(id), key, in.PriceID, in.Payment, openrails.OfferRecurring, publicURL)
	replay, err := api.billing.client.LookupCheckoutSession(c.Context(), request)
	if err == nil {
		if replay.Status == "created" {
			replay, err = api.billing.client.CreateCheckoutSession(c.Context(), request)
			if err != nil {
				return checkoutError(c, err)
			}
		}
		return c.Status(201).JSON(replay)
	}
	if !errors.Is(err, openrails.ErrNotFound) {
		return checkoutError(c, err)
	}
	release, err := api.lock(c.Context(), id)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	state, err := api.membershipState(c.Context(), api.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	if state.Status != membershipOpen || state.Free {
		return clientError(c, 409, "this channel has no paid membership open to join")
	}
	editorial, err := api.allowed(c.Context(), viewer(c), id, channelReadPermission)
	if err != nil {
		return billingUnavailable(c)
	}
	if editorial {
		return clientError(c, 409, "you already have editorial access to this channel")
	}
	if in.Payment.PaymentMethodID == "" {
		return clientError(c, 400, "a verified saved payment method is required")
	}
	result, err := api.billing.client.CreateCheckoutSession(c.Context(), request)
	if err != nil {
		return checkoutError(c, err)
	}
	return c.Status(201).JSON(result)
}
