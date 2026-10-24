package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/open-rails/authkit/iam"
)

const seedPassword = "OnlyDemo-seed-42!"

// seedPost's price is a decimal ("4.99") for ppv and members_ppv posts.
type seedPost struct{ title, body, policy, price string }

// membership is "" for none, "free", or a paid monthly price ("9.99").
type seedCreator struct {
	username, channel, name, membership string
	posts                               []seedPost
}

var seedCreators = []seedCreator{
	{"lunarose", "lunarose", "Luna Rose", "9.99", []seedPost{
		{"Welcome to my channel", "Hi everyone! This is where I share behind-the-scenes photos from every shoot.", "public", ""},
		{"Golden hour set", "The full golden hour set from last weekend, forty unedited frames.", "membership", ""},
		{"Studio diary: lighting notes", "Every light, modifier and setting I used this month, with diagrams.", "ppv", "4.99"},
	}},
	{"chefmarco", "chefmarco", "Chef Marco", "5.99", []seedPost{
		{"Five knife skills everyone needs", "Julienne, brunoise, chiffonade, tourné and the rock chop, step by step.", "public", ""},
		{"My Sunday ragù", "The six-hour ragù my grandmother taught me, with every trick included.", "membership", ""},
		{"Fresh pasta masterclass", "A complete written masterclass: doughs, shapes, fillings and sauces.", "members_ppv", "7.99"},
	}},
	{"fitwithkai", "fitwithkai", "Fit with Kai", "7.99", []seedPost{
		{"Ten-minute morning mobility", "A short routine to loosen hips, shoulders and spine before work.", "public", ""},
		{"12-week strength plan", "Progressive overload plan with three full-body sessions each week.", "membership", ""},
		{"Nutrition guide", "Macros, meal timing and a two-week sample menu.", "ppv", "9.99"},
	}},
	{"inksketch", "inksketch", "Ink & Sketch", "free", []seedPost{
		{"Sketchbook tour", "A flip through this year's sketchbook, page by page.", "public", ""},
		{"Line weight exercises", "Six drills for confident, varied line weight. Free for members.", "membership", ""},
		{"Brush pack and process", "My custom brush pack with a full process write-up.", "members_ppv", "2.99"},
	}},
	{"cityhiker", "cityhiker", "City Hiker", "", []seedPost{
		{"Five urban walking routes", "Five routes through the old town, each under two hours.", "public", ""},
		{"The complete city guidebook", "Every route, café and viewpoint I've mapped, in one guide.", "ppv", "5.99"},
	}},
}

// seed populates display data through the running server's routes, so every
// row is made by the same AuthKit, ContentKit and OpenRails code paths. It is
// idempotent: a creator's channel and posts are reused by handle and title.
func seed(ctx context.Context, base string, out io.Writer) error {
	return withAuth(ctx, func(a *appAuth) error { return seedWith(ctx, base, a, out) })
}

func seedWith(ctx context.Context, base string, a *appAuth, out io.Writer) error {
	for i, creator := range seedCreators {
		c := newAPIClient(base, net.IPv4(127, 0, 1, byte(i+1)))
		if err := c.signIn(ctx, creator.username); err != nil {
			return fmt.Errorf("%s: %w", creator.username, err)
		}
		channel, err := c.channel(ctx, creator.channel, creator.name)
		if err != nil {
			return fmt.Errorf("%s channel: %w", creator.channel, err)
		}
		switch creator.membership {
		case "":
		case "free":
			err = c.call(ctx, "PUT", "/api/v1/products/entities/"+channel, map[string]any{"amount": "0"}, nil)
		default:
			err = c.call(ctx, "PUT", "/api/v1/products/entities/"+channel, map[string]any{"amount": creator.membership, "renew_hours": 720}, nil)
		}
		if err != nil {
			return fmt.Errorf("%s membership: %w", creator.channel, err)
		}
		var existing []struct{ Title string }
		if err = c.call(ctx, "GET", contentkitPrefix+"/v1/manage/post?limit=100&owner_group="+url.QueryEscape(channel), nil, &existing); err != nil {
			return fmt.Errorf("%s posts: %w", creator.channel, err)
		}
		seeded := map[string]bool{}
		for _, p := range existing {
			seeded[p.Title] = true
		}
		for _, p := range creator.posts {
			if seeded[p.title] {
				continue
			}
			if _, err := c.post(ctx, channel, p); err != nil {
				return fmt.Errorf("%s: %w", p.title, err)
			}
		}
		fmt.Fprintf(out, "seeded @%s (%d posts)\n", creator.channel, len(creator.posts))
	}
	if err := markSeedCreatorsVerified(ctx, a); err != nil {
		return err
	}
	fmt.Fprintf(out, "creator logins: <username>@example.test / %s\n", seedPassword)
	return nil
}

// postRule is the access rule of an OnlyDemo policy over its channel.
func postRule(policy, channel string) map[string]any {
	switch policy {
	case "membership":
		return map[string]any{"level": "group", "group_id": channel}
	case "members_ppv":
		return map[string]any{"level": "group_ppv", "group_id": channel}
	case "ppv":
		return map[string]any{"level": "ppv"}
	}
	return map[string]any{"level": "public"}
}

// Registration leaves addresses unproven, and AuthKit refuses new sign-in
// methods until one is proven. The example.test addresses cannot receive mail,
// so seed vouches for its own display accounts. AuthKit retires the password
// on a proof that does not show it, so the vouch restates it.
func markSeedCreatorsVerified(ctx context.Context, a *appAuth) error {
	for _, creator := range seedCreators {
		user, err := a.client.User(ctx, iam.UserByEmail(creator.username+"@example.test"))
		if err != nil {
			return fmt.Errorf("%s: account not found: %v", creator.username, err)
		}
		if !user.EmailVerified {
			verified, password := true, seedPassword
			if _, err := a.client.UpdateUser(ctx, iam.SystemIdentity(), user.ID, iam.UserUpdate{EmailVerified: &verified, Password: &password}); err != nil {
				return fmt.Errorf("%s verify: %w", creator.username, err)
			}
		}
	}
	return nil
}

// apiClient is one person on a running server, from its own loopback
// address: AuthKit rate-limits registration per peer.
type apiClient struct {
	base, token, id string
	client          *http.Client
}

func newAPIClient(base string, ip net.IP) *apiClient {
	return &apiClient{base: base, client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}).DialContext,
	}}}
}

// callError is a refused request.
type callError struct {
	status int
	msg    string
}

func (e *callError) Error() string { return e.msg }

func (c *apiClient) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return &callError{res.StatusCode, fmt.Sprintf("%s %s returned %d: %s", method, path, res.StatusCode, raw)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// signIn registers username if new and signs in.
func (c *apiClient) signIn(ctx context.Context, username string) error {
	credentials := map[string]any{"identifier": username + "@example.test", "password": seedPassword}
	var result struct {
		TokenSet struct {
			AccessToken string `json:"access_token"`
		} `json:"token_set"`
	}
	if err := c.call(ctx, "POST", "/auth/v1/password/login", credentials, &result); err != nil {
		if err = c.call(ctx, "POST", "/auth/v1/register", map[string]any{"identifier": username + "@example.test", "username": username, "password": seedPassword}, nil); err != nil {
			return err
		}
		if err = c.call(ctx, "POST", "/auth/v1/password/login", credentials, &result); err != nil {
			return err
		}
	}
	if c.token = result.TokenSet.AccessToken; c.token == "" {
		return fmt.Errorf("login returned no access token")
	}
	var me struct{ ID string }
	if err := c.call(ctx, "GET", "/auth/v1/me", nil, &me); err != nil {
		return err
	}
	c.id = me.ID
	return nil
}

// channel creates the viewer's channel (ContentKit provisions its AuthKit
// group with the viewer as owner), or reuses the one they own.
func (c *apiClient) channel(ctx context.Context, handle, name string) (string, error) {
	var created []struct{ ID string }
	err := c.call(ctx, "POST", contentkitPrefix+"/v1/entities", []map[string]any{{"kind": kindChannel, "slug": handle,
		"names": []map[string]string{{"language": "en", "kind": "name", "name": name}}}}, &created)
	if err == nil && len(created) == 1 {
		return created[0].ID, nil
	}
	var page struct {
		Groups []struct{ ID string } `json:"entities"`
	}
	if lookup := c.call(ctx, "GET", contentkitPrefix+"/v1/entities?kind=channel&slug="+url.QueryEscape(handle), nil, &page); lookup != nil || len(page.Groups) != 1 {
		return "", err
	}
	var g struct {
		OwnerUserID string `json:"owner_user_id"`
	}
	if lookup := c.call(ctx, "GET", contentkitPrefix+"/v1/entities/"+page.Groups[0].ID, nil, &g); lookup != nil || g.OwnerUserID != c.id {
		return "", err
	}
	return page.Groups[0].ID, nil
}

// post publishes p in channel, with its offer when it sells.
func (c *apiClient) post(ctx context.Context, channel string, p seedPost) (string, error) {
	var created struct{ ID string }
	if err := c.call(ctx, "POST", contentkitPrefix+"/v1/contents", map[string]any{"kind": kindPost, "owner_group_id": channel,
		"text": map[string]any{"en": map[string]string{"title": p.title, "body": p.body}}, "access": postRule(p.policy, channel)}, &created); err != nil {
		return "", err
	}
	if p.price != "" {
		if err := c.call(ctx, "PUT", "/api/v1/products/contents/"+created.ID, map[string]any{"amount": p.price}, nil); err != nil {
			return "", err
		}
	}
	return created.ID, c.call(ctx, "POST", contentkitPrefix+"/v1/contents/"+created.ID+"/publish", map[string]any{}, nil)
}

// removePost deletes editions explicitly; removing the final edition removes publication.
func (c *apiClient) removePost(ctx context.Context, id string) error {
	var post struct{ Versions []struct{ ID string } }
	if err := c.call(ctx, "GET", contentkitPrefix+"/v1/contents/"+id, nil, &post); err != nil {
		return err
	}
	for _, version := range post.Versions {
		if err := c.call(ctx, "DELETE", contentkitPrefix+"/v1/contents/"+id+"/versions/"+version.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}
