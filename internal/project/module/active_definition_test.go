package module

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type projectDefinitionRuntimeStub struct {
	definition projectmanifest.Project
}

func (r projectDefinitionRuntimeStub) Close() error { return nil }
func (r projectDefinitionRuntimeStub) ProjectManifest() projectmanifest.Project {
	return r.definition
}

type projectDefinitionProviderStub struct {
	lease *projectDefinitionLeaseStub
}

func (p *projectDefinitionProviderStub) Acquire(context.Context) (projectruntime.Lease, error) {
	p.lease = &projectDefinitionLeaseStub{runtime: projectDefinitionRuntimeStub{definition: projectmanifest.Project{ID: "project:demo", Title: "Demo"}}}
	return p.lease, nil
}

type projectDefinitionLeaseStub struct {
	runtime  projectruntime.Runtime
	released bool
}

func (l *projectDefinitionLeaseStub) Runtime() projectruntime.Runtime { return l.runtime }
func (l *projectDefinitionLeaseStub) Identity() projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{}
}
func (l *projectDefinitionLeaseStub) Release() { l.released = true }

func TestActiveProjectDefinitionReaderPinsAndReleasesRuntime(t *testing.T) {
	provider := &projectDefinitionProviderStub{}
	reader := NewActiveProjectDefinitionReader(provider)
	definition, err := reader.ProjectDefinition(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != "project:demo" || definition.Title != "Demo" {
		t.Fatalf("definition = %#v", definition)
	}
	if provider.lease == nil || !provider.lease.released {
		t.Fatal("active runtime lease was not released")
	}
}
