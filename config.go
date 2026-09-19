package main

import (
	"strings"

	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Port           int
	DatabaseURL    string
	AuthIssuer     string
	AuthAudience   string
	MigrationsOnly bool
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
			case "AUTH_ISSUER", "AUTH_AUDIENCE":
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

	return Config{
		Port:           port,
		DatabaseURL:    databaseURL,
		AuthIssuer:     authIssuer,
		AuthAudience:   authAudience,
		MigrationsOnly: k.Bool("migrations.only"),
	}, nil
}
