package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Port                 int
	DatabaseURL          string
	AuthIssuer           string
	AuthAudience         string
	MigrationsOnly       bool
	AdminOnly            bool
	AdminRevoke          bool
	AdminUserID          string
	PublicURL            string
	BillingDatabaseURL   string
	StripeSecretKey      string
	StripeAccountID      string
	StripeWebhookSecret  string
	BillingEncryptionKey string
}

func loadConfig() (Config, error) {
	k := koanf.New(".")
	if err := k.Load(env.Provider(".", env.Opt{
		TransformFunc: func(key, value string) (string, any) {
			switch key {
			case "PORT":
				return strings.ToLower(key), value
			case "DATABASE_URL":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
			case "AUTH_ISSUER", "AUTH_AUDIENCE", "PUBLIC_URL", "BILLING_DATABASE_URL", "STRIPE_SECRET_KEY", "STRIPE_ACCOUNT_ID", "STRIPE_WEBHOOK_SECRET", "BILLING_ENCRYPTION_KEY", "ADMIN_USER_ID", "ADMIN_ONLY", "ADMIN_REVOKE":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
			case "MIGRATIONS_ONLY":
				return "migrations.only", value
			default:
				return "", nil
			}
		},
	}), nil); err != nil {
		return Config{}, err
	}

	port := k.Int("port")
	if port == 0 {
		port = 3000
	}

	databaseURL := k.String("database.url")
	if databaseURL == "" {
		databaseURL = "postgres://postgres:postgres@localhost:55433/openrails_demo?sslmode=disable"
	}

	authIssuer := k.String("auth.issuer")
	if authIssuer == "" {
		authIssuer = "http://localhost:3000"
	}

	authAudience := k.String("auth.audience")
	if authAudience == "" {
		authAudience = "openrails-demo"
	}
	publicURL := strings.TrimRight(k.String("public.url"), "/")
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://localhost:%d", port)
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return Config{}, fmt.Errorf("PUBLIC_URL must be an http(s) origin without a path, credentials, query, or fragment")
	}
	billingURL := k.String("billing.database.url")
	if billingURL == "" {
		billingURL = "postgres://openrails_demo_billing:local-demo-billing@localhost:55433/openrails_demo?sslmode=disable"
	}

	return Config{
		Port:                 port,
		DatabaseURL:          databaseURL,
		AuthIssuer:           authIssuer,
		AuthAudience:         authAudience,
		MigrationsOnly:       k.Bool("migrations.only"),
		AdminOnly:            k.Bool("admin.only"),
		AdminRevoke:          k.Bool("admin.revoke"),
		AdminUserID:          k.String("admin.user.id"),
		PublicURL:            publicURL,
		BillingDatabaseURL:   billingURL,
		StripeSecretKey:      k.String("stripe.secret.key"),
		StripeAccountID:      k.String("stripe.account.id"),
		StripeWebhookSecret:  k.String("stripe.webhook.secret"),
		BillingEncryptionKey: k.String("billing.encryption.key"),
	}, nil
}
