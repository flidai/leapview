package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoUIRenderersDoNotMountProductInternals(t *testing.T) {
	files := []string{
		"page.go",
		"develop.go",
		filepath.Join("..", "..", "agent", "ui", "chat.go"),
		filepath.Join("..", "..", "admin", "ui", "page.go"),
		filepath.Join("..", "..", "dashboard", "ui", "page.go"),
	}
	for _, file := range files {
		body := readArchitectureFile(t, file)
		for _, forbidden := range []string{
			`g.El("lv-record-table"`,
			`g.El("lv-code-block"`,
			`g.El("lv-report-canvas"`,
			`g.El("lv-filter-panel"`,
			`g.El("lv-filter-card"`,
			`g.El("lv-kpi-card"`,
			`g.El("lv-echart"`,
			`g.El("lv-report-table"`,
			`g.El("lv-chat-thread"`,
			`g.El("lv-chat-composer"`,
			`g.El("lv-project-access-control"`,
			`g.El("lv-asset-lineage-graph"`,
			`data-project-asset-toolbar`,
			`data-connection-toolbar`,
			`data-filter-dock`,
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s mounts product UI internals %q", file, forbidden)
			}
		}
	}
}

func TestLitRouteRootsDoNotOwnRoutingOrBackendFetches(t *testing.T) {
	files := []string{
		filepath.Join("..", "..", "..", "web", "components", "app", "catalog-page.ts"),
		filepath.Join("..", "..", "..", "web", "components", "dashboard", "dashboard-page.ts"),
		filepath.Join("..", "..", "..", "web", "components", "chat", "chat-page.ts"),
		filepath.Join("..", "..", "..", "web", "components", "admin", "admin-page.ts"),
		filepath.Join("..", "..", "..", "web", "components", "login", "login-page.ts"),
	}
	for _, file := range files {
		body := readArchitectureFile(t, file)
		for _, forbidden := range []string{
			"fetch(",
			"XMLHttpRequest",
			"@lit-labs/router",
			"vaadin-router",
			"navigo",
			"page.js",
			"history.pushState",
			"history.replaceState",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s owns routing or backend fetching via %q", file, forbidden)
			}
		}
	}
}

func readArchitectureFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
