package module

import (
	"net/http/httptest"
	"strings"
	"testing"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	"github.com/stretchr/testify/require"
)

type candidateMetricsStub struct {
	queryruntime.Metrics
}

func TestCandidateHTTPScopesRoutesStreamsAndSessions(t *testing.T) {
	module := &Module{handler: dashboardhttp.Handler{}}
	digest := "sha256:" + strings.Repeat("a", 64)
	handler, err := module.CandidateHTTP(CandidateHTTPConfig{
		Metrics: candidateMetricsStub{}, CandidateID: "cand_1",
		OwnerPrincipalID: "author_1", WorkspaceID: "sales",
		ArtifactDigest: digest, AuthorizationFingerprint: digest,
		RouteBasePath: "/candidates/cand_1/workspaces/sales",
		Restrictions: []CandidateRestriction{{
			ID: "region", WorkspaceID: "sales", ObjectID: "workspace:sales",
			PolicyType: "row_filter", ExpressionJSON: `{"field":"orders.region","value":"EMEA"}`,
		}},
	})
	require.NoError(t, err)
	if handler.RouteScope.BasePath != "/candidates/cand_1/workspaces/sales" ||
		handler.StreamNamespace != "candidate:cand_1" {
		t.Fatalf("candidate handler scope = (%q, %q)", handler.RouteScope.BasePath, handler.StreamNamespace)
	}
	key, err := handler.SessionKey(
		httptest.NewRequest("GET", "/", nil),
		dashboarddefinition.Definition{ID: "sales-dashboard"},
		"client", "stream",
	)
	require.NoError(t, err)
	if key.WorkspaceOrPublication != "candidate:cand_1:sales" ||
		key.ServingStateID != "candidate:cand_1:"+digest {
		t.Fatalf("candidate session key = %#v", key)
	}
}

func TestCandidateHTTPRejectsUncompiledRestrictionAtAssembly(t *testing.T) {
	module := &Module{handler: dashboardhttp.Handler{}}
	digest := "sha256:" + strings.Repeat("a", 64)
	_, err := module.CandidateHTTP(CandidateHTTPConfig{
		Metrics: candidateMetricsStub{}, CandidateID: "cand_1",
		OwnerPrincipalID: "author_1", WorkspaceID: "sales",
		ArtifactDigest: digest, AuthorizationFingerprint: digest,
		RouteBasePath: "/candidates/cand_1/workspaces/sales",
		Restrictions: []CandidateRestriction{{
			ID: "unsafe", WorkspaceID: "sales", ObjectID: "workspace:sales",
			PolicyType: "row_filter", ExpressionJSON: `{}`,
		}},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "unsafe")
}
