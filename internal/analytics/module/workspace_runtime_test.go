package module

import (
	"testing"

	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
)

func TestProjectRuntimeCacheIdentitySeparatesCandidateFromActiveState(t *testing.T) {
	active := projectRuntimeCacheIdentity(analyticsruntime.ProjectRequest{
		ServingStateID: "state_sales",
	})
	candidate := projectRuntimeCacheIdentity(analyticsruntime.ProjectRequest{
		ServingStateID: "state_sales",
		CandidateID:    "cand_1",
	})
	if active != "state_sales" {
		t.Fatalf("active cache identity = %q", active)
	}
	if candidate == "" || candidate == active {
		t.Fatalf(
			"candidate cache identity = %q, active = %q",
			candidate,
			active,
		)
	}
}
