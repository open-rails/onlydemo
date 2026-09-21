package main

import (
	"context"
	"strings"
	"testing"
)

func TestRejectInvalidSchemasBeforeInitialization(t *testing.T) {
	for _, cfg := range []Config{{BillingSchema: "public;DROP SCHEMA public"}, {RiverSchema: "a.b"}, {AuthSchema: "Upper"}, {AppSchema: "pg_catalog"}, {AppSchema: strings.Repeat("x", 64)}} {
		if err := validateDatabaseSchemas(cfg); err == nil {
			t.Fatalf("accepted invalid schema: %+v", cfg)
		}
		if err := initializeDatabase(context.Background(), cfg, nil); err == nil {
			t.Fatal("initializer accepted invalid schema")
		}
	}
}

func TestSharedSchemasAreConfigurable(t *testing.T) {
	for _, name := range []string{"AUTH_SCHEMA", "APP_SCHEMA", "BILLING_SCHEMA", "RIVER_SCHEMA"} {
		t.Setenv(name, "public")
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
