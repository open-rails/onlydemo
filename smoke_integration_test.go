package main

import (
	"net"
	"strings"
	"testing"
)

// TestSmoke runs the smoke walkthrough against a real server: a creator's
// and a fan's journeys on ContentKit's routes, with their own credentials.
func TestSmoke(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var out strings.Builder
	if err := smoke(h.ctx, h.base, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Log(out.String())
}

// TestSeed seeds a real server twice (seeding is idempotent), then signs in
// as a seeded creator with the password seed prints.
func TestSeed(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var out strings.Builder
	for range 2 {
		if err := seedWith(h.ctx, h.base, h.srv.auth, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
	}
	creator := newAPIClient(h.base, net.IPv4(127, 0, 3, 1))
	if err := creator.call(h.ctx, "POST", "/auth/v1/password/login", map[string]any{"identifier": seedCreators[0].username + "@example.test", "password": seedPassword}, nil); err != nil {
		t.Fatalf("a seeded creator signs in: %v", err)
	}
}
