//go:build smoke

// Run explicitly with scripts/smoke.sh. This manual walkthrough uses real local
// database/auth/billing/HTTP behavior and a closed fake Stripe transport.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	"github.com/open-rails/openrails"
	openrailsembed "github.com/open-rails/openrails/embed"
	"github.com/riverqueue/river"
)

const smokeWebhookSecret = "whsec_manual_fake_only"

func main() {
	if err := smoke(); err != nil {
		fmt.Fprintln(os.Stderr, "smoke:", err)
		os.Exit(1)
	}
}

func smoke() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, err := pgxpool.ParseConfig(os.Getenv("SMOKE_DATABASE_URL"))
	if err != nil {
		return err
	}
	ip := net.ParseIP(db.ConnConfig.Host)
	if db.ConnConfig.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("SMOKE_DATABASE_URL must use localhost or a loopback IP")
	}
	admin, err := pgx.ConnectConfig(ctx, db.ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer admin.Close(context.Background())
	name := "demo_smoke_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			fmt.Fprintln(os.Stderr, "remove smoke database", name, err)
		}
	}()
	db.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, db)
	if err != nil {
		return err
	}
	defer pool.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	base := "http://" + listener.Addr().String()
	databaseURL := (&url.URL{Scheme: "postgres", User: url.UserPassword(db.ConnConfig.User, db.ConnConfig.Password), Host: net.JoinHostPort(db.ConnConfig.Host, strconv.Itoa(int(db.ConnConfig.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}).String()
	media, err := loadMediaConfig(func(key string) string { return os.Getenv(strings.ToUpper(key)) })
	if err != nil {
		return err
	}
	postDeletion, err := parsePostDeletionPolicy("", "")
	if err != nil {
		return err
	}
	membershipHours, err := parseMembershipPeriod("")
	if err != nil {
		return err
	}
	cfg := Config{PostDeletion: postDeletion, MembershipHours: membershipHours, Media: media, DatabaseURL: databaseURL, PublicURL: base, AuthIssuer: base, AuthAudience: "demo-smoke", PSPs: fakeStripePSP(smokeWebhookSecret), CheckoutPSP: "stripe"}
	if err = initializeDatabase(ctx, cfg, pool); err != nil {
		return err
	}
	stripe := &smokeStripe{}
	srv, err := startServer(ctx, cfg, pool, billingOptions{Test: func(o *openrailsembed.Options) { o.StripeTransport = stripe }})
	if err != nil {
		return err
	}
	defer srv.Close()
	auth, billing, channels, app := srv.auth, srv.billing, srv.channels, srv.app
	defer app.Shutdown()
	go func() { _ = app.Listener(listener) }()
	owner := smokePeer(base, "127.0.0.2")
	buyer := smokePeer(base, "127.0.0.3")
	ownerToken, err := owner.register("smokeowner")
	if err != nil {
		return err
	}
	buyerToken, err := buyer.register("smokebuyer")
	if err != nil {
		return err
	}
	ch, err := owner.call("POST", "/api/v1/channels", ownerToken, map[string]any{"slug": "smoke-channel", "name": "Manual smoke"}, "", 201)
	if err != nil {
		return err
	}
	if _, err := owner.call("POST", "/api/v1/channels", ownerToken, map[string]any{"slug": "smoke-channel", "name": "Duplicate"}, "", 409); err != nil {
		return fmt.Errorf("duplicate channel slug: %w", err)
	}
	post, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": ch["id"], "slug": "smoke-post", "title": "Manual purchase", "body": "Paid content", "access_policy": "ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}, "", 201)
	if err != nil {
		return err
	}
	path := "/api/v1/posts/" + post["id"].(string)
	if post, err = owner.activeOffer(path, ownerToken); err != nil {
		return err
	}
	if err = smokeChannelScopedSlugs(owner, ownerToken, ch["id"].(string), post["id"]); err != nil {
		return fmt.Errorf("channel-scoped post slugs: %w", err)
	}
	preview, err := buyer.call("GET", "/api/v1/channels/smoke-channel/posts/smoke-post", buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if preview["can_read"] != false || preview["body"] != nil {
		return fmt.Errorf("unpaid content leaked")
	}
	offers := post["offers"].([]any)
	if len(offers) == 0 {
		return fmt.Errorf("post has no offers")
	}
	purchase := map[string]any{"price_id": offers[0].(map[string]any)["price_id"], "payment": stripeRail}
	if _, err = owner.call("POST", path+"/checkout", ownerToken, purchase, "owner-refusal", 409); err != nil {
		return err
	}
	editor := smokePeer(base, "127.0.0.4")
	editorToken, err := editor.register("smokeeditor")
	if err != nil {
		return err
	}
	if _, err = owner.call("POST", "/api/v1/channels/"+ch["id"].(string)+"/members", ownerToken, map[string]any{"username": "smokeeditor", "role": "editor"}, "", 201); err != nil {
		return err
	}
	if _, err = editor.call("POST", path+"/checkout", editorToken, purchase, "editor-refusal", 409); err != nil {
		return err
	}
	checkout, err := buyer.call("POST", path+"/checkout", buyerToken, purchase, "manual-purchase", 201)
	if err != nil {
		return err
	}
	if _, err = owner.call("POST", "/api/v1/channels/"+ch["id"].(string)+"/members", ownerToken, map[string]any{"username": "smokebuyer", "role": "editor"}, "", 201); err != nil {
		return err
	}
	replay, err := buyer.call("POST", path+"/checkout", buyerToken, purchase, "manual-purchase", 201)
	if err != nil {
		return err
	}
	if checkout["id"] != replay["id"] {
		return fmt.Errorf("checkout replay created a new session")
	}
	buyerProfile, err := auth.client.GetUserByUsername(ctx, "smokebuyer")
	if err != nil {
		return err
	}
	if _, err = owner.call("DELETE", "/api/v1/channels/"+ch["id"].(string)+"/members/"+buyerProfile.ID, ownerToken, nil, "", 204); err != nil {
		return err
	}
	webhookPath := ""
	for _, route := range app.GetRoutes(true) {
		if route.Method == "POST" && strings.Contains(route.Path, "webhooks/") {
			webhookPath = strings.NewReplacer(":merchant", "onlydemo", ":provider", "stripe", ":account_id", "acct_demo_test").Replace(route.Path)
			break
		}
	}
	if webhookPath == "" {
		return fmt.Errorf("Stripe callback route missing")
	}
	if err = stripe.settle(ctx, base, webhookPath); err != nil {
		return err
	}
	if got, want := stripe.form.Get("success_url"), base+"/me?tab=library"; got != want {
		return fmt.Errorf("checkout success_url %q, want %q", got, want)
	}
	paid, err := buyer.call("GET", path, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if paid["can_read"] != true || paid["body"] != "Paid content" {
		return fmt.Errorf("signed payment did not grant access")
	}
	if _, err = buyer.call("GET", "/billing/v1/me/payments", buyerToken, nil, "", 200); err != nil {
		return err
	}
	if _, err = buyer.call("POST", "/api/v1/register", "", map[string]any{}, "", 404); err != nil {
		return err
	}
	if _, err = buyer.call("GET", "/.well-known/jwks.json", "", nil, "", 200); err != nil {
		return err
	}
	// Mutable catalog policy cannot prevent an exact accepted replay.
	if _, err = owner.call("PATCH", path, ownerToken, map[string]any{"price": map[string]any{"unit_amount": "5990000", "currency": "USD"}}, "", 200); err != nil {
		return err
	}
	acceptedReplay, err := buyer.call("POST", path+"/checkout", buyerToken, purchase, "manual-purchase", 201)
	if err != nil {
		return err
	}
	if acceptedReplay["id"] != checkout["id"] {
		return fmt.Errorf("reprice changed accepted checkout")
	}
	gatedPost := map[string]any{"channel_id": ch["id"], "slug": "members-extra", "title": "Members buy separately", "body": "Extra permanent purchase", "access_policy": "members_ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}
	if _, err = owner.call("POST", "/api/v1/posts", ownerToken, gatedPost, "", 400); err != nil {
		return fmt.Errorf("members-only post without a membership: %w", err)
	}
	channelID := ch["id"].(string)
	membershipPath := "/api/v1/channels/" + channelID + "/membership"
	if _, err = owner.call("PUT", membershipPath, ownerToken, map[string]any{"enabled": true, "price": map[string]any{"unit_amount": "990000", "currency": "USD"}}, "", 400); err != nil {
		return fmt.Errorf("membership below 1.00: %w", err)
	}
	membership, err := owner.call("PUT", membershipPath, ownerToken, map[string]any{"enabled": true, "price": map[string]any{"unit_amount": "9990000", "currency": "USD"}}, "", 200)
	if err != nil {
		return err
	}
	gated, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": ch["id"], "slug": "members-extra", "title": "Members buy separately", "body": "Extra permanent purchase", "access_policy": "members_ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}, "", 201)
	if err != nil {
		return err
	}
	if gated, err = owner.activeOffer("/api/v1/posts/"+gated["id"].(string), ownerToken); err != nil {
		return err
	}
	gatedPath := "/api/v1/posts/" + gated["id"].(string) + "/checkout"
	gatedPrice := gated["offers"].([]any)[0].(map[string]any)["price_id"]
	if _, err = buyer.call("POST", gatedPath, buyerToken, map[string]any{"price_id": gatedPrice, "payment": stripeRail}, "nonmember-refusal", 403); err != nil {
		return err
	}
	if _, err = buyer.call("POST", "/billing/v1/me/checkout", buyerToken, map[string]any{"price_id": gatedPrice}, "bypass-refusal", 404); err != nil {
		return err
	}
	membershipOffer, _ := membership["membership"].(map[string]any)["offer"].(map[string]any)
	if membershipOffer == nil {
		return fmt.Errorf("membership offer missing")
	}
	membershipPrice := membershipOffer["price_id"].(string)
	rails, err := billing.client.ListCheckoutRailOptions(ctx, membershipPrice)
	if err != nil || len(rails) == 0 {
		return fmt.Errorf("membership rails unavailable: %w", err)
	}
	action, err := buyer.call("POST", "/billing/v1/me/payment-methods/stripe-setup", buyerToken, map[string]any{"psp_id": rails[0].PSPID, "consent": true}, "manual-card-setup", 200)
	if err != nil {
		return err
	}
	method, err := buyer.call("POST", "/billing/v1/me/payment-methods/stripe-setup/"+action["id"].(string)+"/confirm", buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	included, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "slug": "included-post", "title": "Membership post", "body": "Members read this", "access_policy": "membership"}, "", 201)
	if err != nil {
		return err
	}
	includedPath := "/api/v1/posts/" + included["id"].(string)
	beforeMembership, err := buyer.call("GET", includedPath, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if beforeMembership["can_read"] != false {
		return fmt.Errorf("membership content leaked before payment")
	}
	quote, err := buyer.call("POST", "/api/v1/channels/"+channelID+"/subscribe", buyerToken, map[string]any{"price_id": membershipPrice, "payment": map[string]any{"rail": "stripe", "payment_method_id": method["payment_method_id"]}}, "manual-membership", 201)
	if err != nil {
		return err
	}
	nativeQuote, err := buyer.call("GET", "/billing/v1/me/checkout/"+quote["id"].(string), buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if nativeQuote["membership_quote"] == nil {
		return fmt.Errorf("membership quote missing")
	}
	_, err = buyer.call("POST", "/billing/v1/me/checkout/"+quote["id"].(string)+"/confirm", buyerToken, map[string]any{"payment": map[string]string{"rail": "stripe"}}, "", 200)
	if err != nil {
		return err
	}
	afterMembership, err := buyer.call("GET", includedPath, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if afterMembership["can_read"] != true {
		return fmt.Errorf("verified membership did not grant access")
	}
	future, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "slug": "future-included", "title": "New member story", "body": "Future member content", "access_policy": "membership"}, "", 201)
	if err != nil {
		return err
	}
	futureRead, err := buyer.call("GET", "/api/v1/posts/"+future["id"].(string), buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if futureRead["can_read"] != true {
		return fmt.Errorf("membership did not include future post")
	}
	gatedRead, err := buyer.call("GET", "/api/v1/posts/"+gated["id"].(string), buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if gatedRead["can_read"] != false {
		return fmt.Errorf("membership incorrectly granted separately priced post")
	}
	if err = smokeMembershipSettings(owner, ownerToken, buyer, buyerToken, smokePeer(base, "127.0.0.5"), channelID, membershipPrice, includedPath); err != nil {
		return fmt.Errorf("membership settings: %w", err)
	}
	user, err := auth.client.GetUserByUsername(ctx, "smokebuyer")
	if err != nil {
		return err
	}
	groups, err := auth.client.ListSubjectGroups(ctx, authkit.UserSubject(user.ID))
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.GroupID == channelID {
			return fmt.Errorf("subscriber became an editorial member")
		}
	}
	dashboard, err := buyer.call("GET", "/api/v1/me", buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	subscriptions := dashboard["subscriptions"].([]any)
	if len(subscriptions) != 1 {
		return fmt.Errorf("membership not present in dashboard")
	}
	subscription := subscriptions[0].(map[string]any)
	if _, err = buyer.call("DELETE", "/auth/v1/user", buyerToken, map[string]any{"password": "Manual-smoke-password-42!"}, "", 204); err != nil {
		return err
	}
	subscriptionID, err := openrails.ParseSubscriptionID(subscription["id"].(string))
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		current, err := billing.client.GetSubscription(ctx, subscriptionID)
		if err != nil {
			return err
		}
		if current.CancelScheduled || current.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("account deletion did not cancel renewable billing")
		}
		time.Sleep(100 * time.Millisecond)
	}
	recovery, err := buyer.call("POST", "/auth/v1/password/login", "", map[string]any{"identifier": "smokebuyer@example.test", "password": "Manual-smoke-password-42!"}, "", 409)
	if err != nil {
		return err
	}
	token := recovery["error"].(map[string]any)["metadata"].(map[string]any)["recovery"].(map[string]any)["token"]
	if _, err = buyer.call("POST", "/auth/v1/account/recovery/confirm", "", map[string]any{"token": token}, "", 204); err != nil {
		return err
	}
	login, err := buyer.call("POST", "/auth/v1/password/login", "", map[string]any{"identifier": "smokebuyer@example.test", "password": "Manual-smoke-password-42!"}, "", 200)
	if err != nil {
		return err
	}
	buyerToken = login["access_token"].(string)
	current, err := billing.client.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if !current.CancelScheduled && current.Status != "cancelled" {
		return fmt.Errorf("account recovery silently resumed billing")
	}
	// A policy edit never removes permanent purchase access.
	if _, err = owner.call("PATCH", path, ownerToken, map[string]any{"access_policy": "membership"}, "", 200); err != nil {
		return err
	}
	retainedPurchase, err := buyer.call("GET", path, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if retainedPurchase["purchased"] != true || retainedPurchase["can_read"] != true {
		return fmt.Errorf("policy edit removed permanent purchase")
	}
	doomed, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "slug": "doomed-post", "title": "Deleted later", "body": "Soon gone", "access_policy": "ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}, "", 201)
	if err != nil {
		return err
	}
	doomedPath := "/api/v1/posts/" + doomed["id"].(string)
	if doomed, err = owner.activeOffer(doomedPath, ownerToken); err != nil {
		return err
	}
	if _, err = owner.call("DELETE", doomedPath, ownerToken, nil, "", 204); err != nil {
		return err
	}
	if _, err = owner.call("GET", doomedPath, ownerToken, nil, "", 404); err != nil {
		return err
	}
	if _, err = buyer.call("POST", doomedPath+"/checkout", buyerToken, map[string]any{"price_id": doomed["offers"].([]any)[0].(map[string]any)["price_id"], "payment": stripeRail}, "deleted-post", 404); err != nil {
		return err
	}
	var retained bool
	if err = pool.QueryRow(ctx, `SELECT true FROM demo.posts WHERE id=$1 AND deleted_at IS NOT NULL`, doomed["id"]).Scan(&retained); err != nil {
		return fmt.Errorf("deleted post row was not retained: %w", err)
	}
	if product, e := billing.client.Products.RetrieveByKey(ctx, postResource(doomed["id"].(string))); e != nil || !product.Archived {
		return fmt.Errorf("deleted post product was not archived: %v", e)
	}
	if _, err = owner.call("DELETE", "/api/v1/channels/"+channelID, ownerToken, nil, "", 202); err != nil {
		return err
	}
	if _, err = buyer.call("GET", path, buyerToken, nil, "", 404); err != nil {
		return err
	}
	if _, err = buyer.call("GET", "/api/v1/channels/smoke-channel/posts/smoke-post", buyerToken, nil, "", 404); err != nil {
		return err
	}
	if _, err = owner.call("DELETE", "/api/v1/channels/"+channelID, ownerToken, nil, "", 404); err != nil {
		return err
	}
	group, err := auth.client.GroupInstanceByID(ctx, channelID)
	if err != nil || group.DeletedAt == nil {
		return fmt.Errorf("channel group was not retained as retired: %w", err)
	}
	var snooze *river.JobSnoozeError
	if err = channels.finishDeletion(ctx, channelID); !errors.As(err, &snooze) {
		return fmt.Errorf("early cleanup did not defer hard deletion: %w", err)
	}
	var retained bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM demo.channels c JOIN demo.posts p ON p.channel_id=c.id WHERE c.id=$1 AND c.deleted_at IS NOT NULL)`, channelID).Scan(&retained); err != nil {
		return err
	}
	if !retained {
		return fmt.Errorf("soft deletion removed retained channel/post rows")
	}
	if _, err = owner.call("DELETE", "/auth/v1/user", ownerToken, map[string]any{"password": "Manual-smoke-password-42!"}, "", 204); err != nil {
		return fmt.Errorf("retired channel still blocked owner account deletion: %w", err)
	}
	fmt.Println("Manual smoke passed: native auth, channel-scoped post slugs, resource offers/reprice replay, permanent paid access, members-only purchase refusal, native saved-card membership/quote/confirmation/cancellation, membership close/free/join/leave/paid transitions, future included posts, separate publishing roles, post soft deletion with archived product, retained channel soft deletion and owner account deletion. Zero real provider requests.")
	return nil
}

var stripeRail = map[string]string{"rail": "stripe"}

// Post slugs are unique per channel and addressed as /c/<channel>/<post>.
func smokeChannelScopedSlugs(owner smokeClient, token, channelID string, postID any) error {
	other, err := owner.call("POST", "/api/v1/channels", token, map[string]any{"slug": "smoke-other", "name": "Other smoke"}, "", 201)
	if err != nil {
		return err
	}
	twin, err := owner.call("POST", "/api/v1/posts", token, map[string]any{"channel_id": other["id"], "slug": "smoke-post", "title": "Same slug", "body": "Other channel"}, "", 201)
	if err != nil {
		return fmt.Errorf("same slug in another channel: %w", err)
	}
	dup, err := owner.call("POST", "/api/v1/posts", token, map[string]any{"channel_id": channelID, "slug": "smoke-post", "title": "Duplicate", "body": "Same channel"}, "", 409)
	if err != nil {
		return err
	}
	if dup["error"] != "That post slug is already used in this channel. Choose another." {
		return fmt.Errorf("duplicate slug message %v", dup["error"])
	}
	for _, slug := range []string{"new", "Bad Slug", "a--b"} {
		if _, err = owner.call("POST", "/api/v1/posts", token, map[string]any{"channel_id": channelID, "slug": slug, "title": "t", "body": "b"}, "", 400); err != nil {
			return fmt.Errorf("invalid slug %q: %w", slug, err)
		}
	}
	first, err := owner.call("GET", "/api/v1/channels/smoke-channel/posts/smoke-post", "", nil, "", 200)
	if err != nil {
		return err
	}
	second, err := owner.call("GET", "/api/v1/channels/smoke-other/posts/smoke-post", "", nil, "", 200)
	if err != nil {
		return err
	}
	if first["id"] != postID || second["id"] != twin["id"] || second["channel_slug"] != "smoke-other" {
		return fmt.Errorf("slug lookup resolved the wrong posts")
	}
	twinPath := "/api/v1/posts/" + twin["id"].(string)
	if _, err = owner.call("PATCH", twinPath, token, map[string]any{"slug": "smoke-renamed"}, "", 200); err != nil {
		return err
	}
	if _, err = owner.call("GET", "/api/v1/channels/smoke-other/posts/smoke-renamed", "", nil, "", 200); err != nil {
		return err
	}
	if _, err = owner.call("GET", "/api/v1/channels/smoke-other/posts/smoke-post", "", nil, "", 404); err != nil {
		return err
	}
	_, err = owner.call("DELETE", "/api/v1/channels/"+other["id"].(string), token, nil, "", 202)
	return err
}

type smokeClient struct {
	base   string
	client *http.Client
}

// smokeMembershipSettings walks close, paid-to-free, free join/leave and
// free-to-paid on a channel whose buyer holds a paid subscription.
func smokeMembershipSettings(owner smokeClient, ownerToken string, buyer smokeClient, buyerToken string, joiner smokeClient, channelID, paidPrice, includedPath string) error {
	settings := "/api/v1/channels/" + channelID + "/membership"
	readable := func(peer smokeClient, token, path string) (bool, error) {
		post, err := peer.call("GET", path, token, nil, "", 200)
		return post["can_read"] == true, err
	}
	membership := func(peer smokeClient, token string) (map[string]any, error) {
		view, err := peer.call("GET", "/api/v1/channels/"+channelID, token, nil, "", 200)
		if err != nil {
			return nil, err
		}
		return view["membership"].(map[string]any), nil
	}
	joinerToken, err := joiner.register("smokejoiner")
	if err != nil {
		return err
	}
	if _, err = owner.call("PUT", settings, ownerToken, map[string]any{"enabled": false, "price": nil}, "", 200); err != nil {
		return err
	}
	closedPost, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "slug": "after-close", "title": "For existing members", "body": "Still published", "access_policy": "membership"}, "", 201)
	if err != nil {
		return err
	}
	closedPath := "/api/v1/posts/" + closedPost["id"].(string)
	for _, path := range []string{includedPath, closedPath} {
		if ok, err := readable(buyer, buyerToken, path); err != nil || !ok {
			return fmt.Errorf("closing removed a member's access to %s: %v", path, err)
		}
	}
	if _, err = joiner.call("POST", "/api/v1/channels/"+channelID+"/subscribe", joinerToken, map[string]any{"price_id": paidPrice, "payment": map[string]any{"rail": "stripe"}}, "closed-join", 409); err != nil {
		return fmt.Errorf("closed membership sold: %w", err)
	}
	if _, err = joiner.call("POST", "/api/v1/channels/"+channelID+"/join", joinerToken, nil, "", 409); err != nil {
		return fmt.Errorf("closed paid membership joined free: %w", err)
	}
	if _, err = owner.call("PUT", settings, ownerToken, map[string]any{"enabled": true, "price": nil}, "", 200); err != nil {
		return err
	}
	view, err := membership(buyer, buyerToken)
	if err != nil {
		return err
	}
	if view["sync"] != "active" || view["member"] != true || view["free_member"] != true {
		return fmt.Errorf("paid member was not moved to a free grant: %v", view)
	}
	subscriptions, err := buyer.call("GET", "/api/v1/me", buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	for _, raw := range subscriptions["subscriptions"].([]any) {
		sub := raw.(map[string]any)
		if sub["status"] != "cancelled" && sub["cancel_scheduled"] != true {
			return fmt.Errorf("paid subscription still renews after going free: %v", sub["status"])
		}
	}
	if ok, err := readable(joiner, joinerToken, includedPath); err != nil || ok {
		return fmt.Errorf("non-member read a membership post: %v", err)
	}
	if _, err = joiner.call("POST", "/api/v1/channels/"+channelID+"/join", joinerToken, nil, "", 200); err != nil {
		return err
	}
	if ok, err := readable(joiner, joinerToken, includedPath); err != nil || !ok {
		return fmt.Errorf("free member cannot read: %v", err)
	}
	if _, err = joiner.call("POST", "/api/v1/channels/"+channelID+"/leave", joinerToken, nil, "", 200); err != nil {
		return err
	}
	if ok, err := readable(joiner, joinerToken, includedPath); err != nil || ok {
		return fmt.Errorf("left member still reads: %v", err)
	}
	if _, err = joiner.call("POST", "/api/v1/channels/"+channelID+"/join", joinerToken, nil, "", 200); err != nil {
		return err
	}
	if _, err = owner.call("PUT", settings, ownerToken, map[string]any{"enabled": true, "price": map[string]any{"unit_amount": "1000000", "currency": "USD"}}, "", 200); err != nil {
		return err
	}
	view, err = membership(joiner, joinerToken)
	if err != nil {
		return err
	}
	if view["free"] != false || view["offer"] == nil || view["member"] != true {
		return fmt.Errorf("free member lost access when membership became paid: %v", view)
	}
	return nil
}

func smokePeer(base, address string) smokeClient {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(address)}}
	return smokeClient{base, &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}}
}

// Offers are applied after the post commits; poll until the offer is active.
func (s smokeClient) activeOffer(path, token string) (map[string]any, error) {
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		p, err := s.call("GET", path, token, nil, "", 200)
		if err != nil {
			return nil, err
		}
		if p["offer_status"] == "active" && len(p["offers"].([]any)) > 0 {
			return p, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s offer is still %v", path, p["offer_status"])
		}
	}
}
func (s smokeClient) call(method, path, token string, body any, key string, status int) (map[string]any, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, s.base+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != status {
		return nil, fmt.Errorf("%s %s returned%d, want%d: %s", method, path, response.StatusCode, status, raw)
	}
	result := map[string]any{}
	if len(raw) > 0 && raw[0] == '{' {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s smokeClient) register(name string) (string, error) {
	_, err := s.call("POST", "/auth/v1/register", "", map[string]any{"identifier": name + "@example.test", "username": name, "password": "Manual-smoke-password-42!"}, "", 202)
	if err != nil {
		return "", err
	}
	login, err := s.call("POST", "/auth/v1/password/login", "", map[string]any{"identifier": name + "@example.test", "password": "Manual-smoke-password-42!"}, "", 200)
	if err != nil {
		return "", err
	}
	token, _ := login["access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("login returned no access token")
	}
	return token, nil
}

// fakeStripePSP declares the Stripe sandbox account the fake transport serves.
func fakeStripePSP(webhookSecret string) map[string]openrailsembed.PSPConfig {
	return map[string]openrailsembed.PSPConfig{"stripe": {"stripe": {AccountID: "acct_demo_test",
		Secrets:  map[string]string{"secret_key": "sk_test_fake_only", "webhook_signing_secret": webhookSecret},
		Settings: map[string]any{"publishable_key": "pk_test_fake_only"}}}}
}

// Every request is handled locally or rejected. There is no network fallback.
type smokeStripe struct {
	mu        sync.Mutex
	form      url.Values
	customers int
	setup     map[string]any
	payment   map[string]any
}

func (s *smokeStripe) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.URL.Host != "api.stripe.com" {
		return nil, fmt.Errorf("unexpected provider host")
	}
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	var data any
	switch req.Method + " " + req.URL.Path {
	case "GET /v1/account":
		data = map[string]any{"id": "acct_demo_test", "object": "account", "charges_enabled": true, "details_submitted": true, "country": "US", "default_currency": "usd"}
	case "GET /v1/balance":
		data = map[string]any{"object": "balance", "available": []any{}, "pending": []any{}, "livemode": false}
	case "GET /v1/customers/search":
		data = map[string]any{"object": "search_result", "data": []any{}, "has_more": false}
	case "GET /v1/subscriptions", "GET /v1/webhook_endpoints":
		data = map[string]any{"object": "list", "data": []any{}, "has_more": false}
	case "POST /v1/customers":
		s.customers++
		data = map[string]any{"id": fmt.Sprintf("cus_smoke%d", s.customers), "object": "customer"}
	case "POST /v1/setup_intents":
		s.setup = map[string]any{"id": "seti_smoke", "status": "requires_payment_method", "customer": req.PostForm.Get("customer"), "payment_method": "pm_smoke", "usage": "off_session", "payment_method_types": []string{"card"}, "livemode": false, "metadata": smokeMetadata(req.PostForm), "client_secret": "seti_smoke_secret_fake"}
		data = s.setup
	case "GET /v1/setup_intents/seti_smoke":
		s.setup["status"] = "succeeded"
		data = s.setup
	case "GET /v1/payment_methods/pm_smoke":
		data = map[string]any{"id": "pm_smoke", "type": "card", "customer": s.setup["customer"], "livemode": false, "card": map[string]any{"last4": "4242", "brand": "visa", "exp_month": 12, "exp_year": 2035}}
	case "POST /v1/payment_intents":
		amount, err := strconv.ParseInt(req.PostForm.Get("amount"), 10, 64)
		if err != nil {
			return nil, err
		}
		s.payment = map[string]any{"object": "payment_intent", "id": "pi_membershipsmoke", "status": "succeeded", "customer": req.PostForm.Get("customer"), "payment_method": "pm_smoke", "amount": amount, "amount_received": amount, "currency": req.PostForm.Get("currency"), "setup_future_usage": "off_session", "capture_method": "automatic", "confirmation_method": "automatic", "livemode": false, "metadata": smokeMetadata(req.PostForm), "latest_charge": "ch_membershipsmoke"}
		data = s.payment
	case "GET /v1/payment_intents/pi_membershipsmoke":
		data = s.payment
	case "GET /v1/payment_intents":
		data = map[string]any{"data": []any{s.payment}, "has_more": false}
	case "GET /v1/charges/ch_membershipsmoke":
		data = map[string]any{"id": "ch_membershipsmoke", "payment_intent": "pi_membershipsmoke", "customer": s.setup["customer"], "payment_method": "pm_smoke", "amount": 999, "amount_captured": 999, "currency": "usd", "status": "succeeded", "paid": true, "captured": true}
	case "POST /v1/checkout/sessions":
		if s.form != nil {
			return nil, fmt.Errorf("unexpected second provider checkout")
		}
		if req.PostForm.Get("line_items[0][price_data][unit_amount]") != "499" {
			return nil, fmt.Errorf("checkout did not use catalog price")
		}
		s.form = req.PostForm
		data = map[string]any{"id": "cs_smoke", "object": "checkout.session", "status": "open", "payment_status": "unpaid", "url": "https://checkout.stripe.com/c/pay/cs_smoke"}
	default:
		return nil, fmt.Errorf("unexpected fake provider request %s %s", req.Method, req.URL.Path)
	}
	encoded, err := json.Marshal(data)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: req}, err
}
func (s *smokeStripe) settle(ctx context.Context, base, webhookPath string) error {
	s.mu.Lock()
	form := s.form
	s.mu.Unlock()
	if form == nil {
		return fmt.Errorf("checkout never reached fake Stripe")
	}
	metadata := map[string]string{}
	for key, values := range form {
		if strings.HasPrefix(key, "metadata[") {
			metadata[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = values[0]
		}
	}
	payload, err := json.Marshal(map[string]any{"id": "evt_smoke_paid", "object": "event", "type": "checkout.session.completed", "livemode": false, "created": time.Now().Unix(), "data": map[string]any{"object": map[string]any{"id": "cs_smoke", "object": "checkout.session", "mode": "payment", "status": "complete", "payment_status": "paid", "customer": form.Get("customer"), "payment_intent": "pi_smoke", "amount_total": 499, "currency": "usd", "metadata": metadata}}})
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(smokeWebhookSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", base+webhookPath, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t="+timestamp+",v1="+hex.EncodeToString(mac.Sum(nil)))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("fake paid webhook returned%d", response.StatusCode)
	}
	return nil
}

func smokeMetadata(form url.Values) map[string]string {
	out := map[string]string{}
	for key, values := range form {
		if strings.HasPrefix(key, "metadata[") {
			out[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = values[0]
		}
	}
	return out
}
