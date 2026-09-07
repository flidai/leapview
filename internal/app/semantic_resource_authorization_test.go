package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAuthorizeSemanticModelResourceRead(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	modelID := projectgraph.ResourceID("semantic_sales")
	resource, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		ctx         context.Context
		principalID string
		projectID   projectgraph.ResourceID
		resource    access.ResourceRef
		capability  access.Capability
		runtime     canonicalRuntimeHost
		access      canonicalAccessModule
		want        bool
		wantErr     bool
	}{
		{
			name:        "trusted development read allow",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			principalID: "dev",
			projectID:   projectID,
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
			want:        true,
		},
		{
			name:        "ordinary deny",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "alice"}),
			principalID: "alice",
			projectID:   projectID,
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "alice"}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "alice"}}},
		},
		{
			name:        "ordinary explicit grant allow",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "alice"}),
			principalID: "alice",
			projectID:   projectID,
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, true),
			access:      tusAccess{principal: accessmodule.Principal{ID: "alice"}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "alice"}}},
			want:        true,
		},
		{
			name:        "missing principal",
			ctx:         context.Background(),
			principalID: "dev",
			projectID:   projectID,
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
		},
		{
			name:        "mismatched principal",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "other", DevBypass: true}),
			principalID: "dev",
			projectID:   projectID,
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "other", DevBypass: true}, ok: true},
		},
		{
			name:       "empty principal",
			ctx:        accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			projectID:  projectID,
			resource:   resource,
			capability: access.CapabilityResourceRead,
			runtime:    semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:     tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
		},
		{
			name:        "invalid project",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			principalID: "dev",
			projectID:   " ",
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
			wantErr:     true,
		},
		{
			name:        "cross project",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			principalID: "dev",
			projectID:   projectgraph.ResourceID("project_other"),
			resource:    resource,
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
			wantErr:     true,
		},
		{
			name:        "invalid resource",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			principalID: "dev",
			projectID:   projectID,
			resource:    access.ResourceRef{},
			capability:  access.CapabilityResourceRead,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
			wantErr:     true,
		},
		{
			name:        "wrong resource kind",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			principalID: "dev",
			projectID:   projectID,
			resource: func() access.ResourceRef {
				value, _ := access.NewResourceRef(modelID, projectgraph.KindModel)
				return value
			}(),
			capability: access.CapabilityResourceRead,
			runtime:    semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:     tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
		},
		{
			name:        "wrong capability",
			ctx:         accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}),
			principalID: "dev",
			projectID:   projectID,
			resource:    resource,
			capability:  access.CapabilityResourceEdit,
			runtime:     semanticResourceRuntime(t, projectID, identity, modelID, false),
			access:      tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := authorizeSemanticModelResourceRead(test.ctx, test.access, test.runtime, test.principalID, test.projectID, test.resource, test.capability)
			if (err != nil) != test.wantErr {
				t.Fatalf("authorizeSemanticModelResourceRead() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("authorizeSemanticModelResourceRead() = %v, want %v", got, test.want)
			}
		})
	}
}

func semanticResourceRuntime(t *testing.T, projectID projectgraph.ResourceID, identity projectgraph.ServingIdentity, modelID projectgraph.ResourceID, grant bool) canonicalRuntimeHost {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: modelID, Kind: projectgraph.KindSemanticModel, Name: "Sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var grants []accesssnapshot.Grant
	if grant {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
		if err != nil {
			t.Fatal(err)
		}
		resource, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, subject, resource, access.CapabilityResourceRead)
		if err != nil {
			t.Fatal(err)
		}
		grants = []accesssnapshot.Grant{{ID: "grant:semantic-read", Name: "semantic_read", Canonical: canonical}}
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: snapshot}}
}
