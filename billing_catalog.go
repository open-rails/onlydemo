package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/openrails/billing"
	"github.com/open-rails/openrails/catalog"
)

type offerPrice struct {
	UnitAmount int64  `json:"unit_amount,string"`
	Currency   string `json:"currency"`
}

// Products are keyed by the ContentKit entitlement they grant: one per post
// (owning it), and per channel one sold membership and one free (never sold)
// membership granting the same key.
func postResource(id string) string       { return access.Own(postRef(id)).String() }
func membershipResource(id string) string { return access.Members(ref(kindChannel, id)).String() }
func freeMembershipProduct(id string) string {
	return membershipResource(id) + ":free"
}

// policyLevels maps a post's access policy to ContentKit's paywall levels.
var policyLevels = map[string]access.Level{
	"public": access.Public, "membership": access.MembersLevel, "ppv": access.PPV, "members_ppv": access.MembersPPV,
}

// postRule is a post's paywall: owning the post unlocks it at every level, so
// buyers keep access after policy or membership changes; members_ppv sells
// the post to members only.
func postRule(p post) access.Rule {
	return access.Rule{Level: policyLevels[p.AccessPolicy], Scope: ref(kindChannel, p.ChannelID)}
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
		check, err := b.reads.CheckEntitlements(ctx, billing.CheckEntitlementsParams{CustomerID: customer, Entitlements: chunk, At: time.Now()})
		if err != nil {
			return nil, err
		}
		maps.Copy(result, check.Entitlements)
	}
	return result, nil
}

// held answers the viewer's own and members keys among keys from f (the
// request's one entitlement read), reading exactly only the keys of a
// keyspace f holds incompletely.
func (b *billingService) held(ctx context.Context, user string, f *access.Filter, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if user == "" {
		return out, nil
	}
	type keyspace struct {
		own  bool
		kind string
	}
	sets := map[keyspace]map[string]bool{}
	var exact []string
	for _, s := range unique(keys) {
		k, err := access.ParseKey(s)
		r, ok := k.Ref()
		if err != nil || !ok || !f.Complete(r.ContentKind) {
			exact = append(exact, s)
			continue
		}
		ks := keyspace{k.Kind() == access.KeyOwn, r.ContentKind}
		if sets[ks] == nil {
			ids := f.Members(ks.kind)
			if ks.own {
				ids = f.Owned(ks.kind)
			}
			sets[ks] = make(map[string]bool, len(ids))
			for _, id := range ids {
				sets[ks][id] = true
			}
		}
		out[s] = sets[ks][r.ContentID]
	}
	if len(exact) == 0 {
		return out, nil
	}
	read, err := b.access(ctx, user, exact)
	maps.Copy(out, read)
	return out, err
}

type offerKind bool

const (
	offerPermanent offerKind = false
	offerRecurring offerKind = true
)

type offer struct {
	PriceID              billing.PriceID   `json:"price_id"`
	ProductKey           string            `json:"product_key"`
	ProductID            billing.ProductID `json:"product_id"`
	ProductDisplayName   string            `json:"product_display_name"`
	Entitlements         []string          `json:"entitlements"`
	PriceKey             string            `json:"price_key"`
	Kind                 string            `json:"kind"`
	UnitAmount           int64             `json:"unit_amount,string"`
	Currency             string            `json:"currency"`
	AccessDurationHours  *int              `json:"access_duration_hours"`
	BillingIntervalHours *int              `json:"billing_interval_hours"`
}

// offers reads only the app's product keys and filters their current prices.
func (b *billingService) offers(ctx context.Context, kind offerKind, keys []string, limit int) (map[string][]offer, error) {
	out := map[string][]offer{}
	kindName := "permanent"
	if kind == offerRecurring {
		kindName = "recurring"
	}
	live := false
	for chunk := range slices.Chunk(unique(keys), billing.MaxBatchItems) {
		params := billing.ProductListParams{Keys: chunk, Archived: &live, PageRequest: billing.PageRequest{Limit: 100}}
		for {
			page, err := b.reads.ListProducts(ctx, params)
			if err != nil {
				return nil, err
			}
			for _, product := range page.Items {
				if !slices.Contains(product.Entitlements, product.Key) {
					continue
				}
				for _, price := range product.Prices {
					if price.Archived || (price.BillingIntervalHours != nil) != bool(kind) {
						continue
					}
					if !bool(kind) && price.AccessDurationHours != nil {
						continue
					}
					out[product.Key] = append(out[product.Key], offer{PriceID: price.ID, ProductKey: product.Key, ProductID: product.ID, ProductDisplayName: product.DisplayName, Entitlements: product.Entitlements, PriceKey: price.Key, Kind: kindName,
						UnitAmount: price.UnitAmount, Currency: price.Currency, AccessDurationHours: price.AccessDurationHours, BillingIntervalHours: price.BillingIntervalHours})
				}
			}
			if page.Next == "" {
				break
			}
			params.Cursor = page.Next
		}
	}
	for key, offers := range out {
		slices.SortFunc(offers, func(a, b offer) int {
			if (a.Currency == "USD") != (b.Currency == "USD") {
				if a.Currency == "USD" {
					return -1
				}
				return 1
			}
			if byCurrency := cmp.Compare(a.Currency, b.Currency); byCurrency != 0 {
				return byCurrency
			}
			return cmp.Compare(a.PriceID.String(), b.PriceID.String())
		})
		if len(offers) > limit {
			out[key] = offers[:limit]
		}
	}
	return out, nil
}

func (b *billingService) productByKey(ctx context.Context, key string) (*billing.Product, error) {
	page, err := b.client.ListProducts(ctx, billing.ProductListParams{Keys: []string{key}, PageRequest: billing.PageRequest{Limit: 1}})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return nil, billing.ErrNotFound
	}
	return &page.Items[0], nil
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
	product, err := b.productByKey(ctx, key)
	if errors.Is(err, billing.ErrNotFound) {
		product, err = b.client.CreateProduct(ctx, billing.CreateProductParams{Key: key, DisplayName: title, Entitlements: []string{resource}})
	}
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
	product, err := b.productByKey(ctx, resource)
	if errors.Is(err, billing.ErrNotFound) {
		return nil
	}
	if err != nil || product.Archived {
		return err
	}
	_, err = b.client.UpdateProduct(ctx, product.ID, billing.UpdateProductParams{Archived: catalog.Value(true)})
	return err
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

// checkoutRequest creates an unpaid order only after the host authorizes the
// content purchase. A retry reuses its key; creating it never charges a card.
func checkoutRequest(user string, price billing.PriceID, key string) (billing.CreateOrderParams, error) {
	customer, err := customerID(user)
	if err != nil {
		return billing.CreateOrderParams{}, err
	}
	return billing.CreateOrderParams{CustomerID: customer, Lines: []billing.OrderLineParams{{PriceID: price}}, IdempotencyKey: key}, nil
}

// heldProducts checks the requested grant products in bounded pages, retaining
// the distinction between paid and freely granted channel membership.
func (b *billingService) heldProducts(ctx context.Context, customer billing.CustomerID, keys []string) (map[string]bool, error) {
	result := map[string]bool{}
	for chunk := range slices.Chunk(unique(keys), billing.MaxBatchItems) {
		products, err := b.reads.ListProducts(ctx, billing.ProductListParams{Keys: chunk, PageRequest: billing.PageRequest{Limit: 100}})
		if err != nil {
			return nil, err
		}
		ids := make([]billing.ProductID, 0, len(products.Items))
		for _, product := range products.Items {
			ids = append(ids, product.ID)
		}
		if len(ids) == 0 {
			continue
		}
		params := billing.ProductAccessListParams{CustomerIDs: []billing.CustomerID{customer}, ProductIDs: ids, LiveOnly: true, PageRequest: billing.PageRequest{Limit: 100}}
		for {
			page, err := b.reads.ListProductAccess(ctx, params)
			if err != nil {
				return nil, err
			}
			for _, grant := range page.Items {
				result[grant.ProductKey] = true
			}
			if page.Next == "" {
				break
			}
			params.Cursor = page.Next
		}
	}
	return result, nil
}
