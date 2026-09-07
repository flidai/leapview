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
		ServingStateID: "state_one", TargetID: "target:prod", SnapshotSealID: "seal_one", ProjectID: "project:test", Environment: "prod",
	}
	second := first
	second.ServingStateID = "state_two"
	second.SnapshotSealID = "seal_two"
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

func TestProjectResultPartitionScopesResultsByTargetButNotSnapshotSeal(t *testing.T) {
	base := analyticsruntime.ProjectRequest{
		TargetID: "target:prod", SnapshotSealID: "seal_one",
		ProjectID: "project:test", Environment: "prod",
	}
	changedSeal := base
	changedSeal.SnapshotSealID = "seal_two"
	first, err := projectResultPartition(base)
	require.NoError(t, err)
	second, err := projectResultPartition(changedSeal)
	require.NoError(t, err)
	require.Equal(t, projectResultCacheIdentity(first), projectResultCacheIdentity(second), "safe generation cutovers retain stable result partition")

	otherTarget := base
	otherTarget.TargetID = "target:other"
	third, err := projectResultPartition(otherTarget)
	require.NoError(t, err)
	require.NotEqual(t, projectResultCacheIdentity(first), projectResultCacheIdentity(third), "different targets must not share result partitions")
}

func TestProjectResultPartitionFailsClosedWithoutTarget(t *testing.T) {
	base := analyticsruntime.ProjectRequest{TargetID: "target:prod", ProjectID: "project:test", Environment: "prod"}
	for name, request := range map[string]analyticsruntime.ProjectRequest{
		"missing target":      func() analyticsruntime.ProjectRequest { value := base; value.TargetID = ""; return value }(),
		"noncanonical target": func() analyticsruntime.ProjectRequest { value := base; value.TargetID = " target:prod"; return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := projectResultPartition(request); err == nil {
				t.Fatal("projectResultPartition accepted incomplete cache provenance")
			}
		})
	}
}

func TestProjectResultPartitionAllowsPreSealRuntimeWithTarget(t *testing.T) {
	_, err := projectResultPartition(analyticsruntime.ProjectRequest{
		TargetID: "target:prod", ProjectID: "project:test", Environment: "prod",
	})
	require.NoError(t, err, "candidate materialization precedes snapshot seal creation")
}
