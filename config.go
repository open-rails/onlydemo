package main

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
	"github.com/open-rails/openrails"
	openrailsembed "github.com/open-rails/openrails/embed"
)

type Config struct {
	Port          int
	DatabaseURL   string
	AuthIssuer    string
	AuthAudience  string
	AuthKeysPath  string
	PublicURL     string
	AuthSchema    string
	AppSchema     string
	BillingSchema string
	RiverSchema   string
	PSPs          map[string]openrailsembed.PSPConfig
	// CheckoutPSP takes every new purchase and new card; the other PSPs stay
	// declared for their existing cards and subscriptions.
	CheckoutPSP string
	// TrustedCountryHeader names the edge header carrying the buyer's country
	// (e.g. CF-IPCountry). Unset: the header is never trusted.
	TrustedCountryHeader string
	ContentSchema string
	Media         mediaConfig
	PostDeletion  postDeletionPolicy
	// MembershipHours is each new channel membership price's renewal period.
	MembershipHours int
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
			case "AUTH_ISSUER", "AUTH_AUDIENCE", "AUTH_KEYS_PATH", "AUTH_SCHEMA", "APP_SCHEMA", "PUBLIC_URL", "BILLING_SCHEMA", "RIVER_SCHEMA", "BILLING_PSPS":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
			default:
				if key == "CONTENT_SCHEMA" || key == "POST_DELETION_REFUND" || key == "POST_DELETION_REFUND_WINDOW" || key == "MEMBERSHIP_PERIOD" || key == "BILLING_CHECKOUT_PSP" || key == "TRUSTED_COUNTRY_HEADER" || strings.HasPrefix(key, "MEDIA_") {
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

	psps, err := loadPSPs(k.String("billing.psps"), os.LookupEnv)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Port:          port,
		DatabaseURL:   databaseURL,
		AuthIssuer:    authIssuer,
		AuthAudience:  authAudience,
		AuthKeysPath:  authKeysPath,
		PublicURL:     publicURL,
		AuthSchema:    strings.TrimSpace(k.String("auth.schema")),
		AppSchema:     strings.TrimSpace(k.String("app.schema")),
		BillingSchema: strings.TrimSpace(k.String("billing.schema")),
		RiverSchema:   strings.TrimSpace(k.String("river.schema")),
		PSPs:          psps,
		ContentSchema: strings.TrimSpace(k.String("content_schema")),

		TrustedCountryHeader: strings.TrimSpace(k.String("trusted_country_header")),
	}
	if cfg.CheckoutPSP, err = checkoutPSP(k.String("billing_checkout_psp"), psps); err != nil {
		return Config{}, err
	}
	if cfg.PostDeletion, err = parsePostDeletionPolicy(k.String("post_deletion_refund"), k.String("post_deletion_refund_window")); err != nil {
		return Config{}, err
	}
	if cfg.MembershipHours, err = parseMembershipPeriod(k.String("membership_period")); err != nil {
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

// loadPSPs declares each provider listed in BILLING_PSPS from its own
// variables; OpenRails knows each rail's credentials and settings.
func loadPSPs(raw string, lookup func(string) (string, bool)) (map[string]openrailsembed.PSPConfig, error) {
	psps := map[string]openrailsembed.PSPConfig{}
	for _, key := range strings.Split(raw, ",") {
		if key = strings.ToLower(strings.TrimSpace(key)); key == "" {
			continue
		}
		if _, dup := psps[key]; dup {
			return nil, fmt.Errorf("BILLING_PSPS: duplicate provider %q", key)
		}
		psp, err := openrailsembed.PSPFromEnv(key, lookup)
		if err != nil {
			return nil, fmt.Errorf("BILLING_PSPS: %w", err)
		}
		psps[key] = psp
	}
	if len(psps) == 0 {
		return nil, fmt.Errorf("BILLING_PSPS must list at least one provider")
	}
	return psps, nil
}

// checkoutPSP is the declared PSP new checkouts use; implied when only one is.
func checkoutPSP(raw string, psps map[string]openrailsembed.PSPConfig) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" && len(psps) == 1 {
		for only := range psps {
			return only, nil
		}
	}
	if _, ok := psps[key]; !ok {
		return "", fmt.Errorf("BILLING_CHECKOUT_PSP must name one of BILLING_PSPS")
	}
	return key, nil
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

// parseMembershipPeriod defaults to 720h; OpenRails periods are whole hours.
func parseMembershipPeriod(raw string) (int, error) {
	if raw = strings.TrimSpace(raw); raw == "" {
		return 720, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Hour || d%time.Hour != 0 {
		return 0, fmt.Errorf("MEMBERSHIP_PERIOD must be whole hours of at least 1h, such as 720h")
	}
	return int(d / time.Hour), nil
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
