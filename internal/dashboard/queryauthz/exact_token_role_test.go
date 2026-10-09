package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

// An exact token may attenuate an IncludeFuture role to one model. Neither
// the role's broader selector nor independent prerequisite authority may be
// discarded or imported while checking that exact request.
func TestCanonicalExactTokenUsesProjectRoleWithoutWidening(t *testing.T) {
	graph, identity, semantic, _, _ := canonicalGraph(t)
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	binding, err := access.NewTypedRoleBinding("alice-explorer", "explorer", subject, access.PermissionRoleExplorer, canonicalProject)
	require.NoError(t, err)
	queryPair, err := access.NewExactPermissionPair(access.ActionSemanticQuery, canonicalProject, semantic)
	require.NoError(t, err)
	validPairs, err := access.RequiredPermissionPairs(queryPair)
	require.NoError(t, err)
	read, err := access.NewExactPermissionPair(access.ActionSemanticRead, canonicalProject, semantic)
	require.NoError(t, err)
	otherModel, err := access.NewResourceRef("semantic_other", projectgraph.KindSemanticModel)
	require.NoError(t, err)
	otherPair, err := access.NewExactPermissionPair(access.ActionSemanticQuery, canonicalProject, otherModel)
	require.NoError(t, err)
	otherPairs, err := access.RequiredPermissionPairs(otherPair)
	require.NoError(t, err)
	otherProject, err := access.NewExactPermissionPair(access.ActionSemanticQuery, "project:other", semantic)
	require.NoError(t, err)
	otherProjectPairs, err := access.RequiredPermissionPairs(otherProject)
	require.NoError(t, err)
	queryOnlyGrant, err := accesssnapshot.NewTypedPermissionGrant("query-only", "query only", subject, queryPair)
	require.NoError(t, err)
	malformedPair := queryPair
	malformedPair.Profile = "invalid-profile"
	for _, test := range []struct {
		name           string
		pairs          []access.PermissionPair
		profile        string
		removeRole     bool
		queryOnlyGrant bool
		allowed        bool
	}{
		{name: "exact token with project role", pairs: validPairs, profile: access.PermissionCatalogProfile, allowed: true},
		{name: "another resource", pairs: otherPairs, profile: access.PermissionCatalogProfile},
		{name: "another project", pairs: otherProjectPairs, profile: access.PermissionCatalogProfile},
		{name: "metadata action", pairs: []access.PermissionPair{read}, profile: access.PermissionCatalogProfile},
		{name: "missing token prerequisite", pairs: []access.PermissionPair{queryPair}, profile: access.PermissionCatalogProfile},
		{name: "revoked principal role", pairs: validPairs, profile: access.PermissionCatalogProfile, removeRole: true},
		{name: "missing principal prerequisite", pairs: validPairs, profile: access.PermissionCatalogProfile, removeRole: true, queryOnlyGrant: true},
		{name: "omitted token pairs", profile: access.PermissionCatalogProfile},
		{name: "empty token pairs", pairs: []access.PermissionPair{}, profile: access.PermissionCatalogProfile},
		{name: "wrong token profile", pairs: validPairs, profile: "invalid-profile"},
		{name: "malformed token pair", pairs: []access.PermissionPair{malformedPair}, profile: access.PermissionCatalogProfile},
	} {
		t.Run(test.name, func(t *testing.T) {
			bindings := []access.RoleBinding{binding}
			if test.removeRole {
				bindings = nil
			}
			var grants []accesssnapshot.Grant
			if test.queryOnlyGrant {
				grants = []accesssnapshot.Grant{queryOnlyGrant}
			}
			snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, bindings, grants, nil)
			require.NoError(t, err)
			metrics := New(canonicalMetrics{model: &semanticmodel.Model{Name: "sales"}}, Options{
				SnapshotFromContext:  func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
				SubjectsFromContext:  func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{subject}, nil },
				PrincipalFromContext: func(context.Context) (Principal, bool) { return Principal{ID: subject.ID}, true },
				CredentialFromContext: func(context.Context) (access.APICredential, bool) {
					return access.APICredential{Token: access.APIToken{PermissionProfile: test.profile, Permissions: test.pairs}}, true
				},
			})
			request := dataquery.Query{ProjectID: canonicalProject, Surface: dataquery.SurfaceAPI, ModelID: semantic.ID().String(), Kind: dataquery.KindSemanticRows}
			governed, _, err := metrics.GovernDataQuery(t.Context(), request)
			if test.allowed {
				require.NoError(t, err, "exact credential and broad principal role both authorize this model")
				require.Equal(t, subject.ID, governed.PrincipalID)
				require.NotEmpty(t, governed.EffectivePolicyFingerprint)
				return
			}
			var denied DeniedError
			require.True(t, errors.As(err, &denied), "expected authorization denial: %v", err)
			require.Equal(t, access.ActionSemanticQuery, denied.Action)
		})
	}
}
