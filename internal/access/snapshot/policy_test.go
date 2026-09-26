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

func TestRestrictToCurrentRoleBindingsRevokesWithoutImportingNewAuthority(t *testing.T) {
	project := testGraph(t)
	bob := mustSubject(t, access.SubjectKindPrincipal, "bob")
	charlie := mustSubject(t, access.SubjectKindPrincipal, "charlie")
	identity := testIdentity()
	viewer, err := access.NewTypedRoleBinding("binding_1", "viewer", bob, access.PermissionRoleViewer, identity.ProjectID)
	require.NoError(t, err)
	directPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, snapshotResourceRef(t, "dashboard_main", graph.KindDashboard))
	require.NoError(t, err)
	direct, err := NewTypedGrant("grant_1", "reader", mustSubject(t, access.SubjectKindPrincipal, "alice"), []access.PermissionPair{directPair})
	require.NoError(t, err)
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(identity, project, []RoleBinding{viewer}, []Grant{direct}, nil)
	require.NoError(t, err)
	resource, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, snapshotResourceRef(t, "dashboard_main", graph.KindDashboard))
	require.NoError(t, err)
	allowed, err := snapshot.AllowsTyped(bob, resource)
	require.NoError(t, err)
	require.True(t, allowed)

	newViewer, err := access.NewTypedRoleBinding("binding_2", "new viewer", charlie, access.PermissionRoleViewer, identity.ProjectID)
	require.NoError(t, err)
	restricted, err := snapshot.RestrictToCurrentRoleBindings([]RoleBinding{newViewer})
	require.NoError(t, err)
	require.Empty(t, restricted.RoleBindings())
	allowed, err = restricted.AllowsTyped(bob, resource)
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = restricted.AllowsTyped(charlie, resource)
	require.NoError(t, err)
	require.False(t, allowed)
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	allowed, err = restricted.AllowsTyped(alice, resource)
	require.NoError(t, err)
	require.True(t, allowed, "an independent durable grant remains effective")

	current := viewer
	current.Name = "renamed viewer"
	restricted, err = snapshot.RestrictToCurrentRoleBindings([]RoleBinding{current})
	require.NoError(t, err)
	require.Len(t, restricted.RoleBindings(), 1)
	require.Equal(t, viewer.Name, restricted.RoleBindings()[0].Name)
	current, err = access.NewTypedRoleBinding("binding_1", "project admin", bob, access.PermissionRoleProjectAdmin, identity.ProjectID)
	require.NoError(t, err)
	restricted, err = snapshot.RestrictToCurrentRoleBindings([]RoleBinding{current})
	require.NoError(t, err)
	require.Empty(t, restricted.RoleBindings(), "a changed role must await a new generation")
	current.PermissionRole = access.PermissionRoleViewer
	restricted, err = snapshot.RestrictToCurrentRoleBindings([]RoleBinding{current})
	require.Error(t, err, "invalid current role expansion must fail closed")
}

func TestRestrictToCurrentAuthorizationGrantsRevokesChangesAndDoesNotImportAdditions(t *testing.T) {
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"},
		{ID: "dashboard_other", Kind: graph.KindDashboard, Name: "other"},
	}, nil)
	require.NoError(t, err)
	identity := testIdentity()
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	bob := mustSubject(t, access.SubjectKindPrincipal, "bob")
	mainResource := snapshotResourceRef(t, "dashboard_main", graph.KindDashboard)
	otherResource := snapshotResourceRef(t, "dashboard_other", graph.KindDashboard)
	readMain, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, mainResource)
	require.NoError(t, err)
	updateMain, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, identity.ProjectID, mainResource)
	require.NoError(t, err)
	deleteMain, err := access.NewExactPermissionPair(access.ActionDashboardDelete, identity.ProjectID, mainResource)
	require.NoError(t, err)

	legacy := access.AuthorizationGrant{
		ID: "legacy-main-read", Name: "Legacy dashboard reader", Subject: alice,
		Resource: mainResource, Capability: access.CapabilityResourceRead,
	}
	typed := access.AuthorizationGrant{
		ID: "typed-main-reader", Name: "Typed dashboard reader", Subject: alice,
		Resource: mainResource, PermissionProfile: access.PermissionCatalogProfile,
		Permissions: []access.PermissionPair{readMain, updateMain},
	}
	legacyCanonical, err := access.NewCanonicalGrant(project, legacy.Subject, legacy.Resource, legacy.Capability)
	require.NoError(t, err)
	typedSnapshotGrant, err := NewTypedGrant(typed.ID, typed.Name, typed.Subject, typed.Permissions)
	require.NoError(t, err)
	captured, err := NewAuthorizationSnapshotWithRoleBindings(testIdentity(), project, nil, []Grant{
		{ID: legacy.ID, Name: legacy.Name, Canonical: legacyCanonical}, typedSnapshotGrant,
	}, nil)
	require.NoError(t, err)

	newResource, err := access.NewResourceRef("dashboard_next", graph.KindDashboard)
	require.NoError(t, err)
	newPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, newResource)
	require.NoError(t, err)
	added := access.AuthorizationGrant{
		ID: "new-dashboard-reader", Subject: bob, Resource: newResource,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{newPair},
	}
	currentTyped := typed
	currentTyped.Name = "renamed description"
	currentTyped.Permissions = []access.PermissionPair{updateMain, readMain}
	restricted, err := captured.RestrictToCurrentAuthorizationGrants([]access.AuthorizationGrant{legacy, currentTyped, added})
	require.NoError(t, err, "a newly staged resource that is absent from this generation must not break the old generation")
	require.Len(t, restricted.Grants(), 2)
	require.Equal(t, []string{legacy.ID, typed.ID}, []string{restricted.Grants()[0].ID, restricted.Grants()[1].ID})

	changedSubject := typed
	changedSubject.Subject = bob
	restricted, err = captured.RestrictToCurrentAuthorizationGrants([]access.AuthorizationGrant{legacy, changedSubject})
	require.NoError(t, err)
	require.Len(t, restricted.Grants(), 1)
	require.Equal(t, legacy.ID, restricted.Grants()[0].ID)

	changedResource := legacy
	changedResource.Resource = otherResource
	restricted, err = captured.RestrictToCurrentAuthorizationGrants([]access.AuthorizationGrant{changedResource, typed})
	require.NoError(t, err)
	require.Len(t, restricted.Grants(), 1)
	require.Equal(t, typed.ID, restricted.Grants()[0].ID)

	changedPermissions := typed
	changedPermissions.Permissions = []access.PermissionPair{readMain, deleteMain}
	restricted, err = captured.RestrictToCurrentAuthorizationGrants([]access.AuthorizationGrant{legacy, changedPermissions})
	require.NoError(t, err)
	require.Len(t, restricted.Grants(), 1)
	require.Equal(t, legacy.ID, restricted.Grants()[0].ID)

	restricted, err = captured.RestrictToCurrentAuthorizationGrants(nil)
	require.NoError(t, err)
	require.Empty(t, restricted.Grants(), "removed grants must stop taking effect on the next lease")
}

func TestRestrictToCurrentAuthorizationGrantsRejectsInvalidPolicyRows(t *testing.T) {
	project := testGraph(t)
	identity := testIdentity()
	resource := snapshotResourceRef(t, "dashboard_main", graph.KindDashboard)
	foreignPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, graph.ResourceID("project_other"), resource)
	require.NoError(t, err)
	foreign := access.AuthorizationGrant{
		ID: "foreign-grant", Subject: mustSubject(t, access.SubjectKindPrincipal, "alice"), Resource: resource,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{foreignPair},
	}
	captured, err := NewAuthorizationSnapshotWithRoleBindings(identity, project, nil, nil, nil)
	require.NoError(t, err)
	if _, err := captured.RestrictToCurrentAuthorizationGrants([]access.AuthorizationGrant{foreign}); err == nil {
		t.Fatal("accepted a current typed grant for a different project")
	}
	legacy := access.AuthorizationGrant{ID: "duplicate", Subject: mustSubject(t, access.SubjectKindPrincipal, "alice"), Resource: resource, Capability: access.CapabilityResourceRead}
	if _, err := captured.RestrictToCurrentAuthorizationGrants([]access.AuthorizationGrant{legacy, legacy}); err == nil {
		t.Fatal("accepted duplicate current grant identities")
	}
}

func snapshotResourceRef(t *testing.T, id string, kind graph.Kind) access.ResourceRef {
	t.Helper()
	resource, err := access.NewResourceRef(graph.ResourceID(id), kind)
	require.NoError(t, err)
	return resource
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
		{ProjectID: "project demo", Environment: "production", GenerationID: "generation_1"},
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

func TestAuthorizationSnapshotAllowsExactTypedActionTargetAndSubjectOnly(t *testing.T) {
	project := testGraph(t)
	identity := testIdentity()
	subject := mustSubject(t, access.SubjectKindPrincipal, "alice")
	resource := snapshotResourceRef(t, "dashboard_main", graph.KindDashboard)
	readPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, resource)
	require.NoError(t, err)
	grant, err := NewTypedGrant("grant_1", "reader", subject, []access.PermissionPair{readPair})
	require.NoError(t, err)
	snapshot, err := NewAuthorizationSnapshot(identity, project, []Grant{grant}, nil)
	require.NoError(t, err)
	allowed, err := snapshot.AllowsTyped(subject, readPair)
	require.NoError(t, err)
	require.True(t, allowed)
	updatePair, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, identity.ProjectID, resource)
	require.NoError(t, err)
	denied, err := snapshot.AllowsTyped(subject, updatePair)
	require.NoError(t, err)
	require.False(t, denied)
	modelRef, err := access.NewResourceRef("model_orders", graph.KindModel)
	require.NoError(t, err)
	modelReadPair, err := access.NewExactPermissionPair(access.ActionModelRead, identity.ProjectID, modelRef)
	require.NoError(t, err)
	denied, err = snapshot.AllowsTyped(subject, modelReadPair)
	require.NoError(t, err)
	require.False(t, denied)
	group, err := access.NewSubjectRef(access.SubjectKindGroup, "alice")
	require.NoError(t, err)
	denied, err = snapshot.AllowsTyped(group, readPair)
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

func TestAuthorizationSnapshotPreservesLegacyRoleBindingNameWhitespace(t *testing.T) {
	project := testGraph(t)
	binding := RoleBinding{
		ID: "binding_admin", Name: " Legacy administrator ",
		Subject: mustSubject(t, access.SubjectKindPrincipal, "alice"), Role: access.ProjectRoleAdmin,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin),
	}
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(testIdentity(), project, []RoleBinding{binding}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, snapshot.ValidateBound())
	require.Equal(t, binding.Name, snapshot.RoleBindings()[0].Name)
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
