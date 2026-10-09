package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

type namedConnectionRecorder struct{ ids []string }

func (r *namedConnectionRecorder) Resolve(_ context.Context, id string, connection semanticmodel.Connection) (semanticmodel.Connection, error) {
	r.ids = append(r.ids, id)
	return connection, nil
}

func TestMaterializerConnectionNamesUseExactImmutableCompiledMapping(t *testing.T) {
	m := &Module{}
	recorder := &namedConnectionRecorder{}
	key := candidateRuntimeBindingKey{candidateID: "candidate_1", projectID: "sales"}
	token := m.candidateRuntimeBindings.register(key, recorder)
	t.Cleanup(func() { m.candidateRuntimeBindings.remove(key, token) })
	ids := map[string]string{"warehouse": "opaque_b", "opaque_b": "opaque_a"}
	resolver := (&duckDBProjectMaterializer{module: m}).connectionResolver(analyticsmaterialization.Request{
		CandidateID: "candidate_1", Identity: projectgraph.ServingIdentity{ProjectID: "sales"}, ConnectionIDs: ids,
	})
	ids["warehouse"] = "wrong_resource"
	delete(ids, "opaque_b")
	for _, name := range []string{"warehouse", "opaque_b"} {
		logical := semanticmodel.Connection{Kind: "postgres", Scope: "warehouse"}
		got, err := resolver.Resolve(t.Context(), name, logical)
		require.NoError(t, err)
		require.Equal(t, logical, got)
	}
	require.Equal(t, []string{"opaque_b", "opaque_a"}, recorder.ids)
	for _, name := range []string{"opaque_a", "connection:warehouse", " warehouse", "unknown"} {
		_, err := resolver.Resolve(t.Context(), name, semanticmodel.Connection{Kind: "postgres"})
		require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	}
	require.Len(t, recorder.ids, 2, "unknown names must never consult an ID resolver or active fallback")
}

func TestMaterializerConnectionNamesFailClosedForMissingOrInvalidMapping(t *testing.T) {
	for _, ids := range []map[string]string{nil, {}, {"warehouse": ""}, {"warehouse": " wrong"}, {"warehouse": "bad/id"}} {
		m := &Module{}
		recorder := &namedConnectionRecorder{}
		key := candidateRuntimeBindingKey{candidateID: "candidate_1", projectID: "sales"}
		token := m.candidateRuntimeBindings.register(key, recorder)
		resolver := (&duckDBProjectMaterializer{module: m}).connectionResolver(analyticsmaterialization.Request{
			CandidateID: "candidate_1", Identity: projectgraph.ServingIdentity{ProjectID: "sales"}, ConnectionIDs: ids,
		})
		_, err := resolver.Resolve(t.Context(), "warehouse", semanticmodel.Connection{Kind: "postgres"})
		require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
		require.Empty(t, recorder.ids)
		m.candidateRuntimeBindings.remove(key, token)
	}
}

func TestConnectionNamesResolveActiveReleaseByCompiledResourceID(t *testing.T) {
	binding := activeTestBinding(t)
	evidence := binding.Evidence()
	versioned := &activeVersionedResolver{values: map[string]string{"token": "pinned-token"}}
	m := activeTestModule(binding, versioned, ActiveRuntimeBindingEvidence{
		BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID,
		ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision,
		ValidatedVersion: "secret-quack:v7", EndpointConfigHash: evidence.EndpointConfigHash,
	})
	resolver := (&duckDBProjectMaterializer{module: m}).connectionResolver(analyticsmaterialization.Request{
		ConnectionEvidenceServingStateID: "state_sales",
		Identity:                         projectgraph.ServingIdentity{ProjectID: "sales"}, Environment: "prod",
		ConnectionIDs: map[string]string{"warehouse": "quack"},
	})
	got, err := resolver.Resolve(t.Context(), "warehouse", semanticmodel.Connection{Kind: "quack"})
	require.NoError(t, err)
	require.Equal(t, "pinned-token", got.Auth["token"])
	_, err = resolver.Resolve(t.Context(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	require.Equal(t, 1, versioned.calls)
}
