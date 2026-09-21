package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type permissionOptionAuthorityFake struct {
	admin    bool
	subjects []access.SubjectRef
}

func (f permissionOptionAuthorityFake) IsPlatformAdmin(context.Context, string) (bool, error) {
	return f.admin, nil
}

func (f permissionOptionAuthorityFake) AuthorizationSubjects(context.Context, string) ([]access.SubjectRef, error) {
	return append([]access.SubjectRef(nil), f.subjects...), nil
}

func TestCurrentEffectivePermissionOptionsCombinesProjectAndInstanceAuthority(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}
	binding, err := access.NewTypedRoleBinding("release-operator", "Release operator", subject, access.PermissionRoleReleaseOperator, identity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{binding}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	options, err := currentEffectivePermissionOptions(t.Context(), permissionOptionAuthorityFake{admin: true, subjects: []access.SubjectRef{subject}}, subject.ID, "instance_a", func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
		return snapshot, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[access.Action]bool{access.ActionDeliveryBuild: false, access.ActionPlatformAccessManage: false}
	for _, option := range options {
		if _, ok := want[option.Action]; ok {
			want[option.Action] = true
		}
	}
	for action, found := range want {
		if !found {
			t.Fatalf("permission options omitted %q: %#v", action, options)
		}
	}
}

func TestCurrentEffectivePermissionOptionsKeepsPlatformAuthorityWithoutActiveProject(t *testing.T) {
	options, err := currentEffectivePermissionOptions(t.Context(), permissionOptionAuthorityFake{admin: true}, "principal-1", "instance_a", func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
		return accesssnapshot.AuthorizationSnapshot{}, errors.New("no active serving generation")
	})
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewInstancePermissionPair(access.ActionPlatformAccessManage, "instance_a")
	if err != nil {
		t.Fatal(err)
	}
	if !access.PermissionSetAllows(options, manage) {
		t.Fatalf("platform permission options = %#v, want platform.access.manage", options)
	}
}
