package app

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	"github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// BenchmarkSavedExplorationRead measures an in-memory application read with a
// stub authorizer. Runtime acquisition and canonical payload decoding remain
// in the measured path; it is not a database or RBAC latency measurement.
func BenchmarkSavedExplorationRead(b *testing.B) {
	service, request := benchmarkSavedReadService(b)
	ctx := savedAdapterContext("viewer")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		opened, err := service.Read(ctx, request)
		if err != nil {
			b.Fatal(err)
		}
		if opened.Revision.Payload.ContentHash() == "" || opened.Lifecycle.ID != "monthly-revenue" {
			b.Fatalf("opened saved exploration = %#v, want monthly sales revision", opened)
		}
		runtime.KeepAlive(opened)
	}
}

// BenchmarkSavedExplorationMonthlyProjection keeps renderer-neutral
// projection costs visible for the valid monthly table/chart/pivot shapes.
// These are diagnostic benchmarks only; resource limits are enforced by the
// canonical validation and visualization IR validators, not wall-clock gates.
func BenchmarkSavedExplorationMonthlyProjection(b *testing.B) {
	model := savedMonthlyExportModel()
	for _, rowCount := range []int{128, 1000} {
		result, fields := benchmarkMonthlyProjectionResult(rowCount)
		for _, test := range benchmarkMonthlyProjectionCases(int32(rowCount)) {
			b.Run(fmt.Sprintf("rows-%d/%s", rowCount, test.name), func(b *testing.B) {
				if err := canonical.ValidateShape(&test.spec); err != nil {
					b.Fatal(err)
				}
				if err := canonical.ValidateAgainstModel(model, &test.spec); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					projection := projecthttp.ProjectDataExplorerViews(test.spec, result, fields)
					if projection.RecommendedView != test.wantView || projection.Views[test.wantView].Spec.Value == nil {
						b.Fatalf("projection view=%q views=%#v, want %q", projection.RecommendedView, projection.Views, test.wantView)
					}
					runtime.KeepAlive(projection)
				}
			})
		}
	}
}

type benchmarkMonthlyProjectionCase struct {
	name     string
	spec     canonical.ExplorationSpec
	wantView string
}

func benchmarkMonthlyProjectionCases(limit int32) []benchmarkMonthlyProjectionCase {
	base := savedMonthlyExportSpec()
	base.Limit = limit
	table := base
	table.Visualization = &canonical.ExplorationVisualizationConfig{Value: &canonical.TableExplorationVisualization{Kind: "table"}}
	chart := base
	chart.Visualization = &canonical.ExplorationVisualizationConfig{Value: &canonical.CartesianExplorationVisualization{
		Kind: "cartesian", Mark: canonical.ExplorationVisualizationCartesianMarkLine,
		X: &canonical.ExplorationVisualizationFieldRef{Field: "activity_date"},
		Y: &[]canonical.ExplorationVisualizationFieldRef{{Field: "revenue"}},
	}}
	pivot := base
	month := canonical.ExplorationTimeGrainMonth
	monthAlias := "month"
	stateAlias := "state"
	pivot.Pivot = &canonical.ExplorationPivotConfig{
		Rows:    []canonical.ExplorationDimensionRef{{Field: "activity_date", Alias: &monthAlias, Grain: &month}},
		Columns: []canonical.ExplorationDimensionRef{{Field: "customer_state", Alias: &stateAlias}},
		Metrics: []canonical.ExplorationMetricRef{{Field: "revenue"}},
	}
	return []benchmarkMonthlyProjectionCase{
		{name: "table", spec: table, wantView: "table"},
		{name: "chart", spec: chart, wantView: "line"},
		{name: "pivot", spec: pivot, wantView: "pivot"},
	}
}

func benchmarkMonthlyProjectionResult(rowCount int) (projectsignals.DataExploreResultSignal, []projectsignals.DataExploreFieldSignal) {
	rows := make([]map[string]any, rowCount)
	for index := range rows {
		month := time.Date(2026, time.January+time.Month(index/64), 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		state := fmt.Sprintf("state-%02d", index%64)
		rows[index] = map[string]any{"month": month, "state": state, "revenue": fmt.Sprintf("%d.00", 25+(index%76))}
	}
	result := projectsignals.DataExploreResultSignal{
		Columns: []projectsignals.DataPreviewColumnSignal{
			{Key: "month", Label: "Activity date", Type: projectsignals.Pointer("timestamp")},
			{Key: "state", Label: "Customer state", Type: projectsignals.Pointer("string")},
			{Key: "revenue", Label: "Revenue", Type: projectsignals.Pointer("decimal")},
		},
		Rows:       rows,
		RequestSeq: 1,
	}
	fields := []projectsignals.DataExploreFieldSignal{
		{ID: "activity_date", Kind: "dimension", Label: "Activity date", Type: projectsignals.Pointer("timestamp"), DatasetID: "orders", Compatible: true},
		{ID: "customer_state", Kind: "dimension", Label: "Customer state", Type: projectsignals.Pointer("string"), DatasetID: "customers", Compatible: true},
		{ID: "revenue", Kind: "metric", Label: "Revenue", Type: projectsignals.Pointer("decimal"), DatasetID: "orders", Compatible: true},
	}
	return result, fields
}

func benchmarkSavedReadService(b *testing.B) (saved.Service, saved.ReadRequest) {
	b.Helper()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	identity, err := graph.NewServingIdentity(savedAdapterProject, "production", "generation:saved")
	if err != nil {
		b.Fatal(err)
	}
	payload, err := saved.NewExplorationSpecPayload(savedMonthlyExportSpec())
	if err != nil {
		b.Fatal(err)
	}
	revision, err := saved.NewRevision("revision-monthly", 1, now, "owner", payload, identity)
	if err != nil {
		b.Fatal(err)
	}
	record, err := saved.NewSavedExploration(saved.NewInput{
		ProjectID: savedAdapterProject, ID: "monthly-revenue", OwnerPrincipalID: "owner", Title: "Monthly Revenue", Slug: "monthly-revenue", Visibility: saved.VisibilityOrganization,
		SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision,
	})
	if err != nil {
		b.Fatal(err)
	}
	repository := &savedMonthlyRepository{lifecycle: record.Lifecycle(), revision: revision}
	runtimeProvider := &savedMonthlyRuntimeProvider{
		leases:   map[string]*savedAdapterLease{"viewer": {runtime: savedAdapterRuntime{identity: identity}, identity: identity}},
		acquires: map[string]int{},
	}
	service, err := savedapplication.NewService(savedapplication.Options{
		Repository:    repository,
		Authorizer:    savedapplication.AuthorizerFunc(func(context.Context, projectruntime.Lease, savedapplication.AuthorizationRequest) error { return nil }),
		Runtime:       runtimeProvider,
		Now:           func() time.Time { return now },
		NewRevisionID: func() (saved.RevisionID, error) { return "revision-benchmark", nil },
	})
	if err != nil {
		b.Fatal(err)
	}
	return service, saved.ReadRequest{ProjectID: savedAdapterProject, ID: "monthly-revenue", ActorID: "viewer"}
}
