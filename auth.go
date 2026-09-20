package main

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/authhttp"
	"github.com/open-rails/authkit/embedded"
)

func newAuth(config Config, pool *pgxpool.Pool) (*authhttp.Service, error) {
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
		return nil, err
	}

	service, err := authhttp.New(client, authhttp.Config{DirectPeerIP: true})
	if err != nil {
		client.Close()
		return nil, err
	}
	return service, nil
}
