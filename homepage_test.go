package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestHomepageDiscoversRegisteredRoutes(t *testing.T) {
	app := fiber.New()
	app.Get("/", homepage(app))
	// Registered after the homepage handler is constructed: this must appear
	// without adding a second route declaration or changing the HTML template.
	app.Post("/newly-registered", func(c fiber.Ctx) error { return c.SendStatus(http.StatusCreated) })
	app.Use("/middleware-only", func(c fiber.Ctx) error { return c.Next() })

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("homepage response = %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), `data-method="POST" data-path="/newly-registered"`) {
		t.Fatal("homepage did not discover the new route")
	}
	if strings.Contains(string(body), `data-path="/middleware-only"`) {
		t.Fatal("homepage advertised middleware as an endpoint")
	}
	if !strings.Contains(string(body), `data-method="HEAD" data-path="/" data-source="application" hidden`) || !strings.Contains(string(body), `id="show-head" type="checkbox"`) {
		t.Fatal("homepage must retain HEAD routes in the live inventory and hide them until requested")
	}
}
