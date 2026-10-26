package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/authkit/verify"
	"github.com/open-rails/contentkit"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/openrails/billing"
	"github.com/open-rails/openrails/catalog"
)

// Commerce belongs to OpenRails. These host routes only authorize a creator's
// catalog edits against their channel; ContentKit stores the access rule.
func (s *server) mountCommerce(app *fiber.App) {
	app.Get("/api/v1/products", func(c fiber.Ctx) error {
		product, err := s.product(c.Context(), c.Query("key"))
		if err != nil {
			return err
		}
		return c.JSON(product)
	})
	required := authkitfiber.Required(s.auth.client)
	app.Put("/api/v1/products/:kind/:id", required, s.saveProduct)
	app.Delete("/api/v1/products/:kind/:id", required, s.saveProduct)
	app.Post("/api/v1/memberships/:id", required, s.freeMembership)
	app.Delete("/api/v1/memberships/:id", required, s.freeMembership)
}

func (s *server) product(ctx context.Context, key string) (*billing.Product, error) {
	parsed, err := access.ParseKey(key)
	if err != nil || parsed.Tenant() != tenant || parsed.Kind() != access.KeyVersion && parsed.Kind() != access.KeyChannel {
		return nil, fiber.ErrBadRequest
	}
	// Products keep their immutable billing identity across an entitlement
	// migration. Find the product granting this exact edition, not its old key.
	page, err := s.billing.client.ListProducts(ctx, billing.ProductListParams{Entitlements: []string{key}})
	if err != nil || len(page.Items) == 0 {
		return nil, err
	}
	if len(page.Items) != 1 {
		return nil, fiber.NewError(409, "More than one product grants this item; reconcile its catalog before editing")
	}
	return &page.Items[0], nil
}

var amountPattern = regexp.MustCompile(`^[0-9]{1,8}(\.[0-9]{1,2})?$`)

func priceMicros(amount string) (int64, error) {
	if !amountPattern.MatchString(amount) {
		return 0, fiber.NewError(400, "Use a USD amount with at most two decimal places")
	}
	parts := strings.SplitN(amount, ".", 2)
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	var cents int64
	if len(parts) == 2 {
		cents, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 2-len(parts[1])), 10, 64)
	}
	return whole*1_000_000 + cents*10_000, nil
}

func (s *server) saveProduct(c fiber.Ctx) error {
	who, ok := verify.IdentityFromContext(c.Context())
	if !ok {
		return fiber.ErrUnauthorized
	}
	id := c.Params("id")
	if _, err := uuid.Parse(id); err != nil {
		return fiber.ErrBadRequest
	}
	var key access.Key
	var channel, name string
	required := []string{}
	var err error
	switch c.Params("kind") {
	case "channels":
		g, e := s.content.Client().GetChannel(c.Context(), id, contentkit.GetChannelQuery{})
		if e != nil {
			return fiber.ErrNotFound
		}
		channel, name = id, g.Name
		key, err = access.ChannelKey(tenant, id)
	case "contents":
		post, e := s.content.Client().GetContent(c.Context(), id, contentkit.GetContentQuery{})
		if e != nil {
			return fiber.ErrNotFound
		}
		channel, name = post.ChannelID, post.Title
		if len(post.Versions) != 1 || post.Versions[0].Access == nil {
			return fiber.NewError(409, "A post purchase requires exactly one edition with an access rule")
		}
		version := post.Versions[0]
		key, err = access.VersionKey(tenant, id, version.ID)
		if version.Access.Level == access.ChannelPPV {
			membership, e := access.ChannelKey(tenant, version.Access.ChannelID)
			if e != nil {
				return e
			}
			required = []string{membership.String()}
		}
	default:
		return fiber.ErrNotFound
	}
	if err != nil {
		return fiber.ErrBadRequest
	}
	permission := access.ChannelSettingsManage
	if c.Method() == "DELETE" && c.Params("kind") == "contents" {
		permission = access.ChannelContentWrite
	}
	allowed, err := s.auth.client.Can(c.Context(), who, iam.GroupByID(channel), channelPerm(permission))
	if err != nil {
		return err
	}
	if !allowed {
		return fiber.ErrForbidden
	}
	var body struct {
		Amount     string `json:"amount"`
		RenewHours int    `json:"renew_hours"`
	}
	if c.Method() != "DELETE" {
		if err := c.Bind().Body(&body); err != nil {
			return err
		}
	}
	var amount int64
	if c.Method() != "DELETE" {
		amount, err = priceMicros(body.Amount)
		if err != nil {
			return err
		}
		if c.Params("kind") == "contents" && amount < 500_000 || c.Params("kind") == "entities" && amount > 0 && amount < 1_000_000 {
			return fiber.NewError(400, "The price is below the minimum")
		}
		if c.Params("kind") == "contents" && body.RenewHours != 0 || c.Params("kind") == "entities" && amount > 0 && body.RenewHours != 720 {
			return fiber.NewError(400, "Memberships renew every 30 days; post purchases are one time")
		}
	}
	product, err := s.product(c.Context(), key.String())
	if err != nil {
		return err
	}
	if product == nil {
		if c.Method() == "DELETE" {
			return c.JSON(nil)
		}
		product, err = s.billing.client.CreateProduct(c.Context(), billing.CreateProductParams{Key: key.String(), DisplayName: name, Entitlements: []string{key.String()}, RequiredEntitlements: required, Description: "OnlyDemo paid access"})
	} else {
		product, err = s.billing.client.UpdateProduct(c.Context(), product.ID, billing.UpdateProductParams{DisplayName: catalog.Value(name), RequiredEntitlements: catalog.Value(required), Description: catalog.Value("OnlyDemo paid access"), Archived: catalog.Value(c.Method() == "DELETE")})
	}
	if err != nil {
		return err
	}
	if c.Method() == "DELETE" {
		return c.JSON(product)
	}
	if amount == 0 {
		// Retire paid prices; existing subscriptions remain under the terms accepted.
		for _, price := range product.Prices {
			if _, err := s.billing.client.UpdatePrice(c.Context(), price.ID, billing.UpdatePriceParams{Archived: catalog.Value(true)}); err != nil {
				return err
			}
		}
		if _, err := s.billing.client.UpdateProduct(c.Context(), product.ID, billing.UpdateProductParams{Description: catalog.Value(membershipGrantNote)}); err != nil {
			return err
		}
	} else {
		price := billing.CreatePriceParams{ProductID: product.ID, Key: "default", UnitAmount: amount, Currency: "USD"}
		if body.RenewHours > 0 {
			price.BillingIntervalHours = &body.RenewHours
			price.AccessDurationHours = &body.RenewHours
		}
		if _, err := s.billing.client.CreatePrice(c.Context(), price); err != nil {
			return err
		}
	}
	product, err = s.billing.client.GetProduct(c.Context(), product.ID)
	if err != nil {
		return err
	}
	return c.JSON(product)
}

const membershipGrantNote = "OnlyDemo free membership"

func (s *server) freeMembership(c fiber.Ctx) error {
	who, ok := verify.IdentityFromContext(c.Context())
	if !ok {
		return fiber.ErrUnauthorized
	}
	live, err := s.auth.live(c.Context(), who.Subject)
	if err != nil {
		return err
	}
	if !live {
		return fiber.ErrForbidden
	}
	key, err := access.ChannelKey(tenant, c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	entity, err := s.content.Client().GetChannel(c.Context(), c.Params("id"), contentkit.GetChannelQuery{})
	if err != nil || entity.State != "active" {
		return fiber.ErrNotFound
	}
	product, err := s.product(c.Context(), key.String())
	if err != nil {
		return err
	}
	if product == nil {
		return fiber.ErrNotFound
	}
	customer, err := billing.ParseCustomerID(who.Subject)
	if err != nil {
		return fiber.ErrForbidden
	}
	page, err := s.billing.client.ListProductAccess(c.Context(), billing.ProductAccessListParams{CustomerIDs: []billing.CustomerID{customer}, ProductIDs: []billing.ProductID{product.ID}, LiveOnly: true, PageRequest: billing.PageRequest{Limit: 100}})
	if err != nil {
		return err
	}
	var free []billing.ProductAccessGrant
	for _, grant := range page.Items {
		if grant.SourceType == billing.ProductAccessSourceGrant && grant.Note != nil && *grant.Note == membershipGrantNote {
			free = append(free, grant)
		}
	}
	if c.Method() == "DELETE" {
		for _, grant := range free {
			if err := s.billing.client.RevokeProductAccess(c.Context(), grant.ID, billing.RevokeProductAccessParams{Reason: "Member left the free channel membership"}); err != nil {
				return err
			}
		}
	} else {
		if product.Archived || len(product.Prices) > 0 || product.Description != membershipGrantNote {
			return fiber.NewError(409, "This channel does not offer a free membership")
		}
		if len(free) == 0 {
			note := membershipGrantNote
			_, err = s.billing.client.CreateProductAccess(c.Context(), billing.CreateProductAccessBatchParams{IdempotencyKey: "membership:" + uuid.NewString(), Items: []billing.CreateProductAccessParams{{CustomerID: customer, ProductID: product.ID, Reason: billing.GrantReasonComp, Note: &note}}})
			if err != nil {
				return err
			}
		}
	}
	return c.SendStatus(204)
}
