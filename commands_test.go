package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCommandsRejectBeforeConfigurationOrDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "not a database URL")
	t.Setenv("PUBLIC_URL", "also invalid")
	for _, args := range [][]string{{"unknown"}, {"migrate", "extra"}, {"admin"}, {"admin", "grant"}, {"admin", "revoke", "--user-id", ""}, {"serve", "--unexpected"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "configuration") {
			t.Fatalf("command %v did not refuse before configuration: %v", args, err)
		}
	}
	for _, args := range [][]string{{"help"}, {"--help"}, {"admin", "grant", "--help"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("help wrote no usage")
		}
	}
}

func TestCommandModes(t *testing.T) {
	for _, test := range []struct {
		args []string
		want command
	}{
		{nil, command{kind: "serve"}}, {[]string{"serve"}, command{kind: "serve"}}, {[]string{"migrate"}, command{kind: "migrate"}},
		{[]string{"admin", "grant", "--user-id", "alice"}, command{kind: "admin", userID: "alice"}},
		{[]string{"admin", "revoke", "--user-id", "alice"}, command{kind: "admin", userID: "alice", revoke: true}},
	} {
		got, err := parseCommand(test.args, &bytes.Buffer{})
		if err != nil || got != test.want {
			t.Fatalf("parse %v: %+v %v", test.args, got, err)
		}
	}
}
