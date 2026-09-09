package authoring

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
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
		for _, limit := range CanonicalVisualRoleLimits(entry.Type) {
			if limit.Minimum < 0 || limit.Maximum < 0 || (limit.Maximum > 0 && limit.Minimum > limit.Maximum) {
				t.Fatalf("catalog type %q has invalid role limit %#v", entry.Type, limit)
			}
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

func TestCanonicalVisualFormatOptionsFilterByVisualApplicability(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		include []string
		exclude []string
	}{
		{name: "pie", kind: "pie", include: []string{"legend", "labels.density", "rose", "outerRadius"}, exclude: []string{"orientation", "centerLabel", "innerRadius", "align", "sort"}},
		{name: "funnel", kind: "funnel", include: []string{"legend", "labels.density", "orientation", "align", "sort"}, exclude: []string{"rose", "centerLabel", "innerRadius", "outerRadius"}},
		{name: "hierarchy treemap", kind: "treemap", include: []string{"labels.density", "initialDepth", "roam", "breadcrumb"}, exclude: []string{"legend", "orientation", "layout", "nodeGap", "curveness", "focus"}},
		{name: "hierarchy sankey", kind: "sankey", include: []string{"labels.density", "orientation", "nodeGap", "curveness"}, exclude: []string{"legend", "initialDepth", "roam", "layout", "breadcrumb", "focus"}},
		{name: "financial candlestick", kind: "candlestick", include: []string{"axisVisible", "legend", "dataZoom", "displayUnits"}, exclude: []string{"labels.density", "labels.maxCharacters", "labels.minimumSpacing", "labels.tooltipFallback", "labelPosition", "stacking", "orientation", "showSymbols", "smooth", "step", "symbolSize"}},
		{name: "financial boxplot", kind: "boxplot", include: []string{"axisVisible", "dataZoom", "displayUnits"}, exclude: []string{"legend", "labels.density", "labelPosition", "stacking", "orientation", "showSymbols", "smooth", "step", "symbolSize"}},
		{name: "polar gauge", kind: "gauge", include: []string{"labels.density", "displayUnits", "minimum", "maximum", "target", "showPointer", "progressWidth"}, exclude: []string{"axisVisible", "legend", "area"}},
		{name: "polar radar", kind: "radar", include: []string{"labels.density", "displayUnits", "legend", "maximum", "area"}, exclude: []string{"axisVisible", "minimum", "target", "showPointer", "progressWidth"}},
		{name: "geographic map", kind: "map", include: []string{"theme", "basemap", "labelDensity", "roam"}, exclude: []string{"labels.density", "labels.maxCharacters", "labels.minimumSpacing", "labels.tooltipFallback"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := CanonicalVisualFormatOptions(defaultCanonicalVisual(test.kind, "Orders"))
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range test.include {
				if !hasVisualFormatKey(options, key) {
					t.Fatalf("options = %#v, missing applicable key %q", options, key)
				}
			}
			for _, key := range test.exclude {
				if hasVisualFormatKey(options, key) {
					t.Fatalf("options = %#v, contains inapplicable key %q", options, key)
				}
			}
		})
	}

	combo := defaultCanonicalVisual("combo", "Orders")
	comboPresentation := combo.Presentation.Value.(*document.CartesianDashboardPresentation)
	columnSeries := []document.DashboardComboSeries{{Field: "orders", Mark: document.DashboardComboSeriesMarkColumn, Axis: document.DashboardComboSeriesAxisPrimary}}
	comboPresentation.Series = &columnSeries
	options, err := CanonicalVisualFormatOptions(combo)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"showSymbols", "smooth", "step", "symbolSize"} {
		if hasVisualFormatKey(options, key) {
			t.Fatalf("column-only combo options = %#v, contains line control %q", options, key)
		}
	}
	lineSeries := []document.DashboardComboSeries{{Field: "orders", Mark: document.DashboardComboSeriesMarkLine, Axis: document.DashboardComboSeriesAxisPrimary}}
	comboPresentation.Series = &lineSeries
	options, err = CanonicalVisualFormatOptions(combo)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"showSymbols", "smooth", "step", "symbolSize"} {
		if !hasVisualFormatKey(options, key) {
			t.Fatalf("line combo options = %#v, missing line control %q", options, key)
		}
	}

	scatter := defaultCanonicalVisual("scatter", "Orders")
	options, err = CanonicalVisualFormatOptions(scatter)
	if err != nil {
		t.Fatal(err)
	}
	if hasVisualFormatKey(options, "legend") {
		t.Fatalf("non-categorical scatter options = %#v, contains legend", options)
	}
	for _, key := range []string{"labels.density", "labels.maxCharacters", "labels.minimumSpacing", "labels.tooltipFallback"} {
		if !hasVisualFormatKey(options, key) {
			t.Fatalf("scatter options = %#v, missing labels key %q", options, key)
		}
	}
	point := scatter.Presentation.Value.(*document.PointDashboardPresentation)
	color := "status"
	point.Color = &color
	point.ColorScale = &document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindCategorical}
	options, err = CanonicalVisualFormatOptions(scatter)
	if err != nil {
		t.Fatal(err)
	}
	if !hasVisualFormatKey(options, "legend") {
		t.Fatalf("categorical scatter options = %#v, missing legend", options)
	}
}

func TestCanonicalVisualFormatMutationRejectsCompilerInapplicableKeys(t *testing.T) {
	orientation := document.DashboardOrientationHorizontal
	legend := document.DashboardLegendPositionRight
	labels := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAutomatic}
	axisVisible := false
	columnSeries := []document.DashboardComboSeries{{Field: "orders", Mark: document.DashboardComboSeriesMarkColumn, Axis: document.DashboardComboSeriesAxisPrimary}}
	tests := []struct {
		name string
		kind string
		key  string
		set  func(*document.DashboardVisual)
	}{
		{name: "pie orientation", kind: "pie", key: "orientation", set: func(visual *document.DashboardVisual) {
			visual.Presentation.Value.(*document.ProportionalDashboardPresentation).Orientation = &orientation
		}},
		{name: "hierarchy legend", kind: "treemap", key: "legend", set: func(visual *document.DashboardVisual) {
			visual.Presentation.Value.(*document.HierarchyDashboardPresentation).Legend = &legend
		}},
		{name: "financial labels", kind: "candlestick", key: "labels.density", set: func(visual *document.DashboardVisual) {
			visual.Presentation.Value.(*document.CartesianDashboardPresentation).Labels = &labels
		}},
		{name: "polar axis", kind: "gauge", key: "axisVisible", set: func(visual *document.DashboardVisual) {
			visual.Presentation.Value.(*document.PolarDashboardPresentation).AxisVisible = &axisVisible
		}},
		{name: "all-column combo line control", kind: "combo", key: "showSymbols", set: func(visual *document.DashboardVisual) {
			presentation := visual.Presentation.Value.(*document.CartesianDashboardPresentation)
			presentation.Series = &columnSeries
			showSymbols := true
			presentation.ShowSymbols = &showSymbols
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visual := defaultCanonicalVisual(test.kind, "Orders")
			test.set(&visual)
			if err := applyCanonicalVisualFormatOption(&visual, test.key, "true"); err == nil {
				t.Fatalf("applying compiler-inapplicable key %q succeeded", test.key)
			}
			if _, err := compiler.LowerCanonicalDashboardPresentation(visual.Presentation, visual.Type); err == nil {
				t.Fatalf("compiler accepted inapplicable %s on %s", test.key, test.kind)
			}
		})
	}

	mapVisual := defaultCanonicalVisual("map", "Orders")
	if err := applyCanonicalVisualFormatOption(&mapVisual, "labels.density", "dense"); err == nil {
		t.Fatal("geographic data-label mutation succeeded")
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

func hasVisualFormatKey(options []VisualFormatOption, key string) bool {
	for _, option := range options {
		if option.Key == key {
			return true
		}
	}
	return false
}

// Exercise the advertised finite choices against the compiler, rather than
// reproducing the applicability allowlist in the test.
func TestCanonicalVisualFormatChoicesCompile(t *testing.T) {
	for _, entry := range CanonicalVisualCatalog() {
		visual := defaultCanonicalVisual(string(entry.Type), "Orders")
		options, err := CanonicalVisualFormatOptions(visual)
		if err != nil {
			t.Fatal(err)
		}
		for _, option := range options {
			values := []string{}
			for _, choice := range option.Choices {
				values = append(values, choice.Value)
			}
			if option.Control == "toggle" {
				values = []string{"true", "false"}
			}
			if option.Control == "number" {
				// Representative valid values; domain boundaries are compiler
				// tests, not an assertion that every number is meaningful.
				samples := map[string]string{
					"labels.maxCharacters": "24", "labels.minimumSpacing": "6", "symbolSize": "8",
					"innerRadius": "0.3", "outerRadius": "0.8", "initialDepth": "2", "nodeGap": "8", "curveness": "0.3",
					"minimum": "0", "maximum": "100", "target": "50", "progressWidth": "12",
					"camera.zoom": "3", "camera.padding": "32", "camera.minimumZoom": "0", "camera.maximumZoom": "14", "rowHeight": "32",
				}
				if value, ok := samples[option.Key]; ok {
					values = []string{value}
				} else {
					t.Fatalf("missing numeric test value for %s", option.Key)
				}
			}
			if option.Control == "text" {
				values = []string{"Review example"}
			}
			for _, value := range values {
				t.Run(string(entry.Type)+"/"+option.Key+"/"+value, func(t *testing.T) {
					candidate := defaultCanonicalVisual(string(entry.Type), "Orders")
					if candidate.Type == document.DashboardVisualTypeGauge {
						minimum, maximum := 0.0, 100.0
						p := candidate.Presentation.Value.(*document.PolarDashboardPresentation)
						p.Minimum, p.Maximum = &minimum, &maximum
					}
					if option.Key == "camera.mode" {
						center, zoom := []float64{0, 0}, 3.0
						candidate.Presentation.Value.(*document.GeographicDashboardPresentation).Camera = &document.DashboardMapCamera{Center: &center, Zoom: &zoom}
					}
					// Disabling fallback is supported only when labels cannot be
					// suppressed. Other densities are covered by negative tests.
					if option.Key == "labels.tooltipFallback" {
						if err := applyCanonicalVisualFormatOption(&candidate, "labels.density", "always"); err != nil {
							t.Fatal(err)
						}
					}
					if err := applyCanonicalVisualFormatOption(&candidate, option.Key, value); err != nil {
						t.Fatal(err)
					}
					if _, err := compiler.LowerCanonicalDashboardPresentation(candidate.Presentation, candidate.Type); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestCanonicalMapFixedCameraRequiresAuthoredPrerequisites(t *testing.T) {
	visual := defaultCanonicalVisual("map", "Orders")
	if err := applyCanonicalVisualFormatOption(&visual, "camera.mode", "fixed"); err == nil {
		t.Fatal("fixed camera accepted without a YAML-owned center and zoom")
	}
	center, zoom := []float64{0, 0}, 3.0
	visual.Presentation.Value.(*document.GeographicDashboardPresentation).Camera = &document.DashboardMapCamera{Center: &center, Zoom: &zoom}
	if err := applyCanonicalVisualFormatOption(&visual, "camera.mode", "fixed"); err != nil {
		t.Fatal(err)
	}
	if _, err := compiler.LowerCanonicalDashboardPresentation(visual.Presentation, visual.Type); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalVisualFormatRejectsMalformedValuesWithoutMutation(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"labels.maxCharacters", "NaN"}, {"labels.maxCharacters", "+Inf"},
		{"labels.maxCharacters", "1e999"}, {"labels.maxCharacters", "4.5"},
		{"axisVisible", "sometimes"}, {"stacking", "unknown"},
		{"echartsOptions", "{}"}, {"labels.unknown", "true"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			visual := defaultCanonicalVisual("line", "Orders")
			before, err := json.Marshal(visual)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyCanonicalVisualFormatOption(&visual, tc.key, tc.value); err == nil {
				t.Fatal("malformed format option accepted")
			}
			after, err := json.Marshal(visual)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rejected format mutation changed the visual")
			}
		})
	}
}

func TestCanonicalVisualFormatDefaultsMatchCompiledPresentation(t *testing.T) {
	line := defaultCanonicalVisual("line", "Orders")
	compiled, err := compiler.LowerCanonicalDashboardPresentation(line.Presentation, line.Type)
	if err != nil {
		t.Fatal(err)
	}
	p := compiled.(visualizationir.CartesianVisualizationPresentation)
	if p.Legend != visualizationir.VisualizationLegendPositionBottom || p.ShowSymbols {
		t.Fatalf("unexpected compiler defaults: %#v", p)
	}
	assertValues := func(visual document.DashboardVisual, expected map[string]string) {
		t.Helper()
		options, err := CanonicalVisualFormatOptions(visual)
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range expected {
			found := false
			for _, option := range options {
				if option.Key == key {
					found = true
					if option.Value != value {
						t.Errorf("%s default = %q, want %q", key, option.Value, value)
					}
				}
			}
			if !found {
				t.Errorf("missing option %s", key)
			}
		}
	}
	assertValues(line, map[string]string{"legend": "bottom", "showSymbols": "false"})
	mapVisual := defaultCanonicalVisual("map", "Orders")
	compiled, err = compiler.LowerCanonicalDashboardPresentation(mapVisual.Presentation, mapVisual.Type)
	if err != nil {
		t.Fatal(err)
	}
	camera := compiled.(visualizationir.GeographicVisualizationPresentation).Camera
	if camera.Padding != 32 || camera.MaximumZoom != 14 {
		t.Fatalf("unexpected compiler camera defaults: %#v", camera)
	}
	assertValues(mapVisual, map[string]string{"camera.padding": "32", "camera.maximumZoom": "14"})
}
