package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Port                int
	DatabaseURL         string
	AuthIssuer          string
	AuthAudience        string
	PublicURL           string
	AuthSchema          string
	AppSchema           string
	BillingSchema       string
	RiverSchema         string
	StripeSecretKey     string
	StripeAccountID     string
	StripeWebhookSecret string
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
			case "AUTH_ISSUER", "AUTH_AUDIENCE", "AUTH_SCHEMA", "APP_SCHEMA", "PUBLIC_URL", "BILLING_SCHEMA", "RIVER_SCHEMA", "STRIPE_SECRET_KEY", "STRIPE_ACCOUNT_ID", "STRIPE_WEBHOOK_SECRET":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
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

	cfg := Config{
		Port:                port,
		DatabaseURL:         databaseURL,
		AuthIssuer:          authIssuer,
		AuthAudience:        authAudience,
		PublicURL:           publicURL,
		AuthSchema:          strings.TrimSpace(k.String("auth.schema")),
		AppSchema:           strings.TrimSpace(k.String("app.schema")),
		BillingSchema:       strings.TrimSpace(k.String("billing.schema")),
		RiverSchema:         strings.TrimSpace(k.String("river.schema")),
		StripeSecretKey:     k.String("stripe.secret.key"),
		StripeAccountID:     k.String("stripe.account.id"),
		StripeWebhookSecret: k.String("stripe.webhook.secret"),
	}
	if err := validateDatabaseSchemas(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Libraries own distinct relation names and can share a schema. Validate
// identifiers before any initializer runs; never change the host search_path.
var databaseSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func validateDatabaseSchemas(cfg Config) error {
	for name, value := range map[string]string{"AUTH_SCHEMA": cfg.AuthSchema, "APP_SCHEMA": cfg.AppSchema, "BILLING_SCHEMA": cfg.BillingSchema, "RIVER_SCHEMA": cfg.RiverSchema} {
		value = strings.TrimSpace(value)
		if value != "" && (len(value) > 63 || !databaseSchemaPattern.MatchString(value) || strings.HasPrefix(value, "pg_")) {
			return fmt.Errorf("%s must be a lowercase PostgreSQL schema identifier (at most 63 bytes, no pg_ prefix)", name)
		}
	}
	return nil
}

func appSchema(cfg Config) string {
	if schema := strings.TrimSpace(cfg.AppSchema); schema != "" {
		return schema
	}
	return "demo"
}
