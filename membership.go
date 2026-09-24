package main

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/open-rails/authkit"
	"github.com/open-rails/openrails"
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
	adminGrant               = "admin"
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
	Status string                  `json:"status"`
	Free   bool                    `json:"free"`
	Sync   string                  `json:"sync"`
	Offer  *openrails.CatalogOffer `json:"offer"`
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
	allowed, err := api.allowed(c.Context(), viewer(c), id, "channel:settings:manage")
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
		return nil // Channel deletion archives the catalog.
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
		group, err := api.auth.client.GroupInstanceByID(ctx, args.ChannelID)
		if err != nil {
			return err
		}
		err = api.billing.setOffer(ctx, args.ChannelID, resource, group.DisplayName+" membership", args.Price, true, false)
		if err != nil {
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
	resource := membershipResource(channelID)
	customers, err := b.client.ListCustomersWithEntitlement(ctx, resource, time.Time{})
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(customers, 500) {
		held, err := b.client.ListActiveEntitlements(ctx, batch, time.Time{})
		if err != nil {
			return err
		}
		for _, customer := range batch {
			subs := []openrails.SubscriptionID{}
			for _, e := range held[customer] {
				// A renewing or past-due (grace) subscription grants the membership.
				if e.Entitlement == resource && (e.SourceType == "subscription" || e.SourceType == "grace") && e.SourceID != nil && e.RevokedAt == nil {
					id, err := subscriptionID(*e.SourceID)
					if err != nil {
						return err
					}
					if !slices.Contains(subs, id) {
						subs = append(subs, id)
					}
				}
			}
			// Only renewing subscriptions move to a free grant; one whose
			// member already scheduled its cancellation is left to end.
			renewing := []openrails.SubscriptionID{}
			for _, id := range subs {
				sub, err := b.client.GetSubscription(ctx, id)
				if err != nil {
					return err
				}
				if !sub.CancelScheduled && (sub.Status == "active" || sub.Status == "past_due") {
					renewing = append(renewing, id)
				}
			}
			if len(renewing) == 0 {
				continue
			}
			if len(freeGrants(held[customer], channelID)) == 0 {
				if _, err = b.client.GrantEntitlement(ctx, customer, openrails.GrantEntitlementRequest{Entitlement: resource}); err != nil {
					return err
				}
			}
			for _, id := range renewing {
				if err = b.client.CancelSubscription(ctx, id, openrails.CancelSubscriptionRequest{Reason: "membership became free"}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// subscriptionID reads an entitlement's subscription source (bare or sub_ id).
func subscriptionID(source string) (openrails.SubscriptionID, error) {
	if u, err := uuid.Parse(source); err == nil {
		return openrails.SubscriptionID(u), nil
	}
	return openrails.ParseSubscriptionID(source)
}

func freeGrants(held []openrails.EntitlementRecord, channelID string) []openrails.EntitlementRecord {
	grants := []openrails.EntitlementRecord{}
	for _, e := range held {
		if e.Entitlement == membershipResource(channelID) && e.SourceType == adminGrant && e.RevokedAt == nil {
			grants = append(grants, e)
		}
	}
	return grants
}

func (b *billingService) freeGrants(ctx context.Context, user, channelID string) ([]openrails.EntitlementRecord, error) {
	held, err := b.reads.ListEntitlements(ctx, user, time.Time{})
	return freeGrants(held, channelID), err
}

// grantFree requires the channel lock; it grants at most one free membership.
func (b *billingService) grantFree(ctx context.Context, user, channelID string) error {
	grants, err := b.freeGrants(ctx, user, channelID)
	if err != nil || len(grants) > 0 {
		return err
	}
	_, err = b.client.GrantEntitlement(ctx, user, openrails.GrantEntitlementRequest{Entitlement: membershipResource(channelID)})
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
			if err = api.billing.client.RevokeEntitlement(c.Context(), user, g.ID); err != nil {
				return billingUnavailable(c)
			}
		}
	}
	v, err := api.view(c, id)
	if errors.Is(err, authkit.ErrGroupNotFound) {
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
