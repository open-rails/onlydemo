package main

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-rails/authkit"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/authkit/notify"
	notifypg "github.com/open-rails/authkit/notify/postgres"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
)

func TestAccountNotificationAdoption(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	user := h.register("noticeowner", "127.0.0.21")
	other := h.register("noticeother", "127.0.0.22")
	merchant := h.srv.billing.client.MerchantID()
	var accounts billingAccounts
	if err := accounts.bind(h.srv.auth.client, merchant, h.cfg.AuthIssuer); err != nil {
		t.Fatal(err)
	}

	// The public adapter admits only the resolved merchant and native issuer.
	identity := openrails.NotificationRecipient{MerchantID: merchant, CustomerID: billing.CustomerID(uuid.MustParse(user.id)), Issuer: h.cfg.AuthIssuer}
	if id, deliver, err := accounts.recipient(h.ctx, identity); err != nil || !deliver || id != user.id {
		t.Fatalf("native recipient refused: %q %v %v", id, deliver, err)
	}
	foreign := identity
	foreign.MerchantID = billing.MerchantID(uuid.New())
	if _, deliver, err := accounts.recipient(h.ctx, foreign); err != nil || deliver {
		t.Fatalf("foreign merchant mapped to this inbox: %v %v", deliver, err)
	}
	foreign = identity
	foreign.Issuer = "https://other.example.test"
	if _, deliver, err := accounts.recipient(h.ctx, foreign); err != nil || deliver {
		t.Fatalf("foreign issuer mapped to this inbox: %v %v", deliver, err)
	}
	directory, err := accounts.Get(h.ctx, []string{user.id})
	if err != nil || directory[user.id].Username != user.name {
		t.Fatalf("native account directory unavailable after binding: %+v %v", directory, err)
	}

	// Keep the old producer's complete origin and old receipt state. Only its
	// event-shaped source ID becomes the producer's current deterministic ID.
	store, err := notifypg.New(h.pool, notifypg.Config{Schema: contentSchema(h.cfg), Scope: "contentkit:" + tenant})
	if err != nil {
		t.Fatal(err)
	}
	old := notify.Input{Recipient: notify.Recipient{Audience: notify.AudienceUser, ID: user.id},
		Origin: notify.Origin{System: "openrails:onlydemo", Scope: merchant.String(), ID: "ntf_previous"},
		Kind:   "payment_method_failed", CreatedAt: time.Now().UTC(), ActionRequested: true}
	receipt, err := store.Publish(h.ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	path := "/auth/v1/notifications/" + receipt.ID
	other.call("PUT", path+"/read", nil, "", 404)
	h.anonymous("127.0.0.23").call("GET", "/auth/v1/notifications/inbox", nil, "", 401)
	user.call("PUT", path+"/read", nil, "", 200)
	var archived notify.Item
	decode(t, user.call("PUT", path+"/dismiss", nil, "", 200), &archived)
	current := notify.Mutation{Input: receipt.Input, Revision: 1}
	current.Origin = notify.Origin{System: "openrails", Scope: merchant.String(), ID: "payment:current"}
	current.Visibility = notify.VisibilityTenant
	batch, err := onlyDemoAdoptions(billing.NotificationAdoption{MerchantID: merchant, Adoptions: []notify.Adoption{{
		Previous: []notify.Origin{{System: "openrails", Scope: merchant.String(), ID: old.Origin.ID}}, Current: current,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.srv.auth.client.AdoptNotifications(h.ctx, "openrails", batch); err != nil {
			t.Fatal(err)
		}
	}
	adopted, err := h.srv.auth.client.GetNotification(h.ctx, user.id, receipt.ID)
	if err != nil || adopted.ID != receipt.ID || adopted.ReadAt == nil || adopted.DismissedAt == nil ||
		!adopted.ReadAt.Equal(*archived.ReadAt) || !adopted.DismissedAt.Equal(*archived.DismissedAt) ||
		adopted.Origin.System != old.Origin.System || adopted.Origin.Scope != old.Origin.Scope || adopted.Origin.ID != "payment:current" {
		t.Fatalf("adoption changed receipt identity or local state: %+v %v", adopted, err)
	}
	publication := notify.Publication{RecipientID: user.id, ID: adopted.Origin.ID, Revision: adopted.ProducerRevision,
		Kind: adopted.Kind, Data: adopted.Data, CreatedAt: adopted.CreatedAt, ActionRequested: adopted.ActionRequested, Severity: adopted.Severity}
	if err := accounts.Publish(h.ctx, []notify.Publication{publication}); err != nil {
		t.Fatal(err)
	}
	replayed, err := h.srv.auth.client.GetNotification(h.ctx, user.id, receipt.ID)
	if err != nil || replayed.Revision != adopted.Revision || replayed.DismissedAt == nil {
		t.Fatalf("producer replay changed the adopted receipt: %+v %v", replayed, err)
	}

	foreignTenant := iam.NewTenantID()
	if err := h.srv.auth.client.CreateTenant(h.ctx, iam.Tenant{ID: foreignTenant}); err != nil {
		t.Fatal(err)
	}
	foreignAccounts, err := h.srv.auth.client.ForTenant(h.ctx, foreignTenant)
	if err != nil {
		t.Fatal(err)
	}
	foreignUser, err := foreignAccounts.CreateUser(h.ctx, iam.NewUser{Username: "foreignnotice"})
	if err != nil {
		t.Fatal(err)
	}
	foreignPublication := publication
	foreignPublication.RecipientID = foreignUser.ID
	if err := accounts.Publish(h.ctx, []notify.Publication{foreignPublication}); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(h.ctx, notify.Recipient{Audience: notify.AudienceUser, ID: foreignUser.ID}, notify.ListOptions{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("another tenant's account received a notice: %+v %v", page, err)
	}

	results, err := h.srv.auth.client.PurgeUsers(h.ctx, []string{user.id})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatalf("account purge refused: %+v %v", results, err)
	}
	eventually(t, "account purge finishes", func() bool {
		_, err := h.srv.auth.client.User(h.ctx, iam.UserByID(user.id), authkit.IncludeDeleted())
		return errors.Is(err, iam.ErrUserNotFound)
	})
	if err := accounts.Publish(h.ctx, []notify.Publication{publication}); err != nil {
		t.Fatal(err)
	}
	page, err = store.List(h.ctx, old.Recipient, notify.ListOptions{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("delayed publication revived a purged account receipt: %+v %v", page, err)
	}
	if _, err := store.Publish(h.ctx, old); !errors.Is(err, notify.ErrRecipientErased) {
		t.Fatalf("legacy publisher bypassed account erasure fence: %v", err)
	}
}
