package postgres

import (
	"testing"

	servingdb "github.com/flidai/leapview/internal/servingstate/postgres/internal/db"
)

func TestBundleReadCanonicalizesJSONBDocuments(t *testing.T) {
	const (
		manifestRaw    = `{ "z": { "b": 2, "a": 1 }, "a": [ { "d": 4, "c": 3 } ] }`
		manifestWant   = `{"a":[{"c":3,"d":4}],"z":{"a":1,"b":2}}`
		accessRaw      = `{ "policy": { "rules": [ { "effect": "allow", "actions": ["read", "write"] } ], "version": 2 }, "enabled": true }`
		accessWant     = `{"enabled":true,"policy":{"rules":[{"actions":["read","write"],"effect":"allow"}],"version":2}}`
		pubRaw         = `{ "dashboard-z": { "pages": [ { "slug": "overview", "position": 1 } ], "enabled": false }, "dashboard-a": { "pages": [] } }`
		pubWant        = `{"dashboard-a":{"pages":[]},"dashboard-z":{"enabled":false,"pages":[{"position":1,"slug":"overview"}]}}`
		appearanceRaw  = `{ "dashboard-z": { "layout": { "columns": 12, "rows": 4 }, "theme": "dark" }, "dashboard-a": { "theme": "light" } }`
		appearanceWant = `{"dashboard-a":{"theme":"light"},"dashboard-z":{"layout":{"columns":12,"rows":4},"theme":"dark"}}`
	)

	get, err := bundleFromGetRow(servingdb.GetBundleRow{
		ProjectID:                  "project_demo",
		BManifestJson:              manifestRaw,
		BAccessPolicyJson:          accessRaw,
		BDashboardPublicationsJson: pubRaw,
		BDashboardAppearancesJson:  appearanceRaw,
	})
	if err != nil {
		t.Fatalf("bundleFromGetRow: %v", err)
	}
	assertCanonicalBundleDocuments(t, get.ManifestJSON, get.AccessPolicyJSON, get.DashboardPublicationsJSON, get.DashboardAppearancesJSON, manifestWant, accessWant, pubWant, appearanceWant)

	active, err := bundleFromActiveRow(servingdb.GetActiveBundleRow{
		ProjectID:                  "project_demo",
		BManifestJson:              manifestRaw,
		BAccessPolicyJson:          accessRaw,
		BDashboardPublicationsJson: pubRaw,
		BDashboardAppearancesJson:  appearanceRaw,
	})
	if err != nil {
		t.Fatalf("bundleFromActiveRow: %v", err)
	}
	assertCanonicalBundleDocuments(t, active.ManifestJSON, active.AccessPolicyJSON, active.DashboardPublicationsJSON, active.DashboardAppearancesJSON, manifestWant, accessWant, pubWant, appearanceWant)
}

func assertCanonicalBundleDocuments(t *testing.T, manifest, access, publications, appearances, wantManifest, wantAccess, wantPublications, wantAppearances string) {
	t.Helper()
	if manifest != wantManifest || access != wantAccess || publications != wantPublications || appearances != wantAppearances {
		t.Fatalf("canonical bundle documents = %q, %q, %q, %q; want %q, %q, %q, %q", manifest, access, publications, appearances, wantManifest, wantAccess, wantPublications, wantAppearances)
	}
}
