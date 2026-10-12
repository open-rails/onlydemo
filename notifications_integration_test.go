package main

import (
	"testing"

	"github.com/open-rails/contentkit/signal"
	"github.com/open-rails/helpers/notify"
	"github.com/open-rails/openrails"
)

// assertBillingNotices follows real sandbox billing facts into ContentKit's
// authenticated inbox, then proves the local receipt survives replay and
// reload, and that erasure fences the inbox.
func (h *harness) assertBillingNotices(t *testing.T, buyer, other peer, observer *openrails.NotificationConsumer) {
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
	inboxPath := contentkitPrefix + "/v1/notifications/inbox"
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
	path := contentkitPrefix + "/v1/notifications/" + actionable.ID
	other.call("PUT", path+"/read", nil, "", 404)
	h.anonymous("127.0.0.19").call("GET", inboxPath, nil, "", 401)
	buyer.call("PUT", path+"/read", nil, "", 200)
	var archived notify.Item
	decode(t, buyer.call("PUT", path+"/dismiss", nil, "", 200), &archived)
	replayed, err := h.srv.content.Client().PublishNotifications(h.ctx, []notify.Input{actionable.Input})
	if err != nil || len(replayed) != 1 || replayed[0].Revision != archived.Revision || replayed[0].ReadAt == nil || replayed[0].DismissedAt == nil {
		t.Fatalf("replay changed local receipt: %+v %v", replayed, err)
	}
	var restoredPage notify.Page
	decode(t, buyer.call("GET", inboxPath+"?dismissed=true", nil, "", 200), &restoredPage)
	if len(restoredPage.Items) != 1 || restoredPage.Items[0].ID != actionable.ID || restoredPage.Items[0].ReadAt == nil {
		t.Fatalf("reload lost archive/read receipt: %+v", restoredPage)
	}
	buyer.call("DELETE", path+"/dismiss", nil, "", 200)
	buyer.call("DELETE", path+"/read", nil, "", 200)
	if report, err := h.srv.content.EraseSubjects(h.ctx, []signal.Subject{{UserID: buyer.id}}); err != nil || !report.Complete() {
		t.Fatalf("erasure: %+v %v", report, err)
	}
	if again, err := h.srv.content.Client().PublishNotifications(h.ctx, []notify.Input{actionable.Input}); err != nil || len(again) != 0 {
		t.Fatalf("erased receipt recreated: %+v %v", again, err)
	}
}
