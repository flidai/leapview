package gates

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/release"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testBinding = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func baseInput(query Query) Input {
	return Input{
		CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding,
		RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: time.Unix(1_700_000_000, 0).UTC(), Query: query,
	}
}

func observedSource() SourceInput {
	return SourceInput{ID: "source-1", Source: semanticmodel.Source{SchemaMode: "inferred"}, Observed: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Ordinal: 1}}}
}

func modelWithCheck(severity string) ModelInput {
	return ModelInput{ID: "model-1", Model: semanticmodel.Table{Checks: []semanticmodel.ModelCheckSpec{{Type: "non_null", Field: "id", Severity: severity}}}}
}

func rowsQuery(rows semanticquery.Rows) Query {
	return func(context.Context, semanticquery.Plan) (semanticquery.Rows, error) { return rows, nil }
}

func TestEvaluateSuccessAndPerSourceDigest(t *testing.T) {
	evidence, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: time.Unix(1_700_000_000, 0), Sources: []SourceInput{observedSource()}, Query: rowsQuery(nil)})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if evidence.Digest == "" || evidence.Sources[0].SourceDigest == "" || evidence.Sources[0].SchemaDigest == "" {
		t.Fatalf("evidence did not retain canonical source digests: %#v", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("evidence.Validate() error = %v", err)
	}
	if !strings.HasPrefix(evidence.Sources[0].SourceDigest, "sha256:") {
		t.Fatalf("source digest = %q", evidence.Sources[0].SourceDigest)
	}
}

func TestEvaluateWarningAndBlocking(t *testing.T) {
	warning, err := Evaluate(context.Background(), InputWithModel(baseInput(rowsQuery(semanticquery.Rows{{"value": int64(1)}})), modelWithCheck("warning")))
	if err != nil || warning.Checks[0].Outcome != release.GateWarning {
		t.Fatalf("warning result = outcome:%v err:%v", warning.Checks[0].Outcome, err)
	}
	blocking, err := Evaluate(context.Background(), InputWithModel(baseInput(rowsQuery(semanticquery.Rows{{"value": int64(1)}})), modelWithCheck("error")))
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateBlocking || blocking.Checks[0].Outcome != release.GateBlocking {
		t.Fatalf("blocking result = evidence:%#v err:%v", blocking, err)
	}
}

func TestEvaluateUnavailableEmptyTimeoutAndBounds(t *testing.T) {
	if evidence, err := Evaluate(context.Background(), InputWithModel(baseInput(rowsQuery(nil)), ModelInput{ID: "model-1", Model: semanticmodel.Table{}})); err != nil {
		t.Fatalf("model without checks should succeed: %v (%#v)", err, evidence)
	}
	_, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Sources: []SourceInput{{ID: "source-1", Source: semanticmodel.Source{SchemaMode: "inferred"}}}, Query: rowsQuery(nil)})
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable {
		t.Fatalf("unavailable error = %v", err)
	}

	fresh := observedSource()
	fresh.Observed = append(fresh.Observed, semanticmodel.ColumnSchema{Name: "updated_at", PhysicalType: "TIMESTAMP", Ordinal: 2})
	fresh.Relation = "source_relation"
	fresh.FreshnessEmpty = true
	fresh.Source.Freshness = &semanticmodel.SourceFreshnessSpec{Basis: "field", Field: "updated_at", ErrorAfter: &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "hour"}}
	empty, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: time.Unix(1_700_000_000, 0), Sources: []SourceInput{fresh}, Query: rowsQuery(semanticquery.Rows{{"value": nil}})})
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateEmpty || empty.Sources[0].FreshnessOutcome != release.GateEmpty {
		t.Fatalf("empty result = evidence:%#v err:%v", empty, err)
	}
	_, err = Evaluate(context.Background(), InputWithModel(baseInput(func(context.Context, semanticquery.Plan) (semanticquery.Rows, error) {
		return nil, context.DeadlineExceeded
	}), modelWithCheck("error")))
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateTimeout {
		t.Fatalf("timeout error = %v", err)
	}

	bounded := baseInput(rowsQuery(nil))
	bounded.Bounds = Bounds{MaxQueries: 1, MaxRows: 10, MaxMillis: 1000}
	bounded.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{Checks: []semanticmodel.ModelCheckSpec{{Type: "non_null", Field: "id"}, {Type: "non_null", Field: "name"}}}}}
	_, err = Evaluate(context.Background(), bounded)
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable || !errors.Is(err, ErrGateBounds) {
		t.Fatalf("bounds error = %v", err)
	}
}

func TestEvaluateDedupeImpliedAndExplicitChecks(t *testing.T) {
	queries := 0
	input := baseInput(func(context.Context, semanticquery.Plan) (semanticquery.Rows, error) { queries++; return nil, nil })
	input.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{
		Entities: map[string]semanticmodel.ModelEntitySpec{"id": {Type: "primary", Fields: []string{"id"}}},
		Checks:   []semanticmodel.ModelCheckSpec{{Type: "unique", Fields: []string{"id"}, Severity: "warning"}},
	}}}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if queries != 1 || len(evidence.Checks) != 1 {
		t.Fatalf("dedupe queries=%d checks=%d evidence=%#v", queries, len(evidence.Checks), evidence)
	}
}

func TestEvaluateAggregateOutcomeAndOverflowEvidence(t *testing.T) {
	input := baseInput(rowsQuery(semanticquery.Rows{{"value": int64(1)}}))
	input.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{Checks: []semanticmodel.ModelCheckSpec{{Type: "non_null", Field: "id", Severity: "warning"}}}}}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil || evidence.Outcome != release.GateWarning {
		t.Fatalf("warning aggregate = %q err=%v", evidence.Outcome, err)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("warning evidence validation = %v", err)
	}

	queryBound := baseInput(rowsQuery(nil))
	queryBound.Bounds = Bounds{MaxQueries: 1, MaxRows: 10, MaxMillis: 1000}
	queryBound.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{Checks: []semanticmodel.ModelCheckSpec{{Type: "non_null", Field: "id"}, {Type: "non_null", Field: "name"}}}}}
	failed, err := Evaluate(context.Background(), queryBound)
	if err == nil || !failed.QueriesExceeded || failed.Outcome != release.GateUnavailable {
		t.Fatalf("query overflow evidence = %#v err=%v", failed, err)
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("query overflow evidence validation = %v", err)
	}

	rowBound := baseInput(rowsQuery(semanticquery.Rows{{"value": 1}, {"value": 2}}))
	rowBound.Bounds = Bounds{MaxQueries: 4, MaxRows: 1, MaxMillis: 1000}
	rowBound.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{Checks: []semanticmodel.ModelCheckSpec{{Type: "non_null", Field: "id"}}}}}
	rows, err := Evaluate(context.Background(), rowBound)
	if err == nil || !rows.RowsExceeded {
		t.Fatalf("row overflow evidence = %#v err=%v", rows, err)
	}
	if err := rows.Validate(); err != nil {
		t.Fatalf("row overflow evidence validation = %v", err)
	}

	durationBound := baseInput(func(context.Context, semanticquery.Plan) (semanticquery.Rows, error) {
		time.Sleep(5 * time.Millisecond)
		return nil, nil
	})
	durationBound.Bounds = Bounds{MaxQueries: 2, MaxRows: 10, MaxMillis: 1}
	durationBound.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{Checks: []semanticmodel.ModelCheckSpec{{Type: "non_null", Field: "id"}}}}}
	duration, err := Evaluate(context.Background(), durationBound)
	if err != nil {
		// A query adapter that ignores context still produces canonical timeout
		// evidence; the evaluator reports timeout through the aggregate outcome.
		var evaluationErr *EvaluationError
		if !errors.As(err, &evaluationErr) {
			t.Fatalf("duration overflow error = %v", err)
		}
	}
	if !duration.DurationExceeded || duration.Outcome != release.GateTimeout {
		t.Fatalf("duration overflow evidence = %#v", duration)
	}
	if err := duration.Validate(); err != nil {
		t.Fatalf("duration overflow evidence validation = %v", err)
	}
}

func TestEvaluateEvidenceNeverSerializesSourceSecretsOrLocations(t *testing.T) {
	input := baseInput(rowsQuery(nil))
	source := observedSource()
	source.Source.Path = "/private/sentinel-endpoint/path.parquet"
	source.Source.Object = "sentinel-object-location"
	source.Source.EffectiveOptions = map[string]any{"token": "sentinel-auth-token"}
	source.Source.Options = map[string]any{"endpoint": "sentinel-endpoint"}
	input.Sources = []SourceInput{source}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(encoded)
	for _, sentinel := range []string{"sentinel-endpoint", "sentinel-object-location", "sentinel-auth-token", "/private/sentinel"} {
		if strings.Contains(serialized, sentinel) {
			t.Fatalf("gate evidence serialized source sentinel %q: %s", sentinel, serialized)
		}
	}
}

func TestEvaluateSourceObservationFailuresProduceCanonicalEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure string
		outcome release.GateOutcome
	}{
		{name: "unavailable", failure: "unavailable", outcome: release.GateUnavailable},
		{name: "timeout", failure: "timeout", outcome: release.GateTimeout},
		{name: "bounds", failure: "bounds", outcome: release.GateUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := observedSource()
			source.SchemaFailure = test.failure
			evidence, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: time.Unix(1_700_000_000, 0), Sources: []SourceInput{source}, Query: rowsQuery(nil)})
			var evaluationErr *EvaluationError
			if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != test.outcome {
				t.Fatalf("failure evidence err=%v outcome=%v", err, evaluationErr)
			}
			if evidence.Digest == "" || evidence.Outcome != test.outcome {
				t.Fatalf("failure evidence=%#v", evidence)
			}
			if err := evidence.Validate(); err != nil {
				t.Fatalf("failure evidence validation=%v", err)
			}
		})
	}
}

func TestEvaluatePreflightOverflowOutcomeMatchesError(t *testing.T) {
	input := baseInput(rowsQuery(nil))
	input.Bounds = Bounds{MaxRows: 10, MaxQueries: 1, MaxMillis: 100}
	input.PreflightQueries = 2
	evidence, err := Evaluate(context.Background(), input)
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable {
		t.Fatalf("preflight overflow error=%v evaluation=%#v", err, evaluationErr)
	}
	if evidence.Outcome != release.GateUnavailable || !evidence.QueriesExceeded || evidence.Digest == "" {
		t.Fatalf("preflight overflow evidence=%#v", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("preflight overflow evidence validation=%v", err)
	}
}

func InputWithModel(input Input, model ModelInput) Input {
	input.Models = []ModelInput{model}
	return input
}
