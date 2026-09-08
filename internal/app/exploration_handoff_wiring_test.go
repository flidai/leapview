package app

import "testing"

func TestExplorationHandoffSubjectsFailClosedWithoutAccessModule(t *testing.T) {
	for _, routes := range []*capabilityRoutes{nil, {}} {
		subjects, err := routes.explorationAuthorizationSubjects(t.Context(), "viewer")
		if err == nil || len(subjects) != 0 {
			t.Fatalf("unavailable authorization returned subjects=%v, err=%v", subjects, err)
		}
	}
}
