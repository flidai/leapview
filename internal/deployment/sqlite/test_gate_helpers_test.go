package sqlite

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/release"
)

func sqliteResolvedInputs(t *testing.T, plan deployment.DeliveryPlan, candidateID string) deployment.DeliveryResolvedBuildInputs {
	t.Helper()
	evidence, err := (release.GateEvidence{
		Version: 1, CandidateID: candidateID, SourceDigest: plan.SourceDigest,
		BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test",
		Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 15, 0, 0, 0, time.UTC),
		Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000},
	}).Canonical()
	if err != nil {
		t.Fatalf("canonical sqlite gate evidence: %v", err)
	}
	return deployment.DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: &evidence}
}
