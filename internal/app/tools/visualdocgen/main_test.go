package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/site/visualdocs"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestParseVisualExamplesUsesMarkedYAMLAsSource(t *testing.T) {
	t.Parallel()

	source := []byte("" +
		"# Line chart\n\n" +
		"## Basic\n\n" +
		"{{< visual id=\"line_basic\" >}}\n\n" +
		"```yaml visual-example=line_basic\n" +
		"visuals:\n" +
		"  line_basic:\n" +
		"    title: Revenue\n" +
		"    type: line\n" +
		"    query:\n" +
		"      dimensions:\n" +
		"        month: orders.month\n" +
		"      metrics:\n" +
		"        revenue: null\n" +
		"```\n")

	examples, err := parseVisualExamples("line.md", source)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(examples), 1; got != want {
		t.Fatalf("examples = %d, want %d", got, want)
	}
	example := examples[0]
	if example.ID != "line_basic" || example.Chart == nil || example.Chart.Type != "line" {
		t.Fatalf("example = %#v", example)
	}
	if got := example.Chart.Query.Dimensions[0].Field; got != "orders.month" {
		t.Fatalf("dimension = %q, want orders.month", got)
	}
}

func TestNormalizeEnvelopeRevisionUsesCanonicalSpatialTileURL(t *testing.T) {
	envelope := visualizationir.VisualizationEnvelope{
		VisualID: "visual:map",
		DataState: visualizationir.VisualizationDataState{
			Value: &visualizationir.SpatialTiledVisualizationDataState{},
		},
	}

	normalizeEnvelopeRevision(&envelope, 7, 11)
	state, ok := envelope.DataState.Value.(*visualizationir.SpatialTiledVisualizationDataState)
	if !ok {
		t.Fatalf("data state type = %T, want spatial tiled", envelope.DataState.Value)
	}
	want := "/dashboards/dashboard:visual-docs/visuals/visual:map/tiles/documentation/{z}/{x}/{y}.mvt"
	if state.TileURL != want {
		t.Fatalf("tile URL = %q, want %q", state.TileURL, want)
	}
	if strings.Contains(state.TileURL, "/projects/") {
		t.Fatalf("tile URL retains project-prefixed route: %q", state.TileURL)
	}
}

func TestGenerateVisualExamplesExecutesEveryDocumentedQuery(t *testing.T) {
	docsDir := filepath.Join("..", "..", "..", "..", "docs", "visuals")
	artifact, err := generateVisualExamples(docsDir, filepath.Join("testdata", "project", "leapview.yaml"), filepath.Join("testdata", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := artifact.Version, visualdocs.ArtifactVersion; got != want {
		t.Fatalf("version = %d, want %d", got, want)
	}
	lineReference := artifact.References["visuals/line"]
	if got, want := lineReference.Kind, "chart"; got != want {
		t.Fatalf("line reference kind = %q, want %q", got, want)
	}
	if got, want := strings.Join(lineReference.Shapes, ","), "category_series_value,category_value"; got != want {
		t.Fatalf("line reference shapes = %q, want %q", got, want)
	}
	if got := strings.Join(lineReference.Examples["revenue_line_step"].KeyFields, ","); !strings.Contains(got, "presentation.step") || strings.Contains(got, "query.series") {
		t.Fatalf("stepped line key fields = %q", got)
	}
	fields := make(map[string]visualdocs.FieldReference, len(lineReference.Fields))
	for _, field := range lineReference.Fields {
		fields[field.Path] = field
	}
	if got, want := fields["query.dimensions"].Type, "field mapping"; got != want {
		t.Fatalf("query.dimensions type = %q, want %q", got, want)
	}
	if got, want := fields["query.limit"].Default, "no limit"; got != want {
		t.Fatalf("query.limit default = %q, want %q", got, want)
	}
	if got, want := fields["presentation.step"].Type, "boolean"; got != want {
		t.Fatalf("presentation.step type = %q, want %q", got, want)
	}
	if got, want := strings.Join(fields["presentation.step"].AllowedValues, ","), "true,false"; got != want {
		t.Fatalf("presentation.step values = %q, want %q", got, want)
	}
	if fields["presentation.step"].Description == "" {
		t.Fatal("presentation.step description is empty")
	}
	if got := artifact.References["visuals/map"].Accessibility; !strings.Contains(got, "coordinate fields") {
		t.Fatalf("map accessibility guidance = %q", got)
	}
	if got := artifact.References["visuals/kpi"].Accessibility; !strings.Contains(got, "current, comparison, target, and status") {
		t.Fatalf("KPI accessibility guidance = %q", got)
	}
	kpiReference := artifact.References["visuals/kpi"]
	if got := strings.Join(kpiReference.Examples["revenue_kpi_bullet"].KeyFields, ","); !strings.Contains(got, "datasets") ||
		!strings.Contains(got, "kpi.mode") || !strings.Contains(got, "kpi.goal") || !strings.Contains(got, "kpi.ranges") {
		t.Fatalf("bullet KPI key fields = %q", got)
	}
	if got := strings.Join(kpiReference.Presentation, ","); !strings.Contains(got, "kpi.favorable_direction") ||
		!strings.Contains(got, "kpi.missing_comparison") {
		t.Fatalf("KPI presentation reference = %q", got)
	}
	if got, want := len(artifact.Documents), 26; got != want {
		t.Fatalf("documents = %d, want %d", got, want)
	}
	count := 0
	for slug, examples := range artifact.Documents {
		if len(examples) == 0 {
			t.Fatalf("%s has no examples", slug)
		}
		for _, example := range examples {
			count++
			if visualizationEnvelopeRowCount(example) == 0 {
				t.Fatalf("%s/%s has no query data", slug, example.VisualID)
			}
		}
	}
	if got, want := count, 82; got != want {
		t.Fatalf("examples = %d, want %d", got, want)
	}
	if got, want := len(artifact.Showcase), 26; got != want {
		t.Fatalf("showcase examples = %d, want %d", got, want)
	}
	assertCuratedShowcaseExamples(t, artifact)
	barState, ok := artifact.Documents["visuals/bar"][0].DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(barState.Datasets) != 1 || len(barState.Datasets[0].Rows) < 2 {
		t.Fatalf("ranked bar rows are missing: %#v", artifact.Documents["visuals/bar"][0].DataState.Value)
	}
	firstBarValue, firstOK := envelopeNumber(barState.Datasets[0].Rows[0][1])
	secondBarValue, secondOK := envelopeNumber(barState.Datasets[0].Rows[1][1])
	if !firstOK || !secondOK || firstBarValue < secondBarValue {
		t.Fatalf("ranked bar rows do not preserve descending query order: %#v", artifact.Documents["visuals/bar"][0].DataState.Value)
	}
	histogramState, ok := artifact.Documents["visuals/histogram"][0].DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(histogramState.Datasets) != 1 || len(histogramState.Datasets[0].Rows) == 0 || histogramState.Datasets[0].Rows[0][0] != "2-3.81" {
		t.Fatalf("histogram bins do not preserve numeric query order: %#v", artifact.Documents["visuals/histogram"][0].DataState.Value)
	}
	kpis := artifact.Documents["visuals/kpi"]
	if got, want := len(kpis), 9; got != want {
		t.Fatalf("KPI examples = %d, want %d", got, want)
	}
	kpiByID := make(map[string]visualdocs.Payload, len(kpis))
	for _, payload := range kpis {
		kpiByID[payload.VisualID] = payload
	}
	favorablePayload := kpiByID["revenue_kpi_favorable"]
	favorable, ok := favorablePayload.Spec.Value.(*visualizationir.KPIVisualizationSpec)
	if !ok || favorable.Comparison == nil || favorable.Trend == nil ||
		favorable.Presentation.FavorableDirection != visualizationir.VisualizationKPIDirectionIncrease {
		t.Fatalf("favorable KPI spec = %#v", favorablePayload.Spec.Value)
	}
	favorableState, ok := favorablePayload.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(favorableState.Datasets) != 3 || len(favorableState.Datasets[2].Rows) != 12 {
		t.Fatalf("favorable KPI datasets = %#v", favorablePayload.DataState.Value)
	}
	bulletPayload := kpiByID["revenue_kpi_bullet"]
	bullet, ok := bulletPayload.Spec.Value.(*visualizationir.KPIVisualizationSpec)
	if !ok || bullet.Goal == nil || bullet.Presentation.Mode != visualizationir.VisualizationKPIModeBullet {
		t.Fatalf("bullet KPI spec = %#v", bulletPayload.Spec.Value)
	}
	missingPayload := kpiByID["revenue_kpi_missing_comparison"]
	missingState, ok := missingPayload.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(missingState.Datasets) != 2 || len(missingState.Datasets[1].Rows) != 1 ||
		missingState.Datasets[1].Rows[0][0] != nil {
		t.Fatalf("missing comparison KPI datasets = %#v", missingPayload.DataState.Value)
	}
	line := artifact.Documents["visuals/line"]
	seriesSpec, ok := line[1].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || seriesSpec.Series == nil {
		t.Fatalf("series line spec = %#v", line[1].Spec.Value)
	}
	calculationSpec, ok := line[2].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || calculationSpec.Calculations == nil || len(*calculationSpec.Calculations) != 1 ||
		(*calculationSpec.Calculations)[0].Template != visualizationir.VisualizationCalculationTemplateRunningTotal {
		t.Fatalf("visual calculation was not compiled: %#v", line[2].Spec.Value)
	}
	stepSpec, ok := line[3].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || !stepSpec.Presentation.Step {
		t.Fatalf("stepped line presentation was not compiled: %#v", line[3].Spec.Value)
	}
	contextSpec, ok := line[4].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	contextState, inline := line[4].DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || !inline || len(contextSpec.Datasets) != 2 || len(contextState.Datasets) != 2 || contextSpec.MetadataBindings == nil || contextSpec.MetadataBindings.Title == nil || contextSpec.ReferenceLines == nil {
		t.Fatalf("context line spec/state = %#v / %#v", line[4].Spec.Value, line[4].DataState.Value)
	}
	first, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	regenerated, err := generateVisualExamples(docsDir, filepath.Join("testdata", "project", "leapview.yaml"), filepath.Join("testdata", "data"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(regenerated)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("artifact JSON is not deterministic")
	}
}

func assertCuratedShowcaseExamples(t *testing.T, artifact visualExamplesArtifact) {
	t.Helper()

	first := func(slug, visualID string) visualdocs.Payload {
		t.Helper()
		examples := artifact.Documents["visuals/"+slug]
		if len(examples) == 0 {
			t.Fatalf("%s has no examples", slug)
		}
		if examples[0].VisualID != visualID {
			t.Fatalf("%s first example = %q, want %q", slug, examples[0].VisualID, visualID)
		}
		return examples[0]
	}
	rows := func(payload visualdocs.Payload) [][]any {
		t.Helper()
		state, ok := payload.DataState.Value.(*visualizationir.InlineVisualizationDataState)
		if !ok || len(state.Datasets) == 0 {
			t.Fatalf("%s has no inline dataset: %#v", payload.VisualID, payload.DataState.Value)
		}
		return state.Datasets[0].Rows
	}

	kpiPayload := first("kpi", "revenue_kpi_favorable")
	kpi, ok := kpiPayload.Spec.Value.(*visualizationir.KPIVisualizationSpec)
	if !ok || kpi.Comparison == nil || kpi.Trend == nil {
		t.Fatalf("showcase KPI must provide comparison and trend context: %#v", kpiPayload.Spec.Value)
	}

	gaugePayload := first("gauge", "review_gauge")
	gauge, ok := gaugePayload.Spec.Value.(*visualizationir.PolarVisualizationSpec)
	if !ok || gauge.Presentation.Maximum == nil || *gauge.Presentation.Maximum != 5 || gauge.Presentation.Target == nil {
		t.Fatalf("showcase gauge must use a bounded, decision-relevant scale: %#v", gaugePayload.Spec.Value)
	}

	funnelRows := rows(first("funnel", "checkout_funnel"))
	if len(funnelRows) != 5 {
		t.Fatalf("showcase funnel stages = %d, want 5", len(funnelRows))
	}
	previous := math.Inf(1)
	for _, row := range funnelRows {
		value, ok := envelopeNumber(row[1])
		if !ok || value >= previous {
			t.Fatalf("showcase funnel must strictly decrease through its stages: %#v", funnelRows)
		}
		previous = value
	}

	waterfallRows := rows(first("waterfall", "revenue_bridge_waterfall"))
	hasPositive, hasNegative := false, false
	for _, row := range waterfallRows {
		value, ok := envelopeNumber(row[1])
		if !ok {
			t.Fatalf("showcase waterfall has a non-numeric contribution: %#v", row)
		}
		hasPositive = hasPositive || value > 0
		hasNegative = hasNegative || value < 0
	}
	if !hasPositive || !hasNegative {
		t.Fatalf("showcase waterfall must explain gains and losses: %#v", waterfallRows)
	}

	candlestickRows := rows(first("candlestick", "market_candlestick"))
	hasRise, hasFall := false, false
	for _, row := range candlestickRows {
		open, openOK := envelopeNumber(row[1])
		close, closeOK := envelopeNumber(row[2])
		low, lowOK := envelopeNumber(row[3])
		high, highOK := envelopeNumber(row[4])
		if !openOK || !closeOK || !lowOK || !highOK || low > math.Min(open, close) || high < math.Max(open, close) {
			t.Fatalf("showcase candlestick contains invalid OHLC data: %#v", row)
		}
		hasRise = hasRise || close > open
		hasFall = hasFall || close < open
	}
	if !hasRise || !hasFall {
		t.Fatalf("showcase candlestick must include rising and falling periods: %#v", candlestickRows)
	}

	comboPayload := first("combo", "revenue_orders_combo")
	combo, ok := comboPayload.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || combo.Presentation.ComboSeries == nil || !slices.ContainsFunc(*combo.Presentation.ComboSeries, func(series visualizationir.VisualizationComboSeries) bool {
		return series.Axis == visualizationir.VisualizationAxisSecondary
	}) {
		t.Fatalf("showcase combo must use its secondary axis: %#v", comboPayload.Spec.Value)
	}

	donutPayload := first("donut", "orders")
	donut, ok := donutPayload.Spec.Value.(*visualizationir.ProportionalVisualizationSpec)
	if !ok || donut.Presentation.CenterLabel == nil || *donut.Presentation.CenterLabel == "" || donut.Presentation.InnerRadius == nil {
		t.Fatalf("showcase donut must demonstrate its hole and center context: %#v", donutPayload.Spec.Value)
	}

	graphPayload := first("graph", "status_delivery_graph")
	graph, ok := graphPayload.Spec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if !ok || graph.Presentation.Layout == nil || *graph.Presentation.Layout != visualizationir.VisualizationHierarchyLayoutCircular || graph.Source == nil {
		t.Fatalf("showcase graph must demonstrate a navigable network: %#v", graphPayload.Spec.Value)
	}

	treePayload := first("tree", "operating_model_tree")
	tree, ok := treePayload.Spec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if !ok || tree.Parent == nil || tree.Presentation.InitialDepth == nil || len(rows(treePayload)) < 8 || len(rows(treePayload)) > 16 {
		t.Fatalf("showcase tree must demonstrate a multi-level hierarchy: %#v", treePayload.Spec.Value)
	}

	sankeyPayload := first("sankey", "status_delivery_flow")
	sankey, ok := sankeyPayload.Spec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if !ok || sankey.Source == nil || sankey.Target == nil || sankey.Value == nil || len(rows(sankeyPayload)) < 6 {
		t.Fatalf("showcase Sankey must demonstrate a weighted multi-node flow: %#v", sankeyPayload.Spec.Value)
	}

	radarPayload := first("radar", "status_radar")
	radar, ok := radarPayload.Spec.Value.(*visualizationir.PolarVisualizationSpec)
	if !ok || radar.Presentation.Area == nil || !*radar.Presentation.Area || len(rows(radarPayload)) < 4 {
		t.Fatalf("showcase radar must demonstrate a filled multi-axis profile: %#v", radarPayload.Spec.Value)
	}
}

func TestVisualDocumentationCoversEveryPublicTypeAndGeographicLayer(t *testing.T) {
	docsDir := filepath.Join("..", "..", "..", "..", "docs", "visuals")
	schemaPath := filepath.Join("..", "..", "..", "..", "schemas", "json", "dashboard.schema.json")
	publicTypes, publicGeographicLayers := publicVisualizationDiscriminators(t, schemaPath)
	catalogContents, err := os.ReadFile(filepath.Join(docsDir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog visualCatalog
	if err := json.Unmarshal(catalogContents, &catalog); err != nil {
		t.Fatal(err)
	}
	documentedTypes := map[string]bool{}
	documentedGeographicLayers := map[string]bool{}
	for _, document := range catalog.Documents {
		contents, err := os.ReadFile(filepath.Join(docsDir, document.Source+".md"))
		if err != nil {
			t.Fatal(err)
		}
		examples, err := parseVisualExamples(document.Source+".md", contents)
		if err != nil {
			t.Fatal(err)
		}
		for _, example := range examples {
			if example.Tabular != nil {
				documentedTypes[example.Type] = true
				continue
			}
			if example.Chart == nil {
				continue
			}
			documentedTypes[example.Chart.Type] = true
			for _, layer := range example.Chart.Geo.Layers {
				documentedGeographicLayers[layer.Kind] = true
			}
		}
	}
	if got, want := strings.Join(publicTypes, ","), strings.Join(dashboardauthoring.SupportedVisualizationTypes(), ","); got != want {
		t.Fatalf("runtime visualization types = %q, public schema = %q", want, got)
	}
	if got, want := strings.Join(publicGeographicLayers, ","), strings.Join(dashboardauthoring.SupportedGeographicLayerKinds(), ","); got != want {
		t.Fatalf("runtime geographic layer kinds = %q, public schema = %q", want, got)
	}
	for _, visualType := range publicTypes {
		if !documentedTypes[visualType] {
			t.Errorf("public visualization type %q has no executable documentation example", visualType)
		}
	}
	for _, kind := range publicGeographicLayers {
		if !documentedGeographicLayers[kind] {
			t.Errorf("public geographic layer kind %q has no executable documentation example", kind)
		}
	}
}

func publicVisualizationDiscriminators(t *testing.T, schemaPath string) ([]string, []string) {
	t.Helper()
	type schemaNode struct {
		Ref        string                `json:"$ref"`
		Const      any                   `json:"const"`
		Enum       []string              `json:"enum"`
		AnyOf      []schemaNode          `json:"anyOf"`
		Properties map[string]schemaNode `json:"properties"`
	}
	var schema struct {
		Definitions map[string]schemaNode `json:"$defs"`
	}
	contents, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatal(err)
	}
	values := func(node schemaNode) []string {
		out := append([]string{}, node.Enum...)
		if value, ok := node.Const.(string); ok && value != "" {
			out = append(out, value)
		}
		for _, candidate := range node.AnyOf {
			if value, ok := candidate.Const.(string); ok && value != "" {
				out = append(out, value)
			}
		}
		return out
	}
	types := []string{}
	for _, variant := range schema.Definitions["#Visual"].AnyOf {
		name := strings.TrimPrefix(variant.Ref, "#/$defs/")
		name = strings.ReplaceAll(name, "%23", "#")
		types = append(types, values(schema.Definitions[name].Properties["type"])...)
	}
	layers := []string{}
	for _, variant := range schema.Definitions["#GeographicLayer"].AnyOf {
		layers = append(layers, values(variant.Properties["kind"])...)
	}
	slices.Sort(types)
	slices.Sort(layers)
	return types, layers
}

func visualizationEnvelopeRowCount(envelope visualizationir.VisualizationEnvelope) int {
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		count := 0
		for _, dataset := range state.Datasets {
			count += len(dataset.Rows)
		}
		return count
	case *visualizationir.WindowedVisualizationDataState:
		count := 0
		for _, block := range state.Blocks {
			count += len(block.Rows)
		}
		return count
	case *visualizationir.SpatialTiledVisualizationDataState:
		if state.Cardinality.Count != nil {
			return int(*state.Cardinality.Count)
		}
		return 0
	default:
		return 0
	}
}

func TestValidateVisualPayloadRejectsInvalidGeneratedData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		visual  visualExample
		payload []dashboard.Datum
		want    string
	}{
		{
			name:    "non finite metric",
			visual:  visualExample{ID: "bad_number", Chart: reportVisualPointer("category_value", "line", nil)},
			payload: []dashboard.Datum{{"label": "Jan", "value": math.NaN()}},
			want:    `non-finite number at data[0].value`,
		},
		{
			name:    "unknown map region",
			visual:  visualExample{ID: "bad_map", Chart: reportVisualPointer("geo", "map", map[string]any{"map": "brazil_states"})},
			payload: []dashboard.Datum{{"name": "CA", "value": 2.0}},
			want:    `region "CA" is not defined by map "brazil_states"`,
		},
		{
			name:    "incomplete map coverage",
			visual:  visualExample{ID: "incomplete_map", Chart: reportVisualPointer("geo", "map", map[string]any{"map": "brazil_states"})},
			payload: []dashboard.Datum{{"name": "SP", "value": 2.0}},
			want:    `does not provide data for map region`,
		},
		{
			name:    "no numeric values",
			visual:  visualExample{ID: "empty_series", Chart: reportVisualPointer("category_value", "line", nil)},
			payload: []dashboard.Datum{{"label": "Jan"}},
			want:    `has no finite numeric values`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVisualData(tt.visual, tt.payload)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func reportVisual(shape, visualType string, options map[string]any) dashboardauthoring.Visual {
	value := dashboardauthoring.Visual{Type: visualType}
	if mapID, ok := options["map"].(string); ok {
		value.Geo.Layers = []dashboardauthoring.VisualGeoLayer{{ID: "regions", Kind: "choropleth", GeometryAsset: mapID, Join: "name", Value: "value"}}
	}
	return value
}

func reportVisualPointer(shape, visualType string, options map[string]any) *dashboardauthoring.Visual {
	value := reportVisual(shape, visualType, options)
	return &value
}

func TestPersistVisualExamplesCheckDetectsStaleArtifact(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "examples.gen.json")
	artifact := visualExamplesArtifact{Version: 2, Documents: map[string][]visualdocs.Payload{}, Showcase: []visualdocs.Payload{}}
	if err := persistVisualExamples(path, artifact, false); err != nil {
		t.Fatal(err)
	}
	if err := persistVisualExamples(path, artifact, true); err != nil {
		t.Fatalf("current artifact: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := persistVisualExamples(path, artifact, true); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("stale artifact error = %v", err)
	}
}

func TestVisualDocumentationUsesPatternHeadingsAndSpecificGuidance(t *testing.T) {
	t.Parallel()
	docsDir := filepath.Join("..", "..", "..", "..", "docs", "visuals")
	runtimeOnlyHeading := regexp.MustCompile(`(?i)cross[- ]?filter`)
	files, err := filepath.Glob(filepath.Join(docsDir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Base(file) == "index.md" {
			continue
		}
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(contents)
		for _, boilerplate := range []string{
			"Start with the default presentation and keep the query focused",
			"to create this variation while leaving the renderer contract unchanged",
		} {
			if strings.Contains(source, boilerplate) {
				t.Errorf("%s contains generic variation guidance %q", file, boilerplate)
			}
		}
		headings := map[string]struct{}{}
		for _, line := range strings.Split(source, "\n") {
			if strings.HasPrefix(line, "## ") {
				heading := strings.TrimPrefix(line, "## ")
				headings[heading] = struct{}{}
				if runtimeOnlyHeading.MatchString(heading) {
					t.Errorf("%s documents runtime-only cross-filtering as an isolated visual example: %q", file, heading)
				}
			}
		}
		for _, title := range regexp.MustCompile(`(?m)^    title: (.+)$`).FindAllStringSubmatch(source, -1) {
			if _, duplicate := headings[title[1]]; duplicate {
				t.Errorf("%s repeats rendered visual title %q as a variation heading", file, title[1])
			}
		}
	}
}

func TestParseVisualExamplesRejectsBrokenContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing fence",
			body: `{{< visual id="line_basic" >}}`,
			want: `shortcode "line_basic" has no matching visual example`,
		},
		{
			name: "missing shortcode",
			body: "```yaml visual-example=line_basic\nvisuals:\n  line_basic:\n    title: Line\n    type: line\n    query:\n      dimensions: [orders.month]\n      metrics: [revenue]\n```",
			want: `visual example "line_basic" has no matching shortcode`,
		},
		{
			name: "multiple visuals",
			body: "{{< visual id=\"line_basic\" >}}\n```yaml visual-example=line_basic\nvisuals:\n  line_basic: {type: line}\n  other: {type: line}\n```",
			want: `must contain exactly one visual`,
		},
		{
			name: "key mismatch",
			body: "{{< visual id=\"line_basic\" >}}\n```yaml visual-example=line_basic\nvisuals:\n  other: {type: line}\n```",
			want: `must use visual key "line_basic"`,
		},
		{
			name: "duplicate shortcode",
			body: "{{< visual id=\"line_basic\" >}}\n{{< visual id=\"line_basic\" >}}\n```yaml visual-example=line_basic\nvisuals:\n  line_basic: {type: line}\n```",
			want: `duplicate visual shortcode "line_basic"`,
		},
		{
			name: "missing type",
			body: "{{< visual id=\"total\" >}}\n```yaml visual-example=total\nvisuals:\n  total:\n    shape: single_value\n    query:\n      metrics: [revenue]\n```",
			want: `type`,
		},
		{
			name: "legacy kind",
			body: "{{< visual id=\"total\" >}}\n```yaml visual-example=total\nvisuals:\n  total:\n    kind: kpi\n    shape: single_value\n    query:\n      metrics: [revenue]\n```",
			want: `kind`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseVisualExamples("line.md", []byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestEnvelopeRowsReadsWindowBlocksInDatasetOrder(t *testing.T) {
	envelope := visualizationir.VisualizationEnvelope{DataState: visualizationir.VisualizationDataState{Value: &visualizationir.WindowedVisualizationDataState{
		Schema: visualizationir.VisualizationDatasetSchema{Fields: []visualizationir.VisualizationField{{ID: "order_id"}, {ID: "revenue"}}},
		Blocks: map[string]visualizationir.VisualizationWindowBlock{
			"b": {ID: "b", Start: 1, Rows: [][]any{{"o2", 20}}},
			"a": {ID: "a", Start: 0, Rows: [][]any{{"o1", 10}}},
		},
	}}}
	rows := envelopeRows(envelope)
	if len(rows) != 2 || rows[0]["order_id"] != "o1" || rows[1]["revenue"] != 20 {
		t.Fatalf("window rows = %#v", rows)
	}
}
