package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-rails/openrails/permissions"
)

func TestBillingRejectsUnsafeConfigurationBeforeConnecting(t *testing.T) {
	valid := Config{
		StripeSecretKey:      "sk_test_demo",
		StripeAccountID:      "acct_demo",
		StripeWebhookSecret:  "whsec_demo",
		BillingEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		BillingDatabaseURL:   "postgres://unused",
		PublicURL:            "http://localhost:3000",
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"live Stripe key", func(c *Config) { c.StripeSecretKey = "sk_live_refused" }},
		{"missing account", func(c *Config) { c.StripeAccountID = "" }},
		{"missing webhook secret", func(c *Config) { c.StripeWebhookSecret = "" }},
		{"missing encryption key", func(c *Config) { c.BillingEncryptionKey = "" }},
		{"short encryption key", func(c *Config) { c.BillingEncryptionKey = "YWJj" }},
		{"missing billing database", func(c *Config) { c.BillingDatabaseURL = "" }},
		{"relative public URL", func(c *Config) { c.PublicURL = "/api" }},
		{"public URL credentials", func(c *Config) { c.PublicURL = "https://name:secret@example.com" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			billing, err := newBilling(context.Background(), cfg, nil)
			if err == nil || billing != nil {
				t.Fatalf("unsafe configuration accepted: billing=%v error=%v", billing, err)
			}
		})
	}
}

func TestStripeBootstrapGateOnlyAllowsProviderConfiguration(t *testing.T) {
	gate := stripeBootstrapGate{}
	path := "/billing/v1/merchant/payment-providers/stripe"
	for _, tc := range []struct {
		method, path, permission string
		allowed                  bool
	}{
		{http.MethodPut, path, permissions.MerchantPaymentProvidersUpdate, true},
		{http.MethodGet, path, permissions.MerchantPaymentProvidersUpdate, false},
		{http.MethodPut, path + "/extra", permissions.MerchantPaymentProvidersUpdate, false},
		{http.MethodPut, path, permissions.MerchantCatalogUpdate, false},
	} {
		_, err := gate.Authorize(context.Background(), httptest.NewRequest(tc.method, tc.path, nil), tc.permission)
		if (err == nil) != tc.allowed {
			t.Errorf("%s %s %s allowed=%v, want %v", tc.method, tc.path, tc.permission, err == nil, tc.allowed)
		}
	}
}
