// Command visualdocgen compiles executable visual examples embedded in the
// public Markdown documentation into deterministic browser payloads.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/app/site/visualdocs"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardadapter "github.com/flidai/leapview/internal/dashboard/analyticsruntime"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/project/schema"
	"github.com/flidai/leapview/internal/workload"
	"gopkg.in/yaml.v3"
)

var visualShortcodePattern = regexp.MustCompile(`^\s*\{\{<\s*visual\s+id="([a-z0-9_]+)"\s*>}}\s*$`)
var visualFencePattern = regexp.MustCompile("^```ya?ml[ \\t]+visual-example=([a-z0-9_]+)[ \\t]*$")

type visualExample struct {
	ID      string
	Source  string
	Line    int
	Type    string
	Chart   *dashboardauthoring.Visual
	Tabular *dashboardauthoring.TableVisual
}

type visualExampleFragment struct {
	Visuals map[string]yaml.Node `yaml:"visuals"`
}

type visualCatalog struct {
	Documents []struct {
		Source string `json:"source"`
		Title  string `json:"title"`
	} `json:"documents"`
}

type visualExamplesArtifact = visualdocs.Artifact
type visualDocumentReference = visualdocs.DocumentReference
type visualExampleReference = visualdocs.ExampleReference

func main() {
	docsDir := flag.String("docs", "docs/visuals", "visual documentation directory")
	project := flag.String("project", "internal/app/tools/visualdocgen/testdata/project/leapview.yaml", "fixture project")
	data := flag.String("data", "internal/app/tools/visualdocgen/testdata/data", "fixture managed-data root")
	out := flag.String("out", "docs/visuals/examples.gen.json", "generated artifact")
	check := flag.Bool("check", false, "verify the generated artifact is current")
	flag.Parse()

	artifact, err := generateVisualExamples(*docsDir, *project, *data)
	if err == nil {
		err = persistVisualExamples(*out, artifact, *check)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate visual documentation: %v\n", err)
		os.Exit(1)
	}
}

func persistVisualExamples(path string, artifact visualExamplesArtifact, check bool) error {
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if !check {
		return os.WriteFile(path, encoded, 0o644)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, encoded) {
		return fmt.Errorf("%s is out of date; run task docs:generate", path)
	}
	return nil
}

func generateVisualExamples(docsDir, projectPath, dataRoot string) (visualExamplesArtifact, error) {
	catalogContents, err := os.ReadFile(filepath.Join(docsDir, "catalog.json"))
	if err != nil {
		return visualExamplesArtifact{}, err
	}
	var catalog visualCatalog
	if err := json.Unmarshal(catalogContents, &catalog); err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("decode visual catalog: %w", err)
	}

	examplesByPage := make(map[string][]visualExample, len(catalog.Documents))
	globalIDs := map[string]string{}
	for _, document := range catalog.Documents {
		path := filepath.Join(docsDir, document.Source+".md")
		contents, err := os.ReadFile(path)
		if err != nil {
			return visualExamplesArtifact{}, err
		}
		examples, err := parseVisualExamples(path, contents)
		if err != nil {
			return visualExamplesArtifact{}, err
		}
		if len(examples) == 0 {
			return visualExamplesArtifact{}, fmt.Errorf("%s: visual document has no executable examples", path)
		}
		for _, example := range examples {
			if previous := globalIDs[example.ID]; previous != "" {
				return visualExamplesArtifact{}, fmt.Errorf("%s:%d: visual example %q is already declared in %s", path, example.Line, example.ID, previous)
			}
			globalIDs[example.ID] = path
		}
		examplesByPage[document.Source] = examples
	}

	compiled, err := workspacecompiler.CompileProject(projectPath, workspacecompiler.Options{WorkspaceID: "visual_examples"})
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("compile fixture project: %w", err)
	}
	compiledWorkspace, ok := compiled.Workspace("visual_examples")
	if !ok {
		return visualExamplesArtifact{}, fmt.Errorf("fixture project has no visual_examples workspace")
	}
	workspaceManifest := compiledWorkspace.Manifest()
	if workspaceManifest == nil {
		return visualExamplesArtifact{}, fmt.Errorf("fixture project has no visual_examples manifest")
	}
	report := buildExampleDashboard(catalog, examplesByPage)
	compiledReport, err := dashboardcompiler.Compile(*report, workspaceManifest.Models)
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("validate executable examples: %w", err)
	}
	normalizedReport := compiledReport.Normalized
	compiledDashboard := compiledReport.Definition
	workspaceManifest.DashboardDefinitions = map[string]dashboarddefinition.Definition{normalizedReport.ID: compiledDashboard}
	workspaceManifest.Catalog.Dashboards = []manifest.CatalogDashboard{{ID: normalizedReport.ID, Title: normalizedReport.Title, Path: "docs/visuals", Description: normalizedReport.Description}}
	if err := bindFixtureRoot(workspaceManifest, dataRoot); err != nil {
		return visualExamplesArtifact{}, err
	}
	definition := projectartifact.DashboardProjection(workspaceManifest)

	runtimeDir, err := os.MkdirTemp("", "leapview-visual-docs-*")
	if err != nil {
		return visualExamplesArtifact{}, err
	}
	defer os.RemoveAll(runtimeDir)
	database, err := analyticsducklake.Open(context.Background(), analyticsducklake.Config{RootDir: filepath.Join(runtimeDir, "ducklake"), MaxConnections: 1})
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("open fixture DuckDB: %w", err)
	}
	defer database.Close()
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		return visualExamplesArtifact{}, err
	}
	defer controller.Close()
	refreshLease, err := controller.Acquire(context.Background(), workload.Request{Class: workload.Refresh, PrincipalID: "system:visual-docs", Operation: "visual-docs.refresh", EstimatedMemoryBytes: 64 << 20})
	if err != nil {
		return visualExamplesArtifact{}, err
	}
	workspaces := analyticsruntime.WorkspaceFactoryFunc(func(ctx context.Context, request analyticsruntime.WorkspaceRequest) (analyticsruntime.Workspace, error) {
		if len(request.RequiredExtensions) > 0 {
			lease, err := database.Acquire(refreshLease.Context())
			if err != nil {
				return nil, err
			}
			for _, extension := range request.RequiredExtensions {
				if err := database.EnsureExtension(lease.Context(), extension); err != nil {
					lease.Release()
					return nil, err
				}
			}
			lease.Release()
		}
		return analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
			Models: request.Models, Database: database,
			CredentialResolver: analyticsduckdb.NonSecretCredentialResolver{},
			SnapshotID:         request.SnapshotID, ServingStateID: request.ServingStateID,
			WorkspaceID: request.WorkspaceID, Environment: request.Environment,
			SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
			SourceDataDigest: request.SourceDataDigest, ResultLimits: request.ResultLimits,
		})
	})
	service, err := dashboardruntime.NewFromDefinition(refreshLease.Context(), runtimeDir, dashboardadapter.NewFactory(dashboardadapter.Options{Workspaces: workspaces}), definition)
	refreshLease.Release()
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("open fixture runtime: %w", err)
	}
	defer service.Close()

	artifact := visualExamplesArtifact{Version: visualdocs.ArtifactVersion, Documents: map[string][]visualdocs.Payload{}, References: map[string]visualDocumentReference{}, Showcase: make([]visualdocs.Payload, 0, len(catalog.Documents))}
	for _, document := range catalog.Documents {
		queryLease, err := controller.Acquire(context.Background(), workload.Request{Class: workload.Interactive, PrincipalID: "system:visual-docs", Operation: "visual-docs.query", EstimatedMemoryBytes: 64 << 20})
		if err != nil {
			return visualExamplesArtifact{}, err
		}
		patch, err := service.QueryDashboardPage(queryLease.Context(), normalizedReport.ID, document.Source, dashboard.Filters{})
		queryLease.Release()
		if err != nil {
			return visualExamplesArtifact{}, fmt.Errorf("query %s examples: %w", document.Source, err)
		}
		if patch.Status.Error != "" {
			return visualExamplesArtifact{}, fmt.Errorf("query %s examples: %s", document.Source, patch.Status.Error)
		}
		payloads := make([]visualdocs.Payload, 0, len(examplesByPage[document.Source]))
		for _, example := range examplesByPage[document.Source] {
			envelope, ok := patch.Visuals[example.ID]
			if !ok || !envelopeHasData(envelope) {
				return visualExamplesArtifact{}, fmt.Errorf("query %s did not return visual %q data (present=%t status=%v diagnostics=%v)", document.Source, example.ID, ok, envelope.Status.Message, envelope.Diagnostics)
			}
			if err := validateVisualEnvelope(example, envelope); err != nil {
				return visualExamplesArtifact{}, err
			}
			preserveAllOrder := example.Type == "histogram"
			sortColumn, descending := visualExampleSort(example, envelope)
			canonicalizeEnvelopeData(&envelope, sortColumn, descending, preserveAllOrder)
			normalizeEnvelopeRevision(&envelope, 1, 1)
			payloads = append(payloads, envelope)
		}
		slug := "visuals/" + document.Source
		artifact.Documents[slug] = payloads
		reference, err := buildVisualDocumentReference(examplesByPage[document.Source])
		if err != nil {
			return visualExamplesArtifact{}, fmt.Errorf("build %s field reference: %w", document.Source, err)
		}
		artifact.References[slug] = reference
		artifact.Showcase = append(artifact.Showcase, payloads[0])
	}
	return artifact, nil
}

func canonicalizeEnvelopeData(envelope *visualizationir.VisualizationEnvelope, sortColumn int, descending, preserveAllOrder bool) {
	if envelope == nil || preserveAllOrder {
		return
	}
	state, ok := envelope.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok {
		return
	}
	for datasetIndex := range state.Datasets {
		rows := state.Datasets[datasetIndex].Rows
		sortEnvelopeRows(rows, sortColumn, descending)
	}
}

func sortEnvelopeRows(rows [][]any, sortColumn int, descending bool) {
	sort.SliceStable(rows, func(left, right int) bool {
		if sortColumn >= 0 && sortColumn < len(rows[left]) && sortColumn < len(rows[right]) {
			comparison := compareEnvelopeValues(rows[left][sortColumn], rows[right][sortColumn])
			if comparison != 0 {
				if descending {
					return comparison > 0
				}
				return comparison < 0
			}
		}
		for column := 0; column < len(rows[left]) && column < len(rows[right]); column++ {
			if column == sortColumn {
				continue
			}
			comparison := compareEnvelopeValues(rows[left][column], rows[right][column])
			if comparison != 0 {
				return comparison < 0
			}
		}
		return len(rows[left]) < len(rows[right])
	})
}

func visualExampleSort(example visualExample, envelope visualizationir.VisualizationEnvelope) (int, bool) {
	if example.Chart == nil || len(example.Chart.Query.Sort) == 0 {
		return -1, false
	}
	authored := example.Chart.Query.Sort[0]
	state, ok := envelope.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(state.Datasets) == 0 {
		return -1, authored.Direction == "desc"
	}
	columns := state.Datasets[0].Columns
	for index, column := range columns {
		if column == authored.Field {
			return index, authored.Direction == "desc"
		}
	}
	fieldMatches := func(field dashboardauthoring.FieldRef) bool {
		shortField := field.Field
		if separator := strings.LastIndex(shortField, "."); separator >= 0 {
			shortField = shortField[separator+1:]
		}
		return authored.Field != "" && (authored.Field == field.Alias || authored.Field == field.Field || authored.Field == shortField)
	}
	columnIndex := func(name string) int {
		for index, column := range columns {
			if column == name {
				return index
			}
		}
		return -1
	}
	for _, field := range example.Chart.Query.Dimensions {
		if fieldMatches(field) {
			return columnIndex("label"), authored.Direction == "desc"
		}
	}
	if fieldMatches(example.Chart.Query.Series) {
		return columnIndex("series"), authored.Direction == "desc"
	}
	for _, field := range example.Chart.Query.Measures {
		if fieldMatches(field) || authored.Field == "value" {
			return columnIndex("value"), authored.Direction == "desc"
		}
	}
	return -1, authored.Direction == "desc"
}

func compareEnvelopeValues(left, right any) int {
	leftNumber, leftIsNumber := envelopeNumber(left)
	rightNumber, rightIsNumber := envelopeNumber(right)
	if leftIsNumber && rightIsNumber {
		if leftNumber < rightNumber {
			return -1
		}
		if leftNumber > rightNumber {
			return 1
		}
		return 0
	}
	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right))
}

var visualDocMapRegions = map[string]map[string]struct{}{
	"brazil_states": stringSet("RR", "AP", "AM", "PA", "AC", "RO", "MT", "TO", "MA", "PI", "CE", "RN", "PB", "PE", "AL", "SE", "BA", "GO", "DF", "MS", "MG", "ES", "RJ", "SP", "PR", "SC", "RS"),
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func validateVisualEnvelope(example visualExample, envelope visualizationir.VisualizationEnvelope) error {
	if state, ok := envelope.DataState.Value.(*visualizationir.SpatialTiledVisualizationDataState); ok {
		if state.Cardinality.Kind != "exact" || state.Cardinality.Count == nil || *state.Cardinality.Count <= 0 {
			return fmt.Errorf("visual example %q has no tiled coordinate data", example.ID)
		}
		return nil
	}
	return validateVisualData(example, envelopeRows(envelope))
}

func envelopeHasData(envelope visualizationir.VisualizationEnvelope) bool {
	if state, ok := envelope.DataState.Value.(*visualizationir.SpatialTiledVisualizationDataState); ok {
		return state.Cardinality.Kind == "exact" && state.Cardinality.Count != nil && *state.Cardinality.Count > 0
	}
	return len(envelopeRows(envelope)) > 0
}

func validateVisualData(example visualExample, payload []dashboard.Datum) error {
	finiteNumbers := 0
	for index, datum := range payload {
		if len(datum) == 0 {
			return fmt.Errorf("visual example %q has an empty row at data[%d]", example.ID, index)
		}
		if err := inspectPayloadValue(datum, fmt.Sprintf("data[%d]", index), &finiteNumbers); err != nil {
			return fmt.Errorf("visual example %q %w", example.ID, err)
		}
	}
	if finiteNumbers == 0 {
		return fmt.Errorf("visual example %q has no finite numeric values", example.ID)
	}
	if example.Chart == nil || example.Chart.ResultShape() != "geo" {
		return nil
	}
	for _, layer := range example.Chart.Geo.Layers {
		if layer.Kind != "choropleth" {
			continue
		}
		regions, ok := visualDocMapRegions[layer.GeometryAsset]
		if !ok {
			return fmt.Errorf("visual example %q uses unsupported documentation map %q", example.ID, layer.GeometryAsset)
		}
		seenRegions := make(map[string]struct{}, len(payload))
		for index, datum := range payload {
			region, _ := datum[layer.Join].(string)
			if _, ok := regions[region]; !ok {
				return fmt.Errorf("visual example %q region %q is not defined by map %q at data[%d].%s", example.ID, region, layer.GeometryAsset, index, layer.Join)
			}
			seenRegions[region] = struct{}{}
		}
		for _, region := range sortedSet(regions) {
			if _, ok := seenRegions[region]; !ok {
				return fmt.Errorf("visual example %q does not provide data for map region %q in %q", example.ID, region, layer.GeometryAsset)
			}
		}
	}
	return nil
}

func envelopeRows(envelope visualizationir.VisualizationEnvelope) []dashboard.Datum {
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		for _, dataset := range state.Datasets {
			if dataset.ID == "primary" {
				return envelopeDatums(dataset.Columns, dataset.Rows)
			}
		}
		return nil
	case *visualizationir.WindowedVisualizationDataState:
		columns := make([]string, len(state.Schema.Fields))
		for index, field := range state.Schema.Fields {
			columns[index] = field.ID
		}
		blocks := make([]visualizationir.VisualizationWindowBlock, 0, len(state.Blocks))
		for _, block := range state.Blocks {
			blocks = append(blocks, block)
		}
		sort.Slice(blocks, func(left, right int) bool {
			if blocks[left].Start != blocks[right].Start {
				return blocks[left].Start < blocks[right].Start
			}
			return blocks[left].ID < blocks[right].ID
		})
		rows := [][]any{}
		for _, block := range blocks {
			rows = append(rows, block.Rows...)
		}
		return envelopeDatums(columns, rows)
	default:
		return nil
	}
}

func envelopeDatums(columns []string, rows [][]any) []dashboard.Datum {
	out := make([]dashboard.Datum, len(rows))
	for rowIndex, values := range rows {
		if len(values) != len(columns) {
			return nil
		}
		out[rowIndex] = make(dashboard.Datum, len(values))
		for columnIndex, column := range columns {
			out[rowIndex][column] = values[columnIndex]
		}
	}
	return out
}

func normalizeEnvelopeRevision(envelope *visualizationir.VisualizationEnvelope, dataRevision, generation int64) {
	if envelope == nil {
		return
	}
	envelope.DataRevision = dataRevision
	for index := range envelope.Selection {
		envelope.Selection[index].Datum.DataRevision = dataRevision
	}
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		state.DataRevision, state.Generation = dataRevision, generation
		for index := range state.Datasets {
			state.Datasets[index].DataRevision, state.Datasets[index].Generation = dataRevision, generation
		}
	case *visualizationir.WindowedVisualizationDataState:
		state.DataRevision, state.Generation = dataRevision, generation
	case *visualizationir.SpatialTiledVisualizationDataState:
		state.DataRevision, state.Generation = dataRevision, generation
		state.TileURL = "/workspaces/visual_examples/dashboards/visual_examples/visuals/" + envelope.VisualID + "/tiles/documentation/{z}/{x}/{y}.mvt"
	}
}

func envelopeNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func inspectPayloadValue(value any, path string, finiteNumbers *int) error {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Errorf("contains a non-finite number at %s", path)
		}
		*finiteNumbers++
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return fmt.Errorf("contains a non-finite number at %s", path)
		}
		*finiteNumbers++
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		*finiteNumbers++
	case dashboard.Datum:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := inspectPayloadValue(typed[key], path+"."+key, finiteNumbers); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := inspectPayloadValue(typed[key], path+"."+key, finiteNumbers); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := inspectPayloadValue(item, fmt.Sprintf("%s[%d]", path, index), finiteNumbers); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildVisualDocumentReference(examples []visualExample) (visualDocumentReference, error) {
	if len(examples) > 0 && examples[0].Tabular != nil {
		return visualDocumentReference{
			Kind: "visual", Renderer: "tabular", Shapes: []string{examples[0].Type},
			QueryFields: []string{"table", "fields", "rows", "columns", "measures"},
			Fields: []visualdocs.FieldReference{
				{Path: "type", Type: "string", AllowedValues: []string{"table", "matrix", "pivot"}, Description: "Selects the tabular visual behavior."},
				{Path: "query", Type: "tabular query", Description: "Selects record fields or grouped row, column, and measure fields."},
				{Path: "cardinality", Type: "string", AllowedValues: []string{"bounded", "exact"}, Description: "Controls whether the visual resolves an exact row count."},
			},
			Accessibility: "Tabular visuals expose semantic headers and virtualized rows while preserving keyboard navigation.",
			Examples:      map[string]visualExampleReference{examples[0].ID: {KeyFields: []string{"type", "query", "columns"}}},
		}, nil
	}
	kinds := map[string]struct{}{}
	renderers := map[string]struct{}{}
	shapes := map[string]struct{}{}
	queryFields := map[string]struct{}{}
	presentation := map[string]struct{}{}
	hasCalculations := false
	reference := visualDocumentReference{Examples: make(map[string]visualExampleReference, len(examples))}
	var previous *dashboardauthoring.Visual
	for index := range examples {
		visual := *examples[index].Chart
		kinds[visual.KindOrDefault()] = struct{}{}
		capability, _ := dashboardauthoring.VisualizationCapabilityForType(visual.Type)
		renderers[capability.Renderer] = struct{}{}
		shapes[visual.ResultShape()] = struct{}{}
		collectQueryFields(visual.Query, queryFields)
		if len(visual.Datasets) > 0 {
			queryFields["datasets"] = struct{}{}
		}
		if len(visual.Calculations) > 0 {
			hasCalculations = true
		}
		for key := range visualPresentationValues(visual) {
			presentation[key] = struct{}{}
		}
		for key := range visualKPIValues(visual) {
			presentation["kpi."+key] = struct{}{}
		}
		reference.Examples[examples[index].ID] = visualExampleReference{KeyFields: visualKeyFields(previous, visual)}
		previous = examples[index].Chart
	}
	reference.Kind = strings.Join(sortedSet(kinds), ", ")
	reference.Renderer = strings.Join(sortedSet(renderers), ", ")
	reference.Shapes = sortedSet(shapes)
	reference.QueryFields = sortedSet(queryFields)
	reference.Presentation = sortedSet(presentation)
	fields, err := visualFieldReferences(reference.QueryFields, reference.Presentation, examples[0].Chart.Type)
	if err != nil {
		return visualDocumentReference{}, err
	}
	if hasCalculations {
		fields = append(fields, visualdocs.FieldReference{
			Path: "calculations", Type: "closed visual calculation list", Default: "none",
			AllowedValues: []string{"running_total", "moving_average", "difference", "percentage_difference", "percent_of_parent", "percent_of_grand_total", "rank", "cumulative_contribution", "lookup"},
			Description:   "Evaluates governed post-aggregation templates over compiler-owned result-frame aliases with explicit ordering, partitions, and incomplete-frame diagnostics.",
		})
	}
	reference.Fields = fields
	reference.Accessibility = visualAccessibilityGuidance(*examples[0].Chart)
	return reference, nil
}

func collectQueryFields(query dashboardauthoring.VisualQuery, fields map[string]struct{}) {
	if query.Table != "" {
		fields["table"] = struct{}{}
	}
	if len(query.Dimensions) > 0 {
		fields["dimensions"] = struct{}{}
	}
	if !query.Series.IsZero() {
		fields["series"] = struct{}{}
	}
	if len(query.Measures) > 0 {
		fields["measures"] = struct{}{}
	}
	if query.Time.Field != "" {
		fields["time"] = struct{}{}
	}
	if len(query.Sort) > 0 {
		fields["sort"] = struct{}{}
	}
	if query.Limit > 0 {
		fields["limit"] = struct{}{}
	}
}

func visualKeyFields(previous *dashboardauthoring.Visual, visual dashboardauthoring.Visual) []string {
	fields := make([]string, 0, 12)
	changedToValue := func(before, after any) bool {
		return valueIsSet(after) && (previous == nil || !reflect.DeepEqual(before, after))
	}
	queryChecks := []struct {
		name string
		get  func(dashboardauthoring.VisualQuery) any
	}{
		{"table", func(query dashboardauthoring.VisualQuery) any { return query.Table }},
		{"dimensions", func(query dashboardauthoring.VisualQuery) any { return query.Dimensions }},
		{"series", func(query dashboardauthoring.VisualQuery) any { return query.Series }},
		{"measures", func(query dashboardauthoring.VisualQuery) any { return query.Measures }},
		{"time", func(query dashboardauthoring.VisualQuery) any { return query.Time }},
	}
	for _, check := range queryChecks {
		var before any
		if previous != nil {
			before = check.get(previous.Query)
		}
		after := check.get(visual.Query)
		if changedToValue(before, after) {
			fields = append(fields, "query."+check.name)
		}
	}
	values := visualPresentationValues(visual)
	optionKeys := make(map[string]struct{}, len(values))
	for key := range values {
		optionKeys[key] = struct{}{}
	}
	previousValues := map[string]any{}
	if previous != nil {
		previousValues = visualPresentationValues(*previous)
	}
	for _, key := range sortedSet(optionKeys) {
		if previous == nil || !reflect.DeepEqual(previousValues[key], values[key]) {
			fields = append(fields, "presentation."+key)
		}
	}
	if len(visual.Geo.Layers) > 0 && (previous == nil || !reflect.DeepEqual(previous.Geo.Layers, visual.Geo.Layers)) {
		fields = append(fields, "geo.layers")
	}
	if len(visual.Datasets) > 0 && (previous == nil || !reflect.DeepEqual(previous.Datasets, visual.Datasets)) {
		fields = append(fields, "datasets")
	}
	if len(visual.Calculations) > 0 && (previous == nil || !reflect.DeepEqual(previous.Calculations, visual.Calculations)) {
		fields = append(fields, "calculations")
	}
	kpiValues := visualKPIValues(visual)
	previousKPIValues := map[string]any{}
	if previous != nil {
		previousKPIValues = visualKPIValues(*previous)
	}
	kpiKeys := make(map[string]struct{}, len(kpiValues))
	for key := range kpiValues {
		kpiKeys[key] = struct{}{}
	}
	for _, key := range sortedSet(kpiKeys) {
		if previous == nil || !reflect.DeepEqual(previousKPIValues[key], kpiValues[key]) {
			fields = append(fields, "kpi."+key)
		}
	}
	return fields
}

func visualPresentationValues(visual dashboardauthoring.Visual) map[string]any {
	value := reflect.ValueOf(visual.Presentation)
	typeInfo := value.Type()
	out := make(map[string]any)
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.IsZero() {
			continue
		}
		name := typeInfo.Field(index).Tag.Get("yaml")
		if name != "" && name != "-" {
			out[name] = field.Interface()
		}
	}
	return out
}

func visualKPIValues(visual dashboardauthoring.Visual) map[string]any {
	if visual.Type != "kpi" {
		return nil
	}
	value := reflect.ValueOf(visual.KPI)
	typeInfo := value.Type()
	out := make(map[string]any)
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.IsZero() {
			continue
		}
		name := strings.Split(typeInfo.Field(index).Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			out[name] = field.Interface()
		}
	}
	return out
}

func valueIsSet(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	return reflected.IsValid() && !reflected.IsZero()
}

func valueOrZero(previous *dashboardauthoring.Visual, get func(dashboardauthoring.Visual) any) any {
	if previous == nil {
		return nil
	}
	return get(*previous)
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func visualAccessibilityGuidance(visual dashboardauthoring.Visual) string {
	if visual.KindOrDefault() == "kpi" {
		return "State current, comparison, target, and status in text; use a direction cue and label so color is never the only indication of change."
	}
	switch visual.Type {
	case "map":
		return "Use a descriptive summary for the geographic pattern, verify region joins or coordinate fields, and do not rely on color alone to communicate intensity."
	case "graph", "sankey", "tree", "sunburst", "treemap":
		return "Use meaningful node labels and keep the hierarchy or flow small enough to follow without relying on color alone."
	default:
		return "Use a descriptive title and unit, and do not rely on color alone to distinguish series or values."
	}
}

func buildExampleDashboard(catalog visualCatalog, examplesByPage map[string][]visualExample) *dashboardauthoring.Dashboard {
	report := &dashboardauthoring.Dashboard{ID: "visual-docs", Title: "Visual documentation", Description: "Executable documentation examples.", SemanticModel: "visual_examples", Visuals: map[string]dashboardauthoring.AuthoringVisualization{}, Pages: make([]dashboard.Page, 0, len(catalog.Documents))}
	for _, document := range catalog.Documents {
		page := dashboard.Page{ID: document.Source, Title: document.Title, Canvas: dashboard.PageCanvas{Width: 1366, Height: 3000}, Grid: dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}, Visuals: make([]dashboard.PageVisual, 0, len(examplesByPage[document.Source]))}
		for index, example := range examplesByPage[document.Source] {
			component := dashboard.PageVisual{ID: example.ID, Placement: dashboard.PagePlacement{Col: 1, Row: 1 + index*8, ColSpan: 6, RowSpan: 7}}
			if example.Chart != nil {
				report.Visuals[example.ID] = dashboardauthoring.ChartVisualization(*example.Chart)
				component.Kind, component.Visual = "visual", example.ID
			} else {
				report.Visuals[example.ID] = dashboardauthoring.TabularVisualization(example.Type, *example.Tabular)
				component.Kind, component.Visual = "visual", example.ID
			}
			page.Visuals = append(page.Visuals, component)
		}
		report.Pages = append(report.Pages, page)
	}
	return report
}

func bindFixtureRoot(definition *manifest.Workspace, dataRoot string) error {
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return err
	}
	for _, model := range definition.Models {
		for name, connection := range model.Connections {
			if connection.Kind != "managed" {
				continue
			}
			connection.Root = root
			connection.Scope = ""
			model.Connections[name] = connection
		}
	}
	return nil
}

func parseVisualExamples(filename string, source []byte) ([]visualExample, error) {
	lines := strings.Split(strings.ReplaceAll(string(source), "\r\n", "\n"), "\n")
	shortcodes := map[string]int{}
	examples := make([]visualExample, 0)
	seenExamples := map[string]int{}

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if match := visualShortcodePattern.FindStringSubmatch(line); len(match) == 2 {
			id := match[1]
			if previous := shortcodes[id]; previous > 0 {
				return nil, fmt.Errorf("%s:%d: duplicate visual shortcode %q (first declared on line %d)", filename, index+1, id, previous)
			}
			shortcodes[id] = index + 1
			continue
		}

		match := visualFencePattern.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		id := match[1]
		if previous := seenExamples[id]; previous > 0 {
			return nil, fmt.Errorf("%s:%d: duplicate visual example %q (first declared on line %d)", filename, index+1, id, previous)
		}
		closing := -1
		for candidate := index + 1; candidate < len(lines); candidate++ {
			if strings.TrimSpace(lines[candidate]) == "```" {
				closing = candidate
				break
			}
		}
		if closing < 0 {
			return nil, fmt.Errorf("%s:%d: unclosed visual example %q", filename, index+1, id)
		}
		body := strings.Join(lines[index+1:closing], "\n") + "\n"
		decoder := yaml.NewDecoder(bytes.NewBufferString(body))
		decoder.KnownFields(true)
		var fragment visualExampleFragment
		if err := decoder.Decode(&fragment); err != nil {
			return nil, fmt.Errorf("%s:%d: decode visual example %q: %w", filename, index+2, id, err)
		}
		if len(fragment.Visuals) != 1 {
			return nil, fmt.Errorf("%s:%d: visual example %q must contain exactly one visual", filename, index+1, id)
		}
		visualNode, ok := fragment.Visuals[id]
		if !ok {
			keys := make([]string, 0, len(fragment.Visuals))
			for key := range fragment.Visuals {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return nil, fmt.Errorf("%s:%d: visual example %q must use visual key %q, got %q", filename, index+1, id, id, strings.Join(keys, ", "))
		}
		seenExamples[id] = index + 1
		example, err := decodeVisualExample(id, filename, index+1, visualNode)
		if err != nil {
			return nil, err
		}
		examples = append(examples, example)
		index = closing
	}

	for id, line := range shortcodes {
		if seenExamples[id] == 0 {
			return nil, fmt.Errorf("%s:%d: shortcode %q has no matching visual example", filename, line, id)
		}
	}
	for id, line := range seenExamples {
		if shortcodes[id] == 0 {
			return nil, fmt.Errorf("%s:%d: visual example %q has no matching shortcode", filename, line, id)
		}
	}
	return examples, nil
}

func decodeVisualExample(id, filename string, line int, node yaml.Node) (visualExample, error) {
	var tag struct {
		Type string `yaml:"type"`
	}
	if err := node.Decode(&tag); err != nil {
		return visualExample{}, err
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == "kind" {
			return visualExample{}, fmt.Errorf("%s:%d: visual %q uses removed field kind; use type", filename, line, id)
		}
	}
	if strings.TrimSpace(tag.Type) == "" {
		return visualExample{}, fmt.Errorf("%s:%d: visual %q requires type", filename, line, id)
	}
	if err := validateVisualExampleContract(id, filename, node); err != nil {
		return visualExample{}, fmt.Errorf("%s:%d: visual %q: %w", filename, line, id, err)
	}
	var authored dashboardauthoring.AuthoringVisualization
	if err := node.Decode(&authored); err != nil {
		return visualExample{}, fmt.Errorf("%s:%d: decode visual %q: %w", filename, line, id, err)
	}
	example := visualExample{ID: id, Source: filename, Line: line, Type: authored.Type, Chart: authored.Chart, Tabular: authored.Tabular}
	return example, nil
}

func validateVisualExampleContract(id, filename string, node yaml.Node) error {
	var visual any
	if err := node.Decode(&visual); err != nil {
		return err
	}
	resource := map[string]any{
		"apiVersion": "leapview.dev/v1",
		"kind":       "Dashboard",
		"metadata":   map[string]any{"name": "visual-doc-example"},
		"spec": map[string]any{
			"semanticModel": "visual_examples",
			"visuals":       map[string]any{id: visual},
			"pages": []any{map[string]any{
				"id": "example", "title": "Example",
				"components": []any{map[string]any{
					"id": id, "kind": "visual", "visual": id,
					"placement": map[string]int{"col": 1, "row": 1, "col_span": 6, "row_span": 4},
				}},
			}},
		},
	}
	content, err := yaml.Marshal(resource)
	if err != nil {
		return err
	}
	return configschema.ValidateBytes(configschema.KindDashboard, filename, content)
}
