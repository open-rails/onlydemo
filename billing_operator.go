package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gofiber/fiber/v3"
	authkit "github.com/open-rails/authkit"
	"github.com/open-rails/openrails"
)

// billingOperatePermission lets a site admin run OpenRails merchant
// operations: legacy book import, provider refresh, engine takeover, tier
// changes and refunds.
const billingOperatePermission = "root:billing:operate"

type billingOperatorAPI struct {
	auth    *appAuth
	billing *billingService
}

func (api billingOperatorAPI) mount(router fiber.Router, required fiber.Handler) {
	g := router.Group("/api/v1/admin/billing", required, api.authorize)
	g.Post("/import", api.importBook)
	g.Post("/provider-refresh", api.refreshProviders)
	g.Post("/takeovers", api.takeOverBatch)
	g.Get("/subscriptions/:id/takeover", api.subscription(func(c fiber.Ctx, id openrails.SubscriptionID) (any, error) {
		return api.billing.client.GetEngineTakeover(c.Context(), id)
	}))
	g.Post("/subscriptions/:id/takeover/preview", api.subscription(func(c fiber.Ctx, id openrails.SubscriptionID) (any, error) {
		return api.billing.client.PreviewEngineTakeover(c.Context(), id)
	}))
	g.Post("/subscriptions/:id/takeover", api.subscription(func(c fiber.Ctx, id openrails.SubscriptionID) (any, error) {
		return api.billing.client.TakeOverBilling(c.Context(), id, c.Get("Idempotency-Key"))
	}))
	g.Post("/subscriptions/:id/takeover/abandon", api.subscription(func(c fiber.Ctx, id openrails.SubscriptionID) (any, error) {
		return api.billing.client.AbandonEngineTakeover(c.Context(), id)
	}))
	g.Post("/subscriptions/:id/change-tier/preview", api.subscription(func(c fiber.Ctx, id openrails.SubscriptionID) (any, error) {
		var request openrails.ChangeTierRequest
		if err := bindJSON(c, &request); err != nil {
			return nil, fmt.Errorf("%w: %v", openrails.ErrInvalid, err)
		}
		return api.billing.client.PreviewTierChange(c.Context(), id, request)
	}))
	g.Post("/subscriptions/:id/change-tier", api.subscription(func(c fiber.Ctx, id openrails.SubscriptionID) (any, error) {
		var request openrails.ChangeTierRequest
		if err := bindJSON(c, &request); err != nil {
			return nil, fmt.Errorf("%w: %v", openrails.ErrInvalid, err)
		}
		return api.billing.client.ChangeTier(c.Context(), id, c.Get("Idempotency-Key"), request)
	}))
	g.Post("/payments/:id/refunds", api.refund)
}

func (api billingOperatorAPI) authorize(c fiber.Ctx) error {
	if api.billing == nil {
		return billingUnavailable(c)
	}
	ok, err := api.auth.client.Can(c.Context(), authkit.UserSubject(viewer(c)), authkit.RootGroup(), authkit.Perm(billingOperatePermission))
	if err != nil {
		return err
	}
	if !ok {
		return clientError(c, http.StatusForbidden, "billing operator permission required")
	}
	return c.Next()
}

func (api billingOperatorAPI) importBook(c fiber.Ctx) error {
	var book openrails.DeclaredBilling
	if err := bindJSON(c, &book); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}
	return billingResult(c)(api.billing.client.ImportBilling(c.Context(), book))
}

func (api billingOperatorAPI) refreshProviders(c fiber.Ctx) error {
	return billingResult(c)(api.billing.client.RefreshProviders(c.Context()))
}

func (api billingOperatorAPI) takeOverBatch(c fiber.Ctx) error {
	var request openrails.EngineTakeoverBatchRequest
	if err := bindJSON(c, &request); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}
	return billingResult(c)(api.billing.client.TakeOverBillingBatch(c.Context(), request))
}

func (api billingOperatorAPI) subscription(action func(fiber.Ctx, openrails.SubscriptionID) (any, error)) fiber.Handler {
	return func(c fiber.Ctx) error {
		id, err := openrails.ParseSubscriptionID(c.Params("id"))
		if err != nil {
			return clientError(c, http.StatusBadRequest, "invalid subscription id")
		}
		return billingResult(c)(action(c, id))
	}
}

func (api billingOperatorAPI) refund(c fiber.Ctx) error {
	id, err := openrails.ParsePaymentID(c.Params("id"))
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid payment id")
	}
	var params openrails.RefundPaymentParams
	if err := bindJSON(c, &params); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}
	params.IdempotencyKey = c.Get("Idempotency-Key")
	return billingResult(c)(api.billing.client.RefundPayment(c.Context(), id, params))
}

// billingResult relays an OpenRails result, or its typed error unchanged.
func billingResult(c fiber.Ctx) func(any, error) error {
	return func(out any, err error) error {
		var status *openrails.StatusError
		switch {
		case errors.As(err, &status):
			return c.Status(status.Status).JSON(fiber.Map{"error": status.Message, "code": status.Code, "metadata": status.Metadata})
		case errors.Is(err, openrails.ErrInvalid):
			return clientError(c, http.StatusBadRequest, err.Error())
		case err != nil:
			return err
		}
		return c.JSON(out)
	}
}
