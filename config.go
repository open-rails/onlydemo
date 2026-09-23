package main

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
	"github.com/open-rails/openrails"
)

type Config struct {
	Port                  int
	DatabaseURL           string
	AuthIssuer            string
	AuthAudience          string
	AuthKeysPath          string
	PublicURL             string
	AuthSchema            string
	AppSchema             string
	BillingSchema         string
	RiverSchema           string
	BillingPSPs           []string
	StripePublishableKey  string
	StripeSecretKey       string
	StripeAccountID       string
	StripeWebhookSecret   string
	NMIAccountID          string
	NMISandboxSecurityKey string
	NMITokenizationKey    string
	NMITokenizationURL    string
	NMIWebhookSecret      string
	ContentSchema         string
	Media                 mediaConfig
	PostDeletion          postDeletionPolicy
}

// postDeletionPolicy is what deleting a paid post does to its recent purchases.
type postDeletionPolicy struct {
	Action openrails.PurchaseAction
	Window time.Duration
}

func loadConfig() (Config, error) {
	// Make direct `go run .` behave like Taskfile, while preserving any
	// explicitly exported environment variables. Taskfile also loads .env, and
	// godotenv.Load only fills variables that are not already present.
	if err := godotenv.Load(".env"); err != nil {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	k := koanf.New(".")
	if err := k.Load(env.Provider(".", env.Opt{
		TransformFunc: func(key, value string) (string, any) {
			switch key {
			case "PORT":
				return strings.ToLower(key), value
			case "DATABASE_URL":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
			case "AUTH_ISSUER", "AUTH_AUDIENCE", "AUTH_KEYS_PATH", "AUTH_SCHEMA", "APP_SCHEMA", "PUBLIC_URL", "BILLING_SCHEMA", "RIVER_SCHEMA", "BILLING_PSPS", "STRIPE_PUBLISHABLE_KEY", "STRIPE_SECRET_KEY", "STRIPE_ACCOUNT_ID", "STRIPE_WEBHOOK_SECRET", "NMI_ACCOUNT_ID", "NMI_SANDBOX_SECURITY_KEY", "NMI_TOKENIZATION_KEY", "NMI_TOKENIZATION_URL", "NMI_WEBHOOK_SIGNING_SECRET":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
			default:
				if key == "CONTENT_SCHEMA" || key == "POST_DELETION_REFUND" || key == "POST_DELETION_REFUND_WINDOW" || strings.HasPrefix(key, "MEDIA_") {
					return strings.ToLower(key), value
				}
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
		databaseURL = "postgres://postgres:postgres@localhost:55433/onlydemo?sslmode=disable"
	}

	authIssuer := k.String("auth.issuer")
	if authIssuer == "" {
		authIssuer = "http://localhost:3000"
	}

	authAudience := k.String("auth.audience")
	if authAudience == "" {
		authAudience = "onlydemo"
	}
	authKeysPath := k.String("auth.keys.path")
	if authKeysPath == "" {
		authKeysPath = ".runtime/auth"
	}
	publicURL := strings.TrimRight(k.String("public.url"), "/")
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://localhost:%d", port)
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return Config{}, fmt.Errorf("PUBLIC_URL must be an http(s) origin without a path, credentials, query, or fragment")
	}

	psps, err := parseBillingPSPs(k.String("billing.psps"))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Port:                  port,
		DatabaseURL:           databaseURL,
		AuthIssuer:            authIssuer,
		AuthAudience:          authAudience,
		AuthKeysPath:          authKeysPath,
		PublicURL:             publicURL,
		AuthSchema:            strings.TrimSpace(k.String("auth.schema")),
		AppSchema:             strings.TrimSpace(k.String("app.schema")),
		BillingSchema:         strings.TrimSpace(k.String("billing.schema")),
		RiverSchema:           strings.TrimSpace(k.String("river.schema")),
		BillingPSPs:           psps,
		StripePublishableKey:  k.String("stripe.publishable.key"),
		StripeSecretKey:       k.String("stripe.secret.key"),
		StripeAccountID:       k.String("stripe.account.id"),
		StripeWebhookSecret:   k.String("stripe.webhook.secret"),
		NMIAccountID:          k.String("nmi.account.id"),
		NMISandboxSecurityKey: k.String("nmi.sandbox.security.key"),
		NMITokenizationKey:    k.String("nmi.tokenization.key"),
		NMITokenizationURL:    k.String("nmi.tokenization.url"),
		NMIWebhookSecret:      k.String("nmi.webhook.signing.secret"),
		ContentSchema:         strings.TrimSpace(k.String("content_schema")),
	}
	if cfg.PostDeletion, err = parsePostDeletionPolicy(k.String("post_deletion_refund"), k.String("post_deletion_refund_window")); err != nil {
		return Config{}, err
	}
	if cfg.Media, err = loadMediaConfig(k.String); err != nil {
		return Config{}, err
	}
	if err := validateDatabaseSchemas(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseBillingPSPs reads the operator's provider list; unset means Stripe only.
func parseBillingPSPs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{"stripe"}, nil
	}
	var psps []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "stripe" && name != "nmi" {
			return nil, fmt.Errorf("BILLING_PSPS: unsupported provider %q (supported: stripe, nmi)", name)
		}
		if slices.Contains(psps, name) {
			return nil, fmt.Errorf("BILLING_PSPS: duplicate provider %q", name)
		}
		psps = append(psps, name)
	}
	return psps, nil
}

// parsePostDeletionPolicy defaults to refunding purchases from the last 30 days.
func parsePostDeletionPolicy(action, window string) (postDeletionPolicy, error) {
	policy := postDeletionPolicy{Action: openrails.PurchaseActionRefund, Window: 720 * time.Hour}
	switch a := openrails.PurchaseAction(strings.ToLower(strings.TrimSpace(action))); a {
	case "":
	case openrails.PurchaseActionRefund, openrails.PurchaseActionReview, openrails.PurchaseActionNone:
		policy.Action = a
	default:
		return policy, fmt.Errorf("POST_DELETION_REFUND must be refund, review or none")
	}
	if window = strings.TrimSpace(window); window != "" {
		d, err := time.ParseDuration(window)
		if err != nil || d <= 0 {
			return policy, fmt.Errorf("POST_DELETION_REFUND_WINDOW must be a positive duration such as 720h")
		}
		policy.Window = d
	}
	return policy, nil
}

// Libraries own distinct relation names and can share a schema. Validate
// identifiers before any initializer runs; never change the host search_path.
var databaseSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func validateDatabaseSchemas(cfg Config) error {
	for name, value := range map[string]string{"AUTH_SCHEMA": cfg.AuthSchema, "APP_SCHEMA": cfg.AppSchema, "BILLING_SCHEMA": cfg.BillingSchema, "RIVER_SCHEMA": cfg.RiverSchema, "CONTENT_SCHEMA": cfg.ContentSchema} {
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

func contentSchema(cfg Config) string {
	if schema := strings.TrimSpace(cfg.ContentSchema); schema != "" {
		return schema
	}
	return "content"
}
