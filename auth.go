package main

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/authhttp"
	"github.com/open-rails/authkit/embedded"
)

func newAuth(config Config, pool *pgxpool.Pool) (*authhttp.Service, http.Handler, error) {
	client, err := embedded.New(embedded.Config{
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
	}, embedded.Deps{Postgres: pool})
	if err != nil {
		return nil, nil, err
	}

	service, err := authhttp.New(client, authhttp.Config{DirectPeerIP: true})
	if err != nil {
		return nil, nil, err
	}

	mount, err := authhttp.MountHandler(service, authhttp.MountOptions{})
	if err != nil {
		service.Close()
		return nil, nil, err
	}

	return service, mount, nil
}
