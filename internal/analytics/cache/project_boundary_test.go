package cache

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func TestProjectBoundaryCacheKeysDoNotCollide(t *testing.T) {
	seen := map[string]bool{}
	for _, input := range []resultidentity.PartitionInput{
		{Kind: resultidentity.PartitionProduction, TargetID: "instance_a", ProjectID: "project_a", Environment: "prod"},
		{Kind: resultidentity.PartitionProduction, TargetID: "instance_a", ProjectID: "project_b", Environment: "prod"},
		{Kind: resultidentity.PartitionProduction, TargetID: "instance_a", ProjectID: "project_a", Environment: "dev"},
		{Kind: resultidentity.PartitionProduction, TargetID: "instance_b", ProjectID: "project_a", Environment: "prod"},
		{Kind: resultidentity.PartitionCandidate, TargetID: "instance_a", ProjectID: "project_a", Environment: "prod", CandidateID: "candidate_a"},
		{Kind: resultidentity.PartitionCandidate, TargetID: "instance_a", ProjectID: "project_a", Environment: "prod", CandidateID: "candidate_b"},
	} {
		partition, err := resultidentity.NewPartition(input)
		if err != nil {
			t.Fatal(err)
		}
		key, err := NewKeyFromDigests(partition, digest('a'), digest('b'), digest('c'))
		if err != nil {
			t.Fatal(err)
		}
		if seen[key.Digest()] {
			t.Fatal("identical query/data/policy collided across serving partitions")
		}
		seen[key.Digest()] = true
	}
}

func TestProjectBoundaryCacheRejectsMissingScope(t *testing.T) {
	for _, input := range []resultidentity.PartitionInput{
		{Kind: resultidentity.PartitionProduction, TargetID: "instance_a", Environment: "prod"},
		{Kind: resultidentity.PartitionProduction, ProjectID: "project_a", Environment: "prod"},
		{Kind: resultidentity.PartitionProduction, TargetID: "instance_a", ProjectID: "project_a"},
	} {
		if _, err := resultidentity.NewPartition(input); err == nil {
			t.Fatalf("accepted incomplete cache scope: %#v", input)
		}
	}
	if _, err := NewKeyFromDigests(resultidentity.Partition{}, digest('a'), digest('b'), digest('c')); err == nil {
		t.Fatal("accepted an unvalidated zero partition")
	}
}
