package authz

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	"github.com/flidai/leapview/internal/platform"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
)

func TestPublicationArrowFailsBeforeStreamingWhenAuditCannotPersist(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := projectsqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}

	model := governanceTestModel()
	backend := &publicationArrowMetrics{model: model}
	metrics := New(backend, Options{Repo: accesssqlite.NewRepository(store.SQLDB())})
	ctx = WithDashboardPublicationCapability(ctx, DashboardPublicationCapability{
		WorkspaceID: "test", Publication: "website", Dashboard: "dashboard", ModelID: model.Name,
		DependencyAssetIDs: []string{
			"dashboard:test.dashboard", "semantic_model:test.activity", "semantic_table:test.activity.ratings",
			"field:test.activity.ratings.rating",
		},
	})
	sink := &recordingArrowSink{}
	_, err = metrics.ExecuteDataQueryArrow(ctx, dataquery.Query{
		WorkspaceID: "test", Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings", Fields: []dataquery.Field{{Field: "ratings.rating"}},
	}, sink)
	if err == nil {
		t.Fatal("publication Arrow query succeeded without a durable pre-stream audit")
	}
	if backend.executed || sink.schemas != 0 || sink.records != 0 {
		t.Fatalf("stream started before durable audit: executed=%t schemas=%d records=%d", backend.executed, sink.schemas, sink.records)
	}
}

func TestPublicationArrowPersistsStartedAuditBeforeStreaming(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := projectsqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	baseRepo := accesssqlite.NewRepository(store.SQLDB())
	principalID := access.DashboardPublicationSubjectID("test", "website")
	if _, err := baseRepo.UpsertPrincipal(ctx, access.PrincipalInput{ID: principalID, DisplayName: "Website", Kind: access.PrincipalKindDashboardPublication}); err != nil {
		t.Fatal(err)
	}
	repo := &orderedAuditRepository{Repository: baseRepo}
	model := governanceTestModel()
	backend := &publicationArrowMetrics{model: model, beforeExecute: func() error {
		if len(repo.statuses) != 1 || repo.statuses[0] != "started" {
			return fmt.Errorf("audit statuses before execution = %v, want [started]", repo.statuses)
		}
		return nil
	}}
	metrics := New(backend, Options{Repo: repo})
	ctx = WithDashboardPublicationCapability(ctx, DashboardPublicationCapability{
		WorkspaceID: "test", Publication: "website", Dashboard: "dashboard", ModelID: model.Name,
		DependencyAssetIDs: []string{
			"dashboard:test.dashboard", "semantic_model:test.activity", "semantic_table:test.activity.ratings",
			"field:test.activity.ratings.rating",
		},
	})
	if _, err := metrics.ExecuteDataQueryArrow(ctx, dataquery.Query{
		WorkspaceID: "test", Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings", Fields: []dataquery.Field{{Field: "ratings.rating"}},
	}, &recordingArrowSink{}); err != nil {
		t.Fatal(err)
	}
	if len(repo.statuses) != 2 || repo.statuses[0] != "started" || repo.statuses[1] != "success" {
		t.Fatalf("publication Arrow audit statuses = %v, want [started success]", repo.statuses)
	}
}

type orderedAuditRepository struct {
	access.Repository
	statuses []string
}

func (r *orderedAuditRepository) RecordAuditEvent(ctx context.Context, input access.AuditEventInput) error {
	if err := r.Repository.RecordAuditEvent(ctx, input); err != nil {
		return err
	}
	r.statuses = append(r.statuses, input.Status)
	return nil
}

type publicationArrowMetrics struct {
	queryruntime.Metrics
	model         *semanticmodel.Model
	executed      bool
	beforeExecute func() error
}

func (m *publicationArrowMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return m.model, modelID == m.model.Name
}

func (m *publicationArrowMetrics) ExecuteDataQueryArrow(_ context.Context, _ dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	m.executed = true
	if m.beforeExecute != nil {
		if err := m.beforeExecute(); err != nil {
			return dataquery.Result{}, err
		}
	}
	if err := sink.WriteSchema(arrow.NewSchema(nil, nil)); err != nil {
		return dataquery.Result{}, err
	}
	return dataquery.Result{Status: dataquery.StatusSuccess}, nil
}

type recordingArrowSink struct {
	schemas int
	records int
}

func (s *recordingArrowSink) WriteSchema(*arrow.Schema) error {
	s.schemas++
	return nil
}

func (s *recordingArrowSink) WriteRecord(arrow.RecordBatch) error {
	s.records++
	return nil
}

var _ arrowquery.Executor = (*publicationArrowMetrics)(nil)
var _ arrowquery.Sink = (*recordingArrowSink)(nil)
