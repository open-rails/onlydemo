package main

import (
	"github.com/gofiber/fiber/v3"
	"github.com/open-rails/openrails"
	"os"
	"path/filepath"
	"strings"
)

// Vite assets are built explicitly with pnpm. Unknown API paths stay 404 and
// never receive an HTML shell masquerading as a successful API response.
func mountFrontend(app *fiber.App) {
	app.Use(func(c fiber.Ctx) error {
		for _, prefix := range []string{"/api/", "/auth/", "/billing/", "/.well-known/", "/oidc/"} {
			if strings.HasPrefix(c.Path(), prefix) {
				return c.SendStatus(404)
			}
		}
		return c.Next()
	})
	app.Get("/assets/*", func(c fiber.Ctx) error {
		path := filepath.Clean(c.Params("*"))
		if path == "." || strings.HasPrefix(path, "..") || filepath.IsAbs(path) {
			return c.SendStatus(404)
		}
		c.Set("Cache-Control", "public,max-age=31536000,immutable")
		return c.SendFile(filepath.Join("frontend/dist/assets", path))
	})
	app.Get("/*", func(c fiber.Ctx) error {
		for _, prefix := range []string{"/api/", "/auth/", "/billing/", "/.well-known/", "/oidc/"} {
			if strings.HasPrefix(c.Path(), prefix) {
				return c.SendStatus(404)
			}
		}
		if _, err := os.Stat("frontend/dist/index.html"); err != nil {
			return c.Status(503).SendString("Build the frontend with pnpm --dir frontend build")
		}
		c.Set("Cache-Control", "no-store")
		return c.SendFile("frontend/dist/index.html")
	})
}
func publicConfiguration(b *billingService, cfg Config) fiber.Handler {
	return func(c fiber.Ctx) error {
		var key any
		if strings.HasPrefix(cfg.StripePublishableKey, "pk_test_") {
			key = cfg.StripePublishableKey
		}
		var psp any
		psps := []openrails.CheckoutPSPConfig{}
		config, err := b.client.GetCheckoutConfig(c.Context())
		if err == nil {
			psps = config.PSPs
			for _, v := range config.PSPs {
				if v.Rail == "stripe" {
					psp = v.PSPID
					break
				}
			}
		}
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"stripe_publishable_key": key, "stripe_psp_id": psp, "billing_available": true, "psps": psps})
	}
}

// This is a read-only offer document for the shared browser checkout component.
// The purchase wrapper still binds the resource, payer, policy and exact offer.
func checkoutOptions(b *billingService) fiber.Handler {
	return func(c fiber.Ctx) error {
		price, err := b.client.Prices.Retrieve(c.Context(), c.Query("price_id"))
		if err != nil {
			return checkoutError(c, err)
		}
		product, err := b.client.Products.Retrieve(c.Context(), price.ProductID)
		if err != nil {
			return checkoutError(c, err)
		}
		if price.Archived || product.Archived {
			return clientError(c, 404, "offer is no longer available")
		}
		plan, err := openrails.NewHostedCheckoutPlan(product, price)
		if err != nil {
			return checkoutError(c, err)
		}
		options, err := b.client.ListCheckoutRailOptions(c.Context(), price.ID)
		if err != nil {
			return checkoutError(c, err)
		}
		config, err := b.client.GetCheckoutConfig(c.Context())
		if err != nil {
			return billingUnavailable(c)
		}
		psps := []openrails.CheckoutPSPConfig{}
		for _, psp := range config.PSPs {
			for _, option := range options {
				if option.PSPID == psp.PSPID {
					psps = append(psps, psp)
					break
				}
			}
		}
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"plan": plan, "options": options, "psps": psps, "price_id": price.ID, "product_id": product.ID})
	}
}
