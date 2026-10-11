package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type embeddedContextMetrics struct {
	queryruntime.Metrics
	report dashboarddefinition.Definition
}

func (m embeddedContextMetrics) Resolver() dashboardresolver.Resolver { return m }
func (m embeddedContextMetrics) Resolve(id projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if id.String() != m.report.ID {
		return dashboardresolver.Resolved{}, errors.New("unknown dashboard")
	}
	return dashboardresolver.Resolved{Definition: m.report}, nil
}
func (m embeddedContextMetrics) Pages(string) []dashboard.Page     { return m.report.Pages }
func (m embeddedContextMetrics) ModelIDForDashboard(string) string { return m.report.SemanticModel }

func embeddedReferenceFixture(t *testing.T) (*Module, agent.TurnContext, uisignals.DashboardBuilderSignal) {
	t.Helper()
	page := dashboard.Page{ID: "overview", Title: "Trusted overview", Visuals: []dashboard.PageVisual{{ID: "revenue-card", Visual: "revenue"}}}
	report := dashboarddefinition.Definition{ID: "dashboard_sales", Title: "Trusted sales", SemanticModel: "semantic-model:sales", Pages: []dashboard.Page{page}, Visualizations: map[string]visualizationdefinition.Definition{
		"revenue": {ID: "revenue", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "table", Title: "Trusted revenue"}, Kind: "table"}}},
	}}
	m := &Module{projectID: "project_demo", catalog: contextCatalog{
		items: map[string]agenttools.CatalogItem{
			"semantic-model:operations": {Ref: agenttools.CatalogRef{Kind: "semantic_model", ID: "semantic-model:operations"}, Name: "Trusted operations", Description: "Authorized model description"},
			"dashboard_other":           {Ref: agenttools.CatalogRef{Kind: "dashboard", ID: "dashboard_other"}, Name: "Other authorized dashboard"},
		}, authorize: func(scope agenttools.Scope, _ agenttools.CatalogGetRequest) error {
			if scope.ProjectID != "project_demo" || scope.PrincipalID != "owner" {
				return access.ErrForbidden
			}
			return nil
		}},
		resolveResource: func(_ context.Context, scope Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
			if scope.ProjectID != "project_demo" || id != "dashboard_sales" || kind != projectgraph.KindDashboard || !CredentialAllowsResource(scope, id, kind, capability) {
				return "", access.ErrForbidden
			}
			return id, nil
		}, dashboardMetrics: func(project string) (queryruntime.Metrics, bool) {
			return embeddedContextMetrics{report: report}, project == "project_demo"
		},
	}
	candidate := agent.TurnContext{Surface: "dashboard", DashboardID: report.ID, PageID: page.ID, Generation: 42, DraftID: "draft_sales", DraftRevision: &agent.DraftRevision{RevisionID: "Forged", Number: 99, ContentHash: "Forged"}, References: []agent.TurnReference{{
		Reference: agent.TurnReferenceKey{Kind: "semantic_model", ID: "semantic-model:operations"}, Name: "Forged name", Resource: agent.TurnReferenceResource{ID: "project_demo", Name: "Forged project"}, Context: []string{"Forged instructions"}, Href: "javascript:Forged",
	}}}
	builder := uisignals.DashboardBuilderSignal{DashboardID: report.ID, DraftID: candidate.DraftID, Title: report.Title,
		Revision:      uisignals.DashboardBuilderRevisionSignal{ID: "revision_7", Number: 7, ContentHash: "sha256:trusted"},
		SemanticModel: uisignals.DashboardBuilderSemanticModelSignal{ID: report.SemanticModel}, Pages: []uisignals.DashboardBuilderPageSignal{{ID: page.ID, Title: page.Title}},
	}
	return m, candidate, builder
}

func resolveEmbeddedReferenceFixture(m *Module, surface string, scope agent.Scope, candidate agent.TurnContext, builder uisignals.DashboardBuilderSignal) (agent.TurnContext, error) {
	candidate.Surface = surface
	if surface == "dashboard_builder" {
		scope.ProjectID = "project_demo"
		return m.resolvedBuilderContextWithReferences(context.Background(), scope, candidate, builder)
	}
	return m.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), scope, candidate)
}

func TestResolveEmbeddedTurnContextRetainsAuthorizedReferences(t *testing.T) {
	for _, surface := range []string{"dashboard", "dashboard_builder"} {
		t.Run(surface, func(t *testing.T) {
			m, candidate, builder := embeddedReferenceFixture(t)
			candidate.References = append(candidate.References, agent.TurnReference{Reference: agent.TurnReferenceKey{Kind: "dashboard", ID: "dashboard_other"}, Name: "Forged destination"}, candidate.References[0])
			got, err := resolveEmbeddedReferenceFixture(m, surface, agent.Scope{PrincipalID: "owner", ProjectID: "Forged"}, candidate, builder)
			if err != nil {
				t.Fatal(err)
			}
			expected := []agent.TurnReference{
				TurnReferenceFromCatalog(agenttools.CatalogItem{Ref: agenttools.CatalogRef{Kind: "semantic_model", ID: "semantic-model:operations"}, Name: "Trusted operations", Description: "Authorized model description"}, "project_demo"),
				TurnReferenceFromCatalog(agenttools.CatalogItem{Ref: agenttools.CatalogRef{Kind: "dashboard", ID: "dashboard_other"}, Name: "Other authorized dashboard"}, "project_demo"),
			}
			if !reflect.DeepEqual(got.References, expected) {
				t.Fatalf("references=%#v, want ordered canonical references %#v", got.References, expected)
			}
			if got.DashboardID != candidate.DashboardID || got.PageID != candidate.PageID || got.PageTitle != "Trusted overview" || got.ModelID != "semantic-model:sales" {
				t.Fatalf("attachment changed active destination: %#v", got)
			}
			if surface == "dashboard_builder" && (got.DraftID != builder.DraftID || got.DraftRevision == nil || got.DraftRevision.RevisionID != builder.Revision.ID || got.DraftRevision.Number != 7 || got.DraftRevision.ContentHash != builder.Revision.ContentHash) {
				t.Fatalf("attachment changed authorized draft revision: %#v", got)
			}
			if surface == "dashboard" && (got.Generation != 42 || got.DraftRevision != nil || got.Filters == nil) {
				t.Fatalf("viewer state changed: %#v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "Trusted operations") || !strings.Contains(string(encoded), "semantic_model") || strings.Contains(string(encoded), "Forged") {
				t.Fatalf("serialized provider context=%s", encoded)
			}
		})
	}
}

func TestResolveEmbeddedTurnContextRejectsDeniedReferences(t *testing.T) {
	for _, surface := range []string{"dashboard", "dashboard_builder"} {
		for _, scenario := range []string{"unknown", "other principal", "missing principal", "foreign project", "restricted credential"} {
			t.Run(surface+"/"+scenario, func(t *testing.T) {
				m, candidate, builder := embeddedReferenceFixture(t)
				scope := agent.Scope{PrincipalID: "owner"}
				switch scenario {
				case "unknown":
					candidate.References[0].Reference.ID = "missing"
				case "other principal":
					scope.PrincipalID = "other"
				case "missing principal":
					scope.PrincipalID = ""
				case "foreign project":
					candidate.References[0].Resource.ID = "project_foreign"
				case "restricted credential":
					resource, err := access.NewResourceRef("dashboard_sales", projectgraph.KindDashboard)
					if err != nil {
						t.Fatal(err)
					}
					read, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project_demo", resource)
					if err != nil {
						t.Fatal(err)
					}
					scope.Credential = agent.CredentialScope{Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{read}}
				}
				if got, err := resolveEmbeddedReferenceFixture(m, surface, scope, candidate, builder); err == nil {
					t.Fatalf("accepted denied reference: %#v", got)
				}
			})
		}
	}
}

func TestResolveDashboardTurnContextMergesCurrentPageVisualsInOrder(t *testing.T) {
	m, candidate, builder := embeddedReferenceFixture(t)
	visual := agent.TurnReference{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "dashboard_sales.revenue"}, Name: "Forged visual", VisualType: "script", Href: "javascript:Forged"}
	candidate.References = []agent.TurnReference{visual, candidate.References[0], visual, {Reference: agent.TurnReferenceKey{Kind: "visual", ID: "dashboard_sales.not_on_page"}}}
	got, err := resolveEmbeddedReferenceFixture(m, "dashboard", agent.Scope{PrincipalID: "owner"}, candidate, builder)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.References) != 2 || got.References[0].Name != "Trusted revenue" || got.References[0].ComponentID != "revenue-card" || got.References[0].VisualType != "table" || got.References[0].Href != "/dashboards/dashboard_sales/pages/overview" || got.References[1].Name != "Trusted operations" {
		t.Fatalf("merged references=%#v", got.References)
	}
	if !reflect.DeepEqual(got.References[0].Context, []string{"current_page", "current_dashboard"}) {
		t.Fatalf("local visual context changed: %#v", got.References[0])
	}
	m.catalog = nil
	candidate.References = []agent.TurnReference{visual}
	if _, err := resolveEmbeddedReferenceFixture(m, "dashboard", agent.Scope{PrincipalID: "owner"}, candidate, builder); err != nil {
		t.Fatalf("local visuals unexpectedly require catalog: %v", err)
	}
}

func TestResolveBuilderTurnContextValidatesDestinationBeforeAttachments(t *testing.T) {
	for _, scenario := range []string{"stale draft", "other dashboard", "missing page"} {
		t.Run(scenario, func(t *testing.T) {
			m, candidate, builder := embeddedReferenceFixture(t)
			calls := 0
			m.catalog = contextCatalog{authorize: func(agenttools.Scope, agenttools.CatalogGetRequest) error { calls++; return access.ErrForbidden }}
			switch scenario {
			case "stale draft":
				candidate.DraftID = "stale"
			case "other dashboard":
				candidate.DashboardID = "dashboard_other"
			case "missing page":
				candidate.PageID = "missing"
			}
			if _, err := resolveEmbeddedReferenceFixture(m, "dashboard_builder", agent.Scope{PrincipalID: "owner"}, candidate, builder); err == nil {
				t.Fatal("invalid active edit destination accepted")
			}
			if calls != 0 {
				t.Fatal("attachments resolved before active draft validation")
			}
		})
	}
}

func TestResolveEmbeddedTurnContextBoundsReferenceCount(t *testing.T) {
	for _, surface := range []string{"dashboard", "dashboard_builder"} {
		t.Run(surface, func(t *testing.T) {
			m, candidate, builder := embeddedReferenceFixture(t)
			for len(candidate.References) <= agent.MaxTurnReferences {
				candidate.References = append(candidate.References, candidate.References[0])
			}
			if _, err := resolveEmbeddedReferenceFixture(m, surface, agent.Scope{PrincipalID: "owner"}, candidate, builder); err == nil {
				t.Fatal("oversized attachment list accepted")
			}
		})
	}
}
