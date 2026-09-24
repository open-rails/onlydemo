package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const seedPassword = "OnlyDemo-seed-42!"

type seedPost struct{ slug, title, body, policy, price string }

// membership is "" for none, "free", or a paid price in micro-units.
type seedCreator struct {
	username, channel, name, membership string
	posts                               []seedPost
}

var seedCreators = []seedCreator{
	{"lunarose", "lunarose", "Luna Rose", "9990000", []seedPost{
		{"welcome", "Welcome to my channel", "Hi everyone! This is where I share behind-the-scenes photos from every shoot.", "public", ""},
		{"golden-hour", "Golden hour set", "The full golden hour set from last weekend, forty unedited frames.", "membership", ""},
		{"studio-diary", "Studio diary: lighting notes", "Every light, modifier and setting I used this month, with diagrams.", "ppv", "4990000"},
	}},
	{"chefmarco", "chefmarco", "Chef Marco", "5990000", []seedPost{
		{"knife-skills", "Five knife skills everyone needs", "Julienne, brunoise, chiffonade, tourné and the rock chop, step by step.", "public", ""},
		{"sunday-ragu", "My Sunday ragù", "The six-hour ragù my grandmother taught me, with every trick included.", "membership", ""},
		{"pasta-masterclass", "Fresh pasta masterclass", "A complete written masterclass: doughs, shapes, fillings and sauces.", "members_ppv", "7990000"},
	}},
	{"fitwithkai", "fitwithkai", "Fit with Kai", "7990000", []seedPost{
		{"morning-mobility", "Ten-minute morning mobility", "A short routine to loosen hips, shoulders and spine before work.", "public", ""},
		{"12-week-plan", "12-week strength plan", "Progressive overload plan with three full-body sessions each week.", "membership", ""},
		{"nutrition-guide", "Nutrition guide", "Macros, meal timing and a two-week sample menu.", "ppv", "9990000"},
	}},
	{"inksketch", "inksketch", "Ink & Sketch", "free", []seedPost{
		{"ink-hello", "Sketchbook tour", "A flip through this year's sketchbook, page by page.", "public", ""},
		{"ink-line-weight", "Line weight exercises", "Six drills for confident, varied line weight. Free for members.", "membership", ""},
		{"ink-brush-pack", "Brush pack and process", "My custom brush pack with a full process write-up.", "members_ppv", "2990000"},
	}},
	{"cityhiker", "cityhiker", "City Hiker", "", []seedPost{
		{"hiker-routes", "Five urban walking routes", "Five routes through the old town, each under two hours.", "public", ""},
		{"hiker-guidebook", "The complete city guidebook", "Every route, café and viewpoint I've mapped, in one guide.", "ppv", "5990000"},
	}},
}

// seed populates display data through the running server's public API, so
// every row is created by the same AuthKit, OpenRails and app code paths.
// It is idempotent: existing users, channels and posts are reused, and an
// existing post is skipped so re-running never creates a duplicate offer.
func seed(ctx context.Context, base string, out io.Writer) error {
	for i, creator := range seedCreators {
		// AuthKit rate-limits registration per peer IP; each creator uses its own loopback address.
		client := seedClient{base, &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 1, byte(i+1))}}).DialContext,
		}}}
		token, err := client.login(ctx, creator.username)
		if err != nil {
			return fmt.Errorf("%s: %w", creator.username, err)
		}
		channelID, err := client.channel(ctx, token, creator)
		if err != nil {
			return fmt.Errorf("%s channel: %w", creator.channel, err)
		}
		if creator.membership != "" {
			var price any
			if creator.membership != "free" {
				price = map[string]any{"unit_amount": creator.membership, "currency": "USD"}
			}
			if err = client.call(ctx, "PUT", "/api/v1/channels/"+channelID+"/membership", token, map[string]any{"enabled": true, "price": price}, nil); err != nil {
				return fmt.Errorf("%s membership: %w", creator.channel, err)
			}
		}
		var existing []struct{ Slug string }
		if err = client.call(ctx, "GET", "/api/v1/posts?limit=50&channel_id="+channelID, token, nil, &existing); err != nil {
			return fmt.Errorf("%s posts: %w", creator.channel, err)
		}
		seeded := map[string]bool{}
		for _, item := range existing {
			seeded[item.Slug] = true
		}
		for _, p := range creator.posts {
			if seeded[p.slug] {
				continue
			}
			body := map[string]any{"channel_id": channelID, "slug": p.slug, "title": p.title, "body": p.body, "access_policy": p.policy}
			if p.price != "" {
				body["price"] = map[string]any{"unit_amount": p.price, "currency": "USD"}
			}
			if err := client.call(ctx, "POST", "/api/v1/posts", token, body, nil); err != nil {
				return fmt.Errorf("%s post: %w", p.slug, err)
			}
		}
		fmt.Fprintf(out, "seeded @%s (%d posts)\n", creator.channel, len(creator.posts))
	}
	if err := markSeedCreatorsVerified(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "creator logins: <username>@example.test / %s\n", seedPassword)
	return nil
}

// Registration leaves addresses unproven, and AuthKit refuses new sign-in
// methods until one is proven. The example.test addresses cannot receive mail,
// so seed vouches for its own display accounts.
func markSeedCreatorsVerified(ctx context.Context) error {
	return withAuth(ctx, func(a *appAuth) error {
		for _, creator := range seedCreators {
			user, err := a.client.GetUserByEmail(ctx, creator.username+"@example.test")
			if err != nil || user == nil {
				return fmt.Errorf("%s: account not found: %v", creator.username, err)
			}
			if !user.EmailVerified {
				if err := a.client.MarkEmailVerified(ctx, user.ID); err != nil {
					return fmt.Errorf("%s verify: %w", creator.username, err)
				}
			}
		}
		return nil
	})
}

type seedClient struct {
	base   string
	client *http.Client
}

func (s seedClient) call(ctx context.Context, method, path, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %d: %s", method, path, res.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (s seedClient) login(ctx context.Context, username string) (string, error) {
	credentials := map[string]any{"identifier": username + "@example.test", "password": seedPassword}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := s.call(ctx, "POST", "/auth/v1/password/login", "", credentials, &result); err != nil {
		if err = s.call(ctx, "POST", "/auth/v1/register", "", map[string]any{"identifier": username + "@example.test", "username": username, "password": seedPassword}, nil); err != nil {
			return "", err
		}
		if err = s.call(ctx, "POST", "/auth/v1/password/login", "", credentials, &result); err != nil {
			return "", err
		}
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("login returned no access token")
	}
	return result.AccessToken, nil
}

// channel creates the creator's channel, or reuses the one they already own.
func (s seedClient) channel(ctx context.Context, token string, creator seedCreator) (string, error) {
	var result struct {
		ID        string
		CanManage bool `json:"can_manage"`
	}
	if err := s.call(ctx, "POST", "/api/v1/channels", token, map[string]any{"slug": creator.channel, "name": creator.name}, &result); err != nil {
		// A taken slug is reused only when this creator already owns it.
		if lookup := s.call(ctx, "GET", "/api/v1/channels/"+creator.channel, token, nil, &result); lookup != nil || !result.CanManage {
			return "", err
		}
	}
	if result.ID == "" {
		return "", fmt.Errorf("channel create returned no id")
	}
	return result.ID, nil
}
