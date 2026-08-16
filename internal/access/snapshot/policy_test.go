package snapshot

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
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
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(testIdentity(), project, []RoleBinding{{
		ID: "binding_1", Name: "viewer", Subject: mustSubject(t, access.SubjectKindPrincipal, "alice"), Role: access.ProjectRoleViewer,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}}, []Grant{{ID: "grant_1", Name: "reader", Canonical: testGrant(t, project)}}, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	decoded, err := Decode(encoded, project)
	require.NoError(t, err)
	require.Equal(t, snapshot.Identity(), decoded.Identity())
	require.Equal(t, snapshot.Grants()[0].Canonical.GrantKey(), decoded.Grants()[0].Canonical.GrantKey())
	require.Len(t, decoded.RoleBindings(), 1)
	require.Equal(t, access.ProjectRoleViewer, decoded.RoleBindings()[0].Role)
	digest, err := snapshot.Digest()
	require.NoError(t, err)
	require.NotEmpty(t, digest)
}

func mustSubject(t *testing.T, kind access.SubjectKind, id string) access.SubjectRef {
	t.Helper()
	subject, err := access.NewSubjectRef(kind, id)
	require.NoError(t, err)
	return subject
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

func TestAuthorizationSnapshotEffectiveCapabilitiesProjectsDirectAndGroupGrants(t *testing.T) {
	project := testGraph(t)
	dashboard, err := access.NewResourceRef("dashboard_main", graph.KindDashboard)
	require.NoError(t, err)
	model, err := access.NewResourceRef("model_orders", graph.KindModel)
	require.NoError(t, err)
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	sales := mustSubject(t, access.SubjectKindGroup, "sales")
	dashboardGrant, err := access.NewCanonicalGrant(project, alice, dashboard, access.CapabilityResourceRead)
	require.NoError(t, err)
	modelGrant, err := access.NewCanonicalGrant(project, sales, model, access.CapabilityResourceUse)
	require.NoError(t, err)
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{
		{ID: "grant_dashboard", Canonical: dashboardGrant},
		{ID: "grant_model", Canonical: modelGrant},
	}, nil)
	require.NoError(t, err)

	capabilities, err := snapshot.EffectiveCapabilities([]access.SubjectRef{alice})
	require.NoError(t, err)
	require.Equal(t, []access.Capability{access.CapabilityResourceRead}, capabilities)

	capabilities, err = snapshot.EffectiveCapabilities([]access.SubjectRef{alice, sales})
	require.NoError(t, err)
	require.Equal(t, []access.Capability{access.CapabilityResourceUse, access.CapabilityResourceRead}, capabilities)

	capabilities, err = snapshot.EffectiveCapabilities([]access.SubjectRef{mustSubject(t, access.SubjectKindPrincipal, "nobody")})
	require.NoError(t, err)
	require.Empty(t, capabilities)
}

func TestAuthorizationSnapshotEffectiveCapabilitiesFailsClosed(t *testing.T) {
	_, err := (AuthorizationSnapshot{}).EffectiveCapabilities(nil)
	require.Error(t, err)

	project := testGraph(t)
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, nil, nil)
	require.NoError(t, err)
	invalid := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: ""}
	_, err = snapshot.EffectiveCapabilities([]access.SubjectRef{invalid})
	require.Error(t, err)
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
	require.Equal(t, "a", normalized.Grants()[0].ID)
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
	input := []Grant{{ID: "grant_1", Canonical: grant}}
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, input, nil)
	require.NoError(t, err)
	input[0].Canonical = access.CanonicalGrant{}
	input[0].ID = "mutated"
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "grant_1")
	// The getter is defensive as well, so callers cannot mutate installed state.
	grants := snapshot.Grants()
	grants[0].Canonical = access.CanonicalGrant{}
	_, err = json.Marshal(snapshot)
	require.NoError(t, err)
}

func TestAuthorizationSnapshotPolicyGetterDeepCopiesCompiledTree(t *testing.T) {
	project := testGraph(t)
	resource, err := access.NewResourceRef("model_orders", graph.KindModel)
	require.NoError(t, err)
	compiled, err := accesspolicy.Compile("policy_1", accesspolicy.TypeRowFilter, `{"filters":[{"groups":[{"filters":[{"field":"tenant_id","operator":"in","values":["acme"]}]}]}]}`)
	require.NoError(t, err)
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, nil, []DataPolicy{{
		ID: "policy_1", Resource: resource, PolicyType: accesspolicy.TypeRowFilter,
		ExpressionJSON: `{"filters":[{"groups":[{"filters":[{"field":"tenant_id","operator":"in","values":["acme"]}]}]}]}`,
		Compiled:       compiled,
	}})
	require.NoError(t, err)
	policies := snapshot.DataPolicies()
	require.Len(t, policies, 1)
	policies[0].Compiled.RowFilter.Filters[0].Groups[0].Filters[0].Values[0] = "mutated"
	policies[0].Compiled.RowFilter.Filters[0].Groups[0].Filters = append(policies[0].Compiled.RowFilter.Filters[0].Groups[0].Filters, accesspolicy.Filter{Field: "other"})
	second := snapshot.DataPolicies()
	require.Len(t, second[0].Compiled.RowFilter.Filters[0].Groups[0].Filters, 1)
	require.Equal(t, "acme", second[0].Compiled.RowFilter.Filters[0].Groups[0].Filters[0].Values[0])
}

func TestAuthorizationSnapshotAllowsExactKindCapabilityAndSubjectOnly(t *testing.T) {
	project := testGraph(t)
	grant := testGrant(t, project)
	snapshot, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{{ID: "grant_1", Canonical: grant}}, nil)
	require.NoError(t, err)
	subject := grant.Subject()
	resource := grant.Resource()
	allowed, err := snapshot.Allows(subject, resource, access.CapabilityResourceRead)
	require.NoError(t, err)
	require.True(t, allowed)
	denied, err := snapshot.Allows(subject, resource, access.CapabilityResourceEdit)
	require.NoError(t, err)
	require.False(t, denied)
	modelRef, err := access.NewResourceRef("model_orders", graph.KindModel)
	require.NoError(t, err)
	denied, err = snapshot.Allows(subject, modelRef, access.CapabilityResourceRead)
	require.NoError(t, err)
	require.False(t, denied)
	group, err := access.NewSubjectRef(access.SubjectKindGroup, "alice")
	require.NoError(t, err)
	denied, err = snapshot.Allows(group, resource, access.CapabilityResourceRead)
	require.NoError(t, err)
	require.False(t, denied)
}

func TestAuthorizationSnapshotRoleBindingCapturesDefensiveCapabilityBundle(t *testing.T) {
	project := testGraph(t)
	bundle := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(testIdentity(), project, []RoleBinding{{
		ID: "binding_admin", Subject: mustSubject(t, access.SubjectKindPrincipal, "alice"), Role: access.ProjectRoleAdmin, Capabilities: bundle,
	}}, nil, nil)
	require.NoError(t, err)
	bundle[0] = access.CapabilityResourceUse
	bindings := snapshot.RoleBindings()
	require.Equal(t, access.CapabilityProjectAdmin, bindings[0].Capabilities[0])
	bindings[0].Capabilities[0] = access.CapabilityResourceUse
	require.Equal(t, access.CapabilityProjectAdmin, snapshot.RoleBindings()[0].Capabilities[0])
}

func TestAuthorizationSnapshotRejectsRoleBundleDrift(t *testing.T) {
	project := testGraph(t)
	bundle := access.ProjectRoleCapabilities(access.ProjectRoleViewer)
	bundle = append(bundle, access.CapabilityResourceEdit)
	_, err := NewAuthorizationSnapshotWithRoleBindings(testIdentity(), project, []RoleBinding{{
		ID: "binding_viewer", Subject: mustSubject(t, access.SubjectKindPrincipal, "alice"), Role: access.ProjectRoleViewer, Capabilities: bundle,
	}}, nil, nil)
	require.Error(t, err)
}
