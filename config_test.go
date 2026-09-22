package main

import (
	"strings"
	"testing"
)

func TestRejectInvalidSchemasBeforeInitialization(t *testing.T) {
	t.Setenv("PUBLIC_URL", "http://localhost:3000")
	for _, field := range []string{"APP_SCHEMA", "AUTH_SCHEMA", "BILLING_SCHEMA", "RIVER_SCHEMA"} {
		t.Setenv(field, "")
	}
	for _, test := range []struct{ field, value string }{
		{"BILLING_SCHEMA", "public;DROP SCHEMA public"}, {"RIVER_SCHEMA", "a.b"},
		{"AUTH_SCHEMA", "Upper"}, {"APP_SCHEMA", "pg_catalog"}, {"APP_SCHEMA", strings.Repeat("x", 64)},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv(test.field, test.value)
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("configuration accepted invalid %s: %v", test.field, err)
			}
		})
	}
}

func TestSharedSchemasAreConfigurable(t *testing.T) {
	for _, name := range []string{"AUTH_SCHEMA", "APP_SCHEMA", "BILLING_SCHEMA", "RIVER_SCHEMA"} {
		t.Setenv(name, " public ")
	}
	t.Setenv("PUBLIC_URL", "http://localhost:3000")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthSchema != "public" || cfg.AppSchema != "public" || cfg.BillingSchema != "public" || cfg.RiverSchema != "public" {
		t.Fatalf("schema settings lost: %+v", cfg)
	}
}
