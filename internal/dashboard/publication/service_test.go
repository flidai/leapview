package publication

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type serviceRepository struct {
	row    Publication
	action Action
}

func (r *serviceRepository) GetByPublicID(context.Context, string) (Publication, error) {
	return r.row, nil
}
func (r *serviceRepository) Suspend(context.Context, projectgraph.ResourceID, string, string, int64) (Publication, error) {
	r.action = ActionSuspend
	return r.row, nil
}
func (r *serviceRepository) Resume(context.Context, projectgraph.ResourceID, string, string, int64) (Publication, error) {
	r.action = ActionResume
	return r.row, nil
}
func (r *serviceRepository) Rotate(context.Context, projectgraph.ResourceID, string, string, int64) (Publication, error) {
	r.action = ActionRotate
	return r.row, nil
}

func TestServiceMutationsRevokeOnlyInvalidatingActions(t *testing.T) {
	for _, action := range []Action{ActionSuspend, ActionResume, ActionRotate} {
		t.Run(string(action), func(t *testing.T) {
			repo := &serviceRepository{row: Publication{ID: "publication", Configured: true, ServingStateID: "state"}}
			revocations := 0
			service := NewService(repo, func(id string) {
				if id != repo.row.ID {
					t.Fatalf("revoked id = %q", id)
				}
				revocations++
			})
			if _, err := service.Mutate(context.Background(), "workspace", "name", "actor", action, 1); err != nil {
				t.Fatal(err)
			}
			want := 0
			if action == ActionSuspend || action == ActionRotate {
				want = 1
			}
			if revocations != want || repo.action != action {
				t.Fatalf("action = %q revocations = %d, want %d", repo.action, revocations, want)
			}
		})
	}
}
