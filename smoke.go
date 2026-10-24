package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
)

// smoke walks a running server through a creator's and a fan's journeys on
// ContentKit's routes, as the browser does: a channel with a free membership
// and three posts, the locked feed, joining and leaving, a post's offer and
// deletions. Payments are TestNMIBillingEndToEnd's.
func smoke(ctx context.Context, base string, out io.Writer) error {
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	run := "smoke" + hex.EncodeToString(suffix)
	creator, fan, anon := newAPIClient(base, net.IPv4(127, 0, 2, 1)), newAPIClient(base, net.IPv4(127, 0, 2, 2)), newAPIClient(base, net.IPv4(127, 0, 2, 3))
	step := func(what string, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		fmt.Fprintf(out, "ok  %s\n", what)
		return nil
	}
	if err := step("creator signs up", creator.signIn(ctx, run+"c")); err != nil {
		return err
	}
	if err := step("fan signs up", fan.signIn(ctx, run+"f")); err != nil {
		return err
	}
	channel, err := creator.channel(ctx, run, "Smoke "+run)
	if err := step("creator opens a channel", err); err != nil {
		return err
	}
	if err := step("creator makes membership free", creator.call(ctx, "PUT", "/api/v1/products/entities/"+channel, map[string]any{"amount": "0"}, nil)); err != nil {
		return err
	}
	public, err := creator.post(ctx, channel, seedPost{"Hello", "Everyone reads this.", "public", ""})
	if err := step("creator publishes a public post", err); err != nil {
		return err
	}
	members, err := creator.post(ctx, channel, seedPost{"For members", "Members read this.", "membership", ""})
	if err := step("creator publishes a members-only post", err); err != nil {
		return err
	}
	paid, err := creator.post(ctx, channel, seedPost{"For sale", "Buyers read this.", "ppv", "1.99"})
	if err := step("creator sells a post", err); err != nil {
		return err
	}

	var feed struct {
		Contents []struct {
			ID     string
			Access struct {
				Allowed bool     `json:"allowed"`
				Sell    []string `json:"sell"`
			} `json:"access"`
		} `json:"contents"`
	}
	err = anon.call(ctx, "GET", contentkitPrefix+"/v1/contents?kind=post&owner_group="+url.QueryEscape(channel), nil, &feed)
	if err == nil {
		open := map[string]bool{}
		for _, c := range feed.Contents {
			open[c.ID] = c.Access.Allowed
			if c.ID == paid && len(c.Access.Sell) == 0 {
				err = fmt.Errorf("the paid post offers nothing to buy")
			}
		}
		if err == nil && (len(open) != 3 || !open[public] || open[members] || open[paid]) {
			err = fmt.Errorf("the channel feed opens %v", open)
		}
	}
	if err := step("the channel's feed lists its posts, members-only and paid ones locked", err); err != nil {
		return err
	}
	opens := func(c *apiClient, id string) (bool, error) {
		var content struct {
			Versions []struct {
				Decision struct {
					Allowed bool `json:"allowed"`
				} `json:"decision"`
			} `json:"versions"`
		}
		if err := c.call(ctx, "GET", contentkitPrefix+"/v1/contents/"+id, nil, &content); err != nil {
			return false, err
		}
		return len(content.Versions) > 0 && content.Versions[0].Decision.Allowed, nil
	}
	expect := func(c *apiClient, id string, want bool) error {
		got, err := opens(c, id)
		if err == nil && got != want {
			err = fmt.Errorf("opens %s = %v, want %v", id, got, want)
		}
		return err
	}
	join := "/api/v1/memberships/" + channel
	if err := step("a fan joins the free membership", fan.call(ctx, "POST", join, nil, nil)); err != nil {
		return err
	}
	if err := step("membership opens the members-only post", expect(fan, members, true)); err != nil {
		return err
	}
	if err := step("membership leaves the paid post locked", expect(fan, paid, false)); err != nil {
		return err
	}
	if err := step("the fan leaves", fan.call(ctx, "DELETE", join, nil, nil)); err != nil {
		return err
	}
	if err := step("leaving locks the members-only post again", expect(fan, members, false)); err != nil {
		return err
	}
	var library struct {
		Items []struct{} `json:"data"`
	}
	err = fan.call(ctx, "GET", "/billing/v1/me/entitlements?prefix=onlydemo:content:", nil, &library)
	if err == nil && len(library.Items) != 0 {
		err = fmt.Errorf("library holds %d items", len(library.Items))
	}
	if err := step("the fan's library is empty", err); err != nil {
		return err
	}
	if err := step("creator deletes the public post", creator.removePost(ctx, public)); err != nil {
		return err
	}
	err = anon.call(ctx, "GET", contentkitPrefix+"/v1/contents/"+public, nil, nil)
	if ce, ok := err.(*callError); ok && ce.status == 404 {
		err = nil
	} else if err == nil {
		err = fmt.Errorf("a deleted post is still served")
	}
	if err := step("a deleted post is gone for readers who never bought it", err); err != nil {
		return err
	}
	return step("creator deletes the channel", creator.call(ctx, "DELETE", contentkitPrefix+"/v1/entities/"+channel, nil, nil))
}
