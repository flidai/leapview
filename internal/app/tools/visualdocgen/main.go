// Command visualdocgen compiles executable visual examples embedded in the
// public Markdown documentation into deterministic browser payloads.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"

	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/app/site/visualdocs"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardadapter "github.com/flidai/leapview/internal/dashboard/analyticsruntime"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationdecimal "github.com/flidai/leapview/internal/dashboard/visualization/decimal"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/extension"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/schema"
	"github.com/flidai/leapview/internal/workload"
	"gopkg.in/yaml.v3"
)

var visualShortcodePattern = regexp.MustCompile(`^\s*\{\{<\s*visual\s+id="([a-z0-9_]+)"\s*>}}\s*$`)
var visualFencePattern = regexp.MustCompile("^```ya?ml[ \\t]+visual-example=([a-z0-9_]+)[ \\t]*$")

const (
	visualDocsDashboardID projectgraph.ResourceID = "dashboard:visual-docs"
)

// visualDocExtensionAdmission is an explicit test-tool admission boundary.
// The generator never asks DuckDB to install or autoload an extension; it
// resolves the already-installed DuckLake artifact and supplies its exact
// immutable path to the fixture runtime.
type visualDocExtensionAdmission struct {
}

func (a visualDocExtensionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	path, err := findVisualDocExtension(name)
	if err != nil {
		return extension.AdmittedExtension{}, err
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return extension.AdmittedExtension{}, fmt.Errorf("read visual-doc %s extension: %w", name, err)
	}
	digest := sha256.Sum256(bytes)
	return extension.AdmittedExtension{
		Name: name, Identity: "visual-doc-fixture/" + name, Version: "fixture",
		Platform: runtime.GOOS + "-" + runtime.GOARCH, Digest: "sha256:" + hex.EncodeToString(digest[:]), Path: path,
	}, nil
}

func visualDocDuckLakeAdmission() (extension.Admission, error) {
	// Resolve DuckLake during fixture setup so a missing reviewed local cache
	// fails before opening the runtime. Other approved extensions are resolved
	// through the same explicit admission object on demand.
	if _, err := findVisualDocExtension("ducklake"); err != nil {
		return nil, err
	}
	return visualDocExtensionAdmission{}, nil
}

func findVisualDocExtension(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("visual-doc extension name is required")
	}
	filename := name + ".duckdb_extension"
	if name == "sqlite" {
		filename = "sqlite_scanner.duckdb_extension"
	}
	roots := make([]string, 0, 2)
	if configured := strings.TrimSpace(os.Getenv("DUCKDB_EXTENSION_DIRECTORY")); configured != "" {
		roots = append(roots, configured)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, filepath.FromSlash(".cache/leapview/ci-duckdb-extensions")))
		roots = append(roots, filepath.Join(home, ".duckdb", "extensions"))
	}
	for _, root := range roots {
		root = filepath.Clean(root)
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		var found string
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if path != root && len(strings.Split(strings.TrimPrefix(path, root), string(filepath.Separator))) > 4 {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() == filename && found == "" {
				fileInfo, statErr := entry.Info()
				if statErr == nil && fileInfo.Mode().IsRegular() {
					found = path
				}
			}
			return nil
		})
		if err != nil {
			continue
		}
		if found != "" {
			absolute, absErr := filepath.Abs(found)
			if absErr == nil && filepath.Clean(absolute) == absolute {
				return absolute, nil
			}
		}
	}
	return "", fmt.Errorf("visual-doc %s extension is not installed; run task ci:extensions:prepare or set DUCKDB_EXTENSION_DIRECTORY", name)
}

type visualExample struct {
	ID     string
	Source string
	Line   int
	Type   string
	Visual dashboarddocument.DashboardVisual
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

	compiled, err := projectcompiler.CompileProject(projectPath)
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("compile fixture project: %w", err)
	}
	manifest := compiled.Manifest()
	semanticModelID := projectgraph.ResourceID(manifest.NameIndex.SemanticModels["visual_examples"])
	if err := semanticModelID.Validate(); err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("fixture project semantic model identity: %w", err)
	}
	models := compiled.Models()
	if _, ok := models[semanticModelID.String()]; !ok {
		return visualExamplesArtifact{}, fmt.Errorf("fixture project has no visual_examples semantic model")
	}
	report := buildExampleDashboard(catalog, examplesByPage, visualDocsDashboardID, semanticModelID)
	compiledReport, err := dashboardcompiler.CompileDocument(report, models)
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("validate executable examples: %w", err)
	}
	normalizedReport := compiledReport.Normalized
	compiledDashboard := compiledReport.Definition
	if err := bindFixtureDataRoot(models, dataRoot); err != nil {
		return visualExamplesArtifact{}, err
	}
	modelDefinitions := make(map[projectgraph.ResourceID]*semanticmodel.Model, len(models))
	for id, model := range models {
		resourceID, err := projectgraph.NewResourceID(id)
		if err != nil {
			return visualExamplesArtifact{}, fmt.Errorf("fixture semantic model %q: %w", id, err)
		}
		modelDefinitions[resourceID] = model
	}
	definition, err := dashboardruntime.NewProjectDefinition(
		compiled.ProjectID(), manifest.Title, manifest.Description, modelDefinitions,
		map[projectgraph.ResourceID]dashboarddefinition.Definition{visualDocsDashboardID: compiledDashboard},
	)
	if err != nil {
		return visualExamplesArtifact{}, fmt.Errorf("build fixture dashboard definition: %w", err)
	}

	runtimeDir, err := os.MkdirTemp("", "leapview-visual-docs-*")
	if err != nil {
		return visualExamplesArtifact{}, err
	}
	defer os.RemoveAll(runtimeDir)
	ducklakeAdmission, err := visualDocDuckLakeAdmission()
	if err != nil {
		return visualExamplesArtifact{}, err
	}
	database, err := analyticsducklake.Open(context.Background(), analyticsducklake.Config{RootDir: filepath.Join(runtimeDir, "ducklake"), MaxConnections: 1, ExtensionAdmission: ducklakeAdmission})
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
	projects := analyticsruntime.ProjectFactoryFunc(func(ctx context.Context, request analyticsruntime.ProjectRequest) (analyticsruntime.Project, error) {
		if err := bindFixtureDataRoot(request.Models, dataRoot); err != nil {
			return nil, err
		}
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
		return analyticsduckdb.OpenProjectMaterializeRuntime(ctx, analyticsduckdb.ProjectRuntimeConfig{
			Models: request.Models, Database: database,
			CredentialResolver: analyticsduckdb.NonSecretCredentialResolver{},
			SnapshotID:         request.SnapshotID, ServingStateID: request.ServingStateID,
			ProjectID: request.ProjectID, Environment: request.Environment,
			SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
			SourceDataDigest: request.SourceDataDigest, ResultLimits: request.ResultLimits,
		})
	})
	identity, err := projectgraph.NewServingIdentity(compiled.ProjectID(), "development", "visual-docs")
	if err != nil {
		refreshLease.Release()
		return visualExamplesArtifact{}, fmt.Errorf("build fixture serving identity: %w", err)
	}
	service, err := dashboardruntime.NewFromGeneration(refreshLease.Context(), runtimeDir, dashboardadapter.NewFactory(dashboardadapter.Options{Projects: projects, ProjectID: compiled.ProjectID(), Environment: "development"}), identity, definition)
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
		patch, err := service.QueryDashboardPage(queryLease.Context(), normalizedReport.Metadata.ID, document.Source, dashboard.Filters{})
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
		reference, err := buildVisualDocumentReference(examplesByPage[document.Source], compiledDashboard.Visualizations)
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
	base, err := envelope.Spec.Base()
	if err != nil {
		return
	}
	for datasetIndex := range state.Datasets {
		dataset := state.Datasets[datasetIndex]
		columnTypes := envelopeColumnTypes(*base, datasetIndex, dataset)
		sortEnvelopeRows(dataset.Rows, sortColumn, descending, columnTypes)
	}
}

func envelopeColumnTypes(base visualizationir.VisualizationSpecBase, datasetIndex int, dataset visualizationir.VisualizationInlineDataset) []visualizationir.VisualizationDataType {
	var schema *visualizationir.VisualizationDatasetSchema
	for index := range base.Datasets {
		candidate := &base.Datasets[index]
		if candidate.ID == dataset.ID || (dataset.ID == "" && index == 0) {
			schema = candidate
			break
		}
	}
	if schema == nil {
		if datasetIndex >= 0 && datasetIndex < len(base.Datasets) {
			schema = &base.Datasets[datasetIndex]
		}
	}
	types := make([]visualizationir.VisualizationDataType, len(dataset.Columns))
	if schema == nil {
		return types
	}
	for columnIndex, column := range dataset.Columns {
		for _, field := range schema.Fields {
			if field.ID == column {
				types[columnIndex] = field.DataType
				break
			}
		}
	}
	return types
}

func sortEnvelopeRows(rows [][]any, sortColumn int, descending bool, columnTypes []visualizationir.VisualizationDataType) {
	sort.SliceStable(rows, func(left, right int) bool {
		if sortColumn >= 0 && sortColumn < len(rows[left]) && sortColumn < len(rows[right]) {
			comparison := compareEnvelopeValues(rows[left][sortColumn], rows[right][sortColumn], columnTypeAt(columnTypes, sortColumn))
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
			comparison := compareEnvelopeValues(rows[left][column], rows[right][column], columnTypeAt(columnTypes, column))
			if comparison != 0 {
				return comparison < 0
			}
		}
		return len(rows[left]) < len(rows[right])
	})
}

func columnTypeAt(columnTypes []visualizationir.VisualizationDataType, column int) visualizationir.VisualizationDataType {
	if column < 0 || column >= len(columnTypes) {
		return ""
	}
	return columnTypes[column]
}

func visualExampleSort(example visualExample, envelope visualizationir.VisualizationEnvelope) (int, bool) {
	authored, ok := canonicalVisualSort(example.Visual)
	if !ok {
		return -1, false
	}
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
	fieldMatches := func(field canonicalVisualField) bool {
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
	for _, field := range canonicalVisualDimensions(example.Visual.Query) {
		if fieldMatches(field) {
			return columnIndex("label"), authored.Direction == "desc"
		}
	}
	for _, field := range canonicalVisualMetrics(example.Visual.Query) {
		if fieldMatches(field) || authored.Field == "value" {
			return columnIndex("value"), authored.Direction == "desc"
		}
	}
	return -1, authored.Direction == "desc"
}

func compareEnvelopeValues(left, right any, dataType visualizationir.VisualizationDataType) int {
	var leftNumber, rightNumber *big.Rat
	var leftIsNumber, rightIsNumber bool
	switch dataType {
	case visualizationir.VisualizationDataTypeDecimal:
		leftNumber, leftIsNumber = exactDecimalEnvelopeNumber(left)
		rightNumber, rightIsNumber = exactDecimalEnvelopeNumber(right)
	case visualizationir.VisualizationDataTypeInteger, visualizationir.VisualizationDataTypeFloat:
		leftNumber, leftIsNumber = exactEnvelopeNumber(left)
		rightNumber, rightIsNumber = exactEnvelopeNumber(right)
	}
	if leftIsNumber && rightIsNumber {
		return leftNumber.Cmp(rightNumber)
	}
	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right))
}

// exactEnvelopeNumber converts the numeric values allowed at the visualization
// boundary into exact rationals. Decimal metrics are canonical fixed-point
// strings, so they must never pass through float64 during deterministic row
// ordering.
func exactEnvelopeNumber(value any) (*big.Rat, bool) {
	switch typed := value.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(typed)), true
	case int8:
		return new(big.Rat).SetInt64(int64(typed)), true
	case int16:
		return new(big.Rat).SetInt64(int64(typed)), true
	case int32:
		return new(big.Rat).SetInt64(int64(typed)), true
	case int64:
		return new(big.Rat).SetInt64(typed), true
	case uint:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), true
	case uint8:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), true
	case uint16:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), true
	case uint32:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), true
	case uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(typed)), true
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(float64(typed)), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(typed), true
	default:
		return nil, false
	}
}

func exactDecimalEnvelopeNumber(value any) (*big.Rat, bool) {
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	parsed, _, err := visualizationdecimal.Parse(text)
	if err != nil {
		return nil, false
	}
	return parsed, true
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
		state.TileURL = "/dashboards/" + url.PathEscape(visualDocsDashboardID.String()) + "/visuals/" + url.PathEscape(envelope.VisualID) + "/tiles/documentation/{z}/{x}/{y}.mvt"
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
	case string:
		if visualizationdecimal.Validate(typed) == nil {
			*finiteNumbers++
		}
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

func buildVisualDocumentReference(examples []visualExample, compiledVisualizations map[string]visualizationdefinition.Definition) (visualDocumentReference, error) {
	if len(examples) == 0 {
		return visualDocumentReference{}, fmt.Errorf("visual document has no examples")
	}
	if len(examples) > 0 && isTabularVisual(examples[0].Visual.Type) {
		compiled, ok := compiledVisualizations[examples[0].ID]
		if !ok {
			return visualDocumentReference{}, fmt.Errorf("compiled visual %q is missing", examples[0].ID)
		}
		reference := visualDocumentReference{
			Kind: visualKindFromRenderer(compiled.RendererID), Renderer: compiled.RendererID, Shapes: []string{string(compiled.Query.ResultShape)},
			QueryFields: []string{"dataset", "fields", "rows", "columns", "metrics"},
			Fields: []visualdocs.FieldReference{
				{Path: "type", Type: "string", AllowedValues: []string{"table", "matrix", "pivot"}, Description: "Selects the tabular visual behavior."},
				{Path: "query", Type: "tabular query", Description: "Selects record fields or grouped row, column, and metric fields."},
				{Path: "cardinality", Type: "string", AllowedValues: []string{"bounded", "exact"}, Description: "Controls whether the visual resolves an exact row count."},
			},
			Accessibility: "Tabular visuals expose semantic headers and virtualized rows while preserving keyboard navigation.",
			Examples:      make(map[string]visualExampleReference, len(examples)),
		}
		presentation := map[string]struct{}{}
		var previous *dashboarddocument.DashboardVisual
		for index := range examples {
			for key := range visualPresentationValues(examples[index].Visual) {
				presentation[key] = struct{}{}
			}
			keyFields := visualKeyFields(previous, examples[index].Visual)
			if len(keyFields) == 0 {
				keyFields = []string{"type", "query"}
			}
			reference.Examples[examples[index].ID] = visualExampleReference{KeyFields: keyFields}
			previous = &examples[index].Visual
		}
		reference.Presentation = sortedSet(presentation)
		presentationFields, err := visualFieldReferences(nil, reference.Presentation, string(examples[0].Visual.Type))
		if err != nil {
			return visualDocumentReference{}, err
		}
		reference.Fields = append(reference.Fields, presentationFields...)
		return reference, nil
	}
	kinds := map[string]struct{}{}
	renderers := map[string]struct{}{}
	shapes := map[string]struct{}{}
	queryFields := map[string]struct{}{}
	presentation := map[string]struct{}{}
	hasCalculations := false
	reference := visualDocumentReference{Examples: make(map[string]visualExampleReference, len(examples))}
	var previous *dashboarddocument.DashboardVisual
	for index := range examples {
		visual := examples[index].Visual
		compiled, ok := compiledVisualizations[examples[index].ID]
		if !ok {
			return visualDocumentReference{}, fmt.Errorf("compiled visual %q is missing", examples[index].ID)
		}
		kinds[visualKindFromRenderer(compiled.RendererID)] = struct{}{}
		renderers[compiled.RendererID] = struct{}{}
		shapes[string(compiled.Query.ResultShape)] = struct{}{}
		collectQueryFields(visual.Query, queryFields)
		if visual.Datasets != nil && len(*visual.Datasets) > 0 {
			queryFields["datasets"] = struct{}{}
		}
		if visual.Calculations != nil && len(*visual.Calculations) > 0 {
			hasCalculations = true
		}
		for key := range visualPresentationValues(visual) {
			presentation[key] = struct{}{}
		}
		reference.Examples[examples[index].ID] = visualExampleReference{KeyFields: visualKeyFields(previous, visual)}
		previous = &examples[index].Visual
	}
	reference.Kind = strings.Join(sortedSet(kinds), ", ")
	reference.Renderer = strings.Join(sortedSet(renderers), ", ")
	reference.Shapes = sortedSet(shapes)
	reference.QueryFields = sortedSet(queryFields)
	reference.Presentation = sortedSet(presentation)
	fields, err := visualFieldReferences(reference.QueryFields, reference.Presentation, string(examples[0].Visual.Type))
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
	reference.Accessibility = visualAccessibilityGuidance(examples[0].Visual)
	return reference, nil
}

func collectQueryFields(query dashboarddocument.DashboardQuery, fields map[string]struct{}) {
	switch value := query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		if len(value.Dimensions) > 0 {
			fields["dimensions"] = struct{}{}
		}
		if len(value.Metrics) > 0 {
			fields["metrics"] = struct{}{}
		}
		if value.Sort != nil {
			fields["sort"] = struct{}{}
		}
		if value.Limit != nil {
			fields["limit"] = struct{}{}
		}
	case *dashboarddocument.RecordsDashboardQuery:
		fields["dataset"] = struct{}{}
		if len(value.Fields) > 0 {
			fields["fields"] = struct{}{}
		}
		if value.Sort != nil {
			fields["sort"] = struct{}{}
		}
		if value.Limit != nil {
			fields["limit"] = struct{}{}
		}
	case *dashboarddocument.PivotDashboardQuery:
		if len(value.Rows) > 0 {
			fields["rows"] = struct{}{}
		}
		if len(value.Columns) > 0 {
			fields["columns"] = struct{}{}
		}
		if len(value.Metrics) > 0 {
			fields["metrics"] = struct{}{}
		}
		if value.Sort != nil {
			fields["sort"] = struct{}{}
		}
	case *dashboarddocument.HistogramDashboardQuery, *dashboarddocument.DistributionDashboardQuery:
		fields["field"] = struct{}{}
	}
}

type canonicalVisualField struct{ Field, Alias string }

func canonicalDimensionField(value dashboarddocument.DashboardDimensionSelection) canonicalVisualField {
	if value.String != nil {
		return canonicalVisualField{Field: *value.String}
	}
	if value.Reference != nil {
		alias := ""
		if value.Reference.Alias != nil {
			alias = *value.Reference.Alias
		}
		return canonicalVisualField{Field: value.Reference.Dimension, Alias: alias}
	}
	return canonicalVisualField{}
}

func canonicalMetricField(value dashboarddocument.DashboardMetricSelection) canonicalVisualField {
	if value.String != nil {
		return canonicalVisualField{Field: *value.String}
	}
	if value.Reference != nil {
		alias := ""
		if value.Reference.Alias != nil {
			alias = *value.Reference.Alias
		}
		return canonicalVisualField{Field: value.Reference.Metric, Alias: alias}
	}
	return canonicalVisualField{}
}

func canonicalRecordField(value dashboarddocument.DashboardRecordFieldSelection) canonicalVisualField {
	if value.String != nil {
		return canonicalVisualField{Field: *value.String}
	}
	if value.Reference != nil {
		alias := ""
		if value.Reference.Alias != nil {
			alias = *value.Reference.Alias
		}
		return canonicalVisualField{Field: value.Reference.Field, Alias: alias}
	}
	return canonicalVisualField{}
}

func canonicalVisualDimensions(query dashboarddocument.DashboardQuery) []canonicalVisualField {
	var values []dashboarddocument.DashboardDimensionSelection
	switch value := query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		values = value.Dimensions
	case *dashboarddocument.PivotDashboardQuery:
		values = append(append([]dashboarddocument.DashboardDimensionSelection{}, value.Rows...), value.Columns...)
	}
	out := make([]canonicalVisualField, 0, len(values))
	for _, value := range values {
		out = append(out, canonicalDimensionField(value))
	}
	return out
}

func canonicalVisualMetrics(query dashboarddocument.DashboardQuery) []canonicalVisualField {
	var values []dashboarddocument.DashboardMetricSelection
	switch value := query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		values = value.Metrics
	case *dashboarddocument.PivotDashboardQuery:
		values = value.Metrics
	case *dashboarddocument.HistogramDashboardQuery:
		values = []dashboarddocument.DashboardMetricSelection{value.Field}
	case *dashboarddocument.DistributionDashboardQuery:
		values = []dashboarddocument.DashboardMetricSelection{value.Field}
	}
	out := make([]canonicalVisualField, 0, len(values))
	for _, value := range values {
		out = append(out, canonicalMetricField(value))
	}
	return out
}

func canonicalVisualRecords(query dashboarddocument.DashboardQuery) []canonicalVisualField {
	if value, ok := query.Value.(*dashboarddocument.RecordsDashboardQuery); ok {
		out := make([]canonicalVisualField, 0, len(value.Fields))
		for _, field := range value.Fields {
			out = append(out, canonicalRecordField(field))
		}
		return out
	}
	return nil
}

func canonicalVisualSort(visual dashboarddocument.DashboardVisual) (dashboarddocument.DashboardSort, bool) {
	var values *[]dashboarddocument.DashboardSort
	switch value := visual.Query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		values = value.Sort
	case *dashboarddocument.RecordsDashboardQuery:
		values = value.Sort
	case *dashboarddocument.PivotDashboardQuery:
		values = value.Sort
	}
	if values == nil || len(*values) == 0 {
		return dashboarddocument.DashboardSort{}, false
	}
	return (*values)[0], true
}

func visualKeyFields(previous *dashboarddocument.DashboardVisual, visual dashboarddocument.DashboardVisual) []string {
	fields := make([]string, 0, 12)
	changedToValue := func(before, after any) bool {
		return valueIsSet(after) && (previous == nil || !reflect.DeepEqual(before, after))
	}
	queryChecks := []struct {
		name string
		get  func(dashboarddocument.DashboardQuery) any
	}{
		{"dataset", func(query dashboarddocument.DashboardQuery) any {
			if value, ok := query.Value.(*dashboarddocument.RecordsDashboardQuery); ok {
				return value.Dataset
			}
			return ""
		}},
		{"dimensions", func(query dashboarddocument.DashboardQuery) any { return canonicalVisualDimensions(query) }},
		{"metrics", func(query dashboarddocument.DashboardQuery) any { return canonicalVisualMetrics(query) }},
		{"fields", func(query dashboarddocument.DashboardQuery) any { return canonicalVisualRecords(query) }},
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
	if visual.Datasets != nil && (previous == nil || !reflect.DeepEqual(previous.Datasets, visual.Datasets)) {
		fields = append(fields, "datasets")
	}
	if visual.Calculations != nil && len(*visual.Calculations) > 0 && (previous == nil || !reflect.DeepEqual(previous.Calculations, visual.Calculations)) {
		fields = append(fields, "calculations")
	}
	return fields
}

func visualPresentationValues(visual dashboarddocument.DashboardVisual) map[string]any {
	if visual.Presentation.Value == nil {
		return nil
	}
	value := reflect.ValueOf(visual.Presentation.Value)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	typeInfo := value.Type()
	out := make(map[string]any)
	if base, err := visual.Presentation.Base(); err == nil && base.ConditionalFormatting != nil {
		out["conditionalFormatting"] = base.ConditionalFormatting
	}
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.IsZero() {
			continue
		}
		name := strings.Split(typeInfo.Field(index).Tag.Get("yaml"), ",")[0]
		if name == "labelPolicy" {
			// The compiler's IR calls the lowered policy labelPolicy; the
			// authored canonical Dashboard field is labels.
			name = "labels"
		}
		if name != "" && name != "-" && name != "type" && canonicalPresentationField(name) {
			out[name] = field.Interface()
		}
	}
	return out
}

func canonicalPresentationField(name string) bool {
	switch name {
	case "displayUnits", "legend", "labels", "stacking", "orientation", "rose", "centerLabel", "innerRadius", "outerRadius", "align", "sort", "initialDepth", "roam", "layout", "breadcrumb", "nodeGap", "curveness", "focus", "showSymbols", "smooth", "step", "dataZoom", "symbolSize", "labelPosition", "identity", "x", "y", "size", "color", "series", "label", "tooltip", "colorScale", "sizeScale", "overplot", "minimum", "maximum", "target", "showPointer", "area", "progressWidth", "rowHeight", "showHeader", "striped", "conditionalFormatting", "note", "tone", "mode", "delta", "favorableDirection", "missingComparison", "ranges", "thresholds", "comparison", "goal", "trend", "theme", "basemap", "labelDensity", "camera", "controls", "layers":
		return true
	default:
		return false
	}
}

func valueIsSet(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	return reflected.IsValid() && !reflected.IsZero()
}

func valueOrZero(previous *dashboarddocument.DashboardVisual, get func(dashboarddocument.DashboardVisual) any) any {
	if previous == nil {
		return nil
	}
	return get(*previous)
}

func isTabularVisual(value dashboarddocument.DashboardVisualType) bool {
	return value == dashboarddocument.DashboardVisualTypeTable || value == dashboarddocument.DashboardVisualTypeMatrix || value == dashboarddocument.DashboardVisualTypePivot
}

func visualKindFromRenderer(renderer string) string {
	switch renderer {
	case visualizationdefinition.RendererHTML:
		return "kpi"
	case visualizationdefinition.RendererTanStack:
		return "grid"
	default:
		return "chart"
	}
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func visualAccessibilityGuidance(visual dashboarddocument.DashboardVisual) string {
	if visual.Type == dashboarddocument.DashboardVisualTypeKpi {
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

func buildExampleDashboard(catalog visualCatalog, examplesByPage map[string][]visualExample, dashboardID, semanticModelID projectgraph.ResourceID) dashboarddocument.DashboardDocument {
	displayName, description := "Visual documentation", "Executable documentation examples."
	report := dashboarddocument.DashboardDocument{
		APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1,
		Kind:       dashboarddocument.DashboardResourceKindDashboard,
		Metadata:   dashboarddocument.DashboardMetadata{ID: dashboardID.String(), Name: "visual-docs", DisplayName: &displayName, Description: &description},
		Spec: dashboarddocument.DashboardSpec{SemanticModel: semanticModelID.String(), Filters: []dashboarddocument.DashboardFilter{}, Visuals: map[string]dashboarddocument.DashboardVisual{}, Pages: make([]dashboarddocument.DashboardPage, 0, len(catalog.Documents)),
			Layout: &dashboarddocument.DashboardLayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}},
	}
	for _, document := range catalog.Documents {
		page := dashboarddocument.DashboardPage{ID: document.Source, Title: document.Title, Components: make([]dashboarddocument.DashboardPageComponent, 0, len(examplesByPage[document.Source]))}
		for index, example := range examplesByPage[document.Source] {
			report.Spec.Visuals[example.ID] = example.Visual
			page.Components = append(page.Components, dashboarddocument.DashboardPageComponent{Value: &dashboarddocument.VisualDashboardPageComponent{
				DashboardPageComponentBase: dashboarddocument.DashboardPageComponentBase{ID: example.ID, Type: "visual", Placement: dashboarddocument.DashboardPlacement{Column: 1, Row: int32(1 + index*7), ColumnSpan: 6, RowSpan: 7}},
				Type:                       "visual", Visual: example.ID,
			}})
		}
		report.Spec.Pages = append(report.Spec.Pages, page)
	}
	return report
}

func bindFixtureDataRoot(models map[string]*semanticmodel.Model, dataRoot string) error {
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return err
	}
	for _, model := range models {
		for name, connection := range model.Connections {
			if connection.Kind != "managed" {
				continue
			}
			connection.Root = root
			connection.Scope = ""
			model.Connections[name] = connection
		}
		for name, source := range model.Sources {
			if source.EffectivePathLocation != nil || source.PathLocation == nil {
				continue
			}
			connection, ok := model.Connections[source.Connection]
			if !ok {
				return fmt.Errorf("fixture source %q references unknown connection %q", name, source.Connection)
			}
			effective, err := projectcompiler.ResolveEffectivePathLocation(source, connection)
			if err != nil {
				return fmt.Errorf("fixture source %q effective options: %w", name, err)
			}
			source.EffectivePathLocation = effective
			model.Sources[name] = source
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
	var rawVisual any
	if err := node.Decode(&rawVisual); err != nil {
		return visualExample{}, fmt.Errorf("%s:%d: decode visual %q: %w", filename, line, id, err)
	}
	encodedVisual, err := json.Marshal(rawVisual)
	if err != nil {
		return visualExample{}, fmt.Errorf("%s:%d: encode visual %q: %w", filename, line, id, err)
	}
	var visual dashboarddocument.DashboardVisual
	if err := json.Unmarshal(encodedVisual, &visual); err != nil {
		return visualExample{}, fmt.Errorf("%s:%d: decode visual %q: %w", filename, line, id, err)
	}
	example := visualExample{ID: id, Source: filename, Line: line, Type: string(visual.Type), Visual: visual}
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
		"metadata": map[string]any{
			"id":   "dashboard:visual-doc-example",
			"name": "visual-doc-example",
		},
		"spec": map[string]any{
			"semanticModel": "visual_examples",
			"filters":       []any{},
			"visuals":       map[string]any{id: visual},
			"pages": []any{map[string]any{
				"id": "example", "title": "Example",
				"components": []any{map[string]any{
					"id": id, "type": "visual", "visual": id,
					"placement": map[string]int{"column": 1, "row": 1, "columnSpan": 6, "rowSpan": 4},
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
