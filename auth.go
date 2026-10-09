package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	"github.com/open-rails/authkit/iam"
	"github.com/open-rails/openrails/billing"
)

var (
	rbac = authkit.NewRoles()

	// A channel is a permission group: its owners manage it, its editors publish.
	channelPersona          = rbac.Persona("channel")
	channelReadPermission   = channelPersona.Permission("posts", "read")
	channelCreatePermission = channelPersona.Permission("posts", "create")
	channelEditPermission   = channelPersona.Permission("posts", "edit")
	channelRemovePermission = channelPersona.Permission("posts", "delete")
	channelSettingsRead     = channelPersona.Permission("settings", "read")
	channelSettingsManage   = channelPersona.Permission("settings", "manage")
	channelOwnerRole        = channelPersona.Owner
	channelEditorRole       = channelPersona.Role("editor", channelReadPermission, channelCreatePermission, channelEditPermission, channelRemovePermission, channelSettingsRead, channelPersona.Members.Read)

	// Site admins moderate every post and run billing for customers.
	postReadPermission   = rbac.Root.Permission("posts", "read")
	postEditPermission   = rbac.Root.Permission("posts", "edit")
	postDeletePermission = rbac.Root.Permission("posts", "delete")
	customersRead        = rbac.Root.Permission("customers", "read")
	customersUpdate      = rbac.Root.Permission("customers", "update")
	adminRole            = rbac.Root.Role("admin", postReadPermission, postEditPermission, postDeletePermission, customersRead, customersUpdate)
)

type appAuth struct {
	client  *authkit.Client
	billing *billingService
	media   *mediaService
	// observe sees each AuthKit read a request makes (tests count them).
	observe func(context.Context)
}

func (a *appAuth) read(ctx context.Context) {
	if a.observe != nil {
		a.observe(ctx)
	}
}

func (a *appAuth) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.client.Close(ctx); err != nil {
		log.Printf("close AuthKit: %v", err)
	}
}

// newAuth builds AuthKit; New creates or upgrades its tables.
func newAuth(ctx context.Context, config Config, pool *pgxpool.Pool) (*appAuth, error) {
	a := &appAuth{}
	keys, totpKey, err := authKeys(config.AuthKeysPath)
	if err != nil {
		return nil, err
	}
	client, err := authkit.New(ctx, authkit.Config{
		Database: authkit.DatabaseConfig{Schema: strings.TrimSpace(config.AuthSchema), RiverSchema: riverSchema(config)},
		HTTP:     &authkit.HTTPConfig{DirectPeerIP: true, APIPath: "/auth", RefreshCookie: true},
		Roles:    rbac,
		Token: authkit.TokenConfig{
			Issuer:              config.AuthIssuer,
			IssuedAudiences:     []string{config.AuthAudience},
			ExpectedAudiences:   []string{config.AuthAudience},
			AccessTokenDuration: 15 * time.Minute,
		},
		Registration: authkit.RegistrationConfig{
			NativeUserMode: iam.RegistrationModeOpen,
			Verification:   iam.RegistrationVerificationOptional,
		},
		Keys: keys,
		TwoFactor: authkit.TwoFactorConfig{
			Mode:          iam.TwoFactorOptional,
			Methods:       []iam.TwoFactorMethod{iam.TwoFactorTOTP, iam.TwoFactorEmail},
			TOTPSecretKey: totpKey,
		},
	}, authkit.Deps{Postgres: pool, Email: logEmail{}, OnEvent: a.onEvent, OnPurge: a.purgeDeletedAccount})
	if err != nil {
		return nil, err
	}
	a.client = client
	return a, nil
}

// riverSchema is the host fleet's schema, which AuthKit and OpenRails share.
func riverSchema(cfg Config) string {
	if schema := strings.TrimSpace(cfg.RiverSchema); schema != "" {
		return schema
	}
	return "public"
}

// authKeys keeps dev signing and TOTP keys under path so sessions and
// authenticator apps survive restarts; an empty path keeps both in memory.
func authKeys(path string) (authkit.KeysConfig, []byte, error) {
	keys := authkit.KeysConfig{AllowEphemeralDevKeys: true}
	if path == "" {
		key := make([]byte, 32)
		_, err := rand.Read(key)
		return keys, key, err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return keys, nil, err
	}
	keys.Path = path
	file := filepath.Join(path, "totp.key")
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return keys, nil, err
		}
		if err := os.WriteFile(file, []byte(hex.EncodeToString(key)), 0o600); err != nil {
			return keys, nil, err
		}
	} else if err != nil {
		return keys, nil, err
	}
	return keys, nil, nil
}

// logEmail is the demo's outbox: sandbox mail goes to the server log.
type logEmail struct{}

func (logEmail) Send(_ context.Context, msg iam.EmailMessage) error {
	log.Printf("[outbox] to=%s kind=%s purpose=%s code=%s link=%s", msg.To, msg.Kind, msg.Purpose, msg.Code, msg.Link)
	return nil
}

func (logEmail) CheckHealth(context.Context) error { return nil }

// onEvent keeps billing's customer facts current with the account, and a
// deleted account's renewals stop. AuthKit retries a failure.
func (a *appAuth) onEvent(ctx context.Context, event iam.Event) error {
	switch event.Kind {
	case iam.EventUserDeleted:
		if err := a.cancelDeletedAccountBilling(ctx, event.UserID); err != nil {
			return err
		}
		return a.pushCustomer(ctx, event.UserID)
	case iam.EventUserRegistered, iam.EventUserEmailChanged, iam.EventUserUsernameChanged,
		iam.EventUserBanned, iam.EventUserUnbanned, iam.EventUserRestored:
		return a.pushCustomer(ctx, event.UserID)
	}
	return nil
}

// pushCustomer declares the account's facts to billing: a deleted or banned
// account is blocked and has no email.
func (a *appAuth) pushCustomer(ctx context.Context, userID string) error {
	if a.billing == nil {
		return errors.New("billing lifecycle has not been composed")
	}
	customer, err := billing.ParseCustomerID(userID)
	if err != nil {
		return err
	}
	users, err := a.client.Users(ctx, []string{userID})
	if err != nil {
		return err
	}
	p := billing.EnsureCustomerParams{ID: customer, Blocked: true}
	if user, ok := users[userID]; ok {
		p.Blocked = user.DeletedAt != nil || user.Ban != nil
		if !p.Blocked && user.Email != nil && *user.Email != "" {
			p.Email = user.Email
		}
		if user.Username != "" {
			p.Username = &user.Username
		}
	}
	_, err = a.billing.client.EnsureCustomers(ctx, []billing.EnsureCustomerParams{p})
	return err
}

// cancelDeletedAccountBilling stops the account's renewals, keeping the paid
// period and permanent purchases; restoring the account does not restart them.
func (a *appAuth) cancelDeletedAccountBilling(ctx context.Context, userID string) error {
	if a.billing == nil {
		return errors.New("billing lifecycle has not been composed")
	}
	customer, err := billing.ParseCustomerID(userID)
	if err != nil {
		return err
	}
	params := billing.SubscriptionListParams{CustomerID: customer, PageRequest: billing.PageRequest{Limit: 100}}
	for {
		page, err := a.billing.client.ListSubscriptions(ctx, params)
		if err != nil {
			return err
		}
		for _, sub := range page.Items {
			if sub.CancelScheduled || sub.CanceledAt != nil || sub.Status == billing.SubscriptionCanceled {
				continue
			}
			if _, err := a.billing.client.CancelSubscription(ctx, sub.ID, billing.CancelSubscriptionParams{Reason: "account deleted", AccountDeletion: true}); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		params.Cursor = page.Next
	}
}

// A purge also erases the account's avatar folder.
func (a *appAuth) purgeDeletedAccount(ctx context.Context, deletion iam.UserDeletion) error {
	if err := a.cancelDeletedAccountBilling(ctx, deletion.UserID); err != nil {
		return err
	}
	if a.media == nil {
		return errors.New("media lifecycle has not been composed")
	}
	return a.media.eraseUser(ctx, deletion.UserID)
}

// grantAdmin is only called by the explicit, one-off admin:grant command.
// Normal startup never restores a permission that an operator has revoked.
func (a *appAuth) grantAdmin(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("a registered user ID is required")
	}
	_, err := a.client.SetGroupRole(ctx, iam.SystemIdentity(), iam.RootGroup(), iam.UserSubject(userID), adminRole)
	if errors.Is(err, iam.ErrUserNotFound) {
		return errors.New("admin user does not exist; register the user first")
	}
	return err
}

// revokeAdmin is the matching explicit operator command; ordinary requests
// cannot invoke either bootstrap method.
func (a *appAuth) revokeAdmin(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("a registered user ID is required")
	}
	return a.client.RemoveGroupMember(ctx, iam.SystemIdentity(), iam.RootGroup(), iam.UserSubject(userID))
}

// can reports a user's permission in a group, checked live (bans, deletion
// and role changes apply at once).
func (a *appAuth) can(ctx context.Context, user string, group iam.GroupRef, perm iam.Perm) (bool, error) {
	if user == "" {
		return false, nil
	}
	a.read(ctx)
	return a.client.Can(ctx, iam.UserIdentity(user), group, perm)
}

// effective reads a live user's grants on each channel, their root grants
// included, in one call (a banned or deleted account has none).
func (a *appAuth) effective(ctx context.Context, user string, channels []string) (map[string][]iam.Perm, error) {
	out := map[string][]iam.Perm{}
	if user == "" || len(channels) == 0 {
		return out, nil
	}
	refs := []iam.GroupRef{iam.RootGroup()}
	for _, id := range channels {
		refs = append(refs, iam.GroupByID(id))
	}
	a.read(ctx)
	perms, err := a.client.EffectivePermissions(ctx, iam.UserIdentity(user), refs)
	if err != nil {
		return nil, err
	}
	var root []iam.Perm
	for id, grants := range perms {
		if !slices.Contains(channels, id) {
			root = grants // the only other group asked for
		}
	}
	for _, id := range channels {
		if grants := append(slices.Clone(perms[id]), root...); len(grants) > 0 {
			out[id] = grants
		}
	}
	return out, nil
}

func (a *appAuth) user(ctx context.Context, id string) (iam.User, error) {
	a.read(ctx)
	user, err := a.client.User(ctx, iam.UserByID(id))
	if err != nil {
		return iam.User{}, fmt.Errorf("read account %s: %w", id, err)
	}
	return user, nil
}

// live reports an account that holds authority: not deleted, not banned.
func (a *appAuth) live(ctx context.Context, user string) (bool, error) {
	a.read(ctx)
	users, err := a.client.Users(ctx, []string{user})
	if err != nil {
		return false, err
	}
	u, ok := users[user]
	return ok && u.DeletedAt == nil && u.Ban == nil, nil
}

// channels lists the channels user holds a role in.
func (a *appAuth) channels(ctx context.Context, user string) ([]string, error) {
	ids := []string{}
	page := iam.PageRequest{Limit: 100}
	for {
		a.read(ctx)
		memberships, err := a.client.ListMemberships(ctx, iam.UserSubject(user), page)
		if err != nil {
			return nil, err
		}
		for _, m := range memberships.Items {
			if m.Group.Persona == channelPersona.Persona {
				ids = append(ids, m.Group.ID)
			}
		}
		if memberships.Next == "" {
			return ids, nil
		}
		page.Cursor = memberships.Next
	}
}
