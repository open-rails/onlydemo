package main

import (
	"testing"

	"github.com/open-rails/authkit/notify"
	"github.com/open-rails/contentkit/signal"
	"github.com/open-rails/openrails/billing"
)

// assertBillingNotices follows real sandbox billing facts into AuthKit's
// authenticated inbox, then proves the local receipt survives replay and
// reload, and that app-only erasure preserves account-owned billing notices.
func (h *harness) assertBillingNotices(t *testing.T, buyer, other peer) {
	t.Helper()
	source, err := h.srv.billing.client.ListEvents(h.ctx, billing.EventQuery{Limit: 100})
	if err != nil || len(source.Events) == 0 {
		t.Fatalf("source events: %v %v", source, err)
	}
	inboxPath := "/auth/v1/notifications/inbox"
	var inbox notify.Page
	eventually(t, "transactional billing notice", func() bool {
		decode(t, buyer.call("GET", inboxPath, nil, "", 200), &inbox)
		for _, item := range inbox.Items {
			if item.ActionRequested {
				return true
			}
		}
		return false
	})
	after, err := h.srv.billing.client.ListEvents(h.ctx, billing.EventQuery{Limit: 100})
	if err != nil || len(after.Events) < len(source.Events) {
		t.Fatalf("inbox consumed source history: %v %v", after, err)
	}
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
	if actionable.Origin.System != "openrails:onlydemo" || actionable.Origin.Scope != h.srv.billing.client.MerchantID().String() || actionable.Recipient.ID != buyer.id {
		t.Fatalf("unverified recipient mapping: %+v", actionable)
	}
	path := "/auth/v1/notifications/" + actionable.ID
	other.call("PUT", path+"/read", nil, "", 404)
	h.anonymous("127.0.0.19").call("GET", inboxPath, nil, "", 401)
	buyer.call("PUT", path+"/read", nil, "", 200)
	var archived notify.Item
	decode(t, buyer.call("PUT", path+"/dismiss", nil, "", 200), &archived)
	sink, err := h.srv.auth.client.Notifications("openrails")
	if err != nil {
		t.Fatal(err)
	}
	publication := notify.Publication{RecipientID: buyer.id, ID: actionable.Origin.ID, Revision: actionable.ProducerRevision,
		Kind: actionable.Kind, Data: actionable.Data, CreatedAt: actionable.CreatedAt, ActionRequested: actionable.ActionRequested, Severity: actionable.Severity}
	if err := sink.Publish(h.ctx, []notify.Publication{publication}); err != nil {
		t.Fatal(err)
	}
	replayed, err := h.srv.auth.client.GetNotification(h.ctx, buyer.id, actionable.ID)
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
	if report, err := h.srv.content.EraseAppSubjects(h.ctx, []signal.Subject{{UserID: buyer.id}}); err != nil || !report.Complete() {
		t.Fatalf("erasure: %+v %v", report, err)
	}
	// Erasing this app's content cannot erase the account's billing notices.
	if _, err := h.srv.auth.client.GetNotification(h.ctx, buyer.id, actionable.ID); err != nil {
		t.Fatalf("content erasure removed an account-owned billing notice: %v", err)
	}
}
