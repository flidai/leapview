package authoring

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalVisualCatalogMatchesExecutableVisualReference(t *testing.T) {
	var reference struct {
		Documents []struct {
			Source string `json:"source"`
			Title  string `json:"title"`
		} `json:"documents"`
	}
	encoded, err := os.ReadFile("../../../docs/visuals/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &reference); err != nil {
		t.Fatal(err)
	}
	catalog := CanonicalVisualCatalog()
	if len(catalog) != 26 || len(catalog) != len(reference.Documents) {
		t.Fatalf("visual catalog/reference counts = %d/%d", len(catalog), len(reference.Documents))
	}
	for index, entry := range catalog {
		doc := reference.Documents[index]
		if string(entry.Type) != doc.Source || entry.Label != doc.Title || entry.ReferenceHref != "/docs/visuals/"+doc.Source {
			t.Fatalf("catalog[%d] = %#v, reference = %#v", index, entry, doc)
		}
		if !CanonicalVisualTypeSupported(entry.Type) {
			t.Fatalf("catalog type %q is not supported by the reducer", entry.Type)
		}
		if len(CanonicalVisualRoles(entry.Type)) == 0 {
			t.Fatalf("catalog type %q has no field roles", entry.Type)
		}
	}
}

func TestCanonicalVisualFormatOptionsArePresentationScopedAndRoundTrip(t *testing.T) {
	visual := defaultCanonicalVisual("bar", "Orders")
	options, err := CanonicalVisualFormatOptions(visual)
	if err != nil {
		t.Fatal(err)
	}
	if !hasVisualFormatOption(options, "stacking", "select") || !hasVisualFormatOption(options, "axisVisible", "toggle") {
		t.Fatalf("cartesian options = %#v", options)
	}
	if err := applyCanonicalVisualFormatOption(&visual, "stacking", "percent"); err != nil {
		t.Fatal(err)
	}
	if err := applyCanonicalVisualFormatOption(&visual, "labels.density", "dense"); err != nil {
		t.Fatal(err)
	}
	if err := applyCanonicalVisualFormatOption(&visual, "labels.maxCharacters", "18"); err != nil {
		t.Fatal(err)
	}
	presentation, ok := visual.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation.Stacking == nil || *presentation.Stacking != document.DashboardStackingModePercent || presentation.Labels == nil || presentation.Labels.Density != document.DashboardLabelDensityDense || presentation.Labels.MaxCharacters == nil || *presentation.Labels.MaxCharacters != 18 {
		t.Fatalf("updated cartesian presentation = %#v", visual.Presentation)
	}
	if err := applyCanonicalVisualFormatOption(&visual, "stacking", "invented"); err == nil {
		t.Fatal("invalid enum value was accepted")
	}
	if err := applyCanonicalVisualFormatOption(&visual, "rowHeight", "40"); err == nil {
		t.Fatal("table-only format option was accepted by a cartesian presentation")
	}

	table := defaultCanonicalVisual("table", "Orders")
	if err := applyCanonicalVisualFormatOption(&table, "rowHeight", "40"); err != nil {
		t.Fatal(err)
	}
	if err := applyCanonicalVisualFormatOption(&table, "striped", "true"); err != nil {
		t.Fatal(err)
	}
	tablePresentation, ok := table.Presentation.Value.(*document.TableDashboardPresentation)
	if !ok || tablePresentation.RowHeight != 40 || !tablePresentation.Striped {
		t.Fatalf("updated table presentation = %#v", table.Presentation)
	}

	geographic := defaultCanonicalVisual("map", "Orders")
	if err := applyCanonicalVisualFormatOption(&geographic, "camera.mode", "preserve"); err != nil {
		t.Fatal(err)
	}
	if err := applyCanonicalVisualFormatOption(&geographic, "controls.compass", "false"); err != nil {
		t.Fatal(err)
	}
	mapPresentation, ok := geographic.Presentation.Value.(*document.GeographicDashboardPresentation)
	if !ok || mapPresentation.Camera == nil || mapPresentation.Camera.Mode == nil || string(*mapPresentation.Camera.Mode) != "preserve" || mapPresentation.Controls == nil || mapPresentation.Controls.Compass == nil || *mapPresentation.Controls.Compass {
		t.Fatalf("updated geographic presentation = %#v", geographic.Presentation)
	}
}

func hasVisualFormatOption(options []VisualFormatOption, key, control string) bool {
	for _, option := range options {
		if option.Key == key && option.Control == control {
			return true
		}
	}
	return false
}
