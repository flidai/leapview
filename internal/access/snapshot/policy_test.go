package snapshot

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func testGraph(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_demo", Kind: graph.KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main", Metadata: graph.Metadata{Domain: "sales"}},
		{ID: "model_orders", Kind: graph.KindModel, Name: "orders"},
	}, nil)
	require.NoError(t, err)
	return project
}

func testIdentity() graph.ServingIdentity {
	return graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_1"}
}

func testGrant(t *testing.T, project graph.ProjectGraph) access.CanonicalGrant {
	t.Helper()
	resource, err := access.NewResourceRef("dashboard_main", graph.KindDashboard)
	require.NoError(t, err)
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
	require.NoError(t, err)
	grant, err := access.NewCanonicalGrant(project, subject, resource, access.CapabilityResourceRead)
	require.NoError(t, err)
	return grant
}

func TestAuthorizationSnapshotRoundTripAndDigest(t *testing.T) {
	project := testGraph(t)
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{{ID: "grant_1", Name: "reader", Canonical: testGrant(t, project)}}, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	decoded, err := Decode(encoded, project)
	require.NoError(t, err)
	require.Equal(t, snapshot.Identity, decoded.Identity)
	require.Equal(t, snapshot.Grants[0].Canonical.GrantKey(), decoded.Grants[0].Canonical.GrantKey())
	digest, err := snapshot.Digest()
	require.NoError(t, err)
	require.NotEmpty(t, digest)
}

func TestAuthorizationSnapshotRejectsWorkspaceDomainAndPathFields(t *testing.T) {
	project := testGraph(t)
	for _, value := range []string{
		`{"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"grants":[{"id":"g","subject":{"kind":"principal","id":"alice"},"resource":{"id":"dashboard_main","kind":"dashboard","workspace":"sales","domain":"finance","path":"legacy"},"capability":"RESOURCE_READ"}]}`,
		`{"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"grants":[{"id":"g","subject":{"kind":"principal","id":"alice"},"resource":{"id":"dashboard_main","kind":"dashboard"},"capability":"RESOURCE_READ","workspaceId":"sales"}]}`,
	} {
		if _, err := Decode([]byte(value), project); err == nil {
			t.Fatalf("snapshot accepted legacy authorization identity: %s", value)
		}
	}
}

func TestAuthorizationSnapshotRejectsWrongKindAndUnboundGraph(t *testing.T) {
	project := testGraph(t)
	wrongKind := []byte(`{"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"grants":[{"id":"g","subject":{"kind":"principal","id":"alice"},"resource":{"id":"model_orders","kind":"dashboard"},"capability":"RESOURCE_READ"}]}`)
	if _, err := Decode(wrongKind, project); !errors.Is(err, access.ErrResourceKindMismatch) {
		t.Fatalf("wrong-kind snapshot error = %v", err)
	}
	withoutGraph := []byte(`{"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"grants":[{"id":"g","subject":{"kind":"principal","id":"alice"},"resource":{"id":"unknown","kind":"dashboard"},"capability":"RESOURCE_READ"}]}`)
	if _, err := Decode(withoutGraph, project); !errors.Is(err, access.ErrResourceNotFound) {
		t.Fatalf("unbound resource snapshot error = %v", err)
	}
}

func TestAuthorizationSnapshotRejectsDuplicateAndTrailingJSON(t *testing.T) {
	project := testGraph(t)
	duplicate := []byte(`{"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"}}`)
	if _, err := Decode(duplicate, project); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	trailing := []byte(`{"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"}} {}`)
	if _, err := Decode(trailing, project); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestAuthorizationSnapshotRejectsDuplicateIDsAndCanonicalKeys(t *testing.T) {
	project := testGraph(t)
	grant := testGrant(t, project)
	_, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{
		{ID: "g", Canonical: grant},
		{ID: "g", Canonical: grant},
	}, nil)
	require.Error(t, err)

	otherSubject, err := access.NewSubjectRef(access.SubjectKindGroup, "sales")
	require.NoError(t, err)
	otherGrant, err := access.NewCanonicalGrant(project, otherSubject, grant.Resource(), grant.Capability())
	require.NoError(t, err)
	normalized, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{
		{ID: "z", Canonical: otherGrant},
		{ID: "a", Canonical: grant},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "a", normalized.Grants[0].ID)
}

func TestAuthorizationSnapshotRejectsMalformedServingIdentity(t *testing.T) {
	project := testGraph(t)
	for _, identity := range []graph.ServingIdentity{
		{ProjectID: "project_demo", Environment: "", GenerationID: "generation_1"},
		{ProjectID: "project_demo", Environment: "prod/uction", GenerationID: "generation_1"},
		{ProjectID: "project_demo", Environment: "production", GenerationID: ""},
		{ProjectID: "other_project", Environment: "production", GenerationID: "generation_1"},
	} {
		_, err := NewAuthorizationSnapshot(identity, project, nil, nil)
		require.Error(t, err)
	}
}

func TestAuthorizationSnapshotMarshalRejectsPostConstructionMutation(t *testing.T) {
	project := testGraph(t)
	grant := testGrant(t, project)
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{{ID: "grant_1", Canonical: grant}}, nil)
	require.NoError(t, err)
	// The exported slice is intentionally tolerated for source compatibility,
	// but serialization must never emit a value that was mutated after install.
	snapshot.Grants[0].Canonical = access.CanonicalGrant{}
	_, err = json.Marshal(snapshot)
	require.ErrorIs(t, err, access.ErrUnboundCanonicalGrant)

	snapshot, err = NewAuthorizationSnapshot(testIdentity(), project, []Grant{{ID: "grant_1", Canonical: grant}}, nil)
	require.NoError(t, err)
	snapshot.Identity.Environment = "prod/uction"
	_, err = snapshot.Digest()
	require.Error(t, err)
}
