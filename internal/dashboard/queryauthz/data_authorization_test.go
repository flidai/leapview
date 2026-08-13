package authz

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
	"github.com/stretchr/testify/require"
)

type semanticModelMetrics struct {
	queryruntime.Metrics
	model *semanticmodel.Model
}

func (m semanticModelMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return m.model, modelID == m.model.Name
}

func TestGovernModelAggregateUsesTransitivePhysicalPolicies(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "analyst", Email: "analyst@example.com"})
	require.NoError(t, err)

	model := governanceTestModel()
	modelObject := access.ItemObject(access.SecurableSemanticModel, "test", model.Name)
	ratingsDataset := access.ItemObjectWithParent(access.SecurableDataset, "test", model.Name+"/ratings", modelObject)
	tagsDataset := access.ItemObjectWithParent(access.SecurableDataset, "test", model.Name+"/tags", modelObject)
	ratingColumn := access.ItemObjectWithParent(access.SecurableColumn, "test", model.Name+"/ratings/rating", ratingsDataset)
	for _, object := range []access.ObjectRef{
		modelObject,
		access.ItemObjectWithParent(access.SecurableSemanticField, "test", model.Name+"/average_value", modelObject),
		access.ItemObjectWithParent(access.SecurableSemanticField, "test", model.Name+"/rating_sum", modelObject),
		access.ItemObjectWithParent(access.SecurableSemanticField, "test", model.Name+"/rating_count", modelObject),
		access.ItemObjectWithParent(access.SecurableSemanticField, "test", model.Name+"/tag_count", modelObject),
		ratingsDataset,
		tagsDataset,
		ratingColumn,
	} {
		if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
			t.Fatalf("upsert %s: %v", object.CanonicalID(), err)
		}
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object: modelObject, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeQueryData,
	}); err != nil {
		t.Fatal(err)
	}
	rowFilter, _ := json.Marshal(map[string]any{"field": "ratings.status", "operator": "equals", "values": []any{"published"}})
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{Object: ratingsDataset, PolicyType: "row_filter", ExpressionJSON: string(rowFilter)}); err != nil {
		t.Fatal(err)
	}
	columnMask, _ := json.Marshal(map[string]any{"field": "ratings.rating", "mask": "null"})
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{Object: ratingColumn, PolicyType: "column_mask", ExpressionJSON: string(columnMask)}); err != nil {
		t.Fatal(err)
	}

	metrics := New(semanticModelMetrics{model: model}, Options{
		Repo: repo,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: principal.ID}, true
		},
	})
	request := dataquery.Query{
		WorkspaceID: "test",
		ModelID:     model.Name,
		Kind:        dataquery.KindSemanticAggregate,
		Measures: []dataquery.Field{
			{Field: "average_value", Alias: "average_value"},
			{Field: "tag_count", Alias: "tag_count"},
		},
	}
	governed, _, err := metrics.GovernDataQuery(ctx, request)
	require.NoError(t, err)
	if len(governed.Filters) != 1 || governed.Filters[0].Fact != "ratings" {
		t.Fatalf("governed filters = %#v, want ratings-targeted row policy", governed.Filters)
	}
	if len(governed.ColumnMasks) != 1 || governed.ColumnMasks[0].Field != "ratings.rating" {
		t.Fatalf("governed masks = %#v, want transitive rating mask", governed.ColumnMasks)
	}

	_, err = semanticquery.NewPlanner(model).Plan(semanticquery.Request{
		Measures: []semanticquery.Field{{Field: "average_value"}, {Field: "tag_count"}},
		Filters:  dataFiltersToSemanticFilters(governed.Filters),
		ColumnMasks: []semanticquery.ColumnMask{{
			Field: governed.ColumnMasks[0].Field,
			Mask:  governed.ColumnMasks[0].Mask,
		}},
	})
	if err == nil {
		t.Fatal("masked metric dependency was accepted by the aggregate planner")
	}
}

func TestGovernDashboardCountUsesAuthorizationProjectionPolicies(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "analyst", Email: "analyst@example.com"})
	require.NoError(t, err)

	model := governanceTestModel()
	modelObject := access.ItemObject(access.SecurableSemanticModel, "test", model.Name)
	dataset := access.ItemObjectWithParent(access.SecurableDataset, "test", model.Name+"/ratings", modelObject)
	column := access.ItemObjectWithParent(access.SecurableColumn, "test", model.Name+"/ratings/rating", dataset)
	for _, object := range []access.ObjectRef{modelObject, dataset, column} {
		if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object: dataset, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeQueryData,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		Object: column, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.status","operator":"equals","values":["published"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		Object: column, PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.rating","mask":"null"}`,
	}); err != nil {
		t.Fatal(err)
	}

	metrics := New(semanticModelMetrics{model: model}, Options{
		Repo: repo,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: principal.ID}, true
		},
	})
	governed, _, err := metrics.GovernDataQuery(ctx, dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardCount,
		WorkspaceID: "test", ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings", IncludeTotal: true,
		AuthorizationFields: []dataquery.Field{{Field: "ratings.rating", Alias: "rating"}},
	})
	require.NoError(t, err)
	if len(governed.Filters) != 1 || governed.Filters[0].Field != "ratings.status" {
		t.Fatalf("governed count filters = %#v", governed.Filters)
	}
	if len(governed.ColumnMasks) != 1 || governed.ColumnMasks[0].Field != "ratings.rating" {
		t.Fatalf("governed count masks = %#v", governed.ColumnMasks)
	}
}

func TestGovernDataQueryRejectsPrincipalEscalationAgainstAuthenticatedActor(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(
		ctx,
		workspace.EnsureInput{ID: "test", Title: "Test"},
	); err != nil {
		t.Fatal(err)
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	actor, err := repo.UpsertPrincipal(
		ctx,
		access.PrincipalInput{ID: "author", Email: "author@example.com"},
	)
	require.NoError(t, err)
	privileged, err := repo.UpsertPrincipal(
		ctx,
		access.PrincipalInput{ID: "admin", Email: "admin@example.com"},
	)
	require.NoError(t, err)
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object: access.WorkspaceObject("test"), SubjectType: access.SubjectPrincipal,
		SubjectID: privileged.ID, Privilege: access.PrivilegePreviewData,
	}); err != nil {
		t.Fatal(err)
	}
	metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{
		Repo: repo,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: actor.ID}, true
		},
	})

	_, _, err = metrics.GovernDataQuery(ctx, dataquery.Query{
		WorkspaceID: "test", PrincipalID: privileged.ID,
		ModelID: "activity", Kind: dataquery.KindModelTableRows, Target: "ratings",
		Fields: []dataquery.Field{{Field: "rating"}},
	})
	if !IsDenied(err) {
		t.Fatalf("GovernDataQuery() error = %v, want denied principal escalation", err)
	}
	events, auditErr := repo.ListAuditEvents(
		ctx,
		access.AuditEventFilter{WorkspaceID: "test", PrincipalID: actor.ID},
	)
	if auditErr != nil {
		t.Fatal(auditErr)
	}
	if len(events) != 1 || events[0].Status != "denied" {
		t.Fatalf("escalation audit events = %#v", events)
	}
}

func TestResolvedDependencyObjectsIncludesRowQueryFilterFields(t *testing.T) {
	model := governanceTestModel()
	metrics := New(semanticModelMetrics{model: model}, Options{})
	_, physicalObjects, err := metrics.resolvedDependencyObjects(dataquery.Query{
		WorkspaceID: "test", ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings",
		Fields:  []dataquery.Field{{Field: "ratings.rating"}},
		Filters: []dataquery.Filter{{Field: "ratings.status", Operator: "equals", Values: []any{"published"}}},
	}, true)
	require.NoError(t, err)
	if !containsObject(physicalObjects, access.SecurableColumn, "activity/ratings/status") {
		t.Fatalf("physical dependencies = %#v, want ratings/status", physicalObjects)
	}
}

func TestPublicationQueryFailsClosedWhenAuditIdentityIsMissing(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	model := governanceTestModel()
	metrics := New(semanticModelMetrics{model: model}, Options{Repo: accesssqlite.NewRepository(store.SQLDB())})
	ctx = WithDashboardPublicationCapability(ctx, DashboardPublicationCapability{
		WorkspaceID: "test", Publication: "website", Dashboard: "dashboard", ModelID: model.Name,
		DependencyAssetIDs: []string{
			"dashboard:test.dashboard", "semantic_model:test.activity", "semantic_table:test.activity.ratings",
			"field:test.activity.ratings.rating",
		},
	})
	_, transform, err := metrics.GovernDataQuery(ctx, dataquery.Query{
		WorkspaceID: "test", Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings", Fields: []dataquery.Field{{Field: "ratings.rating"}},
	})
	if err != nil {
		t.Fatalf("govern public query: %v", err)
	}
	result := dataquery.Result{Status: dataquery.StatusSuccess}
	if err := transform(&result, nil); err == nil {
		t.Fatal("publication query succeeded without a durable audit identity")
	}
}

func TestPublicationQueryAppliesGlobalAndPublicationPoliciesAndPersistsAudit(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	principalID := access.DashboardPublicationSubjectID("test", "website")
	if _, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: principalID, DisplayName: "website", Kind: access.PrincipalKindDashboardPublication}); err != nil {
		t.Fatal(err)
	}
	model := governanceTestModel()
	modelObject := access.ItemObject(access.SecurableSemanticModel, "test", model.Name)
	dataset := access.ItemObjectWithParent(access.SecurableDataset, "test", model.Name+"/ratings", modelObject)
	rating := access.ItemObjectWithParent(access.SecurableColumn, "test", model.Name+"/ratings/rating", dataset)
	for _, object := range []access.ObjectRef{modelObject, dataset, rating} {
		if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		Object: dataset, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.status","operator":"equals","values":["published"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		Object: rating, SubjectType: access.SubjectDashboardPublication, SubjectID: principalID,
		PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.rating","mask":"null"}`,
	}); err != nil {
		t.Fatal(err)
	}
	metrics := New(semanticModelMetrics{model: model}, Options{Repo: repo})
	ctx = WithDashboardPublicationCapability(ctx, DashboardPublicationCapability{
		WorkspaceID: "test", Publication: "website", Dashboard: "dashboard", ModelID: model.Name,
		DependencyAssetIDs: []string{
			"dashboard:test.dashboard", "semantic_model:test.activity", "semantic_table:test.activity.ratings",
			"field:test.activity.ratings.rating", "field:test.activity.ratings.status",
		},
	})
	governed, transform, err := metrics.GovernDataQuery(ctx, dataquery.Query{
		WorkspaceID: "test", Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings", Fields: []dataquery.Field{{Field: "ratings.rating"}},
	})
	require.NoError(t, err)
	if len(governed.Filters) != 1 || governed.Filters[0].Field != "ratings.status" {
		t.Fatalf("publication row filters = %#v", governed.Filters)
	}
	if len(governed.ColumnMasks) != 1 || governed.ColumnMasks[0].Field != "ratings.rating" {
		t.Fatalf("publication column masks = %#v", governed.ColumnMasks)
	}
	result := dataquery.Result{Status: dataquery.StatusSuccess}
	if err := transform(&result, nil); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", PrincipalID: principalID})
	if err != nil || len(events) != 1 || events[0].Action != "data_query.executed" {
		t.Fatalf("publication audit events = %#v error=%v", events, err)
	}
}

func TestPublicationQueryRejectsCandidateAuthority(t *testing.T) {
	model := governanceTestModel()
	repository := &candidateAuthorizationRepository{}
	metrics := New(semanticModelMetrics{model: model}, Options{
		Repo: repository,
	})
	ctx := WithDashboardPublicationCapability(t.Context(), DashboardPublicationCapability{
		WorkspaceID: "test", Publication: "website", Dashboard: "dashboard", ModelID: model.Name,
		DependencyAssetIDs: []string{
			"dashboard:test.dashboard", "semantic_model:test.activity", "semantic_table:test.activity.ratings",
			"field:test.activity.ratings.rating",
		},
	})
	ctx = WithCandidateQueryCapability(ctx, CandidateQueryCapability{
		CandidateID: "cand_1", OwnerPrincipalID: "author_1", WorkspaceID: "test",
		PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	_, _, err := metrics.GovernDataQuery(ctx, dataquery.Query{
		WorkspaceID: "test", Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings",
		Fields: []dataquery.Field{{Field: "ratings.rating"}},
	})
	if err == nil {
		t.Fatal("public query accepted candidate authority")
	}
	if len(repository.audits) != 1 || repository.audits[0].Status != "denied" {
		t.Fatalf("publication candidate audits = %#v", repository.audits)
	}
}

func TestGovernDataQueryRejectsMissingWorkspaceScope(t *testing.T) {
	metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{})
	_, _, err := metrics.GovernDataQuery(t.Context(), dataquery.Query{
		ModelID: "activity", Kind: dataquery.KindModelTableRows, Target: "ratings",
		Fields: []dataquery.Field{{Field: "rating"}},
	})
	if err == nil {
		t.Fatal("GovernDataQuery accepted a query without workspace scope")
	}
}

func containsObject(objects []access.ObjectRef, typ access.SecurableType, id string) bool {
	for _, object := range objects {
		if object.Type == typ && object.ObjectID == id {
			return true
		}
	}
	return false
}

func governanceTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "activity",
		Tables: map[string]semanticmodel.Table{
			"ratings": {Dimensions: map[string]semanticmodel.MetricDimension{
				"rating": {Type: "number"},
				"status": {Type: "string"},
			}},
			"tags": {Dimensions: map[string]semanticmodel.MetricDimension{
				"tag": {Type: "string"},
			}},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"rating_sum":   {Fact: "ratings", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "ratings.rating"}, Empty: "null"},
			"rating_count": {Fact: "ratings", Aggregation: "count", Empty: "zero"},
			"tag_count":    {Fact: "tags", Aggregation: "count", Empty: "zero"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"average_value": {Expression: "safe_divide(${rating_sum}, ${rating_count})"},
		},
	}
}
