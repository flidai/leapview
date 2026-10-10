package compiler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
)

type compileSourceRootBenchmarkCase struct {
	name               string
	files              map[string]string
	wantResources      int
	wantEdges          int
	wantSemanticModels int
}

// BenchmarkCompileSourceRoot measures the production source-root compiler
// boundary with authored fixtures. It records a baseline only; it does not
// compare an alternate implementation or claim a user-facing latency.
func BenchmarkCompileSourceRoot(b *testing.B) {
	b.StopTimer()
	for _, tc := range compileSourceRootBenchmarkCases() {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			b.StopTimer()
			if err := checkCompileSourceRootBenchmarkLimits(tc.files); err != nil {
				b.Fatal(err)
			}

			primaryRoot := writeCompileSourceRootBenchmarkFixture(b, tc.files)
			independentRoot := writeCompileSourceRootBenchmarkFixture(b, tc.files)
			primary := preflightCompileSourceRootBenchmarkFixture(b, primaryRoot, tc)
			independent := preflightCompileSourceRootBenchmarkFixture(b, independentRoot, tc)
			if primary.Digest() != independent.Digest() || !bytes.Equal(primary.Canonical(), independent.Canonical()) {
				b.Fatalf("independently compiled fixture bundles differ: %s / %s", primary.Digest(), independent.Digest())
			}
			resourceIDs, edgeIDs := compileSourceRootBenchmarkGraphInventory(primary)
			b.Logf("fixture_sha256=%s source_files=%q bundle_digest=%s graph_digest=%s resource_ids=%q edges=%q",
				compileSourceRootBenchmarkFixtureDigest(tc.files), compileSourceRootBenchmarkFileNames(tc.files),
				primary.Digest(), primary.Graph().Digest(), resourceIDs, edgeIDs)

			b.ReportAllocs()
			b.ResetTimer()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Compile(primaryRoot); err != nil {
					b.Fatalf("Compile(source root): %v", err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(tc.wantResources), "resources/op")
			b.ReportMetric(float64(tc.wantEdges), "edges/op")
			b.ReportMetric(float64(tc.wantSemanticModels), "semantic_models/op")
		})
	}
}

func compileSourceRootBenchmarkCases() []compileSourceRootBenchmarkCase {
	cases := make([]compileSourceRootBenchmarkCase, 0, 10)
	// The resource ladder has one Connection and N-1 Sources, so every
	// non-empty SourceRoot has exactly N resources and N-1 graph edges.
	for _, size := range []int{1, 10, 20, 100} {
		cases = append(cases, compileSourceRootBenchmarkCase{
			name:               fmt.Sprintf("resources/%d", size),
			files:              compileSourceRootResourceScaleFixture(size),
			wantResources:      size,
			wantEdges:          size - 1,
			wantSemanticModels: 0,
		})
	}

	for _, density := range []string{"sparse", "dense"} {
		files := compileSourceRootDAGDensityFixture(density)
		// Both graphs have 8 source-to-connection edges and 1 semantic-model
		// edge. Sparse has 10 model-to-source edges; dense has 80
		// model-to-source plus 45 model-to-model edges.
		edgeCount := 19
		if density == "dense" {
			edgeCount = 134
		}
		cases = append(cases, compileSourceRootBenchmarkCase{
			name:               "dag_density/" + density,
			files:              files,
			wantResources:      20,
			wantEdges:          edgeCount,
			wantSemanticModels: 1,
		})
	}

	// Each semantic-model count fixture shares one Connection, Source, and
	// Model. Every SemanticModel adds one graph edge to that Model.
	for _, count := range []int{1, 10, 20, 100} {
		cases = append(cases, compileSourceRootBenchmarkCase{
			name:               fmt.Sprintf("semantic_models/%d", count),
			files:              compileSourceRootSemanticModelCountFixture(count),
			wantResources:      count + 3,
			wantEdges:          count + 2,
			wantSemanticModels: count,
		})
	}
	return cases
}

func compileSourceRootResourceScaleFixture(resourceCount int) map[string]string {
	files := map[string]string{
		"connections/warehouse.yaml": compileSourceRootConnectionYAML(),
	}
	for index := 0; index < resourceCount-1; index++ {
		name := fmt.Sprintf("source_%03d", index)
		files["sources/"+name+".yaml"] = compileSourceRootSourceYAML(name, "warehouse")
	}
	return files
}

func compileSourceRootDAGDensityFixture(density string) map[string]string {
	const sourceCount = 8
	const modelCount = 10
	files := map[string]string{"connections/warehouse.yaml": compileSourceRootConnectionYAML()}
	for index := 0; index < sourceCount; index++ {
		name := fmt.Sprintf("source_%02d", index)
		files["sources/"+name+".yaml"] = compileSourceRootSourceYAML(name, "warehouse")
	}

	for modelIndex := 0; modelIndex < modelCount; modelIndex++ {
		modelName := fmt.Sprintf("model_%02d", modelIndex)
		sourceRefs := make([]string, 0, sourceCount)
		modelRefs := make([]string, 0, modelIndex)
		if density == "dense" {
			for sourceIndex := 0; sourceIndex < sourceCount; sourceIndex++ {
				sourceRefs = append(sourceRefs, fmt.Sprintf("source_%02d", sourceIndex))
			}
			for dependencyIndex := 0; dependencyIndex < modelIndex; dependencyIndex++ {
				modelRefs = append(modelRefs, fmt.Sprintf("model_%02d", dependencyIndex))
			}
		} else {
			sourceRefs = append(sourceRefs, fmt.Sprintf("source_%02d", modelIndex%sourceCount))
		}
		files["models/"+modelName+".yaml"] = compileSourceRootModelYAML(modelName, compileSourceRootModelSQL(sourceRefs, modelRefs))
	}

	files["semantic-models/sales.yaml"] = compileSourceRootSemanticModelYAML("sales", []string{"model_00"})
	return files
}

func compileSourceRootSemanticModelCountFixture(count int) map[string]string {
	files := map[string]string{
		"connections/warehouse.yaml": compileSourceRootConnectionYAML(),
		"sources/orders.yaml":        compileSourceRootSourceYAML("orders", "warehouse"),
		"models/orders.yaml":         compileSourceRootModelYAML("orders_model", "SELECT id FROM source.orders"),
	}
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("sales_%03d", index)
		files["semantic-models/"+name+".yaml"] = compileSourceRootSemanticModelYAML(name, []string{"orders_model"})
	}
	return files
}

func compileSourceRootConnectionYAML() string {
	return `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`
}

func compileSourceRootSourceYAML(name, connection string) string {
	return fmt.Sprintf(`apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:%s, name: %s}
spec: {connection: %s, location: {type: path, path: %s.csv, format: csv}}
`, name, name, connection, name)
}

func compileSourceRootModelYAML(name, sql string) string {
	return fmt.Sprintf(`apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:%s, name: %s}
spec:
  definition: {type: sql, sql: %q}
  entities: [{name: id, type: primary, fields: [id]}]
  grain: {entity: id}
  fields: [{name: id, datatype: String}]
`, name, name, sql)
}

func compileSourceRootModelSQL(sources, models []string) string {
	refs := make([]string, 0, len(sources)+len(models))
	for _, source := range sources {
		refs = append(refs, "source."+source)
	}
	for _, model := range models {
		refs = append(refs, "model."+model)
	}
	if len(refs) == 0 {
		return "SELECT 1 AS id"
	}
	query := "SELECT r0.id FROM " + refs[0] + " AS r0"
	for index, ref := range refs[1:] {
		query += fmt.Sprintf(" JOIN %s AS r%d USING (id)", ref, index+1)
	}
	return query
}

func compileSourceRootSemanticModelYAML(name string, modelNames []string) string {
	var datasets bytes.Buffer
	for index, modelName := range modelNames {
		fmt.Fprintf(&datasets, "  - name: dataset_%03d\n    model: %s\n    metrics:\n      - name: row_count_%03d\n        type: simple\n        empty: zero\n        agg: count\n        field: id\n", index, modelName, index)
	}
	return fmt.Sprintf(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:%s, name: %s}
spec:
  datasets:
%s`, name, name, datasets.String())
}

func compileSourceRootBenchmarkFixtureDigest(files map[string]string) string {
	hash := sha256.New()
	for _, name := range compileSourceRootBenchmarkFileNames(files) {
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(files[name]))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func compileSourceRootBenchmarkFileNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func compileSourceRootBenchmarkGraphInventory(bundle projectartifact.SourceBundle) ([]string, []string) {
	resources := bundle.Graph().Resources()
	resourceIDs := make([]string, 0, len(resources))
	for _, resource := range resources {
		resourceIDs = append(resourceIDs, fmt.Sprintf("%s[%s]", resource.ID, resource.Kind))
	}
	edges := bundle.Graph().Edges()
	edgeIDs := make([]string, 0, len(edges))
	for _, edge := range edges {
		edgeIDs = append(edgeIDs, fmt.Sprintf("%s-[%s]->%s", edge.From, edge.Relation, edge.To))
	}
	sort.Strings(resourceIDs)
	sort.Strings(edgeIDs)
	return resourceIDs, edgeIDs
}

func checkCompileSourceRootBenchmarkLimits(files map[string]string) error {
	if len(files) > maxAuthoredSourceFiles {
		return fmt.Errorf("benchmark fixture has %d files, exceeds compiler limit %d", len(files), maxAuthoredSourceFiles)
	}
	directories := make(map[string]struct{})
	var totalBytes int64
	for name, content := range files {
		for directory := filepath.ToSlash(filepath.Dir(name)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			directories[directory] = struct{}{}
		}
		fileBytes := int64(len(content))
		if fileBytes > maxAuthoredSourceFileBytes {
			return fmt.Errorf("benchmark fixture file %q has %d bytes, exceeds compiler limit %d", name, fileBytes, maxAuthoredSourceFileBytes)
		}
		totalBytes += fileBytes
	}
	entries := len(files) + len(directories)
	if entries > maxAuthoredSourceEntries {
		return fmt.Errorf("benchmark fixture has %d filesystem entries, exceeds compiler limit %d", entries, maxAuthoredSourceEntries)
	}
	if totalBytes > maxAuthoredSourceBytes {
		return fmt.Errorf("benchmark fixture has %d bytes, exceeds compiler limit %d", totalBytes, maxAuthoredSourceBytes)
	}
	return nil
}

func writeCompileSourceRootBenchmarkFixture(b testing.TB, files map[string]string) string {
	b.Helper()
	root := filepath.Join(b.TempDir(), "source-root")
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

func preflightCompileSourceRootBenchmarkFixture(b testing.TB, root string, tc compileSourceRootBenchmarkCase) projectartifact.SourceBundle {
	b.Helper()
	bundle, err := Compile(root)
	if err != nil {
		b.Fatalf("preflight Compile(%q): %v", tc.name, err)
	}
	graph := bundle.Graph()
	if got := len(graph.Resources()); got != tc.wantResources {
		b.Fatalf("preflight %q graph resources = %d, want %d", tc.name, got, tc.wantResources)
	}
	if got := len(graph.Edges()); got != tc.wantEdges {
		b.Fatalf("preflight %q graph edges = %d, want %d", tc.name, got, tc.wantEdges)
	}
	if got := len(bundle.Manifest().SemanticModels); got != tc.wantSemanticModels {
		b.Fatalf("preflight %q semantic models = %d, want %d", tc.name, got, tc.wantSemanticModels)
	}
	decoded, err := projectartifact.Decode(bundle.Canonical())
	if err != nil {
		b.Fatalf("preflight %q canonical bundle decode: %v", tc.name, err)
	}
	if bundle.Digest() != decoded.Digest() || !bytes.Equal(bundle.Canonical(), decoded.Canonical()) {
		b.Fatalf("preflight %q canonical bundle round trip changed digest or bytes", tc.name)
	}
	return bundle
}
