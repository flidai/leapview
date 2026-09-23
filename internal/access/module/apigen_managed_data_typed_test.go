package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestManagedDataStagingRequiresTypedCreateAuthorityForMissingConnection(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	connectionID := projectgraph.ResourceID("connection_new")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewExactPermissionPair(access.ActionConnectionManage, projectID, connection)
	if err != nil {
		t.Fatal(err)
	}
	create, err := access.NewProjectPermissionPair(access.ActionConnectionCreate, projectID)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewProjectPermissionPair(access.ActionProjectAccessRead, projectID)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := access.NewSubjectRef(access.SubjectKindPrincipal, "publisher")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		pairs []access.PermissionPair
		want  bool
	}{
		{name: "create only", pairs: []access.PermissionPair{create}, want: true},
		{name: "other project action", pairs: []access.PermissionPair{read}},
		{name: "no typed authority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grants := []accesssnapshot.Grant(nil)
			if len(tc.pairs) != 0 {
				grant, err := accesssnapshot.NewTypedGrant("grant_publisher", "publisher", principal, tc.pairs)
				if err != nil {
					t.Fatal(err)
				}
				grants = append(grants, grant)
			}
			snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
			if err != nil {
				t.Fatal(err)
			}
			authorizer := &APIGenAuthorizer{
				module:  browserGuardModule(browserGuardRepository{}, Principal{}, false),
				runtime: apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
			}
			found, allowed, err := authorizer.authorizeManagedDataConnection(context.Background(), "publisher", projectID, connection, []access.PermissionPair{manage}, []access.PermissionPair{create})
			if err != nil || found || allowed != tc.want {
				t.Fatalf("found=%t allowed=%t error=%v, want missing allowed=%t", found, allowed, err, tc.want)
			}
		})
	}
}
