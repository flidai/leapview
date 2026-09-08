package release

import (
	"math"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func validGateEvidenceForTest() GateEvidence {
	return GateEvidence{
		Version: 1, CandidateID: "candidate-1", SourceDigest: "sha256:" + strings.Repeat("a", 64),
		BindingGeneration: "sha256:" + strings.Repeat("b", 64), RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1",
		Bounds: GateBounds{MaxRows: 10, MaxQueries: 4, MaxMillis: 100}, Outcome: GateSuccess,
		EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Queries: 1, ObservedRows: 1,
		Sources: []GateSourceEvidence{{ID: "source-1", Mode: "inferred", SourceDigest: "sha256:" + strings.Repeat("c", 64), SchemaOutcome: GateSuccess, ObservationQueries: 1, ObservationRows: 1, ObservedSchema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Ordinal: 1}}}},
	}
}

func TestGateEvidenceRejectsDuplicateAndClosedVocabulary(t *testing.T) {
	evidence := validGateEvidenceForTest()
	evidence.Sources = append(evidence.Sources, evidence.Sources[0])
	if err := evidence.Validate(); err == nil {
		t.Fatal("duplicate source evidence was accepted")
	}
	evidence = validGateEvidenceForTest()
	evidence.Sources[0].Mode = "authored"
	if err := evidence.Validate(); err == nil {
		t.Fatal("unknown source mode was accepted")
	}
	evidence = validGateEvidenceForTest()
	evidence.Sources[0].ObservedSchema = append(evidence.Sources[0].ObservedSchema, evidence.Sources[0].ObservedSchema[0])
	if err := evidence.Validate(); err == nil {
		t.Fatal("duplicate observed column was accepted")
	}
	evidence = validGateEvidenceForTest()
	evidence.Sources[0].SchemaFailure = GateObservationFailure("driver-error-with-secret")
	if err := evidence.Validate(); err == nil {
		t.Fatal("unknown source observation failure was accepted")
	}
}

func TestGateEvidenceRejectsAggregateTampering(t *testing.T) {
	evidence := validGateEvidenceForTest()
	evidence.Queries = 0
	if err := evidence.Validate(); err == nil {
		t.Fatal("aggregate query total below component evidence was accepted")
	}
	evidence = validGateEvidenceForTest()
	evidence.Outcome = GateWarning
	if err := evidence.Validate(); err == nil {
		t.Fatal("tampered aggregate outcome was accepted")
	}
}

func TestGateEvidenceRejectsComponentAggregateOverflow(t *testing.T) {
	evidence := validGateEvidenceForTest()
	evidence.Bounds.MaxRows = math.MaxInt64
	evidence.ObservedRows = math.MaxInt64
	evidence.Sources[0].ObservationRows = 1 << 62
	evidence.Checks = []GateCheckEvidence{{Identity: "model-1:row_count", Kind: "row_count", ResourceID: "model-1", Outcome: GateSuccess, Severity: "error", ObservedRows: 1 << 62, Queries: 0}}
	if err := evidence.Validate(); err == nil {
		t.Fatal("overflowing component row total was accepted")
	}
}
