package module

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

type candidateMetricsStub struct {
	queryruntime.Metrics
}

func TestCandidateHTTPScopesRoutesStreamsAndSessions(t *testing.T) {
	module := &Module{handler: dashboardhttp.Handler{
		ProjectID: "active_project",
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "", errors.New("no active serving state")
		},
	}}
	digest := "sha256:" + strings.Repeat("a", 64)
	resource, err := access.NewResourceRef(projectgraph.ResourceID("dashboard_1"), projectgraph.KindDashboard)
	require.NoError(t, err)
	handler, err := module.CandidateHTTP(CandidateHTTPConfig{
		Metrics: candidateMetricsStub{}, CandidateID: "cand_1",
		OwnerPrincipalID: "author_1", ProjectID: "project_1",
		ArtifactDigest: digest, AuthorizationFingerprint: digest,
		RouteBasePath: "/candidates/cand_1/projects/project_1",
		Restrictions: []CandidateRestriction{{
			ID: "region", Resource: resource,
			PolicyType: "row_filter", ExpressionJSON: `{"field":"orders.region","value":"EMEA"}`,
		}},
	})
	require.NoError(t, err)
	if handler.RouteScope.BasePath != "/candidates/cand_1/projects/project_1" ||
		handler.StreamNamespace != "candidate:cand_1" {
		t.Fatalf("candidate handler scope = (%q, %q)", handler.RouteScope.BasePath, handler.StreamNamespace)
	}
	if handler.ProjectID != "project_1" || handler.ResolveProjectID != nil {
		t.Fatalf("candidate project identity = (%q, resolver configured=%t)", handler.ProjectID, handler.ResolveProjectID != nil)
	}
	key, err := handler.SessionKey(
		httptest.NewRequest("GET", "/", nil),
		dashboarddefinition.Definition{ID: "sales-dashboard"},
		"client", "stream",
	)
	require.NoError(t, err)
	if key.ProjectID != "project_1" ||
		key.ServingStateID != "candidate:cand_1:"+digest {
		t.Fatalf("candidate session key = %#v", key)
	}
}

func TestCandidateHTTPRejectsUncompiledRestrictionAtAssembly(t *testing.T) {
	module := &Module{handler: dashboardhttp.Handler{}}
	digest := "sha256:" + strings.Repeat("a", 64)
	resource, err := access.NewResourceRef(projectgraph.ResourceID("dashboard_1"), projectgraph.KindDashboard)
	require.NoError(t, err)
	_, err = module.CandidateHTTP(CandidateHTTPConfig{
		Metrics: candidateMetricsStub{}, CandidateID: "cand_1",
		OwnerPrincipalID: "author_1", ProjectID: "project_1",
		ArtifactDigest: digest, AuthorizationFingerprint: digest,
		RouteBasePath: "/candidates/cand_1/projects/project_1",
		Restrictions: []CandidateRestriction{{
			ID: "unsafe", Resource: resource,
			PolicyType: "row_filter", ExpressionJSON: `{}`,
		}},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "unsafe")
}
