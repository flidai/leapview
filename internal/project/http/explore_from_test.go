package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/go-chi/chi/v5"
)

type exploreFromSourceStub struct {
	calls     int
	source    sourceadapter.Source
	published sourceadapter.Source
	err       error
}

type exploreFromRouteAppStub struct {
	addDashboardAuthoringStub
	calls      int
	source     sourceadapter.Source
	published  sourceadapter.Source
	sourceKind sourceadapter.SourceKind
	err        error
}

func (s *exploreFromRouteAppStub) LoadDashboardSource(_ context.Context, _ projectgraph.ResourceID, _ dashboardauthoring.DashboardID, _ string) (sourceadapter.Source, error) {
	s.calls++
	return s.source, s.err
}

func (s *exploreFromRouteAppStub) LoadPublishedDashboardSource(_ context.Context, _ projectgraph.ResourceID, _ dashboardauthoring.DashboardID, _ string) (sourceadapter.Source, error) {
	s.calls++
	return s.published, s.err
}

func (s *exploreFromRouteAppStub) ResolveDashboardSourceKind(context.Context, projectgraph.ResourceID, dashboardauthoring.DashboardID, string) (sourceadapter.SourceKind, error) {
	if s.sourceKind == "" {
		return sourceadapter.SourceProject, s.err
	}
	return s.sourceKind, s.err
}

func (s *exploreFromSourceStub) LoadDashboardSource(_ context.Context, _ projectgraph.ResourceID, _ dashboardauthoring.DashboardID, _ string) (sourceadapter.Source, error) {
	s.calls++
	return s.source, s.err
}

func (s *exploreFromSourceStub) LoadPublishedDashboardSource(_ context.Context, _ projectgraph.ResourceID, _ dashboardauthoring.DashboardID, _ string) (sourceadapter.Source, error) {
	s.calls++
	return s.published, s.err
}

type exploreFromDefinitionStub struct {
	calls      int
	definition projectmanifest.Project
	compiled   map[string]*semanticquery.CompiledModel
	err        error
}

func (s *exploreFromDefinitionStub) AuthorizedExploreModel(_ context.Context, projectID projectgraph.ResourceID, _ string, modelID string) (*model.Model, *semanticquery.CompiledModel, projectgraph.ServingIdentity, error) {
	if s.err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, s.err
	}
	value := s.definition.SemanticModels[modelID]
	compiled := s.compiled[modelID]
	if value == nil || compiled == nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model unavailable")
	}
	return value, compiled, projectgraph.ServingIdentity{ProjectID: projectID, Environment: "production", GenerationID: "generation:test"}, nil
}

func (s *exploreFromDefinitionStub) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	s.calls++
	if s.err != nil {
		return projectmanifest.Project{}, nil, s.err
	}
	return s.definition, s.compiled, nil
}

func TestExploreFromDashboardAuthorizesSourceBeforeViewerLookup(t *testing.T) {
	definition := &exploreFromDefinitionStub{}
	source := &exploreFromSourceStub{err: errors.New("forbidden")}
	_, err := ExploreFromDashboard(t.Context(), ExploreFromDashboardOptions{Sources: source, Definition: definition}, ExploreFromDashboardRequest{
		ProjectID: "project:sales", DashboardID: "dashboard:sales", VisualID: "sales", ActorID: "principal:alice", Return: ExploreReturnContext{Surface: ExploreReturnExplorer},
	})
	if err == nil || err.Error() != "forbidden" {
		t.Fatalf("handoff error = %v, want source authorization error", err)
	}
	if source.calls != 1 || definition.calls != 0 {
		t.Fatalf("source/definition calls = %d/%d, want 1/0", source.calls, definition.calls)
	}
}

func TestExploreFromDashboardUsesActiveAuthoredSourceAndCanonicalURL(t *testing.T) {
	modelValue, compiled := exploreFromModel(t)
	doc := document.DashboardDocument{Spec: document.DashboardSpec{
		SemanticModel: "semantic:sales",
		Visuals: map[string]document.DashboardVisual{"sales": {
			Type:  document.DashboardVisualTypeTable,
			Title: exploreFromString("Revenue"),
			Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
				DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
				Dimensions: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "status"}}},
				Metrics:    []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue"}}},
			}},
			Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}},
		}},
		Pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "sales_card"}, Type: "visual", Visual: "sales"}}}}},
	}}
	source := &exploreFromSourceStub{source: sourceadapter.Source{Ref: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project:sales", DashboardID: "dashboard:sales"}, Document: doc, Provenance: sourceadapter.Provenance{Kind: sourceadapter.SourceProject, Project: &sourceadapter.ProjectProvenance{ProjectID: "project:sales", DashboardID: "dashboard:sales", Identity: projectgraph.ServingIdentity{ProjectID: "project:sales", Environment: "production", GenerationID: "generation:test"}}}}}
	definition := &exploreFromDefinitionStub{definition: projectmanifest.Project{ID: "project:sales", SemanticModels: map[string]*model.Model{"semantic:sales": modelValue}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}}
	result, err := ExploreFromDashboard(t.Context(), ExploreFromDashboardOptions{Sources: source, Definition: definition}, ExploreFromDashboardRequest{
		ProjectID: "project:sales", DashboardID: "dashboard:sales", PageID: "overview", VisualID: "sales", ActorID: "principal:alice", Return: ExploreReturnContext{Surface: ExploreReturnDashboard, DashboardID: "dashboard:sales"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReturnPath != "/dashboards/dashboard:sales/pages/overview" || !strings.HasPrefix(result.URL, "/explore?") {
		t.Fatalf("handoff URL/return = %q/%q", result.URL, result.ReturnPath)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("mode") != "explore" || parsed.Query().Get("v") != "2" || parsed.Query().Get("state") == "" {
		t.Fatalf("canonical URL query = %#v", parsed.Query())
	}
	command, err := dataExploreCommandFromQuery(parsed.Query())
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(command.Spec)
	wantJSON, _ := json.Marshal(result.Spec)
	if string(gotJSON) != string(wantJSON) || command.Spec.ModelID != "semantic:sales" || command.Spec.DatasetID == nil || *command.Spec.DatasetID != "orders" || command.Spec.Dimensions[0].Field != "orders.status" {
		t.Fatalf("canonical spec = %#v (%s), want %#v (%s)", command.Spec, gotJSON, result.Spec, wantJSON)
	}
}

func TestExploreFromDashboardCarriesTypedStateOnlyForSelectedVisual(t *testing.T) {
	modelValue, compiled := exploreFromModel(t)
	doc := document.DashboardDocument{Spec: document.DashboardSpec{
		SemanticModel: "semantic:sales",
		Filters: []document.DashboardFilter{{
			ID: "fixed_region", Dimension: "status", ReaderEditable: boolPointerForTest(false),
			Targets: stringSlicePointerForTest([]string{"overview/sales_card"}),
			Default: &document.DashboardFilterExpression{Value: &document.ComparisonDashboardFilterExpression{
				DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "comparison"}, Type: "comparison",
				Operator: document.DashboardFilterOperatorEquals,
				Value:    document.DashboardFilterValue{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "EMEA"}},
			}},
		}, {
			ID: "editable_region", Dimension: "status", ReaderEditable: boolPointerForTest(true),
			Targets: stringSlicePointerForTest([]string{"overview/sales_card"}),
			Default: &document.DashboardFilterExpression{Value: &document.ComparisonDashboardFilterExpression{
				DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "comparison"}, Type: "comparison",
				Operator: document.DashboardFilterOperatorEquals,
				Value:    document.DashboardFilterValue{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "US"}},
			}},
		}, {
			ID: "other_region", Dimension: "status", ReaderEditable: boolPointerForTest(false),
			Targets: stringSlicePointerForTest([]string{"overview/other_card"}),
			Default: &document.DashboardFilterExpression{Value: &document.ComparisonDashboardFilterExpression{
				DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "comparison"}, Type: "comparison",
				Operator: document.DashboardFilterOperatorEquals,
				Value:    document.DashboardFilterValue{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "APAC"}},
			}},
		}},
		Visuals: map[string]document.DashboardVisual{
			"sales": document.DashboardVisual{
				Type:         document.DashboardVisualTypeTable,
				Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}},
				Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
					DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"},
					Type:               "aggregate",
					Dimensions:         []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "status"}}},
					Metrics:            []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue"}}},
				}},
			},
		},
		Pages: []document.DashboardPage{{
			ID: "overview",
			Components: []document.DashboardPageComponent{
				{Value: &document.VisualDashboardPageComponent{
					DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "sales_card"},
					Type:                       "visual",
					Visual:                     "sales",
				}},
				{Value: &document.VisualDashboardPageComponent{
					DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "other_card"},
					Type:                       "visual",
					Visual:                     "sales",
				}},
			},
		}},
	}}
	source := &exploreFromSourceStub{source: sourceadapter.Source{Ref: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project:sales", DashboardID: "dashboard:sales"}, Document: doc, Provenance: sourceadapter.Provenance{Kind: sourceadapter.SourceProject, Project: &sourceadapter.ProjectProvenance{ProjectID: "project:sales", DashboardID: "dashboard:sales", Identity: projectgraph.ServingIdentity{ProjectID: "project:sales", Environment: "production", GenerationID: "generation:test"}}}}}
	definition := &exploreFromDefinitionStub{definition: projectmanifest.Project{ID: "project:sales", SemanticModels: map[string]*model.Model{"semantic:sales": modelValue}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}}
	filter := exploration.ExplorationFilter{Field: "orders.status", DatasetID: stringPointerForTest("orders"), Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}}}}
	result, err := ExploreFromDashboard(t.Context(), ExploreFromDashboardOptions{Sources: source, Definition: definition}, ExploreFromDashboardRequest{ProjectID: "project:sales", DashboardID: "dashboard:sales", PageID: "overview", ComponentID: "sales_card", VisualID: "sales", ActorID: "principal:alice", Return: ExploreReturnContext{Surface: ExploreReturnDashboard, DashboardID: "dashboard:sales", PageID: "overview"}, State: &DashboardExploreState{ModelID: "semantic:sales", DatasetID: stringPointerForTest("orders"), Filters: []exploration.ExplorationFilter{filter}, FilterBindingIDs: []string{"editable_region"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Spec.Filters) != 2 || result.Spec.Filters[0].Field != "orders.status" || result.Spec.Filters[1].Field != "orders.status" {
		t.Fatalf("typed current filters = %#v", result.Spec.Filters)
	}
	fixed, fixedOK := result.Spec.Filters[0].Expression.Value.(*exploration.ComparisonExplorationFilterExpression)
	current, currentOK := result.Spec.Filters[1].Expression.Value.(*exploration.ComparisonExplorationFilterExpression)
	if !fixedOK || !currentOK || fixed.Value.Value.(*exploration.StringExplorationFilterValue).Value != "EMEA" || current.Value.Value.(*exploration.StringExplorationFilterValue).Value != "paid" {
		t.Fatalf("scoped filter overlay = %#v, want fixed EMEA plus current paid", result.Spec.Filters)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("returnSurface") != "dashboard" || parsed.Query().Get("returnDashboard") != "dashboard:sales" || parsed.Query().Get("returnPage") != "overview" {
		t.Fatalf("return context query = %#v", parsed.Query())
	}
}

func TestDashboardExploreStateFromRequestRetainsScopedFilterBindingIDs(t *testing.T) {
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: stringPointerForTest("orders"), Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{{Field: "orders.status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.UnfilteredExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "unfiltered"}, Kind: "unfiltered"}}}}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:sales/pages/overview/components/sales_card/explore?v=2&mode=explore&state="+url.QueryEscape(string(encoded))+"&filterBinding=editable_region", nil)
	state, err := dashboardExploreStateFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || len(state.FilterBindingIDs) != 1 || state.FilterBindingIDs[0] != "editable_region" {
		t.Fatalf("dashboard filter binding state = %#v", state)
	}
}

func TestDashboardFiltersForVisualUsesExactComponentTarget(t *testing.T) {
	firstTarget := []string{"overview/sales_card"}
	secondTarget := []string{"overview/other_card"}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{
		{ID: "sales", Dimension: "status", Targets: &firstTarget},
		{ID: "other", Dimension: "status", Targets: &secondTarget},
	}}}
	filters := dashboardFiltersForVisual(doc, "sales", "overview", "sales_card")
	if len(filters) != 1 || filters[0].ID != "sales" {
		t.Fatalf("component-scoped filters = %#v, want only sales_card target", filters)
	}
}

func TestDashboardFilterOverlayMatchesDefaultExpressionAndDataset(t *testing.T) {
	fixed := dashboardComparisonFilterForTest("fixed", "EMEA", false)
	fixed.Targets = stringSlicePointerForTest([]string{"overview/card"})
	editable := dashboardComparisonFilterForTest("editable", "US", true)
	editable.Targets = stringSlicePointerForTest([]string{"overview/card"})
	spec := exploration.ExplorationSpec{Filters: []exploration.ExplorationFilter{
		{Field: "orders.status", DatasetID: stringPointerForTest("orders"), Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "US"}}}}},
		{Field: "orders.status", DatasetID: stringPointerForTest("orders"), Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "EMEA"}}}}},
	}}
	overlay, err := newDashboardExploreFilterOverlay(document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{fixed, editable}}}, "sales", "overview", "card", explorationadapter.ReverseOptions{Bindings: map[string]string{"status": "orders.status"}, FilterDatasets: map[string]string{"fixed": "orders", "editable": "orders"}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := overlay.editableFilterIndices["editable"]; got != 0 {
		t.Fatalf("editable filter index = %d, want exact default match at 0", got)
	}
}

func dashboardComparisonFilterForTest(id, value string, editable bool) document.DashboardFilter {
	return document.DashboardFilter{
		ID: id, Dimension: "status", ReaderEditable: boolPointerForTest(editable),
		Default: &document.DashboardFilterExpression{Value: &document.ComparisonDashboardFilterExpression{
			DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "comparison"}, Type: "comparison",
			Operator: document.DashboardFilterOperatorEquals,
			Value:    document.DashboardFilterValue{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: value}},
		}},
	}
}

func TestExploreFromDashboardSupportsPublishedInstanceSource(t *testing.T) {
	modelValue, compiled := exploreFromModel(t)
	doc := document.DashboardDocument{Spec: document.DashboardSpec{SemanticModel: "semantic:sales", Visuals: map[string]document.DashboardVisual{"sales": {Type: document.DashboardVisualTypeTable, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "status"}}}, Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue"}}}}}, Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}}}}}}
	token := dashboardauthoring.RevisionToken{RevisionID: "revision:published", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	source := &exploreFromSourceStub{published: sourceadapter.Source{Ref: sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project:sales", DashboardID: "dashboard:sales"}, Document: doc, Provenance: sourceadapter.Provenance{Kind: sourceadapter.SourceInstance, Instance: &sourceadapter.InstanceProvenance{ProjectID: "project:sales", DashboardID: "dashboard:sales", PublishedRevision: token}}}}
	definition := &exploreFromDefinitionStub{definition: projectmanifest.Project{ID: "project:sales", SemanticModels: map[string]*model.Model{"semantic:sales": modelValue}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}}
	result, err := ExploreFromDashboard(t.Context(), ExploreFromDashboardOptions{Sources: source, Definition: definition}, ExploreFromDashboardRequest{ProjectID: "project:sales", DashboardID: "dashboard:sales", VisualID: "sales", ActorID: "principal:alice", SourceKind: sourceadapter.SourceInstance, Return: ExploreReturnContext{Surface: ExploreReturnExplorer}})
	if err != nil || result.Spec.ModelID != "semantic:sales" {
		t.Fatalf("published instance handoff = %#v, %v", result, err)
	}
}

func stringPointerForTest(value string) *string { return &value }

func boolPointerForTest(value bool) *bool { return &value }

func stringSlicePointerForTest(value []string) *[]string { return &value }

func exploreFromString(value string) *string { return &value }

func TestExploreReturnContextIsClosedAndEscaped(t *testing.T) {
	if _, err := (ExploreReturnContext{Surface: ExploreReturnSurface("https://evil.test")}).Path(); err == nil {
		t.Fatal("arbitrary return surface accepted")
	}
	if _, err := (ExploreReturnContext{Surface: ExploreReturnExplorer, DashboardID: "dashboard:sales"}).Path(); err == nil {
		t.Fatal("explorer return accepted an unexpected dashboard")
	}
	path, err := (ExploreReturnContext{Surface: ExploreReturnDashboard, DashboardID: "dashboard:sales"}).Path()
	if err != nil || path != "/dashboards/dashboard:sales" {
		t.Fatalf("dashboard return path = %q/%v", path, err)
	}
	path, err = (ExploreReturnContext{Surface: ExploreReturnDashboard, DashboardID: "dashboard:sales", PageID: "overview"}).Path()
	if err != nil || path != "/dashboards/dashboard:sales/pages/overview" {
		t.Fatalf("dashboard page return path = %q/%v", path, err)
	}
	if _, err := (ExploreReturnContext{Surface: ExploreReturnDashboard, DashboardID: "dashboard:sales", PageID: "../private"}).Path(); err == nil {
		t.Fatal("unsafe dashboard page return accepted")
	}
}

func TestExploreFromDashboardRejectsSourceIdentityMismatchBeforeDefinition(t *testing.T) {
	source := &exploreFromSourceStub{source: sourceadapter.Source{Ref: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project:other", DashboardID: "dashboard:sales"}}}
	definition := &exploreFromDefinitionStub{}
	_, err := ExploreFromDashboard(t.Context(), ExploreFromDashboardOptions{Sources: source, Definition: definition}, ExploreFromDashboardRequest{
		ProjectID: "project:sales", DashboardID: "dashboard:sales", VisualID: "sales", ActorID: "principal:alice", Return: ExploreReturnContext{Surface: ExploreReturnExplorer},
	})
	if err == nil || !strings.Contains(err.Error(), "identity") || definition.calls != 0 {
		t.Fatalf("identity error/calls = %v/%d, want identity error and no definition lookup", err, definition.calls)
	}
}

func TestExploreFromDashboardRouteReturnsCanonicalLocationAndSafePage(t *testing.T) {
	modelValue, compiled := exploreFromModel(t)
	doc := document.DashboardDocument{Spec: document.DashboardSpec{
		SemanticModel: "semantic:sales",
		Visuals: map[string]document.DashboardVisual{"sales": {
			Type:         document.DashboardVisualTypeTable,
			Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}},
			Query:        document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "status"}}}, Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue"}}}}},
		}},
		Pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "sales_card"}, Type: "visual", Visual: "sales"}}}}},
	}}
	identity := projectgraph.ServingIdentity{ProjectID: "project:sales", Environment: "production", GenerationID: "generation:test"}
	app := &exploreFromRouteAppStub{source: sourceadapter.Source{Ref: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project:sales", DashboardID: "dashboard:sales"}, Document: doc, Provenance: sourceadapter.Provenance{Kind: sourceadapter.SourceProject, Project: &sourceadapter.ProjectProvenance{ProjectID: "project:sales", DashboardID: "dashboard:sales", Identity: identity}}}}
	definition := &exploreFromDefinitionStub{definition: projectmanifest.Project{ID: "project:sales", SemanticModels: map[string]*model.Model{"semantic:sales": modelValue}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}}
	h := &BrowserHandler{DashboardAuthoring: app, ProjectDefinitionReader: definition, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:sales", nil }, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:alice"}, true }}
	router := chi.NewRouter()
	h.MountAuthenticated(router)
	request := httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:sales/pages/overview/components/sales_card/explore", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != stdhttp.StatusSeeOther {
		t.Fatalf("route status = %d, body = %q", response.Code, response.Body.String())
	}
	location := response.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/explore" || parsed.Query().Get("returnSurface") != "dashboard" || parsed.Query().Get("returnDashboard") != "dashboard:sales" || parsed.Query().Get("returnPage") != "overview" {
		t.Fatalf("route location = %q", location)
	}
}

func TestExploreFromDashboardRouteSupportsPublishedInstanceComponent(t *testing.T) {
	modelValue, compiled := exploreFromModel(t)
	doc := document.DashboardDocument{Spec: document.DashboardSpec{
		SemanticModel: "semantic:sales",
		Visuals:       map[string]document.DashboardVisual{"sales": {Type: document.DashboardVisualTypeTable, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "status"}}}, Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue"}}}}}, Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}}}},
		Pages:         []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "sales_card"}, Type: "visual", Visual: "sales"}}}}},
	}}
	token := dashboardauthoring.RevisionToken{RevisionID: "revision:published", Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64)}
	source := sourceadapter.Source{Ref: sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project:sales", DashboardID: "dashboard:sales"}, Document: doc, Provenance: sourceadapter.Provenance{Kind: sourceadapter.SourceInstance, Instance: &sourceadapter.InstanceProvenance{ProjectID: "project:sales", DashboardID: "dashboard:sales", PublishedRevision: token}}}
	app := &exploreFromRouteAppStub{source: source, published: source, sourceKind: sourceadapter.SourceInstance}
	definition := &exploreFromDefinitionStub{definition: projectmanifest.Project{ID: "project:sales", SemanticModels: map[string]*model.Model{"semantic:sales": modelValue}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}}
	h := &BrowserHandler{DashboardAuthoring: app, ProjectDefinitionReader: definition, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:sales", nil }, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:alice"}, true }}
	router := chi.NewRouter()
	h.MountAuthenticated(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:sales/pages/overview/components/sales_card/explore", nil))
	if response.Code != stdhttp.StatusSeeOther {
		t.Fatalf("instance route status = %d, body = %q", response.Code, response.Body.String())
	}
	if parsed, err := url.Parse(response.Header().Get("Location")); err != nil || parsed.Path != "/explore" {
		t.Fatalf("instance route location = %q/%v", response.Header().Get("Location"), err)
	}
}

func TestExploreFromDashboardRouteDoesNotDiscloseDeniedSource(t *testing.T) {
	app := &exploreFromRouteAppStub{err: errors.New("dashboard is forbidden")}
	definition := &exploreFromDefinitionStub{}
	h := &BrowserHandler{DashboardAuthoring: app, ProjectDefinitionReader: definition, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:sales", nil }, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:alice"}, true }}
	request := httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:sales/visual/sales", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("dashboard", "dashboard:sales")
	route.URLParams.Add("visual", "sales")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	h.ExploreFromDashboardRoute(response, request)
	if response.Code != stdhttp.StatusNotFound || definition.calls != 0 {
		t.Fatalf("denied route status = %d definition calls = %d, want 404/0", response.Code, definition.calls)
	}
}

func TestExploreFromDashboardRouteRejectsUnscopedTimeOverride(t *testing.T) {
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []exploration.ExplorationDimensionRef{},
		Metrics:       []exploration.ExplorationMetricRef{},
		Filters:       []exploration.ExplorationFilter{},
		Time:          &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainMonth},
		Sort:          []exploration.ExplorationSort{},
		Limit:         100,
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	app := &exploreFromRouteAppStub{}
	h := &BrowserHandler{
		DashboardAuthoring: app,
		ResolveProjectID:   func(context.Context) (projectgraph.ResourceID, error) { return "project:sales", nil },
		CurrentUser:        func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:alice"}, true },
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:sales/pages/overview/components/sales_card/explore?v=2&mode=explore&state="+url.QueryEscape(string(encoded)), nil)
	response := httptest.NewRecorder()
	h.ExploreFromDashboardRoute(response, request)
	if response.Code != stdhttp.StatusBadRequest || app.calls != 0 {
		t.Fatalf("unscoped time override status/source calls = %d/%d, want 400/0", response.Code, app.calls)
	}
}

func exploreFromModel(t *testing.T) (*model.Model, *semanticquery.CompiledModel) {
	t.Helper()
	value := &model.Model{
		Name: "sales",
		Tables: map[string]model.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities: map[string]model.EntityDefinition{"order": {Type: "primary", Fields: []string{"status", "revenue"}}},
			Dimensions: map[string]model.MetricDimension{
				"status":  {Type: "string", Datatype: model.DataTypeString},
				"revenue": {Type: "number", Datatype: model.DataTypeDecimal},
			},
		}},
		Datasets:   map[string]model.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]model.SemanticDimension{"status": {Type: "string", Datatype: model.DataTypeString, Bindings: map[string]model.DimensionBinding{"orders": {Field: "orders.status"}}}},
		Metrics:    map[string]model.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &model.MetricInput{Field: "orders.revenue"}}},
	}
	compiled, err := semanticquery.CompileModel(value)
	if err != nil {
		t.Fatal(err)
	}
	return value, compiled
}
