package main

import (
	"errors"
	"testing"

	"github.com/open-rails/helpers/notify"
	"github.com/open-rails/openrails"
)

// assertBillingNotices follows real sandbox billing facts into the authenticated
// ContentKit API, then proves the local receipt survives replay and reload.
func (h *nmiHarness) assertBillingNotices(t *testing.T, buyer, other peer, observer *openrails.NotificationConsumer) {
	t.Helper()
	if err := h.srv.notices.importEvents(h.ctx); err != nil {
		t.Fatal(err)
	}
	source, err := observer.Pending(h.ctx, 100)
	if err != nil || len(source) == 0 {
		t.Fatalf("independent consumer lost its facts: %v %v", source, err)
	}
	pending, err := h.srv.notices.source.Pending(h.ctx, 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("host source did not acknowledge delivery: %v %v", pending, err)
	}
	inboxPath := contentkitPrefix + "/notifications/inbox"
	var inbox notify.Page
	decode(t, buyer.call("GET", inboxPath, nil, "", 200), &inbox)
	var actionable *notify.Item
	for i := range inbox.Items {
		item := &inbox.Items[i]
		if item.Kind == "one_off_purchase_completed" || item.Kind == "premium_renewed" {
			t.Fatalf("optional receipt delivered by default: %s", item.Kind)
		}
		if item.ActionRequested {
			actionable = item
		}
	}
	if actionable == nil {
		t.Fatalf("declined renewal produced no actionable notice: %+v", inbox.Items)
	}
	if actionable.Status != notify.StatusNotice {
		t.Fatalf("historical request became local workflow: %+v", actionable)
	}
	if actionable.Origin.Subject != buyer.id || actionable.Recipient.ID != buyer.id {
		t.Fatalf("unverified recipient mapping: %+v", actionable)
	}
	path := contentkitPrefix + "/notifications/" + actionable.ID
	other.call("PUT", path+"/read", nil, "", 404)
	anon := peer{h: h.mediaHarness, name: "anonymous", client: h.client("127.0.0.19")}
	anon.call("GET", inboxPath, nil, "", 401)
	buyer.call("PUT", path+"/read", nil, "", 200)
	var archived notify.Item
	decode(t, buyer.call("PUT", path+"/dismiss", nil, "", 200), &archived)
	replayed, err := h.srv.notices.store.Publish(h.ctx, actionable.Input)
	if err != nil || replayed.Revision != archived.Revision || replayed.ReadAt == nil || replayed.DismissedAt == nil {
		t.Fatalf("replay changed local receipt: %+v %v", replayed, err)
	}
	var restoredPage notify.Page
	decode(t, buyer.call("GET", inboxPath+"?dismissed=true", nil, "", 200), &restoredPage)
	if len(restoredPage.Items) != 1 || restoredPage.Items[0].ID != actionable.ID || restoredPage.Items[0].ReadAt == nil {
		t.Fatalf("reload lost archive/read receipt: %+v", restoredPage)
	}
	buyer.call("DELETE", path+"/dismiss", nil, "", 200)
	buyer.call("DELETE", path+"/read", nil, "", 200)
	if err = h.srv.notices.store.EraseRecipient(h.ctx, actionable.Recipient); err != nil {
		t.Fatal(err)
	}
	if _, err = h.srv.notices.store.Publish(h.ctx, actionable.Input); !errors.Is(err, notify.ErrRecipientErased) {
		t.Fatalf("erased receipt recreated: %v", err)
	}
}
