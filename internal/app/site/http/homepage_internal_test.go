package http

import (
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectors"
	siteassets "github.com/flidai/leapview/site"
	xhtml "golang.org/x/net/html"
)

func TestHomepageOrbitFeaturesRegisteredIntegrations(t *testing.T) {
	type integration struct {
		category string
		registry string
		format   bool
	}
	want := map[string]integration{
		"postgresql":         {category: "database", registry: "postgres"},
		"mysql":              {category: "database", registry: "mysql"},
		"sqlite":             {category: "database", registry: "sqlite"},
		"amazons3":           {category: "storage", registry: "s3"},
		"microsoftazure":     {category: "storage", registry: "azure_blob"},
		"googlecloudstorage": {category: "storage", registry: "gcs"},
		"cloudflare":         {category: "storage", registry: "r2"},
		"hetzner":            {category: "storage", registry: "s3"},
		"csv":                {category: "format", registry: "csv", format: true},
		"json":               {category: "format", registry: "json", format: true},
		"apacheparquet":      {category: "format", registry: "parquet", format: true},
		"excel":              {category: "format", registry: "excel", format: true},
		"vortex":             {category: "format", registry: "vortex", format: true},
		"deltalake":          {category: "format", registry: "delta", format: true},
		"apacheiceberg":      {category: "format", registry: "iceberg", format: true},
		"lance":              {category: "format", registry: "lance", format: true},
		"ducklake":           {category: "format", registry: "ducklake"},
	}
	document, err := xhtml.Parse(strings.NewReader(siteassets.Homepage()))
	if err != nil {
		t.Fatalf("parse homepage: %v", err)
	}
	seen := make(map[string]bool)
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && node.Data == "button" {
			attributes := make(map[string]string, len(node.Attr))
			for _, attribute := range node.Attr {
				attributes[attribute.Key] = attribute.Val
			}
			if slices.Contains(strings.Fields(attributes["class"]), "orbit-node") {
				icon := attributes["data-integration"]
				spec, ok := want[icon]
				if !ok {
					t.Errorf("unexpected orbit integration %q", icon)
				} else {
					if seen[icon] {
						t.Errorf("duplicate orbit integration %q", icon)
					}
					seen[icon] = true
					if attributes["data-category"] != spec.category {
						t.Errorf("orbit integration %q category = %q, want %q", icon, attributes["data-category"], spec.category)
					}
					if spec.format {
						if _, ok := connectors.LookupFormat(spec.registry); !ok {
							t.Errorf("orbit format %q (%s) is not registered", icon, spec.registry)
						}
					} else if _, ok := connectors.LookupConnection(spec.registry); !ok {
						t.Errorf("orbit connection %q (%s) is not registered", icon, spec.registry)
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	for icon := range want {
		if !seen[icon] {
			t.Errorf("homepage missing orbit integration %q", icon)
		}
	}
}
