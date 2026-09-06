package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type controlRevisionRepository struct {
	access.Repository
	value access.AuthorizationControlRevision
	err   error
	calls int
}

func (r *controlRevisionRepository) ReadAuthorizationControlRevision(context.Context, string) (access.AuthorizationControlRevision, error) {
	r.calls++
	return r.value, r.err
}

func TestAuthorizationControlRevisionModuleFailsClosed(t *testing.T) {
	for _, name := range []string{"valid", "wrong returned instance", "wrong requested instance", "missing revision", "repository error", "unsupported repository"} {
		t.Run(name, func(t *testing.T) {
			repo := &controlRevisionRepository{value: access.AuthorizationControlRevision{InstanceID: "instance-a", ProjectID: "project-a", Revision: 3}}
			var repository access.Repository = repo
			requested := "instance-a"
			switch name {
			case "wrong returned instance":
				repo.value.InstanceID = "instance-b"
			case "wrong requested instance":
				requested = "instance-b"
			case "missing revision":
				repo.value.Revision = 0
			case "repository error":
				repo.err = errors.New("unavailable")
			case "unsupported repository":
				repository = &semanticAttributeResolutionRepository{}
			}
			m, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
			if err != nil {
				t.Fatal(err)
			}
			m.instanceID = "instance-a"
			got, err := m.ReadAuthorizationControlRevision(t.Context(), requested)
			if name == "valid" {
				if err != nil || got != repo.value {
					t.Fatalf("got=%#v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid authority accepted")
			}
			if name == "wrong requested instance" && repo.calls != 0 {
				t.Fatal("cross-instance request reached repository")
			}
		})
	}
}
