package app

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestStoredDashboardReadRequiresExactAuthorityWithoutOpeningServingReads(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_draft_reads", "dev", "generation_draft_reads")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "published", Kind: projectgraph.KindDashboard, Name: "Published"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := access.NewResourceRef("stored_draft", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	other, err := access.NewResourceRef("other_draft", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, draft)
	if err != nil {
		t.Fatal(err)
	}
	update, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, identity.ProjectID, draft)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "draft-reader"}
	binding, err := access.NewTypedRoleBinding("draft-reader-role", "Draft reader", subject, access.PermissionRoleEditor, identity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{binding}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		resource access.ResourceRef
		project  projectgraph.ResourceID
		subjects []access.SubjectRef
		stored   bool
		token    []access.PermissionPair
		want     bool
	}{
		{name: "stored exact read", resource: draft, project: identity.ProjectID, subjects: []access.SubjectRef{subject}, stored: true, want: true},
		{name: "ordinary serving read remains closed", resource: draft, project: identity.ProjectID, subjects: []access.SubjectRef{subject}},
		{name: "token cannot read other dashboard", resource: other, project: identity.ProjectID, subjects: []access.SubjectRef{subject}, stored: true, token: []access.PermissionPair{read}},
		{name: "unassigned principal denied", resource: draft, project: identity.ProjectID, stored: true},
		{name: "other project denied", resource: draft, project: "different_project", subjects: []access.SubjectRef{subject}, stored: true},
		{name: "matching read token", resource: draft, project: identity.ProjectID, subjects: []access.SubjectRef{subject}, stored: true, token: []access.PermissionPair{read}, want: true},
		{name: "update token cannot read", resource: draft, project: identity.ProjectID, subjects: []access.SubjectRef{subject}, stored: true, token: []access.PermissionPair{update}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.token != nil {
				ctx = accessmodule.WithAPICredential(ctx, access.APICredential{
					Principal: access.Principal{ID: subject.ID},
					Token:     access.APIToken{ID: "draft-token", PrincipalID: subject.ID, PermissionProfile: access.PermissionCatalogProfile, Permissions: test.token},
				})
			}
			typed, allowed, err := typedPermissionDecisionForSnapshotWithDraft(ctx, subject.ID, test.project, []access.ResourceRef{test.resource}, func(access.ResourceRef) (access.Action, bool) {
				return access.ActionDashboardRead, true
			}, snapshot, test.subjects, test.stored)
			if err != nil || !typed || allowed != test.want {
				t.Fatalf("decision typed=%t allowed=%t err=%v; want allowed=%t", typed, allowed, err, test.want)
			}
		})
	}
}
