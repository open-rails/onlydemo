package main

import (
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
