package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/joho/godotenv"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
	"github.com/open-rails/contentkit/media"
	mediaS3 "github.com/open-rails/contentkit/media/s3"
	"github.com/open-rails/openrails"
)

type Config struct {
	Port         int
	DatabaseURL  string
	AuthIssuer   string
	AuthAudience string
	AuthKeysPath string
	PublicURL    string
	// ReturnOrigins are the exact origins checkout and billing-portal return
	// URLs may name: PUBLIC_URL plus RETURN_ORIGINS.
	ReturnOrigins []string
	AuthSchema    string
	BillingSchema string
	RiverSchema   string
	// Merchant is the seller and its PSPs, from merchant.yaml (MERCHANT_FILE).
	Merchant openrails.MerchantDeclaration
	// TrustedCountryHeader names the edge header carrying the buyer's country
	// (e.g. CF-IPCountry). Unset: the header is never trusted.
	TrustedCountryHeader string
	ContentSchema        string
	Media                mediaConfig
	// NotificationReceipts opts in to future optional successful-charge notices.
	NotificationReceipts bool
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
			case "AUTH_ISSUER", "AUTH_AUDIENCE", "AUTH_KEYS_PATH", "AUTH_SCHEMA", "PUBLIC_URL", "BILLING_SCHEMA", "RIVER_SCHEMA":
				return strings.ToLower(strings.ReplaceAll(key, "_", ".")), value
			default:
				if key == "NOTIFICATION_RECEIPTS" || key == "CONTENT_SCHEMA" || key == "MERCHANT_FILE" || key == "TRUSTED_COUNTRY_HEADER" || key == "RETURN_ORIGINS" || strings.HasPrefix(key, "MEDIA_") {
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
	if !isOrigin(publicURL) {
		return Config{}, fmt.Errorf("PUBLIC_URL must be an http(s) origin without a path, credentials, query, or fragment")
	}
	returnOrigins, err := parseReturnOrigins(publicURL, k.String("return_origins"))
	if err != nil {
		return Config{}, err
	}

	merchantFile := strings.TrimSpace(k.String("merchant_file"))
	if merchantFile == "" {
		merchantFile = "merchant.yaml"
	}
	merchant, err := openrails.ReadMerchantFile(merchantFile)
	if err != nil {
		return Config{}, fmt.Errorf("read %s (copy merchant.example.yaml): %w", merchantFile, err)
	}
	if len(merchant.PSPs) == 0 {
		return Config{}, fmt.Errorf("%s must declare at least one PSP", merchantFile)
	}

	cfg := Config{
		NotificationReceipts: k.Bool("notification_receipts"),
		Port:                 port,
		DatabaseURL:          databaseURL,
		AuthIssuer:           authIssuer,
		AuthAudience:         authAudience,
		AuthKeysPath:         authKeysPath,
		PublicURL:            publicURL,
		ReturnOrigins:        returnOrigins,
		AuthSchema:           strings.TrimSpace(k.String("auth.schema")),
		BillingSchema:        strings.TrimSpace(k.String("billing.schema")),
		RiverSchema:          strings.TrimSpace(k.String("river.schema")),
		Merchant:             merchant,
		ContentSchema:        strings.TrimSpace(k.String("content_schema")),

		TrustedCountryHeader: strings.TrimSpace(k.String("trusted_country_header")),
	}
	if cfg.Media, err = loadMediaConfig(k.String); err != nil {
		return Config{}, err
	}
	if err := validateDatabaseSchemas(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func isOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == ""
}

// parseReturnOrigins is PUBLIC_URL plus the comma-separated RETURN_ORIGINS.
func parseReturnOrigins(publicURL, raw string) ([]string, error) {
	origins := []string{publicURL}
	for _, origin := range strings.Split(raw, ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin == "" {
			continue
		}
		if !isOrigin(origin) {
			return nil, fmt.Errorf("RETURN_ORIGINS entry %q must be an http(s) origin without a path", origin)
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	return origins, nil
}

// Libraries own distinct relation names and can share a schema. Validate
// identifiers before any initializer runs; never change the host search_path.
var databaseSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func validateDatabaseSchemas(cfg Config) error {
	for name, value := range map[string]string{"AUTH_SCHEMA": cfg.AuthSchema, "BILLING_SCHEMA": cfg.BillingSchema, "RIVER_SCHEMA": cfg.RiverSchema, "CONTENT_SCHEMA": cfg.ContentSchema} {
		value = strings.TrimSpace(value)
		if value != "" && (len(value) > 63 || !databaseSchemaPattern.MatchString(value) || strings.HasPrefix(value, "pg_")) {
			return fmt.Errorf("%s must be a lowercase PostgreSQL schema identifier (at most 63 bytes, no pg_ prefix)", name)
		}
	}
	return nil
}

func contentSchema(cfg Config) string {
	if schema := strings.TrimSpace(cfg.ContentSchema); schema != "" {
		return schema
	}
	return "contentkit"
}

type mediaConfig struct {
	S3Endpoint       string
	S3PublicEndpoint string
	S3Bucket         string
	S3Region         string
	S3AccessKeyID    string
	S3SecretKey      string
	URL              string // media-gateway origin
	Delivery         media.DeliveryMode
	CookieDomain     string
	TokenKey         string
	TokenKeyPrevious string
}

func loadMediaConfig(get func(string) string) (mediaConfig, error) {
	or := func(key, def string) string {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v
		}
		return def
	}
	c := mediaConfig{
		S3Endpoint:       or("media_s3_endpoint", ""),
		S3Bucket:         or("media_s3_bucket", "onlydemo-media"),
		S3Region:         or("media_s3_region", "us-east-1"),
		S3AccessKeyID:    or("media_s3_access_key_id", ""),
		S3SecretKey:      or("media_s3_secret_access_key", ""),
		URL:              strings.TrimRight(or("media_url", ""), "/"),
		Delivery:         media.DeliveryMode(or("media_delivery", string(media.DeliverCookie))),
		CookieDomain:     or("media_cookie_domain", ""),
		TokenKey:         or("media_token_key", ""),
		TokenKeyPrevious: or("media_token_key_previous", ""),
	}
	c.S3PublicEndpoint = or("media_s3_public_endpoint", c.S3Endpoint)
	for name, v := range map[string]string{"MEDIA_S3_ENDPOINT": c.S3Endpoint, "MEDIA_S3_ACCESS_KEY_ID": c.S3AccessKeyID,
		"MEDIA_S3_SECRET_ACCESS_KEY": c.S3SecretKey, "MEDIA_URL": c.URL, "MEDIA_TOKEN_KEY": c.TokenKey} {
		if v == "" {
			return c, fmt.Errorf("%s is required", name)
		}
	}
	return c, nil
}

// openMediaStore opens the bucket and checks it answers.
func openMediaStore(ctx context.Context, mc mediaConfig) (*mediaS3.Store, error) {
	store, err := mediaS3.New(mediaS3.Config{Bucket: mc.S3Bucket, Region: mc.S3Region, Endpoint: mc.S3Endpoint, PublicEndpoint: mc.S3PublicEndpoint,
		AccessKeyID: mc.S3AccessKeyID, SecretAccessKey: mc.S3SecretKey, UsePathStyle: true})
	if err != nil {
		return nil, err
	}
	if err := store.Check(ctx, "_probe/"); err != nil {
		return nil, fmt.Errorf("media bucket %s: %w", mc.S3Bucket, err)
	}
	return store, nil
}
