package main

// NMI billing end to end on real services: PostgreSQL (app, AuthKit,
// OpenRails, River) and OpenRails's nmimock gateway on loopback; media only
// needs its startup probe, served by an in-process S3. One fake clock drives
// OpenRails and the gateway, so a renewal is a clock step, not a wait.
//
//	DEMO_TEST_DATABASE_URL   loopback PostgreSQL admin URL; a database is created and dropped

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/jonboulle/clockwork"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
	"github.com/open-rails/openrails/openrailstest/nmimock"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const nmiTestAccount = "demo-nmi-test"

type nmiHarness struct {
	*mediaHarness
	nmi   *nmimock.Mock
	clock *clockwork.FakeClock
}

// TestNMIBillingEndToEnd: signup, a saved card buys a post and a membership
// through app-minted checkout sessions, the membership renews, and a declined
// renewal enters dunning.
func TestNMIBillingEndToEnd(t *testing.T) {
	h := newNMIHarness(t)
	ctx := h.ctx
	visa := nmimock.Card{Brand: "visa", Last4: "4242"}

	owner := h.register("nmiowner", "127.0.0.2")
	buyer := h.register("nmibuyer", "127.0.0.3")
	channelID := owner.call("POST", "/api/v1/channels", map[string]any{"slug": "nmi-studio", "name": "NMI studio"}, "", 201)["id"].(string)
	membership := owner.call("PUT", "/api/v1/channels/"+channelID+"/membership", map[string]any{"enabled": true, "price": map[string]any{"unit_amount": "9990000", "currency": "USD"}}, "", 200)
	membershipPrice := membership["membership"].(map[string]any)["offer"].(map[string]any)["price_id"].(string)
	post := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "title": "Paid", "body": "Paid content",
		"access_policy": "ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}, "", 201)
	included := owner.call("POST", "/api/v1/posts", map[string]any{"channel_id": channelID, "title": "Members", "body": "Members read this", "access_policy": "membership"}, "", 201)
	postPath, includedPath := "/api/v1/posts/"+post["code"].(string), "/api/v1/posts/"+included["code"].(string)

	psps, err := h.srv.billing.client.ListPSPs(ctx, billing.PSPListParams{})
	if err != nil || len(psps.Items) != 1 {
		t.Fatalf("PSPs: %v %v", psps, err)
	}
	saved := buyer.call("POST", "/billing/v1/me/payment-methods", map[string]any{"psp_id": psps.Items[0].ID,
		"payment_token": h.nmi.Tokenize(visa), "billing_details": map[string]any{"name": "NMI Buyer"}}, "save-card", 201)
	method := saved["id"].(string)

	// A permanent post purchase: one sale at NMI, then access.
	var offered map[string]any
	eventually(t, "post offer", func() bool {
		offered = buyer.call("GET", postPath, nil, "", 200)
		return offered["offer_status"] == "active" && len(offered["offers"].([]any)) > 0
	})
	postPrice := offered["offers"].([]any)[0].(map[string]any)["price_id"]
	session := buyer.call("POST", postPath+"/checkout", map[string]any{"price_id": postPrice}, "", 201)
	h.confirm(buyer, session["id"].(string), method)
	eventually(t, "post purchase", func() bool { return buyer.call("GET", postPath, nil, "", 200)["purchased"] == true })

	// A membership: minted, paid with the saved card, charged once.
	quote := buyer.call("POST", "/api/v1/channels/"+channelID+"/subscribe", map[string]any{"price_id": membershipPrice}, "", 201)
	h.confirm(buyer, quote["id"].(string), method)
	h.settle()
	if buyer.call("GET", includedPath, nil, "", 200)["can_read"] != true {
		t.Fatal("membership did not grant access")
	}
	sub := h.subscription(buyer.id)
	vault := h.nmi.LastSale().Vault
	if n := len(h.nmi.Ledger(vault)); n != 2 {
		t.Fatalf("after post and membership NMI holds %d sales, want 2", n)
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
	if odd := h.nmi.Unexpected(); len(odd) > 0 {
		t.Fatalf("requests nmimock does not model: %v", odd)
	}
}

func newNMIHarness(t *testing.T) *nmiHarness {
	dbURL := os.Getenv("DEMO_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("DEMO_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	// Engine time starts in the past so everything it schedules is already
	// due on River's wall clock; the test steps it.
	clock := clockwork.NewFakeClockAt(time.Now().UTC().AddDate(-4, 0, 0).Truncate(time.Second))
	gateway := nmimock.New(nmimock.Options{Clock: clock.Now})
	t.Cleanup(gateway.Close)
	h := &nmiHarness{mediaHarness: &mediaHarness{t: t, ctx: ctx}, nmi: gateway, clock: clock}

	pool := testDatabase(t, ctx, dbURL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + listener.Addr().String()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	h.bucket = "demo-nmi"
	s3 := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(s3.Close)
	endpoint, access, secret := s3.URL, "nmi-test", "nmi-test-secret"
	store, err := mediaS3.New(mediaS3.Config{Bucket: h.bucket, Endpoint: endpoint, AccessKeyID: access, SecretAccessKey: secret, UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Client().CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &h.bucket}); err != nil {
		t.Fatal(err)
	}

	h.cfg = Config{MembershipHours: 720, DatabaseURL: pool.Config().ConnString(), PublicURL: h.base, ReturnOrigins: []string{h.base}, AuthIssuer: h.base, AuthAudience: "demo-nmi-test",
		Merchant: testMerchant(openrails.NMIPSP{AccountID: nmiTestAccount, SecurityKey: "nmimock-security-key",
			WebhookSigningSecret: "nmimock-webhook-secret", TokenizationKey: "nmimock-tokenization"}.PSPConfig()),
		Media: mediaConfig{S3Endpoint: endpoint, S3PublicEndpoint: endpoint, S3Bucket: h.bucket, S3Region: "us-east-1",
			S3AccessKeyID: access, S3SecretKey: secret, URL: "http://127.0.0.1:9", Delivery: media.DeliverCookie, CookieDomain: "localhost",
			TokenKey: "k1:" + base64.StdEncoding.EncodeToString(key), FilesPerHour: testFilesPerHour, BytesPerDay: 1 << 30, ChannelQuota: testQuota}}
	if err = initializeDatabase(ctx, h.cfg, pool); err != nil {
		t.Fatal(err)
	}
	h.srv, err = startServer(ctx, h.cfg, pool, billingOptions{
		Test:    func(d *openrails.Deps) { d.Clock = clock },
		Sandbox: &openrails.ProviderSandboxConfig{NMIGatewayURL: gateway.URL()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.srv.Close)
	go func() { _ = h.srv.app.Listener(listener) }()
	t.Cleanup(func() { _ = h.srv.app.Shutdown() })
	return h
}

// confirm pays a minted session with the buyer's saved card.
func (h *nmiHarness) confirm(p peer, session, method string) {
	path := "/billing/v1/me/checkout-sessions/" + session
	options, _ := p.call("GET", path, nil, "", 200)["options"].([]any)
	if len(options) == 0 {
		h.t.Fatalf("session %s offers no payment option", session)
	}
	option := options[0].(map[string]any)["id"]
	done := p.call("POST", path+"/pay", map[string]any{"option_id": option, "payment_method_id": method}, "", 200)
	if done["status"] != "succeeded" {
		h.t.Fatalf("checkout %s: %v", session, done)
	}
}

func (h *nmiHarness) subscription(customer string) billing.Subscription {
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
func (h *nmiHarness) renewAt(end time.Time) {
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
func (h *nmiHarness) refresh() {
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
func (h *nmiHarness) settle() {
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
