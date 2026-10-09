package app

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/go-chi/chi/v5"
)

// Some mounts delegate protocol dispatch to an opaque handler. A wildcard
// records the mounted surface, not support for every method below that path.
// The audit inventory reads this table alongside the unconditional routes.
const conditionalRouteInventory = `
* /mcp
* /scim/*
* /upload-protocols/tus
* /upload-protocols/tus/*
GET /oauth/authorize
POST /oauth/authorize
`

func TestConditionalRouteInventory(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, accessmodule.AuthConfig{LocalAuth: true})
	paths := func(t *testing.T, handler http.Handler) map[string]bool {
		t.Helper()
		routes, ok := handler.(chi.Routes)
		if !ok {
			t.Fatal("application handler does not expose chi routes")
		}
		result := map[string]bool{}
		if err := chi.Walk(routes, func(method string, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if path == "/mcp" || strings.HasPrefix(path, "/scim/") || strings.HasPrefix(path, "/upload-protocols/") {
				method = "*"
			}
			result[method+" "+path] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := paths(t, assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{})).Routes())
	var catalog []string
	for _, row := range strings.Split(strings.TrimSpace(conditionalRouteInventory), "\n") {
		method, path, ok := strings.Cut(row, " ")
		if !ok || method == "" || !strings.HasPrefix(path, "/") || base[row] || slices.Contains(catalog, row) {
			t.Fatalf("invalid or unconditional protocol mount %q", row)
		}
		catalog = append(catalog, row)
	}
	slices.Sort(catalog)
	for _, tc := range []struct {
		name          string
		scim          string
		auth, tus     bool
		noPersistence bool
		want          []string
	}{
		{name: "disabled"},
		{name: "blank bearer", scim: " \t "},
		{name: "SCIM", scim: "inventory-only-token", want: []string{"* /scim/*"}},
		{name: "MCP and OAuth", auth: true, want: []string{"* /mcp", "GET /oauth/authorize", "POST /oauth/authorize"}},
		{name: "TUS", tus: true, want: []string{"* /upload-protocols/tus", "* /upload-protocols/tus/*"}},
		{name: "all enabled", scim: "inventory-only-token", auth: true, tus: true, want: catalog},
		{name: "persistence disabled", scim: "inventory-only-token", auth: true, tus: true, noPersistence: true, want: []string{"GET /oauth/authorize", "POST /oauth/authorize"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := assemblyConfig{SCIMBearerToken: tc.scim}
			if tc.auth {
				config.Auth = auth
			}
			if tc.tus {
				config.ManagedDataTus = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					t.Fatal("inventory must not execute a protocol handler")
				})
			}
			server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, config))
			if tc.noPersistence {
				server.runtime.persistenceConfigured = false
			}
			mounted := paths(t, server.Routes())
			var got []string
			for path := range mounted {
				if !base[path] {
					got = append(got, path)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("conditional mounts = %v, want %v", got, tc.want)
			}
		})
	}
}
