package main

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/open-rails/openrails/billing"
	"github.com/riverqueue/river"
)

// A channel optionally sells one membership: none until its owner creates one,
// then open (free or paid) or closed. Closing only stops new joins; existing
// members keep access and their subscriptions keep renewing at accepted terms.
const (
	membershipNone   = "none"
	membershipOpen   = "open"
	membershipClosed = "closed"
	// Free membership grants carry no payment; paid ones must be at least $1.00.
	minMembershipPrice int64 = 1_000_000
)

type membershipState struct {
	Status   string
	Free     bool
	Sync     string
	Revision int64
}

func (api *channelAPI) membershipState(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (membershipState, error) {
	var s membershipState
	err := q.QueryRow(ctx, `SELECT membership,membership_free,membership_sync,membership_revision FROM `+api.table+` WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&s.Status, &s.Free, &s.Sync, &s.Revision)
	return s, err
}

// channelMembership is the public membership view of a channel. Offer is the
// purchasable price while a paid membership is open.
type channelMembership struct {
	Status string `json:"status"`
	Free   bool   `json:"free"`
	Sync   string `json:"sync"`
	Offer  *offer `json:"offer"`
	// Member: the viewer holds a paid subscription or free grant.
	Member bool `json:"member"`
	// FreeMember: the viewer holds a free grant, which they can leave.
	FreeMember bool `json:"free_member"`
}

type membershipInput struct {
	Enabled bool        `json:"enabled"`
	Price   *offerPrice `json:"price"`
}

type membershipSyncArgs struct {
	ChannelID string      `json:"channel_id"`
	Revision  int64       `json:"revision"`
	Price     *offerPrice `json:"price,omitempty"`
}

func (membershipSyncArgs) Kind() string { return "demo_sync_membership" }

type membershipSyncWorker struct {
	river.WorkerDefaults[membershipSyncArgs]
	api *channelAPI
}

func (w *membershipSyncWorker) Work(ctx context.Context, job *river.Job[membershipSyncArgs]) error {
	release, err := w.api.lock(ctx, job.Args.ChannelID)
	if err != nil {
		return err
	}
	defer release()
	err = w.api.syncMembership(ctx, job.Args)
	if err != nil && job.Attempt >= job.MaxAttempts {
		_, _ = w.api.pool.Exec(ctx, `UPDATE `+w.api.table+` SET membership_sync='failed' WHERE id=$1 AND membership_revision=$2 AND membership_sync='pending'`, job.Args.ChannelID, job.Args.Revision)
	}
	return err
}

// setMembership is PUT /api/v1/channels/:id/membership with
// {"enabled": bool, "price": {"unit_amount","currency"} | null}; a null price
// is a free membership. Disabling closes an existing membership to new joins.
func (api *channelAPI) setMembership(c fiber.Ctx) error {
	if viewer(c) == "" {
		return clientError(c, 401, "a user access token is required")
	}
	id, err := channelID(c.Params("id"))
	if err != nil {
		return clientError(c, 400, "invalid channel id")
	}
	var in membershipInput
	if err = bindJSON(c, &in); err != nil {
		return clientError(c, 400, "expected {enabled, price}")
	}
	if in.Enabled && in.Price != nil {
		if _, err = checkPrice(in.Price, minMembershipPrice); err != nil {
			return clientError(c, 400, err.Error())
		}
	}
	release, err := api.lock(c.Context(), id)
	if err != nil {
		return databaseError(c, err)
	}
	defer release()
	allowed, err := api.allowed(c.Context(), viewer(c), id, channelSettingsManage)
	if err != nil {
		return billingUnavailable(c)
	}
	if !allowed {
		return clientError(c, 404, "channel not found")
	}
	if api.jobs == nil {
		return clientError(c, 503, errJobsUnavailable.Error())
	}
	current, err := api.membershipState(c.Context(), api.pool, id)
	if err != nil {
		return databaseError(c, err)
	}
	next := membershipState{Status: membershipClosed, Free: current.Free, Sync: "pending", Revision: current.Revision + 1}
	job := membershipSyncArgs{ChannelID: id, Revision: next.Revision}
	switch {
	case in.Enabled:
		next.Status, next.Free, job.Price = membershipOpen, in.Price == nil, in.Price
	case current.Status == membershipNone:
		return clientError(c, 409, "this channel has no membership to close")
	case in.Price != nil:
		return clientError(c, 400, "a closed membership takes no price")
	}
	err = pgx.BeginFunc(c.Context(), api.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(c.Context(), `UPDATE `+api.table+` SET membership=$2,membership_free=$3,membership_sync='pending',membership_revision=$4 WHERE id=$1`, id, next.Status, next.Free, next.Revision); err != nil {
			return err
		}
		_, err := api.jobs.InsertTx(c.Context(), tx, job, nil)
		return err
	})
	if err != nil {
		return databaseError(c, err)
	}
	inline(c.Context(), "membership sync", func(ctx context.Context) error { return api.syncMembership(ctx, job) })
	v, err := api.view(c, id)
	if err != nil {
		return billingUnavailable(c)
	}
	return c.JSON(v)
}

// syncMembership requires the channel lock. Every step is idempotent, so the
// queued job may repeat an inline success.
func (api *channelAPI) syncMembership(ctx context.Context, args membershipSyncArgs) error {
	s, err := api.membershipState(ctx, api.pool, args.ChannelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // Channel deletion archives its products.
	}
	if err != nil {
		return err
	}
	if s.Revision != args.Revision || s.Sync == "active" {
		return nil
	}
	resource := membershipResource(args.ChannelID)
	switch {
	case s.Status == membershipOpen && !s.Free:
		name, err := api.name(ctx, args.ChannelID)
		if err != nil {
			return err
		}
		if err = api.billing.setOffer(ctx, resource, name+" membership", args.Price, true); err != nil {
			return err
		}
	default:
		// Archiving the product stops new checkouts only; accepted
		// subscriptions keep renewing and existing grants keep access.
		if err = api.billing.archiveResource(ctx, resource); err != nil {
			return err
		}
		if s.Status == membershipOpen && s.Free {
			if err = api.billing.paidToFree(ctx, args.ChannelID); err != nil {
				return err
			}
		}
	}
	_, err = api.pool.Exec(ctx, `UPDATE `+api.table+` SET membership_sync='active' WHERE id=$1 AND membership_revision=$2`, args.ChannelID, args.Revision)
	return err
}

// paidToFree is the policy for paid members when a membership becomes free:
// each keeps access through a free grant, and their subscription stops
// renewing at the end of the paid period. Change this one function to change
// the policy.
func (b *billingService) paidToFree(ctx context.Context, channelID string) error {
	paid, err := b.productByKey(ctx, membershipResource(channelID))
	if errors.Is(err, billing.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	params := billing.EntitlementListParams{Entitlements: []string{membershipResource(channelID)}, PageRequest: billing.PageRequest{Limit: 100}}
	for {
		page, err := b.client.ListEntitlements(ctx, params)
		if err != nil {
			return err
		}
		for _, entitlement := range page.Items {
			customer := entitlement.CustomerID
			renewing, err := b.renewing(ctx, customer, paid.ID)
			if err != nil {
				return err
			}
			if len(renewing) == 0 {
				continue
			}
			if err = b.grantFree(ctx, customer.String(), channelID); err != nil {
				return err
			}
			for _, id := range renewing {
				if _, err = b.client.CancelSubscription(ctx, id, billing.CancelSubscriptionParams{Reason: "membership became free"}); err != nil {
					return err
				}
			}
		}
		if page.Next == "" {
			return nil
		}
		params.Cursor = page.Next
	}
}

// renewing are the customer's subscriptions to product still set to renew:
// one whose member already scheduled its cancellation is left to end.
func (b *billingService) renewing(ctx context.Context, customer billing.CustomerID, product billing.ProductID) ([]billing.SubscriptionID, error) {
	out := []billing.SubscriptionID{}
	params := billing.SubscriptionListParams{CustomerID: customer, PageRequest: billing.PageRequest{Limit: 100}}
	for {
		page, err := b.client.ListSubscriptions(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, sub := range page.Items {
			if sub.ProductID == product && !sub.CancelScheduled && (sub.Status == billing.SubscriptionActive || sub.Status == billing.SubscriptionPastDue) {
				out = append(out, sub.ID)
			}
		}
		if page.Next == "" {
			return out, nil
		}
		params.Cursor = page.Next
	}
}

// freeGrants are user's live grants of the channel's free membership.
func (b *billingService) freeGrants(ctx context.Context, user, channelID string) ([]billing.ProductAccessGrant, error) {
	customer, err := customerID(user)
	if err != nil {
		return nil, err
	}
	out := []billing.ProductAccessGrant{}
	params := billing.ProductAccessListParams{CustomerIDs: []billing.CustomerID{customer}, LiveOnly: true, PageRequest: billing.PageRequest{Limit: 100}}
	for {
		page, err := b.client.ListProductAccess(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, grant := range page.Items {
			if grant.ProductKey == freeMembershipProduct(channelID) && grant.SourceType == billing.ProductAccessSourceGrant {
				out = append(out, grant)
			}
		}
		if page.Next == "" {
			return out, nil
		}
		params.Cursor = page.Next
	}
}

// grantFree requires the channel lock; it grants at most one free membership.
func (b *billingService) grantFree(ctx context.Context, user, channelID string) error {
	customer, err := customerID(user)
	if err != nil {
		return err
	}
	grants, err := b.freeGrants(ctx, user, channelID)
	if err != nil || len(grants) > 0 {
		return err
	}
	product, err := b.ensureProduct(ctx, freeMembershipProduct(channelID), "Free membership", membershipResource(channelID))
	if err != nil {
		return err
	}
	// The check above under the channel lock keeps it to one grant; a key
	// would replay a grant the member has since left.
	_, err = b.client.CreateProductAccess(ctx, billing.CreateProductAccessBatchParams{
		Items: []billing.CreateProductAccessParams{{CustomerID: customer, ProductID: product.ID, Reason: billing.GrantReasonComp}},
	})
	return err
}

// join is POST /api/v1/channels/:id/join: a free membership needs no payment.
func (api *channelAPI) join(c fiber.Ctx) error {
	return api.freeMembership(c, true)
}

// leave is POST /api/v1/channels/:id/leave: it revokes the viewer's free
// grant. Paid subscriptions are cancelled through billing.
func (api *channelAPI) leave(c fiber.Ctx) error {
	return api.freeMembership(c, false)
}

func (api *channelAPI) freeMembership(c fiber.Ctx, joining bool) error {
	user := viewer(c)
	if user == "" {
		return clientError(c, 401, "a user access token is required")
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
	s, err := api.membershipState(c.Context(), api.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return databaseError(c, err)
	}
	if joining {
		if s.Status != membershipOpen || !s.Free {
			return clientError(c, 409, "this channel has no free membership open to join")
		}
		editorial, err := api.allowed(c.Context(), user, id, channelReadPermission)
		if err != nil {
			return billingUnavailable(c)
		}
		if editorial {
			return clientError(c, 409, "you already have editorial access to this channel")
		}
		if err = api.billing.grantFree(c.Context(), user, id); err != nil {
			return billingUnavailable(c)
		}
	} else {
		grants, err := api.billing.freeGrants(c.Context(), user, id)
		if err != nil {
			return billingUnavailable(c)
		}
		for _, g := range grants {
			if err = api.billing.client.RevokeProductAccess(c.Context(), g.ID, billing.RevokeProductAccessParams{Reason: "left free membership"}); err != nil {
				return billingUnavailable(c)
			}
		}
	}
	v, err := api.view(c, id)
	if errors.Is(err, errNoChannel) {
		return clientError(c, 404, "channel not found")
	}
	if err != nil {
		return billingUnavailable(c)
	}
	return c.JSON(v)
}

// requireMembership rejects membership-dependent post policies on a channel
// that has never had a membership.
func (api *channelAPI) requireMembership(ctx context.Context, id, policy string) (bool, error) {
	if policy != "membership" && policy != "members_ppv" {
		return true, nil
	}
	s, err := api.membershipState(ctx, api.pool, id)
	if err != nil {
		return false, err
	}
	return s.Status != membershipNone, nil
}
