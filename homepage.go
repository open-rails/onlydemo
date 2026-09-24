package main

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"
	authkitfiber "github.com/open-rails/authkit/adapters/fiber"
	openrailsfiber "github.com/open-rails/openrails/adapters/fiber"
)

type routeInfo struct {
	Method string
	Path   string
}

func (r routeInfo) CanOpen() bool {
	return r.Method == http.MethodGet && !strings.ContainsAny(r.Path, ":{}*")
}

type routeSection struct {
	ID          string
	Name        string
	Description string
	Routes      []routeInfo
}

//go:embed homepage.html
var homepageHTML string

var homepageTemplate = template.Must(template.New("homepage").Parse(homepageHTML))

func homepage(app *fiber.App) fiber.Handler {
	return func(c fiber.Ctx) error {
		var appRoutes []routeInfo
		var mountedAuthRoutes []routeInfo
		var mountedBillingRoutes []routeInfo
		// Application and library endpoints come from Fiber's live route table.
		for _, route := range app.GetRoutes(true) {
			info := routeInfo{Method: route.Method, Path: route.Path}
			if strings.HasPrefix(route.Name, authkitfiber.RouteNamePrefix) {
				mountedAuthRoutes = append(mountedAuthRoutes, info)
				continue
			}
			if strings.HasPrefix(route.Name, openrailsfiber.RouteNamePrefix) {
				mountedBillingRoutes = append(mountedBillingRoutes, info)
				continue
			}
			appRoutes = append(appRoutes, info)
		}
		sections := []routeSection{
			{ID: "application", Name: "Application routes", Description: "Routes registered by this demo, including the homepage, channel and post API.", Routes: sortedRoutes(appRoutes)},
			{ID: "openrails", Name: "OpenRails routes", Description: "Routes enabled by the billing runtime configuration, including verified provider callbacks.", Routes: sortedRoutes(mountedBillingRoutes)},
			{ID: "auth", Name: "Authentication routes", Description: "Sign-in, registration and account routes enabled by the current configuration. Disabled features are omitted.", Routes: sortedRoutes(mountedAuthRoutes)},
		}
		var body bytes.Buffer
		if err := homepageTemplate.Execute(&body, sections); err != nil {
			return err
		}
		c.Set(fiber.HeaderCacheControl, "no-store")
		c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
		return c.Send(body.Bytes())
	}
}

func sortedRoutes(routes []routeInfo) []routeInfo {
	result := append([]routeInfo(nil), routes...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Method < result[j].Method
	})
	return result
}
