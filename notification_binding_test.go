package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/open-rails/authkit/notify"
	"github.com/open-rails/openrails"
)

func TestBillingAccountsFailClosedBeforeBinding(t *testing.T) {
	var accounts billingAccounts
	ctx := context.Background()
	if _, err := accounts.Get(ctx, []string{uuid.NewString()}); err == nil {
		t.Fatal("unbound account directory returned success")
	}
	if _, err := accounts.Search(ctx, "someone", 10); err == nil {
		t.Fatal("unbound account search returned success")
	}
	if err := accounts.Publish(ctx, []notify.Publication{{RecipientID: uuid.NewString()}}); err == nil {
		t.Fatal("unbound publication silently succeeded")
	}
	if err := accounts.Withdraw(ctx, []notify.Withdrawal{{RecipientID: uuid.NewString()}}); err == nil {
		t.Fatal("unbound withdrawal silently succeeded")
	}
	if _, ok, err := accounts.recipient(ctx, openrails.NotificationRecipient{}); err == nil || ok {
		t.Fatalf("unbound identity accepted: deliver=%v err=%v", ok, err)
	}
}
