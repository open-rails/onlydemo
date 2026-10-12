package main

// NMI billing end to end on real services: PostgreSQL (app, AuthKit,
// OpenRails, ContentKit, River) and OpenRails's nmimock gateway on loopback.
// One fake clock drives OpenRails and the gateway, so a renewal is a clock
// step, not a wait.

import (
	"testing"
	"time"

	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
	"github.com/open-rails/openrails/openrailstest/nmimock"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// TestNMIBillingEndToEnd: a creator sells their channel's membership through
// ContentKit; a fan's saved card buys it with an OpenRails order for the
// offer's price, which opens the channel's members-only posts; the
// membership renews, and a declined renewal enters dunning.
func TestNMIBillingEndToEnd(t *testing.T) {
	h := newHarness(t, harnessOptions{clock: true})
	ctx := h.ctx
	observer, err := h.srv.billing.client.NotificationEvents("onlydemo-notice-proof")
	if err != nil {
		t.Fatal(err)
	}
	if err = observer.Register(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Retire(ctx) })
	visa := nmimock.Card{Brand: "visa", Last4: "4242"}

	owner := h.register("nmiowner", "127.0.0.2")
	buyer := h.register("nmibuyer", "127.0.0.3")
	if err := h.srv.auth.grantAdmin(ctx, owner.id); err != nil {
		t.Fatal(err)
	}
	channelID := owner.call("POST", "/api/v1/channels", map[string]any{"slug": "nmi-studio", "name": "NMI studio"}, "", 201)["id"].(string)
	offerPath := contentkitPrefix + "/v1/groups/" + channelID + "/offer"
	owner.call("PUT", offerPath, map[string]any{"amount": "9.99", "renew_hours": 720}, "", 200)
	var membershipPrice string
	eventually(t, "the membership price", func() bool {
		page := owner.call("GET", contentkitPrefix+"/v1/offers", nil, "", 200)
		for _, o := range page["offers"].([]any) {
			o := o.(map[string]any)
			if o["synced"] == true && o["price_id"] != nil {
				membershipPrice = o["price_id"].(string)
			}
		}
		return membershipPrice != ""
	})
	included := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "title": "Members", "body": "Members read this", "access_policy": "membership"}, "", 201)
	includedPath := "/api/v1/posts/" + included["id"].(string)
	if buyer.call("GET", includedPath, nil, "", 200)["can_read"] == true {
		t.Fatal("members-only post open before the membership")
	}

	psps, err := h.srv.billing.client.ListPSPs(ctx, billing.PSPListParams{})
	if err != nil || len(psps.Items) != 1 {
		t.Fatalf("PSPs: %v %v", psps, err)
	}
	saved := buyer.call("POST", "/billing/v1/me/payment-methods", map[string]any{"psp_id": psps.Items[0].ID,
		"token": h.nmi.Tokenize(visa), "billing_details": map[string]any{"name": "NMI Buyer"}}, "save-card", 201)
	method := saved["id"].(string)

	// The membership: an order for the offer's price, paid with the saved card.
	order := buyer.call("POST", "/billing/v1/me/orders", map[string]any{"lines": []map[string]any{{"price_id": membershipPrice}}}, "membership-order", 201)
	h.confirm(buyer, order["id"].(string), method)
	h.settle()
	eventually(t, "the membership opening the post", func() bool { return buyer.call("GET", includedPath, nil, "", 200)["can_read"] == true })
	sub := h.subscription(buyer.id)
	vault := h.nmi.LastSale().Vault
	if n := len(h.nmi.Ledger(vault)); n != 1 {
		t.Fatalf("after the membership NMI holds %d sales, want 1", n)
	}

	// Renewal: step the shared clock past the period; the due pass charges.
	end := *sub.CurrentPeriodEndsAt
	h.renewAt(end)
	sub = h.subscription(buyer.id)
	if !sub.CurrentPeriodEndsAt.After(end) || sub.Status != "active" {
		t.Fatalf("renewal did not extend the membership: %s ends %v", sub.Status, sub.CurrentPeriodEndsAt)
	}
	if got := h.nmi.Charged(vault, ""); got != 999+999 {
		t.Fatalf("NMI charged %d cents, want %d", got, 999+999)
	}

	// Decline: the issuer refuses the next renewal; the membership enters dunning.
	h.nmi.SetDecline(visa.Last4, "202")
	h.renewAt(*sub.CurrentPeriodEndsAt)
	declined := h.nmi.LastDecline()
	if declined == nil || declined.Vault != vault || declined.Declined != "202" {
		t.Fatalf("no declined renewal at NMI: %+v", declined)
	}
	sub = h.subscription(buyer.id)
	if sub.Status != "past_due" {
		t.Fatalf("declined renewal left the membership %q, want past_due", sub.Status)
	}
	if got := h.nmi.Charged(vault, ""); got != 999+999 {
		t.Fatalf("a declined renewal moved money: %d cents", got)
	}
	h.assertBillingNotices(t, buyer, owner, observer)
	if odd := h.nmi.Unexpected(); len(odd) > 0 {
		t.Fatalf("requests nmimock does not model: %v", odd)
	}
}

// confirm pays a minted session with the buyer's saved card.
func (h *harness) confirm(p peer, orderID, method string) {
	path := "/billing/v1/me/orders/" + orderID
	order := p.call("GET", path, nil, "", 200)
	done := p.call("POST", path+"/pay", map[string]any{"expected_total": order["total"], "payment": map[string]any{"payment_method_id": method}}, "", 200)
	if done["status"] != "complete" {
		h.t.Fatalf("order %s: %v", orderID, done)
	}
}

func (h *harness) subscription(customer string) billing.Subscription {
	h.t.Helper()
	id, err := billing.ParseCustomerID(customer)
	if err != nil {
		h.t.Fatal(err)
	}
	subs, err := h.srv.billing.client.ListSubscriptions(h.ctx, billing.SubscriptionListParams{CustomerID: id})
	if err != nil || len(subs.Items) != 1 {
		h.t.Fatalf("subscriptions of %s: %v %v", customer, subs, err)
	}
	return subs.Items[0]
}

type duePass struct{}

func (duePass) Kind() string { return "openrails.dunning" }

// renewAt steps the shared clock just past end and runs OpenRails's
// scheduled due pass once, then waits for the work it accepted. OpenRails
// holds renewals on a PSP without fresh provider coverage, so the PSP is
// refreshed first, as its two-hourly refresh would have been.
func (h *harness) renewAt(end time.Time) {
	h.t.Helper()
	h.clock.Advance(end.Add(-time.Minute).Sub(h.clock.Now()))
	h.refresh()
	h.clock.Advance(end.Sub(h.clock.Now()) + time.Second)
	res, err := h.srv.jobs.Insert(h.ctx, duePass{}, &river.InsertOpts{Queue: openrails.QueueBilling})
	if err != nil {
		h.t.Fatal(err)
	}
	eventually(h.t, "due pass", func() bool {
		job, err := h.srv.jobs.JobGet(h.ctx, res.Job.ID)
		return err == nil && job.State == rivertype.JobStateCompleted
	})
	h.settle()
}

// refresh runs a provider refresh of the PSPs to completion. Its bounded
// catch-up snoozes between history windows; due snoozes are promoted at once.
func (h *harness) refresh() {
	h.t.Helper()
	res, err := h.srv.billing.client.RefreshPSPs(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	eventually(h.t, "provider refresh", func() bool {
		job, err := h.srv.jobs.JobGet(h.ctx, res.JobID)
		if err != nil {
			return false
		}
		if job.State == rivertype.JobStateDiscarded {
			h.t.Fatalf("provider refresh failed: %v", job.Errors)
		}
		if job.State == rivertype.JobStateScheduled && !job.ScheduledAt.After(time.Now()) {
			_, _ = h.srv.jobs.JobRetry(h.ctx, job.ID)
		}
		return job.State == rivertype.JobStateCompleted
	})
	h.settle()
}

// settle waits until no billing operation is runnable. Jobs the engine
// scheduled for a moment already past are promoted at once.
func (h *harness) settle() {
	h.t.Helper()
	kinds := []string{"openrails.provider_operation", "openrails.subscription_converge"}
	eventually(h.t, "billing work", func() bool {
		scheduled, err := h.srv.jobs.JobList(h.ctx, river.NewJobListParams().Kinds(kinds...).States(rivertype.JobStateScheduled, rivertype.JobStateRetryable).First(100))
		if err != nil {
			return false
		}
		for _, job := range scheduled.Jobs {
			if !job.ScheduledAt.After(time.Now()) {
				_, _ = h.srv.jobs.JobRetry(h.ctx, job.ID)
			}
		}
		busy, err := h.srv.jobs.JobList(h.ctx, river.NewJobListParams().Kinds(kinds...).
			States(rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStatePending).First(10))
		return err == nil && len(busy.Jobs) == 0
	})
}
