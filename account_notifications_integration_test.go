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

func TestAccountNotifications(t *testing.T) {
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

	// The billing producer delivers into the ContentKit-scoped inbox.
	store, err := notifypg.New(h.pool, notifypg.Config{Schema: contentSchema(h.cfg), Scope: "contentkit:" + tenant})
	if err != nil {
		t.Fatal(err)
	}
	publication := notify.Publication{RecipientID: user.id, ID: "payment:current", Revision: 1,
		Kind: "payment_method_failed", CreatedAt: time.Now().UTC(), ActionRequested: true}
	if err := accounts.Publish(h.ctx, []notify.Publication{publication}); err != nil {
		t.Fatal(err)
	}
	recipient := notify.Recipient{Audience: notify.AudienceUser, ID: user.id}
	page, err := store.List(h.ctx, recipient, notify.ListOptions{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("billing notice not delivered: %+v %v", page, err)
	}
	receipt := page.Items[0]
	path := "/auth/v1/me/notifications/" + receipt.ID
	var own notify.Item
	decode(t, user.call("GET", path, nil, "", 200), &own)
	if own.ID != receipt.ID || own.Recipient.ID != user.id || own.Origin.System != "openrails:onlydemo" || own.Origin.Scope != merchant.String() {
		t.Fatalf("native route did not return the owner's receipt: %+v", own)
	}
	other.call("GET", path, nil, "", 404)
	other.call("PUT", path+"/read", nil, "", 404)
	h.anonymous("127.0.0.23").call("GET", "/auth/v1/me/notifications", nil, "", 401)
	user.call("PUT", path+"/read", nil, "", 200)
	user.call("PUT", path+"/dismiss", nil, "", 200)
	if err := accounts.Publish(h.ctx, []notify.Publication{publication}); err != nil {
		t.Fatal(err)
	}
	replayed, err := h.srv.auth.client.GetNotification(h.ctx, user.id, receipt.ID)
	if err != nil || replayed.ReadAt == nil || replayed.DismissedAt == nil {
		t.Fatalf("producer replay changed the receipt's local state: %+v %v", replayed, err)
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
	page, err = store.List(h.ctx, notify.Recipient{Audience: notify.AudienceUser, ID: foreignUser.ID}, notify.ListOptions{})
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
	page, err = store.List(h.ctx, recipient, notify.ListOptions{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("delayed publication revived a purged account receipt: %+v %v", page, err)
	}
	direct := notify.Input{Recipient: recipient, Origin: notify.Origin{System: "openrails:onlydemo", Scope: merchant.String(), ID: "payment:late"},
		Kind: "payment_method_failed", CreatedAt: time.Now().UTC()}
	if _, err := store.Publish(h.ctx, direct); !errors.Is(err, notify.ErrRecipientErased) {
		t.Fatalf("direct publisher bypassed account erasure fence: %v", err)
	}
}
