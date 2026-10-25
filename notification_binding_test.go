package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/open-rails/authkit/notify"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
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

func TestNotificationAdoptionKeepsOnlyDemoOrigin(t *testing.T) {
	merchant := billing.MerchantID(uuid.New())
	snapshot := billing.NotificationAdoption{MerchantID: merchant, Adoptions: []notify.Adoption{{
		Previous: []notify.Origin{{System: "openrails", Scope: merchant.String(), ID: "ntf_original"}},
		Current:  notify.Mutation{Input: notify.Input{Origin: notify.Origin{System: "openrails", Scope: merchant.String(), ID: "payment:current"}}, Revision: 1},
	}}}
	got, err := onlyDemoAdoptions(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Current.Origin.System != "openrails:onlydemo" || got[0].Previous[0].System != "openrails:onlydemo" || got[0].Previous[0].Scope != merchant.String() {
		t.Fatalf("existing producer identity changed: %+v", got)
	}
	if snapshot.Adoptions[0].Previous[0].System != "openrails" || snapshot.Adoptions[0].Current.Origin.System != "openrails" {
		t.Fatal("rewrote the retained source snapshot")
	}
	snapshot.Adoptions[0].Previous[0].Scope = uuid.NewString()
	if _, err := onlyDemoAdoptions(snapshot); err == nil {
		t.Fatal("adoption accepted another merchant's receipt")
	}
}
