package main

import (
	"context"
	"testing"
)

func TestBillingRejectsUnsafeConfigurationBeforeConnecting(t *testing.T) {
	valid := Config{
		StripeSecretKey:     "sk_test_demo",
		StripeAccountID:     "acct_demo",
		StripeWebhookSecret: "whsec_demo",
		PublicURL:           "http://localhost:3000",
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"live Stripe key", func(c *Config) { c.StripeSecretKey = "sk_live_refused" }},
		{"missing account", func(c *Config) { c.StripeAccountID = "" }},
		{"missing webhook secret", func(c *Config) { c.StripeWebhookSecret = "" }},
		{"relative public URL", func(c *Config) { c.PublicURL = "/api" }},
		{"public URL credentials", func(c *Config) { c.PublicURL = "https://name:secret@example.com" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			billing, err := newBilling(context.Background(), cfg, nil, nil, billingOptions{})
			if err == nil || billing != nil {
				t.Fatalf("unsafe configuration accepted: billing=%v error=%v", billing, err)
			}
		})
	}
}
