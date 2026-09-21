package main

import (
	"context"
	"testing"
)

func TestRejectSchemaCollisionsBeforeInitialization(t *testing.T) {
	for _, cfg := range []Config{
		{BillingSchema: "demo"},
		{BillingSchema: " profiles "},
		{RiverSchema: "demo"},
		{RiverSchema: "profiles"},
		{BillingSchema: "shared", RiverSchema: "shared"},
		{BillingSchema: " public "},
		{RiverSchema: " billing "},
	} {
		t.Run(cfg.BillingSchema+"/"+cfg.RiverSchema, func(t *testing.T) {
			t.Setenv("BILLING_SCHEMA", cfg.BillingSchema)
			t.Setenv("RIVER_SCHEMA", cfg.RiverSchema)
			t.Setenv("PUBLIC_URL", "http://localhost:3000")
			t.Setenv("MIGRATIONS_ONLY", "false")
			if _, err := loadConfig(); err == nil {
				t.Fatal("configuration accepted colliding schemas")
			}
			// Nil pools prove direct callers are rejected before connecting,
			// migrating storage.
			if err := initializeDatabase(context.Background(), cfg, nil); err == nil {
				t.Fatal("initialization accepted colliding schemas")
			}
		})
	}
}
