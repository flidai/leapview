package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticRegistryContextRepository struct {
	access.Repository
	value access.SemanticRegistryContext
	calls int
}

func (r *semanticRegistryContextRepository) ReadSemanticRegistry(context.Context, string) (access.SemanticRegistryContext, error) {
	r.calls++
	return r.value, nil
}

func TestSemanticRegistryContextModuleScope(t *testing.T) {
	repo := &semanticRegistryContextRepository{value: access.SemanticRegistryContext{Control: access.AuthorizationControlRevision{InstanceID: "instance-a", ProjectID: "project-a", Revision: 1}, Registry: access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}}}
	m := &Module{instanceID: "instance-a", repository: func() (access.Repository, error) { return repo, nil }}
	if _, err := m.ReadSemanticRegistry(t.Context(), "instance-b"); err == nil || repo.calls != 0 {
		t.Fatal("foreign scope reached repository")
	}
	if _, err := m.ReadSemanticRegistry(t.Context(), "instance-a"); err != nil {
		t.Fatal(err)
	}
	repo.value.Control.InstanceID = "instance-b"
	if _, err := m.ReadSemanticRegistry(t.Context(), "instance-a"); err == nil {
		t.Fatal("foreign returned scope accepted")
	}
	repo.value.Control.InstanceID = "instance-a"
	repo.value.Registry.State.Revision = 0
	if _, err := m.ReadSemanticRegistry(t.Context(), "instance-a"); err == nil {
		t.Fatal("missing registry revision accepted")
	}
}
