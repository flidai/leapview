package compileradapter_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring/compileradapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type compilerFixture struct {
	adapter *compileradapter.Adapter
	runtime *compilerRuntime
	lease   *compilerLease
	doc     document.DashboardDocument
	model   *semanticmodel.Model
}

func newCompilerFixture(t *testing.T) compilerFixture {
	t.Helper()
	runtime := &compilerRuntime{model: compilerModel()}
	identity, err := graph.NewServingIdentity("project", "production", "serving-sales")
	if err != nil {
		t.Fatal(err)
	}
	lease := &compilerLease{runtime: runtime, identity: identity}
	adapter, err := compileradapter.New(compileradapter.Options{AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return lease, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return compilerFixture{adapter: adapter, runtime: runtime, lease: lease, doc: compilerDocument(), model: runtime.model}
}

func compilerDocument() document.DashboardDocument {
	status, metric := "status", "order_count"
	return document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "sales", Name: "sales", DisplayName: stringPtr("Sales")}, Spec: document.DashboardSpec{SemanticModel: "sales_model", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{"orders": {Type: document.DashboardVisualTypeBar, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &status}}, Metrics: []document.DashboardMetricSelection{{String: &metric}}}}, Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}}}}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}
}
func stringPtr(value string) *string { return &value }

func compilerModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "sales_model", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "status", Entities: map[string]semanticmodel.ModelEntitySpec{"status": {Type: "primary", Fields: []string{"status"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Dimensions: map[string]semanticmodel.SemanticDimension{"status": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}, Empty: "zero"}}}
}

func TestCompileUsesOneLeaseAndReturnsServingIdentity(t *testing.T) {
	f := newCompilerFixture(t)
	result, err := f.adapter.Compile(t.Context(), "project", "sales_model", f.doc)
	if err != nil {
		t.Fatal(err)
	}
	if f.runtime.projectionCalls != 1 || f.lease.releaseCalls != 1 {
		t.Fatalf("projection/release = %d/%d", f.runtime.projectionCalls, f.lease.releaseCalls)
	}
	if result.Definition.ID != "sales" || result.Definition.SemanticModel != "sales_model" || result.SemanticIdentity != f.lease.identity {
		t.Fatalf("compilation = %#v", result)
	}
}

func TestCompileRejectsSemanticMismatchBeforeLeaseAndReleasesAllPostAcquirePaths(t *testing.T) {
	f := newCompilerFixture(t)
	bad := f.doc
	bad.Spec.SemanticModel = "other_model"
	if _, err := f.adapter.Compile(t.Context(), "project", "sales_model", bad); !errors.Is(err, compileradapter.ErrSemanticMismatch) {
		t.Fatalf("document mismatch = %v", err)
	}
	if f.lease.releaseCalls != 0 {
		t.Fatal("document mismatch acquired a lease")
	}
	f = newCompilerFixture(t)
	f.runtime.model = &semanticmodel.Model{Name: "other_model"}
	if _, err := f.adapter.Compile(t.Context(), "project", "sales_model", f.doc); !errors.Is(err, compileradapter.ErrSemanticMismatch) {
		t.Fatalf("runtime mismatch = %v", err)
	}
	if f.lease.releaseCalls != 1 {
		t.Fatalf("runtime mismatch release = %d", f.lease.releaseCalls)
	}
	f = newCompilerFixture(t)
	f.runtime.model = nil
	if _, err := f.adapter.Compile(t.Context(), "project", "sales_model", f.doc); !errors.Is(err, compileradapter.ErrSemanticMismatch) {
		t.Fatalf("missing model = %v", err)
	}
	if f.lease.releaseCalls != 1 {
		t.Fatalf("missing model release = %d", f.lease.releaseCalls)
	}
}

func TestCompileStrictErrorReleasesAndDoesNotMutateCanonicalInputOrModel(t *testing.T) {
	f := newCompilerFixture(t)
	invalid := f.doc
	value := invalid.Spec.Visuals["orders"]
	metric := "missing_metric"
	value.Query.Value = &document.AggregateDashboardQuery{Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: &metric}}}
	invalid.Spec.Visuals["orders"] = value
	beforeDoc, err := invalid.Clone()
	if err != nil {
		t.Fatal(err)
	}
	beforeModel := *f.model
	if _, err := f.adapter.Compile(t.Context(), "project", "sales_model", invalid); err == nil {
		t.Fatal("invalid canonical dashboard compiled")
	}
	if f.lease.releaseCalls != 1 {
		t.Fatalf("release = %d", f.lease.releaseCalls)
	}
	if !reflect.DeepEqual(invalid, beforeDoc) || !reflect.DeepEqual(*f.model, beforeModel) {
		t.Fatal("compile mutated document or model")
	}
}

func TestCompileSuccessfulResultIsDetached(t *testing.T) {
	f := newCompilerFixture(t)
	before, err := f.doc.Clone()
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.adapter.Compile(t.Context(), "project", "sales_model", f.doc)
	if err != nil {
		t.Fatal(err)
	}
	result.Definition.Pages[0].Title = "changed"
	if !reflect.DeepEqual(f.doc, before) {
		t.Fatal("successful compile mutated authored document")
	}
}

type compilerLease struct {
	runtime      projectruntime.Runtime
	identity     graph.ServingIdentity
	releaseCalls int
}

func (l *compilerLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *compilerLease) Identity() graph.ServingIdentity { return l.identity }
func (l *compilerLease) Release()                        { l.releaseCalls++ }

type compilerRuntime struct {
	model           *semanticmodel.Model
	projectionCalls int
}

func (r *compilerRuntime) Close() error { return nil }
func (r *compilerRuntime) SemanticModelProjection(id graph.ResourceID) (*semanticmodel.Model, bool) {
	r.projectionCalls++
	if r.model == nil || r.model.Name != id.String() {
		return nil, false
	}
	copy := *r.model
	return &copy, true
}

var _ authoringservice.Compiler = (*compileradapter.Adapter)(nil)
