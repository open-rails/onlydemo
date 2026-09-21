package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/authhttp"
	"github.com/open-rails/authkit/embedded"
)

const (
	postReadPermission   = "root:posts:read"
	postEditPermission   = "root:posts:edit"
	postDeletePermission = "root:posts:delete"
)

type appAuth struct {
	*authhttp.Service
	client *embedded.Client
}

func (a *appAuth) Close() {
	a.Service.Close()
	a.client.Close()
}

func newAuth(ctx context.Context, config Config, pool *pgxpool.Pool) (*appAuth, error) {
	ownership := embedded.RiverFromHost()
	client, err := embedded.New(embedded.Config{
		RBAC: []embedded.PersonaDef{embedded.IntrinsicRootPersona(embedded.RoleDef{
			Name:        "admin",
			Permissions: []string{postReadPermission, postEditPermission, postDeletePermission},
		})},
		Token: embedded.TokenConfig{
			Issuer:              config.AuthIssuer,
			IssuedAudiences:     []string{config.AuthAudience},
			ExpectedAudiences:   []string{config.AuthAudience},
			AccessTokenDuration: 15 * time.Minute,
		},
		Registration: embedded.RegistrationConfig{
			NativeUserMode: embedded.RegistrationModeOpen,
			Verification:   embedded.RegistrationVerificationNone,
		},
		Keys: embedded.KeysConfig{
			AllowEphemeralDevKeys: true,
		},
		Ephemeral: embedded.EphemeralConfig{
			AllowMemory: true,
		},
		TwoFactor: embedded.TwoFactorConfig{
			Mode: embedded.TwoFactorDisabled,
		},
	}, embedded.Deps{Postgres: pool, River: ownership})
	if err != nil {
		return nil, err
	}
	service, err := authhttp.New(client, authhttp.Config{DirectPeerIP: true})
	if err != nil {
		client.Close()
		return nil, err
	}
	return &appAuth{Service: service, client: client}, nil
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
	return a.client.Genesis().AssignRoleBySlug(ctx, userID, "admin")
}

// revokeAdmin is the matching explicit operator command; ordinary requests
// cannot invoke either bootstrap method.
func (a *appAuth) revokeAdmin(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("a registered user ID is required")
	}
	return a.client.Genesis().RemoveRoleBySlug(ctx, userID, "admin")
}
