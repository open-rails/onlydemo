//go:build smoke

// Run explicitly with scripts/smoke.sh. This manual walkthrough uses real local
// database/auth/billing/HTTP behavior and a closed fake Stripe transport.
package main

import (
	"bytes"
	"context"
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
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/openrails"
	"github.com/open-rails/openrails/billing"
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
	membershipHours, err := parseMembershipPeriod("")
	if err != nil {
		return err
	}
	cfg := Config{MembershipHours: membershipHours, Media: media, DatabaseURL: databaseURL, PublicURL: base, ReturnOrigins: []string{base}, AuthIssuer: base, AuthAudience: "demo-smoke", Merchant: fakeStripeMerchant(smokeWebhookSecret)}
	if err = initializeDatabase(ctx, cfg, pool); err != nil {
		return err
	}
	stripe := &smokeStripe{}
	srv, err := startServer(ctx, cfg, pool, billingOptions{Test: func(d *openrails.Deps) { d.StripeTransport = stripe }})
	if err != nil {
		return err
	}
	defer srv.Close()
	auth, bill, channels, app := srv.auth, srv.billing, srv.channels, srv.app
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
	post, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": ch["id"], "title": "Manual purchase", "body": "Paid content", "access_policy": "ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}, "", 201)
	if err != nil {
		return err
	}
	path := "/api/v1/posts/" + post["code"].(string)
	if post, err = owner.activeOffer(path, ownerToken); err != nil {
		return err
	}
	if err = smokePostURLs(owner, ownerToken, post); err != nil {
		return fmt.Errorf("content URLs: %w", err)
	}
	preview, err := buyer.call("GET", path, buyerToken, nil, "", 200)
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
	purchase := map[string]any{"price_id": offers[0].(map[string]any)["price_id"]}
	if _, err = owner.call("POST", path+"/checkout", ownerToken, purchase, "", 409); err != nil {
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
	if _, err = editor.call("POST", path+"/checkout", editorToken, purchase, "", 409); err != nil {
		return err
	}
	// The buyer saves a card in the page (Stripe Elements), then pays the
	// session the app minted with it.
	psps, err := bill.client.ListPSPs(ctx, billing.PSPListParams{})
	if err != nil || len(psps.Items) != 1 {
		return fmt.Errorf("stripe PSP: %v %w", psps, err)
	}
	setup, err := buyer.call("POST", "/billing/v1/me/payment-method-setups", buyerToken, map[string]any{"psp_id": psps.Items[0].ID, "consent": true}, "manual-card-setup", 200)
	if err != nil {
		return err
	}
	confirmed, err := buyer.call("POST", "/billing/v1/me/payment-method-setups/"+setup["id"].(string)+"/confirm", buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	method, _ := confirmed["payment_method_id"].(string)
	if method == "" {
		return fmt.Errorf("card setup saved no payment method: %v", confirmed)
	}
	checkout, err := buyer.call("POST", path+"/checkout", buyerToken, purchase, "", 201)
	if err != nil {
		return err
	}
	if err = smokePay(buyer, buyerToken, checkout["id"].(string), method); err != nil {
		return err
	}
	paid, err := buyer.call("GET", path, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if paid["can_read"] != true || paid["body"] != "Paid content" {
		return fmt.Errorf("payment did not grant access")
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
	// A reprice retires the old price: it sells no longer, and the paid
	// session keeps its outcome.
	if _, err = owner.call("PATCH", path, ownerToken, map[string]any{"price": map[string]any{"unit_amount": "5990000", "currency": "USD"}}, "", 200); err != nil {
		return err
	}
	if _, err = owner.activeOffer(path, ownerToken); err != nil {
		return err
	}
	if _, err = buyer.call("POST", path+"/checkout", buyerToken, purchase, "", 404); err != nil {
		return fmt.Errorf("retired price still sells: %w", err)
	}
	accepted, err := buyer.call("GET", "/billing/v1/me/checkout-sessions/"+checkout["id"].(string), buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if accepted["status"] != "succeeded" {
		return fmt.Errorf("reprice changed the paid session: %v", accepted["status"])
	}
	gatedPost := map[string]any{"channel_id": ch["id"], "title": "Members buy separately", "body": "Extra permanent purchase", "access_policy": "members_ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}
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
	gated, err := owner.call("POST", "/api/v1/posts", ownerToken, gatedPost, "", 201)
	if err != nil {
		return err
	}
	if gated, err = owner.activeOffer("/api/v1/posts/"+gated["code"].(string), ownerToken); err != nil {
		return err
	}
	gatedPath := "/api/v1/posts/" + gated["code"].(string) + "/checkout"
	gatedPrice := gated["offers"].([]any)[0].(map[string]any)["price_id"]
	if _, err = buyer.call("POST", gatedPath, buyerToken, map[string]any{"price_id": gatedPrice}, "", 403); err != nil {
		return err
	}
	// Customers start no checkout of their own: the app's rules come first.
	if _, err = buyer.call("POST", "/billing/v1/me/checkout-sessions", buyerToken, map[string]any{"price_id": gatedPrice}, "", 404); err != nil {
		return err
	}
	membershipOffer, _ := membership["membership"].(map[string]any)["offer"].(map[string]any)
	if membershipOffer == nil {
		return fmt.Errorf("membership offer missing")
	}
	membershipPrice := membershipOffer["price_id"].(string)
	included, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "title": "Membership post", "body": "Members read this", "access_policy": "membership"}, "", 201)
	if err != nil {
		return err
	}
	includedPath := "/api/v1/posts/" + included["code"].(string)
	beforeMembership, err := buyer.call("GET", includedPath, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if beforeMembership["can_read"] != false {
		return fmt.Errorf("membership content leaked before payment")
	}
	quote, err := buyer.call("POST", "/api/v1/channels/"+channelID+"/subscribe", buyerToken, map[string]any{"price_id": membershipPrice}, "", 201)
	if err != nil {
		return err
	}
	if err = smokePay(buyer, buyerToken, quote["id"].(string), method); err != nil {
		return err
	}
	afterMembership, err := buyer.call("GET", includedPath, buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if afterMembership["can_read"] != true {
		return fmt.Errorf("verified membership did not grant access")
	}
	future, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "title": "New member story", "body": "Future member content", "access_policy": "membership"}, "", 201)
	if err != nil {
		return err
	}
	futureRead, err := buyer.call("GET", "/api/v1/posts/"+future["code"].(string), buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if futureRead["can_read"] != true {
		return fmt.Errorf("membership did not include future post")
	}
	gatedRead, err := buyer.call("GET", "/api/v1/posts/"+gated["code"].(string), buyerToken, nil, "", 200)
	if err != nil {
		return err
	}
	if gatedRead["can_read"] != false {
		return fmt.Errorf("membership incorrectly granted separately priced post")
	}
	if err = smokeMembershipSettings(owner, ownerToken, buyer, buyerToken, smokePeer(base, "127.0.0.5"), channelID, membershipPrice, includedPath); err != nil {
		return fmt.Errorf("membership settings: %w", err)
	}
	user, err := auth.client.User(ctx, iam.UserByUsername("smokebuyer"))
	if err != nil {
		return err
	}
	editorial, err := auth.channels(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, id := range editorial {
		if id == channelID {
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
	if _, err = buyer.call("DELETE", "/auth/v1/me", buyerToken, map[string]any{"password": "Manual-smoke-password-42!"}, "", 204); err != nil {
		return err
	}
	subscriptionID, err := billing.ParseSubscriptionID(subscription["id"].(string))
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		current, err := bill.client.GetSubscription(ctx, subscriptionID)
		if err != nil {
			return err
		}
		if current.CancelScheduled || current.Status == billing.SubscriptionCanceled {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("account deletion did not cancel renewable billing")
		}
		time.Sleep(100 * time.Millisecond)
	}
	recovery, err := buyer.call("POST", "/auth/v1/password/login", "", map[string]any{"identifier": "smokebuyer@example.test", "password": "Manual-smoke-password-42!"}, "", 200)
	if err != nil {
		return err
	}
	if recovery["status"] != "account_recovery_required" {
		return fmt.Errorf("deleted account signed in without recovery: %v", recovery["status"])
	}
	token := recovery["recovery"].(map[string]any)["token"]
	if _, err = buyer.call("POST", "/auth/v1/account/recovery/confirm", "", map[string]any{"token": token}, "", 204); err != nil {
		return err
	}
	if buyerToken, err = buyer.login("smokebuyer"); err != nil {
		return err
	}
	current, err := bill.client.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if !current.CancelScheduled && current.Status != billing.SubscriptionCanceled {
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
	doomed, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "title": "Deleted later", "body": "Soon gone", "access_policy": "ppv", "price": map[string]any{"unit_amount": "4990000", "currency": "USD"}}, "", 201)
	if err != nil {
		return err
	}
	doomedPath := "/api/v1/posts/" + doomed["code"].(string)
	if doomed, err = owner.activeOffer(doomedPath, ownerToken); err != nil {
		return err
	}
	if _, err = owner.call("DELETE", doomedPath, ownerToken, nil, "", 204); err != nil {
		return err
	}
	if _, err = owner.call("GET", doomedPath, ownerToken, nil, "", 404); err != nil {
		return err
	}
	if _, err = buyer.call("POST", doomedPath+"/checkout", buyerToken, map[string]any{"price_id": doomed["offers"].([]any)[0].(map[string]any)["price_id"]}, "", 404); err != nil {
		return err
	}
	var retained bool
	if err = pool.QueryRow(ctx, `SELECT true FROM demo.posts WHERE id=$1 AND deleted_at IS NOT NULL`, doomed["id"]).Scan(&retained); err != nil {
		return fmt.Errorf("deleted post row was not retained: %w", err)
	}
	if product, e := bill.client.GetProductByKey(ctx, postResource(doomed["id"].(string))); e != nil || !product.Archived {
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
	group, err := auth.client.Group(ctx, iam.GroupByID(channelID))
	if err != nil || group.DeletedAt == nil {
		return fmt.Errorf("channel group was not retained as retired: %w", err)
	}
	var snooze *river.JobSnoozeError
	if err = channels.finishDeletion(ctx, channelID); !errors.As(err, &snooze) {
		return fmt.Errorf("early cleanup did not defer hard deletion: %w", err)
	}
	retained = false
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM demo.channels c JOIN demo.posts p ON p.channel_id=c.id WHERE c.id=$1 AND c.deleted_at IS NOT NULL)`, channelID).Scan(&retained); err != nil {
		return err
	}
	if !retained {
		return fmt.Errorf("soft deletion removed retained channel/post rows")
	}
	if _, err = owner.call("DELETE", "/auth/v1/me", ownerToken, map[string]any{"password": "Manual-smoke-password-42!"}, "", 204); err != nil {
		return fmt.Errorf("retired channel still blocked owner account deletion: %w", err)
	}
	fmt.Println("Manual smoke passed: native auth, content URLs, resource offers and reprice, saved-card checkout sessions, permanent paid access, members-only purchase refusal, membership checkout and cancellation, membership close/free/join/leave/paid transitions, future included posts, separate publishing roles, post soft deletion with archived product, retained channel soft deletion and owner account deletion. Zero real provider requests.")
	return nil
}

// smokePay pays a minted checkout session with the saved card, as billing-ui does.
func smokePay(buyer smokeClient, token, session, method string) error {
	path := "/billing/v1/me/checkout-sessions/" + session
	read, err := buyer.call("GET", path, token, nil, "", 200)
	if err != nil {
		return err
	}
	options, _ := read["options"].([]any)
	if len(options) == 0 {
		return fmt.Errorf("session %s offers no payment option", session)
	}
	paid, err := buyer.call("POST", path+"/pay", token, map[string]any{"option_id": options[0].(map[string]any)["id"], "payment_method_id": method}, "", 200)
	// A charge the provider is still confirming settles in the background;
	// the page reads the session until it ends.
	for deadline := time.Now().Add(60 * time.Second); err == nil && paid["status"] == "processing" && time.Now().Before(deadline); {
		time.Sleep(2 * time.Second)
		paid, err = buyer.call("GET", path, token, nil, "", 200)
	}
	if err != nil {
		return err
	}
	if paid["status"] != "succeeded" {
		return fmt.Errorf("session %s: %v", session, paid)
	}
	return nil
}

// Posts are /p/{CODE}/{slug}: every post has its own code, the slug
// follows the title and is decoration, and the API takes codes.
func smokePostURLs(owner smokeClient, token string, post map[string]any) error {
	twin, err := owner.call("POST", "/api/v1/posts", token, map[string]any{"channel_id": post["channel_id"], "title": post["title"], "body": "Same title"}, "", 201)
	if err != nil {
		return err
	}
	if twin["code"] == post["code"] || twin["slug"] != post["slug"] || post["slug"] != "manual-purchase" {
		return fmt.Errorf("same title: %v/%v and %v/%v", post["code"], post["slug"], twin["code"], twin["slug"])
	}
	code := twin["code"].(string)
	page := "/p/" + code + "/manual-purchase"
	for from, to := range map[string]string{"/p/" + code: page, "/p/" + strings.ToLower(code) + "/old": page} {
		req, _ := http.NewRequest("GET", owner.base+from, nil)
		res, err := owner.client.Transport.RoundTrip(req)
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != 301 || res.Header.Get("Location") != to {
			return fmt.Errorf("GET %s = %d %q, want 301 %s", from, res.StatusCode, res.Header.Get("Location"), to)
		}
	}
	renamed, err := owner.call("PATCH", "/api/v1/posts/"+code, token, map[string]any{"title": "Renamed twin"}, "", 200)
	if err != nil || renamed["slug"] != "renamed-twin" {
		return fmt.Errorf("rename: %v %w", renamed, err)
	}
	_, err = owner.call("DELETE", "/api/v1/posts/"+code, token, nil, "", 204)
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
	closedPost, err := owner.call("POST", "/api/v1/posts", ownerToken, map[string]any{"channel_id": channelID, "title": "For existing members", "body": "Still published", "access_policy": "membership"}, "", 201)
	if err != nil {
		return err
	}
	closedPath := "/api/v1/posts/" + closedPost["code"].(string)
	for _, path := range []string{includedPath, closedPath} {
		if ok, err := readable(buyer, buyerToken, path); err != nil || !ok {
			return fmt.Errorf("closing removed a member's access to %s: %v", path, err)
		}
	}
	if _, err = joiner.call("POST", "/api/v1/channels/"+channelID+"/subscribe", joinerToken, map[string]any{"price_id": paidPrice}, "", 409); err != nil {
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
		if sub["status"] != "canceled" && sub["cancel_scheduled"] != true {
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
	if _, err := s.call("POST", "/auth/v1/register", "", map[string]any{"identifier": name + "@example.test", "username": name, "password": "Manual-smoke-password-42!"}, "", 200); err != nil {
		return "", err
	}
	return s.login(name)
}

func (s smokeClient) login(name string) (string, error) {
	login, err := s.call("POST", "/auth/v1/password/login", "", map[string]any{"identifier": name + "@example.test", "password": "Manual-smoke-password-42!"}, "", 200)
	if err != nil {
		return "", err
	}
	tokens, _ := login["token_set"].(map[string]any)
	token, _ := tokens["access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("login returned no access token")
	}
	return token, nil
}

// fakeStripeMerchant declares the Stripe sandbox account the fake transport serves.
func fakeStripeMerchant(webhookSecret string) openrails.MerchantDeclaration {
	return openrails.MerchantDeclaration{Slug: "onlydemo", DisplayName: "OnlyDemo", PSPs: map[string]openrails.PSPConfig{"stripe": openrails.StripePSP{
		AccountID: "acct_demo_test", SecretKey: "sk_test_fake_only", WebhookSigningSecret: webhookSecret, PublishableKey: "pk_test_fake_only",
	}.PSPConfig()}}
}

// Every request is handled locally or rejected. There is no network fallback.
type smokeStripe struct {
	mu        sync.Mutex
	customers int
	setup     map[string]any
	payments  []map[string]any // PaymentIntents in creation order
}

// payment is the PaymentIntent with id, or the one its charge belongs to.
func (s *smokeStripe) payment(id string) map[string]any {
	for _, pi := range s.payments {
		if pi["id"] == id || pi["latest_charge"] == id {
			return pi
		}
	}
	return nil
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
	case "GET /v1/subscriptions", "GET /v1/webhook_endpoints", "GET /v1/charges", "GET /v1/refunds", "GET /v1/invoices", "GET /v1/events":
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
		n := len(s.payments) + 1
		pi := map[string]any{"object": "payment_intent", "id": fmt.Sprintf("pi_smoke%d", n), "status": "succeeded", "customer": req.PostForm.Get("customer"), "payment_method": req.PostForm.Get("payment_method"), "amount": amount, "amount_received": amount, "currency": req.PostForm.Get("currency"), "setup_future_usage": req.PostForm.Get("setup_future_usage"), "capture_method": "automatic", "confirmation_method": "automatic", "livemode": false, "metadata": smokeMetadata(req.PostForm), "latest_charge": fmt.Sprintf("ch_smoke%d", n)}
		s.payments = append(s.payments, pi)
		data = pi
	case "GET /v1/payment_intents":
		list := []any{}
		for _, pi := range s.payments {
			if pi["customer"] == req.URL.Query().Get("customer") {
				list = append(list, pi)
			}
		}
		data = map[string]any{"object": "list", "data": list, "has_more": false}
	default:
		id := strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/v1/payment_intents/"), "/v1/charges/")
		switch pi := s.payment(id); {
		case req.Method == "GET" && pi != nil && strings.HasPrefix(id, "pi_"):
			data = pi
		case req.Method == "GET" && pi != nil && strings.HasPrefix(id, "ch_"):
			data = map[string]any{"id": id, "object": "charge", "payment_intent": pi["id"], "customer": pi["customer"], "payment_method": pi["payment_method"], "amount": pi["amount"], "amount_captured": pi["amount"], "currency": pi["currency"], "status": "succeeded", "paid": true, "captured": true}
		case req.Method == "GET" && strings.Count(req.URL.Path, "/") == 2:
			// A provider read of a list (refresh windows) finds nothing.
			data = map[string]any{"object": "list", "data": []any{}, "has_more": false}
		default:
			return nil, fmt.Errorf("unexpected fake provider request %s %s", req.Method, req.URL.Path)
		}
	}
	encoded, err := json.Marshal(data)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: req}, err
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
