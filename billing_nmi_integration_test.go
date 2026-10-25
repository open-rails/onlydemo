package main

// NMI billing end to end on real services: PostgreSQL (app, AuthKit,
// OpenRails, ContentKit, River) and OpenRails's nmimock gateway on loopback.
// One fake clock drives OpenRails and the gateway, so a renewal is a clock
// step, not a wait.

import (
	"testing"
	"time"

	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
	"github.com/open-rails/openrails/openrailstest/nmimock"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestProviderWritesReadOnly(t *testing.T) {
	h := newHarness(t, harnessOptions{providerWriteMode: openrails.ProviderWritesReadOnly})
	buyer := h.register("readonlybuyer", "127.0.0.2")
	psps, err := h.srv.billing.client.ListPSPs(h.ctx, billing.PSPListParams{})
	if err != nil || len(psps.Items) != 1 {
		t.Fatalf("PSPs: %v %v", psps, err)
	}
	result := buyer.call("POST", "/billing/v1/me/payment-methods", map[string]any{
		"psp_id": psps.Items[0].ID, "token": h.nmi.Tokenize(nmimock.Card{Brand: "visa", Last4: "4242"}),
		"billing_details": map[string]any{"name": "Readonly Buyer"},
	}, "readonly-card", 502)
	// The released NMI adapter returns its provider-boundary refusal here.
	refusal, ok := result["error"].(map[string]any)
	if !ok || refusal["code"] != billing.CodePaymentProviderRejected {
		t.Fatalf("readonly result: %v", result)
	}
	if count := h.nmi.Mutations(); count != 0 {
		t.Fatalf("readonly mode made %d provider writes", count)
	}
}

// TestNMIBillingEndToEnd covers native catalog purchases and renewals through
// NMI, ContentKit access/receipts, and account-deletion event consumption.
func TestNMIBillingEndToEnd(t *testing.T) {
	h := newHarness(t, harnessOptions{clock: true})
	ctx := h.ctx
	visa := nmimock.Card{Brand: "visa", Last4: "4242"}

	owner := h.register("nmiowner", "127.0.0.2")
	buyer := h.register("nmibuyer", "127.0.0.3")
	stranger := h.register("nmistranger", "127.0.0.4")
	channelID := owner.channel("nmi-studio", "NMI studio")
	owner.call("PUT", "/api/v1/products/entities/"+channelID, map[string]any{"amount": "9.99", "renew_hours": 720}, "", 200)
	membershipPrice := buyer.groupPrice(channelID)
	included, _ := owner.post(channelID, "Members", "Members read this", map[string]any{"level": "group", "group_id": channelID})
	paid, paidVersion := owner.post(channelID, "Paid", "Paid content", map[string]any{"level": "ppv"})
	owner.call("PUT", "/api/v1/products/contents/"+paid, map[string]any{"amount": "4.99"}, "", 200)
	postPrice := buyer.contentPrice(paid)
	if buyer.opens(included) || buyer.opens(paid) || buyer.body(paid) != "" {
		t.Fatal("posts open before any purchase")
	}

	psps, err := h.srv.billing.client.ListPSPs(ctx, billing.PSPListParams{})
	if err != nil || len(psps.Items) != 1 {
		t.Fatalf("PSPs: %v %v", psps, err)
	}
	saved := buyer.call("POST", "/billing/v1/me/payment-methods", map[string]any{"psp_id": psps.Items[0].ID,
		"token": h.nmi.Tokenize(visa), "billing_details": map[string]any{"name": "NMI Buyer"}}, "save-card", 201)
	method := saved["id"].(string)
	buy := func(price, key string) {
		order := buyer.call("POST", "/billing/v1/me/orders", map[string]any{"lines": []map[string]any{{"price_id": price}}}, key, 201)
		h.confirm(buyer, order["id"].(string), method)
		h.settle()
	}

	// A purchase opens the post, and only that post.
	buy(postPrice, "post-order")
	eventually(t, "the purchase opening the post", func() bool { return buyer.opens(paid) })
	if buyer.opens(included) || buyer.body(paid) != "Paid content" {
		t.Fatal("buying a post opened the members-only post, or not its own body")
	}
	library := func() []any {
		return buyer.call("GET", "/billing/v1/me/entitlements?prefix=onlydemo:content:", nil, "", 200)["data"].([]any)
	}
	if items := library(); len(items) != 1 || items[0].(map[string]any)["entitlement"] != "onlydemo:content:"+paid+"/"+paidVersion {
		t.Fatalf("library: %v", items)
	}

	// The membership opens the channel's members-only posts.
	buy(membershipPrice, "membership-order")
	eventually(t, "the membership opening the channel", func() bool { return buyer.opens(included) })
	sub := h.subscription(buyer.id)
	vault := h.nmi.LastSale().Vault
	if n := len(h.nmi.Ledger(vault)); n != 2 {
		t.Fatalf("after the post and the membership NMI holds %d sales, want 2", n)
	}

	// Deleting the final version removes publication; billing access/history stays.
	owner.removePost(paid)
	if post := buyer.call("GET", contentkitPrefix+"/v1/contents/"+paid, nil, "", 200); post["state"] != "deleted" {
		t.Fatal("buyer lost the retained deleted post")
	}
	stranger.call("GET", contentkitPrefix+"/v1/contents/"+paid, nil, "", 404)
	if len(library()) != 1 {
		t.Fatal("deleting content erased the billing entitlement")
	}

	// Renewal: step the shared clock past the period; the due pass charges.
	end := *sub.CurrentPeriodEndsAt
	h.renewAt(end)
	sub = h.subscription(buyer.id)
	if !sub.CurrentPeriodEndsAt.After(end) || sub.Status != "active" {
		t.Fatalf("renewal did not extend the membership: %s ends %v", sub.Status, sub.CurrentPeriodEndsAt)
	}
	if got := h.nmi.Charged(vault, ""); got != 499+999+999 {
		t.Fatalf("NMI charged %d cents, want %d", got, 499+999+999)
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
	if got := h.nmi.Charged(vault, ""); got != 499+999+999 {
		t.Fatalf("a declined renewal moved money: %d cents", got)
	}
	h.assertBillingNotices(t, buyer, owner)

	// A free membership changes new sales; existing subscribers keep their terms.
	owner.call("PUT", "/api/v1/products/entities/"+channelID, map[string]any{"amount": "0"}, "", 200)
	if h.subscription(buyer.id).CancelScheduled {
		t.Fatal("catalog edit silently canceled an existing subscription")
	}
	// The host's retained-event reader cancels on account deletion and survives retry.
	if _, err := h.srv.auth.client.DeleteUsers(ctx, iam.SystemIdentity(), []string{buyer.id}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "account deletion cancellation", func() bool {
		s := h.subscription(buyer.id)
		return s.CancelScheduled || s.CanceledAt != nil || s.Status == billing.SubscriptionCanceled
	})
	worker := accountEventWorker{auth: h.srv.auth, pool: h.pool, schema: riverSchema(h.cfg)}
	if err := worker.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
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
