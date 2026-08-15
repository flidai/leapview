package compileradapter_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/compileradapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type fixture struct {
	adapter *compileradapter.Adapter
	runtime *fakeRuntime
	lease   *fakeLease
	doc     authoring.Dashboard
	model   *semanticmodel.Model
	gotWS   string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	doc := testDocument()
	model := testModel()
	runtime := &fakeRuntime{model: model}
	lease := &fakeLease{runtime: runtime, servingState: "serving-sales"}
	var gotProject string
	adapter, err := compileradapter.New(compileradapter.Options{
		AcquireRuntime: func(_ context.Context, projectID string) (runtimehost.Lease, error) {
			gotProject = projectID
			return lease, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{adapter: adapter, runtime: runtime, lease: lease, doc: doc, model: model, gotWS: gotProject}
}

func testDocument() authoring.Dashboard {
	return authoring.Dashboard{
		ID: "sales", Title: "Sales", SemanticModel: "sales_model",
		Visuals: authoring.TabularVisualizations("table", map[string]authoring.TableVisual{
			"orders": {Title: "Orders", Query: authoring.TableQuery{Table: "orders", Fields: []string{"orders.status", "order_count"}}},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}}}}},
	}
}

func testModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales_model",
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string"}}},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count", Input: semanticmodel.MeasureInput{Field: "orders.status"}, Empty: "zero"}},
	}
}

func TestCompileUsesOneProjectLeaseAndReturnsExactState(t *testing.T) {
	fixture := newFixture(t)
	var gotProject string
	fixture.adapter, _ = compileradapter.New(compileradapter.Options{AcquireRuntime: func(_ context.Context, projectID string) (runtimehost.Lease, error) {
		gotProject = projectID
		return fixture.lease, nil
	}})
	result, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", fixture.doc)
	if err != nil {
		t.Fatal(err)
	}
	if gotProject != "project" {
		t.Fatalf("acquired project = %q, want project", gotProject)
	}
	if fixture.runtime.projectionCalls != 1 || fixture.lease.releaseCalls != 1 {
		t.Fatalf("runtime/lease calls = %d/%d, want 1/1", fixture.runtime.projectionCalls, fixture.lease.releaseCalls)
	}
	if result.Definition.ID != fixture.doc.ID.String() || result.Definition.SemanticModel != fixture.doc.SemanticModel.String() || result.SemanticServingStateID != "serving-sales" {
		t.Fatalf("unexpected compilation = %#v", result)
	}
}

func TestCompileRequiresProjectionCapabilityAndReleases(t *testing.T) {
	fixture := newFixture(t)
	fixture.lease.runtime = &runtimeWithoutProjection{}
	if _, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", fixture.doc); err == nil {
		t.Fatal("missing projection capability unexpectedly compiled")
	}
	if fixture.lease.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want one", fixture.lease.releaseCalls)
	}
}

func TestCompileRejectsSemanticMismatchAndMissingModelWithRelease(t *testing.T) {
	t.Run("runtime mismatch", func(t *testing.T) {
		fixture := newFixture(t)
		fixture.runtime.model = &semanticmodel.Model{Name: "other_model"}
		_, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", fixture.doc)
		if !errors.Is(err, compileradapter.ErrSemanticMismatch) {
			t.Fatalf("error = %v, want semantic mismatch", err)
		}
		if fixture.lease.releaseCalls != 1 {
			t.Fatalf("release calls = %d, want one", fixture.lease.releaseCalls)
		}
	})
	t.Run("missing model", func(t *testing.T) {
		fixture := newFixture(t)
		fixture.runtime.model = nil
		_, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", fixture.doc)
		if !errors.Is(err, compileradapter.ErrSemanticMismatch) {
			t.Fatalf("error = %v, want semantic mismatch", err)
		}
		if fixture.lease.releaseCalls != 1 {
			t.Fatalf("release calls = %d, want one", fixture.lease.releaseCalls)
		}
	})
	t.Run("document mismatch", func(t *testing.T) {
		fixture := newFixture(t)
		document := fixture.doc
		document.SemanticModel = "other_model"
		_, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", document)
		if !errors.Is(err, compileradapter.ErrSemanticMismatch) {
			t.Fatalf("error = %v, want semantic mismatch", err)
		}
		if fixture.lease.releaseCalls != 0 {
			t.Fatalf("mismatched document acquired a lease")
		}
	})
}

func TestCompileStrictDraftErrorReleasesAndDoesNotMutateInputOrModel(t *testing.T) {
	fixture := newFixture(t)
	document := fixture.doc
	document.Visuals["orders"] = authoring.TabularVisualizations("table", map[string]authoring.TableVisual{
		"orders": {Title: "Orders", Query: authoring.TableQuery{Table: "missing", Fields: []string{"orders.status", "order_count"}}},
	})["orders"]
	beforeDocument, err := document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	beforeModel := *fixture.model
	beforeTables := map[string]semanticmodel.Table{}
	for name, table := range fixture.model.Tables {
		beforeTables[name] = table
	}
	beforeModel.Tables = beforeTables
	if _, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", document); err == nil || !strings.Contains(err.Error(), "strictly compile dashboard") {
		t.Fatalf("strict compile error = %v", err)
	}
	if fixture.lease.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want one", fixture.lease.releaseCalls)
	}
	if !reflect.DeepEqual(document, beforeDocument) || !reflect.DeepEqual(*fixture.model, beforeModel) {
		t.Fatal("compile mutated authored document or base model")
	}
}

func TestCompileRejectsMissingServingStateAndReleases(t *testing.T) {
	fixture := newFixture(t)
	fixture.lease.servingState = "  "
	if _, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", fixture.doc); err == nil {
		t.Fatal("missing serving state unexpectedly compiled")
	}
	if fixture.lease.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want one", fixture.lease.releaseCalls)
	}
}

func TestCompileImmutabilityAndCompilerContract(t *testing.T) {
	fixture := newFixture(t)
	before, err := fixture.doc.Clone()
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.adapter.Compile(context.Background(), "project", "sales_model", fixture.doc)
	if err != nil {
		t.Fatal(err)
	}
	result.Definition.Pages[0].Title = "changed"
	if !reflect.DeepEqual(fixture.doc, before) {
		t.Fatal("successful compile mutated authored document")
	}
}

type fakeLease struct {
	runtime      runtimehost.Runtime
	servingState servingstate.ID
	releaseCalls int
}

func (l *fakeLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *fakeLease) ServingStateID() servingstate.ID { return l.servingState }
func (l *fakeLease) DuckLakeSnapshotID() int64       { return 42 }
func (l *fakeLease) Release()                        { l.releaseCalls++ }

type fakeRuntime struct {
	model           *semanticmodel.Model
	projectionCalls int
}

func (r *fakeRuntime) Close() error { return nil }
func (r *fakeRuntime) SemanticModelProjection(id string) (*semanticmodel.Model, bool) {
	r.projectionCalls++
	if r.model == nil || r.model.Name != id {
		return nil, false
	}
	// Return a detached top-level projection to model the production runtime
	// capability and ensure the adapter does not use a base-model lookup.
	copy := *r.model
	copy.Tables = map[string]semanticmodel.Table{}
	for name, table := range r.model.Tables {
		copy.Tables[name] = table
	}
	return &copy, true
}

type runtimeWithoutProjection struct{}

func (runtimeWithoutProjection) Close() error { return nil }

var _ authoringservice.Compiler = (*compileradapter.Adapter)(nil)
