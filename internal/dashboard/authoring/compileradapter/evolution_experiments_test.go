package compileradapter_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// This is an isolated future-reader experiment, not a production version or a
// claim about an older released reader. Only the lowered v1 document reaches
// the actual generated decoder, retained-revision code, and compiler adapter.
type evolutionReader struct {
	version  string
	defaults map[string]any
	schema   *jsonschema.Schema
}

func newEvolutionReader(t *testing.T) evolutionReader {
	t.Helper()
	var declaration struct {
		Version        string         `json:"version"`
		LayoutDefaults map[string]any `json:"layoutDefaults"`
		Benchmark      map[string]any `json:"benchmark"`
		TopCategories  map[string]any `json:"topCategories"`
	}
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "playground/dashboard-design-evaluation/evolution/reader-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(content, &declaration); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(filepath.Join("..", "..", "..", "..", "schemas/json/dashboard-document.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(content, &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	defs["DashboardApiVersion"].(map[string]any)["enum"] = []any{declaration.Version}
	props := defs["CartesianDashboardPresentation"].(map[string]any)["properties"].(map[string]any)
	props["benchmark"] = declaration.Benchmark
	defs["EvolutionTopCategoriesQuery"] = declaration.TopCategories
	query := defs["DashboardQuery"].(map[string]any)
	query["oneOf"] = append(query["oneOf"].([]any), map[string]any{"$ref": "#/$defs/EvolutionTopCategoriesQuery"})
	compiler := jsonschema.NewCompiler()
	const location = "memory://dashboard-evolution.json"
	if err = compiler.AddResource(location, schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	return evolutionReader{version: declaration.Version, defaults: declaration.LayoutDefaults, schema: compiled}
}

func evolutionMap(t *testing.T, value any) map[string]any {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func evolutionSpec(value map[string]any) map[string]any { return value["spec"].(map[string]any) }
func evolutionVisual(value map[string]any, id string) map[string]any {
	for _, item := range evolutionSpec(value)["visuals"].([]any) {
		visual := item.(map[string]any)
		if visual["id"] == id {
			return visual
		}
	}
	panic("missing test visual " + id)
}
func evolutionPresentation(value map[string]any) map[string]any {
	return evolutionVisual(value, "trend")["presentation"].(map[string]any)
}
func evolutionCurrentRead(value map[string]any) (document.DashboardDocument, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	var out document.DashboardDocument
	err = configschema.DecodeResource(configschema.KindDashboard, "evolution.json", b, &out)
	return out, err
}
func evolutionFuture(t *testing.T, r evolutionReader, explicit bool) map[string]any {
	t.Helper()
	v := evolutionMap(t, loadRetainedV1Revision(t).Document)
	v["apiVersion"] = r.version
	if explicit {
		evolutionSpec(v)["layout"] = map[string]any{"columns": 12, "rowHeight": 48, "gap": 16, "padding": 16}
	}
	return v
}
func requireEvolutionRead(t *testing.T, r evolutionReader, v map[string]any) document.DashboardDocument {
	t.Helper()
	d, err := r.read(v)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func requireEvolutionReject(t *testing.T, r evolutionReader, v map[string]any, contains string) {
	t.Helper()
	_, err := r.read(v)
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("want rejection containing %q, got %v", contains, err)
	}
}

func TestEvolutionOptionalPresentationCapability(t *testing.T) {
	r := newEvolutionReader(t)
	retained := loadRetainedV1Revision(t)
	original := evolutionMap(t, retained.Document)
	old, err := evolutionCurrentRead(original)
	if err != nil {
		t.Fatal(err)
	}
	newRead := requireEvolutionRead(t, r, original)
	if !reflect.DeepEqual(old, newRead) {
		t.Fatal("new reader changed retained v1 document")
	}
	before := compileRetainedV1Document(t, old)
	if after := compileRetainedV1Document(t, requireEvolutionRead(t, r, evolutionFuture(t, r, true))); !reflect.DeepEqual(before, after) {
		t.Fatal("omitting optional capability changed compiled content")
	}
	for _, label := range []string{"Target", ""} {
		t.Run("label="+label, func(t *testing.T) {
			future := evolutionFuture(t, r, true)
			evolutionPresentation(future)["benchmark"] = map[string]any{"value": 0, "label": label}
			if _, err := evolutionCurrentRead(future); err == nil {
				t.Fatal("current reader accepted future resource version")
			}
			unversioned := evolutionMap(t, future)
			unversioned["apiVersion"] = "leapview.dev/v1"
			if _, err := evolutionCurrentRead(unversioned); err == nil {
				t.Fatal("current closed reader accepted unknown capability at v1")
			}
			requireEvolutionReject(t, r, unversioned, "benchmark")
			lowered := requireEvolutionRead(t, r, future)
			compiled := compileRetainedV1Document(t, lowered)
			spec := compiled.Visualizations["trend"].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
			if spec.ReferenceLines == nil || len(*spec.ReferenceLines) != 1 {
				t.Fatalf("benchmark not compiled: %#v", spec)
			}
			line := (*spec.ReferenceLines)[0]
			number, ok := line.Value.Value.(*visualizationir.NumberVisualizationReferenceValue)
			if !ok || number.Value != 0 || line.ID != "evolution-benchmark" || line.Axis != "primary_y" || line.Tone != "neutral" || line.Label == nil || *line.Label != label {
				t.Fatalf("wrong reference marker: %#v", line)
			}
			if !reflect.DeepEqual(compiled.Visualizations["trend"].Query, before.Visualizations["trend"].Query) || !reflect.DeepEqual(compiled.Visualizations["total"], before.Visualizations["total"]) || !reflect.DeepEqual(compiled.Pages, before.Pages) {
				t.Fatal("capability changed query/neighbor/page meaning")
			}
			exported := exportRetainedV1Document(t, lowered)
			if after := compileRetainedV1Document(t, exported); !reflect.DeepEqual(after, compiled) {
				t.Fatal("lowered compatibility export lost benchmark")
			}
		})
	}
	t.Run("absent label remains absent", func(t *testing.T) {
		v := evolutionFuture(t, r, true)
		evolutionPresentation(v)["benchmark"] = map[string]any{"value": 12}
		got := requireEvolutionRead(t, r, v)
		p := got.Spec.Visuals["trend"].Presentation.Value.(*document.CartesianDashboardPresentation)
		if (*p.ReferenceLines)[0].Label != nil {
			t.Fatal("invented absent label")
		}
	})
	for _, bad := range []any{nil, map[string]any{}, map[string]any{"value": nil}, map[string]any{"value": "12"}, map[string]any{"value": 12, "label": nil}, map[string]any{"value": 12, "extra": true}} {
		t.Run(fmt.Sprintf("invalid-%v", bad), func(t *testing.T) {
			v := evolutionFuture(t, r, true)
			evolutionPresentation(v)["benchmark"] = bad
			requireEvolutionReject(t, r, v, "benchmark")
		})
	}
	t.Run("unsupported mark", func(t *testing.T) {
		v := evolutionFuture(t, r, true)
		evolutionVisual(v, "trend")["type"] = "heatmap"
		evolutionPresentation(v)["benchmark"] = map[string]any{"value": 12}
		requireEvolutionReject(t, r, v, "requires line or area")
	})
	t.Run("explicit empty competing collection", func(t *testing.T) {
		v := evolutionFuture(t, r, true)
		p := evolutionPresentation(v)
		p["benchmark"] = map[string]any{"value": 12}
		p["referenceLines"] = []any{}
		requireEvolutionReject(t, r, v, "cannot combine")
	})
	if hash, err := authoring.DashboardContentHash(retained.Document); err != nil || hash != retainedV1ContentHash {
		t.Fatal("experiment mutated retained revision")
	}
}

func evolutionRanked(t *testing.T, r evolutionReader) map[string]any {
	t.Helper()
	v := evolutionFuture(t, r, true)
	visual := evolutionVisual(v, "trend")
	visual["type"] = "bar"
	visual["presentation"] = map[string]any{"type": "cartesian"}
	visual["query"] = map[string]any{"type": "topCategories", "dimension": "status", "metric": "order_count", "limit": 5}
	return v
}
func TestEvolutionGovernedQueryKind(t *testing.T) {
	r := newEvolutionReader(t)
	future := evolutionRanked(t, r)
	unversioned := evolutionMap(t, future)
	unversioned["apiVersion"] = "leapview.dev/v1"
	if _, err := evolutionCurrentRead(unversioned); err == nil {
		t.Fatal("current reader inferred an unknown query from the visual mark")
	}
	requireEvolutionReject(t, r, unversioned, "query")
	lowered := requireEvolutionRead(t, r, future)
	compiled := compileRetainedV1Document(t, lowered)
	q := compiled.Visualizations["trend"].Query.Aggregate
	if q == nil || len(q.Dimensions) != 1 || q.Dimensions[0].FieldID != "status" || len(q.Metrics) != 1 || q.Metrics[0].FieldID != "order_count" || q.Limit != 5 || len(q.Sort) != 2 || q.Sort[0].FieldID != "order_count" || q.Sort[0].Direction != "desc" || q.Sort[1].FieldID != "status" || q.Sort[1].Direction != "asc" {
		t.Fatalf("wrong governed ranking binding: %#v", q)
	}
	exported := exportRetainedV1Document(t, lowered)
	if after := compileRetainedV1Document(t, exported); !reflect.DeepEqual(after, compiled) {
		t.Fatal("query export changed compiled meaning")
	}
	before := compileRetainedV1Document(t, loadRetainedV1Revision(t).Document)
	if !reflect.DeepEqual(before.Pages, compiled.Pages) || !reflect.DeepEqual(before.Visualizations["total"], compiled.Visualizations["total"]) {
		t.Fatal("query change altered neighbors or layout")
	}
	for _, bad := range []any{0, -1, 1001, nil, "5"} {
		t.Run(fmt.Sprintf("limit-%v", bad), func(t *testing.T) {
			v := evolutionRanked(t, r)
			evolutionVisual(v, "trend")["query"].(map[string]any)["limit"] = bad
			requireEvolutionReject(t, r, v, "query")
		})
	}
	for _, limit := range []int{1, 1000} {
		v := evolutionRanked(t, r)
		evolutionVisual(v, "trend")["query"].(map[string]any)["limit"] = limit
		compileRetainedV1Document(t, requireEvolutionRead(t, r, v))
	}

	for _, field := range []string{"dimension", "metric"} {
		for _, bad := range []any{"", nil} {
			t.Run(fmt.Sprintf("invalid-%s-%v", field, bad), func(t *testing.T) {
				v := evolutionRanked(t, r)
				evolutionVisual(v, "trend")["query"].(map[string]any)[field] = bad
				requireEvolutionReject(t, r, v, "query")
			})
		}
	}
	t.Run("unknown future tag is not inferred", func(t *testing.T) {
		v := evolutionRanked(t, r)
		evolutionVisual(v, "trend")["query"].(map[string]any)["type"] = "topCategoriesV3"
		requireEvolutionReject(t, r, v, "query")
	})
	t.Run("missing bound", func(t *testing.T) {
		v := evolutionRanked(t, r)
		delete(evolutionVisual(v, "trend")["query"].(map[string]any), "limit")
		requireEvolutionReject(t, r, v, "query")
	})
	t.Run("no raw expression extension", func(t *testing.T) {
		v := evolutionRanked(t, r)
		evolutionVisual(v, "trend")["query"].(map[string]any)["sql"] = "select 1"
		requireEvolutionReject(t, r, v, "query")
	})
	t.Run("wrong visual", func(t *testing.T) {
		v := evolutionRanked(t, r)
		evolutionVisual(v, "trend")["type"] = "line"
		requireEvolutionReject(t, r, v, "requires bar or column")
	})
	t.Run("unknown semantic member reaches real compiler", func(t *testing.T) {
		v := evolutionRanked(t, r)
		evolutionVisual(v, "trend")["query"].(map[string]any)["dimension"] = "missing_dimension"
		d := requireEvolutionRead(t, r, v)
		f := newCompilerFixture(t)
		_, err := f.adapter.Compile(t.Context(), "project", "sales_model", d)
		if err == nil || !strings.Contains(err.Error(), "missing_dimension") {
			t.Fatalf("unresolved member accepted: %v", err)
		}
	})
}

func TestEvolutionChangedOmittedDefaultAndExplicitMigration(t *testing.T) {
	r := newEvolutionReader(t)
	retained := loadRetainedV1Revision(t)
	original := evolutionMap(t, retained.Document)
	before := compileRetainedV1Document(t, retained.Document)
	oldViaNew := requireEvolutionRead(t, r, original)
	if hash, err := authoring.DashboardContentHash(oldViaNew); err != nil || hash != retained.ContentHash {
		t.Fatal("new reader failed to preserve v1 omission and authored hash")
	}
	if after := compileRetainedV1Document(t, oldViaNew); !reflect.DeepEqual(after, before) {
		t.Fatal("new reader changed v1 default")
	}
	future := evolutionFuture(t, r, false)
	changed := requireEvolutionRead(t, r, future)
	after := compileRetainedV1Document(t, changed)
	if after.Layout.Defaults.Gap != 8 || after.Pages[0].Grid.Gap != 8 || after.Pages[0].Height != 248 || before.Pages[0].Height != 272 {
		t.Fatalf("default experiment did not change actual compiled geometry: old=%#v new=%#v", before.Pages[0], after.Pages[0])
	}
	if !reflect.DeepEqual(after.Pages[1], before.Pages[1]) {
		t.Fatal("new default overwrote explicit zero page overrides")
	}
	if !reflect.DeepEqual(after.Visualizations, before.Visualizations) {
		t.Fatal("layout default altered query/presentation meaning")
	}
	if compiledExport := compileRetainedV1Document(t, exportRetainedV1Document(t, changed)); !reflect.DeepEqual(compiledExport, after) {
		t.Fatal("lowered future export failed to pin new default")
	}
	migrated, err := r.migrateV1(original)
	if err != nil {
		t.Fatal(err)
	}
	preserved := requireEvolutionRead(t, r, migrated)
	if preserved.Spec.Layout == nil || preserved.Spec.Layout.Gap != 16 {
		t.Fatal("migration did not explicitly materialize old default")
	}
	if compiled := compileRetainedV1Document(t, preserved); !reflect.DeepEqual(compiled, before) {
		t.Fatal("migration changed existing serving meaning")
	}
	// A new canonical revision may pin defaults without changing the retained
	// original. Restore still selects the original omission and original hash.
	revision, err := authoring.NewRevision("revision-migrated", retained.DashboardID, retained.Number+1, retained.CreatedAt.Add(time.Minute), preserved, retained.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	if revision.ContentHash == retained.ContentHash {
		t.Fatal("materialization erased authored identity distinction")
	}
	bytesAfter, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	var reread authoring.Revision
	if err = json.Unmarshal(bytesAfter, &reread); err != nil {
		t.Fatal(err)
	}
	if err = reread.Validate(); err != nil {
		t.Fatal(err)
	}
	if compiled := compileRetainedV1Document(t, exportRetainedV1Document(t, reread.Document)); !reflect.DeepEqual(compiled, before) {
		t.Fatal("persisted migrated revision/export lost meaning")
	}
	restored := loadRetainedV1Revision(t)
	if restored.ContentHash != retained.ContentHash || restored.Document.Spec.Layout != nil {
		t.Fatal("migration rewrote retained revision")
	}
	assertRetainedV1Meaning(t, compileRetainedV1Document(t, restored.Document))
	t.Run("explicit old defaults stay explicit", func(t *testing.T) {
		v := evolutionFuture(t, r, true)
		d := requireEvolutionRead(t, r, v)
		if !reflect.DeepEqual(compileRetainedV1Document(t, d), before) {
			t.Fatal("new reader overwrote explicit values")
		}
	})
	for _, bad := range []any{nil, map[string]any{}, map[string]any{"columns": 12, "rowHeight": 48, "gap": nil, "padding": 16}} {
		t.Run(fmt.Sprintf("layout-%v", bad), func(t *testing.T) {
			v := evolutionFuture(t, r, false)
			evolutionSpec(v)["layout"] = bad
			requireEvolutionReject(t, r, v, "layout")
		})
	}

	t.Run("explicit zero dashboard gap preserved", func(t *testing.T) {
		v := evolutionFuture(t, r, true)
		evolutionSpec(v)["layout"].(map[string]any)["gap"] = 0
		d := requireEvolutionRead(t, r, v)
		compiled := compileRetainedV1Document(t, d)
		if compiled.Layout.Defaults.Gap != 0 || compiled.Pages[0].Grid.Gap != 0 || compiled.Pages[0].Height != 224 {
			t.Fatal("zero layout value incorrectly defaulted")
		}
	})
	t.Run("migration preserves authored explicit layout", func(t *testing.T) {
		v := evolutionMap(t, original)
		evolutionSpec(v)["layout"] = map[string]any{"columns": 12, "rowHeight": 48, "gap": 0, "padding": 16}
		migrated, err := r.migrateV1(v)
		if err != nil {
			t.Fatal(err)
		}
		if requireEvolutionRead(t, r, migrated).Spec.Layout.Gap != 0 {
			t.Fatal("migration overwrote explicit zero")
		}
	})
	t.Run("migration refuses future or malformed source", func(t *testing.T) {
		if _, err := r.migrateV1(future); err == nil {
			t.Fatal("migration accepted already-future source")
		}
		v := evolutionMap(t, original)
		evolutionSpec(v)["layout"] = nil
		if _, err := r.migrateV1(v); err == nil {
			t.Fatal("migration repaired invalid null layout")
		}
	})
	t.Run("null override rejected", func(t *testing.T) {
		v := evolutionFuture(t, r, false)
		evolutionSpec(v)["pages"].([]any)[0].(map[string]any)["layout"] = nil
		requireEvolutionReject(t, r, v, "layout")
	})
	t.Run("unknown version rejected", func(t *testing.T) {
		v := evolutionFuture(t, r, false)
		v["apiVersion"] = "leapview.experiment/v3"
		requireEvolutionReject(t, r, v, "unsupported evolution version")
	})
}

// read validates the complete future source before lowering any field. The
// generated current decoder remains the final authority on canonical output.
func (r evolutionReader) read(value map[string]any) (document.DashboardDocument, error) {
	if value["apiVersion"] == "leapview.dev/v1" {
		return evolutionCurrentRead(value)
	}
	if value["apiVersion"] != r.version {
		return document.DashboardDocument{}, fmt.Errorf("unsupported evolution version %v", value["apiVersion"])
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	var lowered map[string]any
	if err = json.Unmarshal(encoded, &lowered); err != nil {
		return document.DashboardDocument{}, err
	}
	if err = r.schema.Validate(lowered); err != nil {
		return document.DashboardDocument{}, err
	}
	spec := evolutionSpec(lowered)
	for _, item := range spec["visuals"].([]any) {
		visual := item.(map[string]any)
		presentation := visual["presentation"].(map[string]any)
		if raw, exists := presentation["benchmark"]; exists {
			if visual["type"] != "line" && visual["type"] != "area" {
				return document.DashboardDocument{}, fmt.Errorf("benchmark requires line or area, got %v", visual["type"])
			}
			if _, exists := presentation["referenceLines"]; exists {
				return document.DashboardDocument{}, fmt.Errorf("benchmark cannot combine with an authored referenceLines collection, including empty")
			}
			benchmark := raw.(map[string]any)
			line := map[string]any{"id": "evolution-benchmark", "axis": "primary_y", "tone": "neutral", "value": map[string]any{"kind": "number", "value": benchmark["value"]}}
			if label, present := benchmark["label"]; present {
				line["label"] = label
			}
			presentation["referenceLines"] = []any{line}
			delete(presentation, "benchmark")
		}
		query := visual["query"].(map[string]any)
		if query["type"] == "topCategories" {
			if visual["type"] != "bar" && visual["type"] != "column" {
				return document.DashboardDocument{}, fmt.Errorf("topCategories requires bar or column, got %v", visual["type"])
			}
			visual["query"] = map[string]any{"type": "aggregate", "dimensions": []any{query["dimension"]}, "metrics": []any{query["metric"]}, "limit": query["limit"], "sort": []any{map[string]any{"field": query["metric"], "direction": "desc"}, map[string]any{"field": query["dimension"], "direction": "asc"}}}
		}
	}
	// Explicitly materialize the future default into exported v1, after checking
	// the authored shape; null and empty objects must never trigger defaults.
	if _, present := spec["layout"]; !present {
		spec["layout"] = r.defaults
	}
	lowered["apiVersion"] = "leapview.dev/v1"
	return evolutionCurrentRead(lowered)
}

// migrateV1 preserves old serving meaning before opting into the future
// version. The pinned values describe the existing v1 compatibility policy;
// they deliberately do not follow a future reader's new omission defaults.
func (r evolutionReader) migrateV1(value map[string]any) (map[string]any, error) {
	current, err := evolutionCurrentRead(value)
	if err != nil {
		return nil, err
	}
	if current.Spec.Layout == nil {
		current.Spec.Layout = &document.DashboardLayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	var migrated map[string]any
	if err = json.Unmarshal(encoded, &migrated); err != nil {
		return nil, err
	}
	migrated["apiVersion"] = r.version
	return migrated, nil
}
