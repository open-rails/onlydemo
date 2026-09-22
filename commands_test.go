package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCommandsRejectBeforeConfigurationOrDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "not a database URL")
	t.Setenv("PUBLIC_URL", "also invalid")
	for _, args := range [][]string{{"unknown"}, {"migrate", "extra"}, {"admin"}, {"admin", "grant"}, {"admin", "revoke", "--user-id", ""}, {"admin", "grant", "--user-id", "   "}, {"admin", "grant", "extra", "--user-id", "alice"}, {"serve", "--unexpected"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "configuration") {
			t.Fatalf("command %v did not refuse before configuration: %v", args, err)
		}
	}
	for _, args := range [][]string{{"help"}, {"--help"}, {"serve", "--help"}, {"migrate", "--help"}, {"help", "admin"}, {"admin", "grant", "--help"}, {"admin", "revoke", "--help"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("help wrote no usage")
		}
	}
}

func TestValidCommandsLoadConfiguration(t *testing.T) {
	t.Setenv("PUBLIC_URL", "not a URL")
	for _, args := range [][]string{nil, {"serve"}, {"migrate"}, {"admin", "grant", "--user-id", "alice"}, {"admin", "revoke", "--user-id=alice"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "load configuration") {
			t.Fatalf("valid command %v did not reach its startup handler: %v", args, err)
		}
	}
}

func TestCommandCancellationReachesStartup(t *testing.T) {
	t.Setenv("PUBLIC_URL", "http://localhost:3000")
	t.Setenv("DATABASE_URL", "postgres://postgres@127.0.0.1:1/demo?sslmode=disable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{{"serve"}, {"migrate"}} {
		if err := run(ctx, args, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("command %v lost its shutdown context: %v", args, err)
		}
	}
}
