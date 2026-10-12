package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/open-rails/openrails/billing"
)

type checkoutInput struct {
	PriceID billing.PriceID `json:"price_id"`
}

func checkoutBody(c fiber.Ctx) (checkoutInput, error) {
	var in checkoutInput
	if err := bindJSON(c, &in); err != nil {
		return in, err
	}
	if in.PriceID.IsZero() {
		return in, errors.New("price_id is required")
	}
	return in, nil
}

func checkoutError(c fiber.Ctx, err error) error {
	var status *billing.StatusError
	switch {
	case errors.As(err, &status) && status.Status < 500:
		return c.Status(status.Status).JSON(fiber.Map{"error": fiber.Map{"code": status.Code, "message": status.Message}})
	case errors.Is(err, billing.ErrNotFound):
		return clientError(c, 404, "offer not found")
	case errors.Is(err, billing.ErrInvalid):
		return clientError(c, 400, "invalid checkout request")
	case errors.Is(err, billing.ErrDenied):
		return clientError(c, 403, "checkout is not permitted")
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

// mintCheckout creates the viewer's order after host purchase authorization.
func (api *channelAPI) mintCheckout(c fiber.Ctx, price billing.PriceID, publicURL string) error {
	user := viewer(c)
	key := c.Get("Idempotency-Key")
	if key == "" {
		return clientError(c, 400, "Idempotency-Key is required")
	}
	request, err := checkoutRequest(user, price, key)
	if err != nil {
		return clientError(c, 401, "a user access token is required")
	}
	session, err := api.billing.client.CreateOrder(c.Context(), request)
	if err != nil {
		return checkoutError(c, err)
	}
	c.Set("Cache-Control", "no-store")
	return c.Status(http.StatusCreated).JSON(session)
}

// checkout is POST /api/v1/posts/:code/checkout {price_id}: the app's content
// rules decide who may buy; OpenRails decides the offer and payment.
func (api *postAPI) checkout(c fiber.Ctx, publicURL string) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	in, err := checkoutBody(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	id, err := api.postID(c)
	if err != nil {
		return readError(c, err)
	}
	var channel string
	err = api.pool.QueryRow(c.Context(), `SELECT channel_id::text FROM `+api.table+` WHERE id=$1`, id).Scan(&channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	release, err := api.channels.lock(c.Context(), channel)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	post, err := scanPost(api.pool.QueryRow(c.Context(), `SELECT `+postColumns+` FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL`+published+` AND EXISTS(SELECT 1 FROM `+api.channels.table+` ch WHERE ch.id=channel_id AND ch.deleted_at IS NULL)`, id))
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
	if err := api.sold(c.Context(), postResource(post.ID), offerPermanent, in.PriceID); err != nil {
		return checkoutError(c, err)
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
	return api.channels.mintCheckout(c, in.PriceID, publicURL)
}

// sold requires price to be one of resource's current offers: a checkout
// sells only what the app put on sale.
func (api *postAPI) sold(ctx context.Context, resource string, kind offerKind, price billing.PriceID) error {
	return api.channels.sold(ctx, resource, kind, price)
}

func (api *channelAPI) sold(ctx context.Context, resource string, kind offerKind, price billing.PriceID) error {
	offers, err := api.billing.offers(ctx, kind, []string{resource}, 100)
	if err != nil {
		return err
	}
	for _, offer := range offers[resource] {
		if offer.PriceID == price && offer.ProductKey == resource {
			return nil
		}
	}
	return billing.ErrNotFound
}

// subscribe is POST /api/v1/channels/:id/subscribe {price_id}: a paid
// membership open to join, for someone without editorial access.
func (api *channelAPI) subscribe(c fiber.Ctx, publicURL string) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	in, err := checkoutBody(c)
	if err != nil {
		return clientError(c, 400, err.Error())
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
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
	if err := api.sold(c.Context(), membershipResource(id), offerRecurring, in.PriceID); err != nil {
		return checkoutError(c, err)
	}
	editorial, err := api.allowed(c.Context(), viewer(c), id, channelReadPermission)
	if err != nil {
		return billingUnavailable(c)
	}
	if editorial {
		return clientError(c, 409, "you already have editorial access to this channel")
	}
	return api.mintCheckout(c, in.PriceID, publicURL)
}
