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
	"github.com/open-rails/contentkit"
	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/contentkit/signal"
	"github.com/open-rails/openrails/billing"
)

var (
	rbac = authkit.NewRoles()

	// ContentKit's staff permissions, declared in the root scope.
	contentkitPerms = rbac.Root.Declare(access.Names(access.Permissions()...)...)

	// A channel is a ContentKit group whose AuthKit group (its auth_scope)
	// holds these: owners hold all of them, editors publish posts.
	// settings:manage and media:write are what ContentKit checks in the scope.
	channelPersona          = rbac.Persona("channel")
	channelReadPermission   = channelPersona.Permission("posts", "read")
	channelCreatePermission = channelPersona.Permission("posts", "create")
	channelEditPermission   = channelPersona.Permission("posts", "edit")
	channelRemovePermission = channelPersona.Permission("posts", "delete")
	channelSettingsRead     = channelPersona.Permission("settings", "read")
	channelSettingsManage   = channelPersona.Permission("settings", "manage")
	channelMediaWrite       = channelPersona.Permission("media", "write")
	channelOwnerRole        = channelPersona.Owner
	channelEditorRole       = channelPersona.Role("editor", channelReadPermission, channelCreatePermission, channelEditPermission, channelRemovePermission, channelSettingsRead, channelPersona.Members.Read)

	// Site admins moderate every post, group and offer, and run billing for
	// customers.
	postReadPermission   = rbac.Root.Permission("posts", "read")
	postEditPermission   = rbac.Root.Permission("posts", "edit")
	postDeletePermission = rbac.Root.Permission("posts", "delete")
	customersRead        = rbac.Root.Permission(billing.RootCustomerRead.Resource(), billing.RootCustomerRead.Action())
	customersUpdate      = rbac.Root.Permission(billing.RootCustomerManage.Resource(), billing.RootCustomerManage.Action())
	adminRole            = rbac.Root.Role("admin", append([]iam.Grant{postReadPermission, postEditPermission, postDeletePermission, customersRead, customersUpdate},
		staffGrants(access.GroupsWrite, access.OffersWrite, access.CommentsModerate, access.CommentsBan, access.ModerationReview)...)...)
)

// staffGrants are the declared ContentKit permissions among ps.
func staffGrants(ps ...access.Permission) []iam.Grant {
	var out []iam.Grant
	for _, p := range contentkitPerms {
		if slices.Contains(ps, access.Permission(p.String())) {
			out = append(out, p)
		}
	}
	return out
}

type appAuth struct {
	client  *authkit.Client
	billing *billingService
	content *contentkit.Engine
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

// Billing reads current contacts through AuthKit's directory. Account deletion
// still stops renewals; AuthKit retries a failure before acknowledging its event.
func (a *appAuth) onEvent(ctx context.Context, event iam.Event) error {
	if event.Kind == iam.EventUserDeleted {
		return a.cancelDeletedAccountBilling(ctx, event.UserID)
	}
	return nil
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

// A purge also erases the account from ContentKit; AuthKit retries until
// every plane is erased.
func (a *appAuth) purgeDeletedAccount(ctx context.Context, deletion iam.UserDeletion) error {
	if err := a.cancelDeletedAccountBilling(ctx, deletion.UserID); err != nil {
		return err
	}
	if a.content == nil {
		return errors.New("content lifecycle has not been composed")
	}
	report, err := a.content.EraseSubjects(ctx, []signal.Subject{{UserID: deletion.UserID}})
	if err == nil && !report.Complete() {
		err = fmt.Errorf("content erasure pending: %v", report.Remaining)
	}
	return err
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
	user, err := a.client.User(ctx, iam.UserByID(id))
	if err != nil {
		return iam.User{}, fmt.Errorf("read account %s: %w", id, err)
	}
	return user, nil
}

// live reports an account that holds authority: not deleted, not banned.
func (a *appAuth) live(ctx context.Context, user string) (bool, error) {
	users, err := a.client.Users(ctx, []string{user})
	if err != nil {
		return false, err
	}
	u, ok := users[user]
	return ok && u.DeletedAt == nil && u.Ban == nil, nil
}
