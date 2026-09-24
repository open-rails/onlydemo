package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/open-rails/contentkit/media/tiered"
	"github.com/open-rails/openrails"
)

type offerPrice struct {
	UnitAmount int64  `json:"unit_amount,string"`
	Currency   string `json:"currency"`
}

func postResource(key string) string      { return "post:" + key }
func membershipResource(id string) string { return "channel:" + id + ":membership" }
func (b *billingService) access(ctx context.Context, user string, keys []string) (map[string]bool, error) {
	result := map[string]bool{}
	if user == "" {
		return result, nil
	}
	for len(keys) > 0 {
		n := min(100, len(keys))
		page, err := b.client.CheckEntitlements(ctx, user, keys[:n], time.Time{})
		if err != nil {
			return nil, err
		}
		for k, v := range page {
			result[k] = v
		}
		keys = keys[n:]
	}
	return result, nil
}

// checker adapts OpenRails entitlements to media/tiered.
func (b *billingService) checker() tiered.Checker {
	return tiered.CheckerFunc(b.access)
}
func (b *billingService) offers(ctx context.Context, key string, recurring bool) ([]openrails.CatalogOffer, error) {
	kind := openrails.OfferPermanent
	if recurring {
		kind = openrails.OfferRecurring
	}
	page, err := b.client.ListOffersForEntitlement(ctx, key, openrails.OfferListParams{Kind: kind, PreferredCurrency: "USD", Limit: 100})
	if err != nil {
		return nil, err
	}
	if page.Data == nil {
		return []openrails.CatalogOffer{}, nil
	}
	return page.Data, nil
}

// Amounts are in micro-units; posts start at 0.50 and memberships at 1.00.
const minPostPrice int64 = 500_000

func checkPrice(price *offerPrice, min int64) (string, error) {
	if price == nil || price.UnitAmount < min || price.UnitAmount > 999999990000 {
		return "", fmt.Errorf("price must be between %d.%02d and 999999.99 in native currency", min/1_000_000, min%1_000_000/10_000)
	}
	currency := strings.ToUpper(strings.TrimSpace(price.Currency))
	if currency == "" {
		currency = "USD"
	}
	if len(currency) != 3 {
		return "", errors.New("currency must be a three-letter code")
	}
	return currency, nil
}

// The host has already authorized the publisher against the channel. Catalog
// owns offer versions and history; no commercial fields are stored on a post.
func (b *billingService) setOffer(ctx context.Context, channelID, resource, title string, price *offerPrice, recurring, archived bool) error {
	catalog, err := b.client.EnsureCatalogForOwner(ctx, channelID)
	if err != nil {
		return err
	}
	product := openrails.CatalogApplyProduct{Key: resource, Archived: openrails.CatalogValue(archived)}
	if !archived {
		floor := minPostPrice
		if recurring {
			floor = minMembershipPrice
		}
		currency, err := checkPrice(price, floor)
		if err != nil {
			return err
		}
		product.DisplayName = openrails.CatalogValue(title)
		product.EntitlementsSpec = openrails.CatalogValue(map[string]*int{resource: nil})
		duration := openrails.CatalogNull[int]()
		if recurring {
			duration = openrails.CatalogValue(b.membershipHours)
		}
		product.Prices = []openrails.CatalogApplyPrice{{Key: resource + ":purchase", Currency: openrails.CatalogValue(currency), UnitAmount: openrails.CatalogValue(price.UnitAmount), AccessDurationHours: duration, AutoRenew: openrails.CatalogValue(recurring), Archived: openrails.CatalogValue(false), PSPs: openrails.CatalogValue(b.psps)}}
	}
	for attempts := 0; attempts < 3; attempts++ {
		rev, err := b.client.Catalog.Revision(ctx)
		if err != nil {
			return err
		}
		_, err = b.client.Catalog.Apply(ctx, &openrails.CatalogApplyParams{SchemaVersion: 1, ApplicationID: uuid.NewString(), ExpectedRevision: &rev.Revision, CatalogID: catalog.ID.String(), Products: []openrails.CatalogApplyProduct{product}})
		if !errors.Is(err, openrails.ErrConflict) {
			return err
		}
	}
	return openrails.ErrConflict
}
func (b *billingService) archiveResource(ctx context.Context, resource string) error {
	product, err := b.client.Products.RetrieveByKey(ctx, resource)
	if errors.Is(err, openrails.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	yes := true
	_, err = b.client.Products.Update(ctx, product.ID, &openrails.ProductUpdateParams{Archived: &yes})
	return err
}

// archivePostProduct archives a deleted post's product (never deletes it) and
// applies the host's refund policy to purchases made within the window before
// deletedAt. The key is derived from the post and policy, so the inline attempt
// and the job replay one operation; each call is capped, so replay until done.
func (b *billingService) archivePostProduct(ctx context.Context, resource string, deletedAt time.Time) error {
	policy := b.postDeletion
	params := openrails.ArchiveProductParams{ProductKey: resource, Action: policy.Action, Reason: "post deleted",
		IdempotencyKey: fmt.Sprintf("post-archive:%s:%s:%s", resource, policy.Action, policy.Window)}
	if policy.Action != openrails.PurchaseActionNone {
		params.PurchasedSince = deletedAt.Add(-policy.Window)
	}
	for {
		archive, err := b.client.ArchiveProduct(ctx, params)
		if errors.Is(err, openrails.ErrNotFound) {
			return nil
		}
		if err != nil || archive.Complete {
			return err
		}
	}
}
func (b *billingService) ArchiveChannelCatalog(ctx context.Context, id string) error {
	catalog, err := b.client.GetCatalogForOwner(ctx, id)
	if errors.Is(err, openrails.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	no, yes := false, true
	for {
		page, err := b.client.Products.List(ctx, &openrails.ProductListParams{PageOptions: openrails.PageOptions{Limit: 100}, CatalogID: catalog.ID.String(), Archived: &no})
		if err != nil {
			return err
		}
		if len(page.Items) == 0 {
			return nil
		}
		for _, product := range page.Items {
			if _, err = b.client.Products.Update(ctx, product.ID, &openrails.ProductUpdateParams{Archived: &yes}); err != nil {
				return err
			}
		}
	}
}

// Card payments settle in the page; a redirect-only PSP returns to the library.
func checkoutRequest(user, resource, key, priceID string, payment openrails.CheckoutPaymentOptions, kind openrails.OfferKind, publicURL string) openrails.CreateCheckoutSessionRequest {
	digest := sha256.Sum256([]byte(user + "\x00" + resource + "\x00" + key))
	return openrails.CreateCheckoutSessionRequest{OfferKind: kind, Customer: openrails.CheckoutCustomerIdentity{ID: user}, Entitlement: resource, PriceID: priceID, IdempotencyKey: "demo-" + hex.EncodeToString(digest[:]), PaymentOptions: payment, SuccessURL: publicURL + "/me?tab=library", CancelURL: publicURL + "/me?tab=library", Metadata: map[string]string{"resource": resource}}
}

