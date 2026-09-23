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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit"
	"github.com/open-rails/authkit/authhttp"
	"github.com/open-rails/authkit/embedded"
	"github.com/open-rails/openrails"
)

const (
	postReadPermission                      = "root:posts:read"
	postEditPermission                      = "root:posts:edit"
	postDeletePermission                    = "root:posts:delete"
	channelPersona          authkit.Persona = "channel"
	channelReadPermission   authkit.Perm    = "channel:posts:read"
	channelCreatePermission authkit.Perm    = "channel:posts:create"
	channelEditPermission   authkit.Perm    = "channel:posts:edit"
	channelRemovePermission authkit.Perm    = "channel:posts:delete"
)

type appAuth struct {
	runtime *embedded.Runtime
	client  authkit.Client
	billing *openrails.Client
}

func (a *appAuth) Close() {
	a.runtime.Close()
}

func newAuth(ctx context.Context, config Config, pool *pgxpool.Pool) (*appAuth, error) {
	a := &appAuth{}
	keys, totpKey, err := authKeys(config.AuthKeysPath)
	if err != nil {
		return nil, err
	}
	ownership := embedded.RiverFromHost()
	runtime, err := embedded.New(embedded.Config{
		Schema: strings.TrimSpace(config.AuthSchema),
		HTTP:   authhttp.Config{DirectPeerIP: true, Mount: authhttp.MountOptions{APIPrefix: "/auth/v1", RefreshCookie: true}},
		RBAC: []embedded.PersonaDef{embedded.IntrinsicRootPersona(embedded.RoleDef{
			Name:        "admin",
			Permissions: []string{postReadPermission, postEditPermission, postDeletePermission},
		}), {
			Name: channelPersona, Parent: embedded.RootPersona,
			Catalog: []string{string(channelReadPermission), string(channelCreatePermission), string(channelEditPermission), string(channelRemovePermission)},
			Roles:   []embedded.RoleDef{{Name: "editor", Permissions: []string{string(channelReadPermission), string(channelCreatePermission), string(channelEditPermission), string(channelRemovePermission), "channel:settings:read", "channel:members:read", "channel:roles:read"}}},
		}},
		Token: embedded.TokenConfig{
			Issuer:              config.AuthIssuer,
			IssuedAudiences:     []string{config.AuthAudience},
			ExpectedAudiences:   []string{config.AuthAudience},
			AccessTokenDuration: 15 * time.Minute,
		},
		Registration: embedded.RegistrationConfig{
			NativeUserMode: embedded.RegistrationModeOpen,
			Verification:   embedded.RegistrationVerificationOptional,
		},
		Keys: keys,
		Ephemeral: embedded.EphemeralConfig{
			AllowMemory: true,
		},
		TwoFactor: embedded.TwoFactorConfig{
			Mode:          embedded.TwoFactorOptional,
			Methods:       []embedded.TwoFactorMethod{embedded.TwoFactorTOTP, embedded.TwoFactorEmail},
			TOTPSecretKey: totpKey,
		},
	}, embedded.Deps{Postgres: pool, River: ownership, Email: logEmail{}, OnSoftDelete: a.cancelDeletedAccountBilling, OnHardDelete: a.cancelDeletedAccountBilling})
	if err != nil {
		return nil, err
	}
	a.runtime, a.client = runtime, runtime.Client()
	return a, nil
}

// authKeys keeps dev signing and TOTP keys under path so sessions and
// authenticator apps survive restarts; an empty path keeps both in memory.
func authKeys(path string) (embedded.KeysConfig, []byte, error) {
	keys := embedded.KeysConfig{AllowEphemeralDevKeys: true}
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

func (logEmail) send(to, what string) error {
	log.Printf("[outbox] to=%s %s", to, what)
	return nil
}
func (m logEmail) SendVerification(_ context.Context, email, _ string, msg embedded.VerificationMessage) error {
	return m.send(email, fmt.Sprintf("purpose=%s code=%s link=%s", msg.Purpose, msg.Code, msg.LinkURL))
}
func (m logEmail) SendPasswordResetLink(_ context.Context, email, _, resetURL string) error {
	return m.send(email, "password reset "+resetURL)
}
func (m logEmail) SendAccountRegistrationInvite(_ context.Context, email, inviteURL string) error {
	return m.send(email, "registration invite "+inviteURL)
}
func (m logEmail) SendLoginCode(_ context.Context, email, _, code string) error {
	return m.send(email, "login code="+code)
}
func (m logEmail) SendWelcome(_ context.Context, email, _ string) error {
	return m.send(email, "welcome")
}
func (m logEmail) SendContactChanged(_ context.Context, email, _ string, _ embedded.ContactChange) error {
	return m.send(email, "contact changed")
}
func (m logEmail) SendDeviceKeyEnrolled(_ context.Context, email, _ string, _ embedded.DeviceKeyNotice) error {
	return m.send(email, "device key enrolled")
}

// AuthKit delivers deletion callbacks durably through the shared River fleet.
// Cancel accepted agreements through the portable billing API before identity
// purge; recovery never resumes billing without a new customer instruction.
func (a *appAuth) cancelDeletedAccountBilling(ctx context.Context, event authkit.UserDeletion) error {
	if a.billing == nil {
		return errors.New("billing lifecycle has not been composed")
	}
	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		page, err := a.billing.ListSubscriptions(ctx, openrails.SubscriptionFilter{CustomerID: event.UserID, PageOptions: openrails.PageOptions{Limit: pageSize, Offset: offset}})
		if err != nil {
			return err
		}
		for _, subscription := range page.Data {
			if subscription.CancelScheduled || subscription.CancelledAt != nil {
				continue
			}
			switch subscription.Status {
			case "cancelled", "canceled", "expired", "ended":
				continue
			}
			if err := a.billing.CancelSubscription(ctx, subscription.ID, openrails.CancelSubscriptionRequest{Reason: "Account deletion " + event.ID, RevokeAccess: false}); err != nil {
				return err
			}
		}
		// Do not filter by active status: cancellation changes state while this
		// bounded scan proceeds, and would otherwise skip the next page.
		if !page.HasMore {
			return nil
		}
	}
}

// grantAdmin is only called by the explicit, one-off admin:grant command.
// Normal startup never restores a permission that an operator has revoked.
func (a *appAuth) grantAdmin(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("a registered user ID is required")
	}
	user, err := a.client.AdminGetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil {
		return errors.New("admin user does not exist; register the user first")
	}
	return a.client.OperatorAssignGroupRole(ctx, authkit.RootGroup(), authkit.UserSubject(userID), authkit.Role("admin"))
}

// revokeAdmin is the matching explicit operator command; ordinary requests
// cannot invoke either bootstrap method.
func (a *appAuth) revokeAdmin(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("a registered user ID is required")
	}
	return a.client.OperatorUnassignGroupRole(ctx, authkit.RootGroup(), authkit.UserSubject(userID), authkit.Role("admin"))
}
