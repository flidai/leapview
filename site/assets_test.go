package siteassets

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomepageProjectFilesMatchSalesExample(t *testing.T) {
	for _, name := range []string{
		"connections/olist.yaml",
		"sources/olist.payments.yaml",
		"models/sales_orders.yaml",
		"semantic-models/sales.yaml",
		"pipelines/sales-refresh.yaml",
		"dashboards/executive-sales.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			canonical, err := os.ReadFile(filepath.Join("..", "dashboards", name))
			if err != nil {
				t.Fatalf("read sales example: %v", err)
			}
			preview, err := fs.ReadFile(Static(), filepath.Join("home", "project-files", name))
			if err != nil {
				t.Fatalf("read homepage project file: %v", err)
			}
			if !bytes.Equal(preview, canonical) {
				t.Fatal("homepage project file differs from the sales example")
			}
		})
	}
}

func TestFaviconUsesSelectedApertureRing(t *testing.T) {
	contents, err := fs.ReadFile(Static(), "favicon.svg")
	if err != nil {
		t.Fatalf("read favicon: %v", err)
	}
	icon := string(contents)
	for _, expected := range []string{`<circle cx="12" cy="12" r="10"`, `d="m14.31 8 5.74 9.94"`} {
		if !strings.Contains(icon, expected) {
			t.Errorf("favicon does not contain %q", expected)
		}
	}
}

func TestSpinnerLabExploresAccessibleApertureLoaders(t *testing.T) {
	contents, err := fs.ReadFile(Static(), "spinner-lab.html")
	if err != nil {
		t.Fatalf("read spinner lab: %v", err)
	}
	page := string(contents)
	for _, expected := range []string{
		`<title>LeapView Spinner Lab</title>`,
		`data-variant="iris"`,
		`data-variant="trace"`,
		`data-variant="orbit"`,
		`data-variant="continuous"`,
		`animation: spin 1.8s linear infinite;`,
		`data-variant="pulse"`,
		`data-variant="halo"`,
		`data-variant="progress"`,
		`data-variant="fill-chase"`,
		`data-variant="fill-rotor"`,
		`data-variant="fill-twin"`,
		`<polygon class="fill-blade"`,
		`role="status"`,
		`aria-label="Loading"`,
		`@media (prefers-reduced-motion: reduce)`,
		`<circle cx="12" cy="12" r="10"`,
		`<script type="module" src="/static/spinner-lab.mjs"></script>`,
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("spinner lab does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"https://", "http://"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("spinner lab contains external dependency %q", forbidden)
		}
	}
	if count := strings.Count(page, `data-context-spinner="continuous"`); count != 3 {
		t.Errorf("spinner lab has %d continuous context spinners, want 3", count)
	}
	if count := strings.Count(page, `data-variant=`); count != 10 {
		t.Errorf("spinner lab has %d spinner variants, want 10", count)
	}
	script, err := fs.ReadFile(Static(), "spinner-lab.mjs")
	if err != nil {
		t.Fatalf("read spinner lab script: %v", err)
	}
	for _, expected := range []string{"motion-toggle", "theme-toggle", "data-sizes"} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("spinner lab script does not contain %q", expected)
		}
	}
}
