package module

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/stretchr/testify/require"
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

func TestProjectCacheIdentitiesSeparateStableResultsFromGenerationBytes(t *testing.T) {
	first := analyticsruntime.ProjectRequest{
		ServingStateID: "state_one", ProjectID: "project:test", Environment: "prod",
	}
	second := first
	second.ServingStateID = "state_two"
	firstPartition, err := projectResultPartition(first)
	require.NoError(t, err)
	secondPartition, err := projectResultPartition(second)
	require.NoError(t, err)
	require.Equal(t, projectResultCacheIdentity(firstPartition), projectResultCacheIdentity(secondPartition))
	require.NotEqual(t, projectRuntimeCacheIdentity(first), projectRuntimeCacheIdentity(second))

	candidate := second
	candidate.CandidateID = "candidate-one"
	candidatePartition, err := projectResultPartition(candidate)
	require.NoError(t, err)
	require.Equal(t, resultidentity.PartitionCandidate, candidatePartition.Kind())
	require.NotEqual(t, projectResultCacheIdentity(firstPartition), projectResultCacheIdentity(candidatePartition))
}
