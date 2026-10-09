package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/open-rails/contentkit/media/tiered"
	"github.com/open-rails/openrails/billing"
	"github.com/open-rails/openrails/catalog"
)

type offerPrice struct {
	UnitAmount int64  `json:"unit_amount,string"`
	Currency   string `json:"currency"`
}

// Products are keyed by the resource they grant: one per post, and per
// channel one sold membership and one free (never sold) membership.
func postResource(key string) string      { return "post:" + key }
func membershipResource(id string) string { return "channel:" + id + ":membership" }
func freeMembershipProduct(id string) string {
	return membershipResource(id) + ":free"
}

// A product sells through one price key; a new price under it archives the
// previous one, so accepted terms stay valid.
const (
	purchasePriceKey   = "purchase"
	membershipPriceKey = "membership"
)

func customerID(user string) (billing.CustomerID, error) { return billing.ParseCustomerID(user) }

func (b *billingService) access(ctx context.Context, user string, keys []string) (map[string]bool, error) {
	result := map[string]bool{}
	if user == "" {
		return result, nil
	}
	customer, err := customerID(user)
	if err != nil {
		return nil, err
	}
	for chunk := range slices.Chunk(unique(keys), billing.MaxEntitlementChecks) {
		check, err := b.reads.CheckEntitlements(ctx, customer, billing.CheckEntitlementsParams{Entitlements: chunk, At: time.Now()})
		if err != nil {
			return nil, err
		}
		maps.Copy(result, check.Entitlements)
	}
	return result, nil
}

// checker adapts OpenRails entitlements to media/tiered.
func (b *billingService) checker() tiered.Checker {
	return tiered.CheckerFunc(b.access)
}

// offers lists each key's active offers, preferred currency first; limit
// bounds each key's list.
func (b *billingService) offers(ctx context.Context, kind billing.OfferKind, keys []string, limit int) (map[string][]billing.Offer, error) {
	out := map[string][]billing.Offer{}
	for chunk := range slices.Chunk(unique(keys), billing.MaxEntitlementChecks) {
		pages, err := b.reads.ListOffers(ctx, billing.OfferListParams{Entitlements: chunk, Kind: kind, PreferredCurrency: "USD", Limit: limit})
		if err != nil {
			return nil, err
		}
		for key, page := range pages {
			if len(page.Items) > 0 {
				out[key] = page.Items
			}
		}
	}
	return out, nil
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

// ensureProduct is the live product keyed by resource, granting resource.
func (b *billingService) ensureProduct(ctx context.Context, key, title, resource string) (*billing.Product, error) {
	product, err := b.client.EnsureProduct(ctx, billing.CreateProductParams{Key: key, DisplayName: title, Entitlements: []string{resource}})
	if err != nil {
		return nil, err
	}
	if product.Archived || product.DisplayName != title || !slices.Equal(product.Entitlements, []string{resource}) {
		return b.client.UpdateProduct(ctx, product.ID, billing.UpdateProductParams{
			DisplayName: catalog.Value(title), Entitlements: catalog.Value([]string{resource}), Archived: catalog.Value(false),
		})
	}
	return product, nil
}

// setOffer sells resource at price: a post forever, a membership renewing
// every membership period. The host has already authorized the publisher;
// no commercial fields are stored on a post or channel. Repeating it with the
// same terms changes nothing.
func (b *billingService) setOffer(ctx context.Context, resource, title string, price *offerPrice, recurring bool) error {
	floor, key := minPostPrice, purchasePriceKey
	if recurring {
		floor, key = minMembershipPrice, membershipPriceKey
	}
	currency, err := checkPrice(price, floor)
	if err != nil {
		return err
	}
	product, err := b.ensureProduct(ctx, resource, title, resource)
	if err != nil {
		return err
	}
	params := billing.CreatePriceParams{ProductID: product.ID, Key: key, UnitAmount: price.UnitAmount, Currency: currency, PSPs: b.psps}
	if recurring {
		hours := b.membershipHours
		params.AccessDurationHours, params.BillingIntervalHours = &hours, &hours
	}
	_, err = b.client.CreatePrice(ctx, params)
	return err
}

// archiveResource stops new sales of resource; buyers and members keep access.
func (b *billingService) archiveResource(ctx context.Context, resource string) error {
	product, err := b.client.GetProductByKey(ctx, resource)
	if errors.Is(err, billing.ErrNotFound) {
		return nil
	}
	if err != nil || product.Archived {
		return err
	}
	_, err = b.client.UpdateProduct(ctx, product.ID, billing.UpdateProductParams{Archived: catalog.Value(true)})
	return err
}

// archivePostProduct archives a deleted post's product (never deletes it) and
// applies the host's refund policy to purchases made within the window before
// deletedAt. The key is derived from the post and policy, so the inline attempt
// and the job replay one operation; each call is capped, so replay until done.
func (b *billingService) archivePostProduct(ctx context.Context, resource string, deletedAt time.Time) error {
	policy := b.postDeletion
	params := billing.ArchiveProductParams{ProductKey: resource, PurchaseAction: policy.Action, Reason: "post deleted",
		IdempotencyKey: fmt.Sprintf("post-archive:%s:%s:%s", resource, policy.Action, policy.Window)}
	if policy.Action != billing.PurchaseActionNone {
		params.PurchaseWindowStartsAt = deletedAt.Add(-policy.Window)
	}
	for {
		archive, err := b.client.ArchiveProduct(ctx, params)
		if errors.Is(err, billing.ErrNotFound) {
			return nil
		}
		if err != nil || archive.Complete {
			return err
		}
	}
}

// archiveChannel stops every sale of a deleted channel: its memberships and posts.
func (b *billingService) archiveChannel(ctx context.Context, id string, posts []string) error {
	for _, resource := range append([]string{membershipResource(id), freeMembershipProduct(id)}, mapped(posts, postResource)...) {
		if err := b.archiveResource(ctx, resource); err != nil {
			return err
		}
	}
	return nil
}

func mapped(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// checkoutRequest mints the customer's checkout session for one price. A
// session moves no money until the customer pays it.
func checkoutRequest(user, username, email string, price billing.PriceID, publicURL string) (billing.CreateCheckoutSessionParams, error) {
	customer, err := customerID(user)
	if err != nil {
		return billing.CreateCheckoutSessionParams{}, err
	}
	return billing.CreateCheckoutSessionParams{
		Customer:   billing.CheckoutCustomerIdentity{ID: customer, Username: username, VerifiedEmail: email},
		PriceID:    price,
		SuccessURL: publicURL + "/me?tab=library",
	}, nil
}
