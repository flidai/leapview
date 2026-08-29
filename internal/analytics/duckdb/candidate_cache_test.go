package duckdb

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func TestProjectQueryCachePartitionSeparatesCandidateSecurityBoundaries(t *testing.T) {
	production, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "sales", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionCandidate, ProjectID: "sales", Environment: "prod", CandidateID: "cand_1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(production.Canonical()) == string(candidate.Canonical()) {
		t.Fatal("production and candidate cache partitions collided")
	}
}
