package gates

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
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
	return ModelInput{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id", Severity: severity}}}}
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

func TestEvaluateRevisionFreshnessWarningBlockingAndUnavailable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	observedAt := now.Add(-2 * time.Minute)
	revisionAt := observedAt
	base := observedSource()
	base.Source.Freshness = &semanticmodel.SourceFreshnessSpec{
		Basis: "revision", Revision: "rev-1", RevisionAt: &revisionAt,
		WarningAfter: &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "minute"},
		ErrorAfter:   &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "hour"},
	}
	base.Revision = "rev-1"
	base.RevisionObserved = observedAt
	warning, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: now, Sources: []SourceInput{base}, Query: rowsQuery(nil)})
	if err != nil || warning.Outcome != release.GateWarning || warning.Sources[0].FreshnessOutcome != release.GateWarning {
		t.Fatalf("revision warning evidence=%#v err=%v", warning, err)
	}

	base.Source.Freshness.WarningAfter = &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "second"}
	base.Source.Freshness.ErrorAfter = &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "minute"}
	blocking, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: now, Sources: []SourceInput{base}, Query: rowsQuery(nil)})
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateBlocking || blocking.Sources[0].FreshnessOutcome != release.GateBlocking {
		t.Fatalf("revision blocking evidence=%#v err=%v", blocking, err)
	}

	base.RevisionObserved = time.Time{}
	unavailable, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: now, Sources: []SourceInput{base}, Query: rowsQuery(nil)})
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable || unavailable.Sources[0].FreshnessOutcome != release.GateUnavailable {
		t.Fatalf("revision unavailable evidence=%#v err=%v", unavailable, err)
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
	bounded.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}, {Type: "non_null", Field: "name"}}}}}
	_, err = Evaluate(context.Background(), bounded)
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable || !errors.Is(err, ErrGateBounds) {
		t.Fatalf("bounds error = %v", err)
	}
}

func TestEvaluateDedupeImpliedAndExplicitChecks(t *testing.T) {
	queries := 0
	input := baseInput(func(context.Context, semanticquery.Plan) (semanticquery.Rows, error) { queries++; return nil, nil })
	input.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1",
		Entities: map[string]semanticmodel.EntityDefinition{"id": {Type: "primary", Fields: []string{"id"}}},
		Checks:   []semanticmodel.ModelCheck{{Type: "unique", Fields: []string{"id"}, Severity: "warning"}},
	}}}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if queries != 2 || len(evidence.Checks) != 2 {
		t.Fatalf("dedupe queries=%d checks=%d evidence=%#v", queries, len(evidence.Checks), evidence)
	}
}

func TestCheckIdentityIsStablePrintableAndUnambiguous(t *testing.T) {
	minimum := int64(1)
	base := semanticmodel.ModelCheck{
		Type: "accepted_values", Field: "state", Fields: []string{"b", "a"},
		Values: []string{"RJ", "SP", "contains\x00nul"}, Minimum: &minimum,
	}
	identity := checkIdentity("model-1", base)
	reordered := base
	reordered.Fields = []string{"a", "b"}
	reordered.Values = []string{"contains\x00nul", "SP", "RJ"}
	if got := checkIdentity("model-1", reordered); got != identity {
		t.Fatalf("reordered check identity = %q, want %q", got, identity)
	}
	if !strings.HasPrefix(identity, "check:") || len(identity) != len("check:")+64 {
		t.Fatalf("check identity = %q, want content-addressed identity", identity)
	}
	for _, r := range identity {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("check identity contains control rune %U: %q", r, identity)
		}
	}
	commaJoined := base
	commaJoined.Fields = []string{"a,b"}
	separate := base
	separate.Fields = []string{"a", "b"}
	if checkIdentity("model-1", commaJoined) == checkIdentity("model-1", separate) {
		t.Fatal("structurally distinct field lists produced the same identity")
	}
}

func TestEvaluateQualificationEvidenceContainsNoNULIdentityEscape(t *testing.T) {
	evidence, err := Evaluate(context.Background(), InputWithModel(baseInput(rowsQuery(nil)), modelWithCheck("error")))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `\u0000`) {
		t.Fatalf("gate evidence contains PostgreSQL-incompatible NUL escape: %s", encoded)
	}
}

func TestEvaluateRelationshipCheckRequiresPatternedReference(t *testing.T) {
	var plans []semanticquery.Plan
	query := func(_ context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
		plans = append(plans, plan)
		return nil, nil
	}
	input := baseInput(query)
	input.Models = []ModelInput{
		{ID: "orders", Model: semanticmodel.Table{ModelName: "orders.v2", Checks: []semanticmodel.ModelCheck{{Type: "relationship", Field: "customer_id", To: "customers.v2.customer_id", Severity: "error"}}}},
		{ID: "customers", Model: semanticmodel.Table{ModelName: "customers.v2"}},
	}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil || evidence.Outcome != release.GateSuccess || len(plans) != 1 {
		t.Fatalf("valid relationship evidence=%#v err=%v plans=%#v", evidence, err, plans)
	}
	if !strings.Contains(plans[0].SQL, `"model"."customers.v2"`) || !strings.Contains(plans[0].SQL, `"model"."orders.v2"`) {
		t.Fatalf("relationship query relation was not safely lowered: %q", plans[0].SQL)
	}

	for _, reference := range []string{"customers", "customers..customer_id", `customers";DROP.customer_id`} {
		input.Models[0].Model.Checks[0].To = reference
		if evidence, err := Evaluate(context.Background(), input); err == nil || evidence.Outcome != release.GateUnavailable {
			t.Fatalf("adversarial relationship %q evidence=%#v err=%v", reference, evidence, err)
		}
	}
}

func TestEvaluateRejectsUnboundModelNamesWithCanonicalEvidence(t *testing.T) {
	input := baseInput(rowsQuery(nil))
	missing := modelWithCheck("error")
	missing.Model.ModelName = ""
	evidence, err := Evaluate(context.Background(), InputWithModel(input, missing))
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable {
		t.Fatalf("missing model name error=%v", err)
	}
	if evidence.Digest == "" || len(evidence.Checks) != 1 || evidence.Checks[0].Outcome != release.GateUnavailable || evidence.Checks[0].Kind != "non_null" {
		t.Fatalf("missing model name evidence=%#v", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("missing model name evidence invalid: %v", err)
	}

	duplicate := input
	duplicate.Models = []ModelInput{
		{ID: "orders", Model: semanticmodel.Table{ModelName: "same", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}}}},
		{ID: "customers", Model: semanticmodel.Table{ModelName: "same", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}}}},
	}
	evidence, err = Evaluate(context.Background(), duplicate)
	if !errors.As(err, &evaluationErr) || evidence.Digest == "" || evidence.Outcome != release.GateUnavailable {
		t.Fatalf("duplicate model names evidence=%#v err=%v", evidence, err)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("duplicate model names evidence invalid: %v", err)
	}
}

func TestEvaluateRejectsMalformedObservationCounters(t *testing.T) {
	input := baseInput(rowsQuery(nil))
	input.PreflightRows = -1
	if _, err := Evaluate(context.Background(), input); !errors.Is(err, ErrGateUnavailable) {
		t.Fatalf("negative preflight error=%v", err)
	}
	source := observedSource()
	source.ObservationQueries = -1
	input.PreflightRows = 0
	input.Sources = []SourceInput{source}
	if _, err := Evaluate(context.Background(), input); !errors.Is(err, ErrGateUnavailable) {
		t.Fatalf("negative source observations error=%v", err)
	}
	source.ObservationQueries = 0
	source.ID = " source-1"
	if _, err := Evaluate(context.Background(), input); !errors.Is(err, ErrGateUnavailable) {
		t.Fatalf("noncanonical source identity error=%v", err)
	}
}

func TestEvaluateImpliedUniqueEntityAlsoRequiresNonNull(t *testing.T) {
	input := baseInput(func(_ context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
		if strings.Contains(plan.SQL, `"email" IS NULL`) {
			return semanticquery.Rows{{"value": int64(1)}}, nil
		}
		return nil, nil
	})
	input.Models = []ModelInput{{ID: "orders", Model: semanticmodel.Table{ModelName: "orders", Entities: map[string]semanticmodel.EntityDefinition{
		"order_id": {Type: "primary", Fields: []string{"order_id"}},
		"email":    {Type: "unique", Fields: []string{"email"}},
	}}}}
	evidence, err := Evaluate(context.Background(), input)
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateBlocking || evidence.Outcome != release.GateBlocking {
		t.Fatalf("alternate unique null evidence=%#v err=%v", evidence, err)
	}
}

func TestEvaluateAggregateOutcomeAndOverflowEvidence(t *testing.T) {
	input := baseInput(rowsQuery(semanticquery.Rows{{"value": int64(1)}}))
	input.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id", Severity: "warning"}}}}}
	evidence, err := Evaluate(context.Background(), input)
	if err != nil || evidence.Outcome != release.GateWarning {
		t.Fatalf("warning aggregate = %q err=%v", evidence.Outcome, err)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("warning evidence validation = %v", err)
	}

	queryBound := baseInput(rowsQuery(nil))
	queryBound.Bounds = Bounds{MaxQueries: 1, MaxRows: 10, MaxMillis: 1000}
	queryBound.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}, {Type: "non_null", Field: "name"}}}}}
	failed, err := Evaluate(context.Background(), queryBound)
	if err == nil || !failed.QueriesExceeded || failed.Outcome != release.GateUnavailable {
		t.Fatalf("query overflow evidence = %#v err=%v", failed, err)
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("query overflow evidence validation = %v", err)
	}

	rowBound := baseInput(rowsQuery(semanticquery.Rows{{"value": 1}, {"value": 2}}))
	rowBound.Bounds = Bounds{MaxQueries: 4, MaxRows: 1, MaxMillis: 1000}
	rowBound.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}}}}}
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
	durationBound.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}}}}}
	duration, err := Evaluate(context.Background(), durationBound)
	var durationErr *EvaluationError
	if !errors.As(err, &durationErr) || durationErr.Outcome != release.GateTimeout {
		// A query adapter that ignores context still produces canonical timeout
		// evidence; elapsed time must be returned as a blocking evaluator error.
		t.Fatalf("duration overflow error = %v", err)
	}
	if !duration.DurationExceeded || duration.Outcome != release.GateTimeout {
		t.Fatalf("duration overflow evidence = %#v", duration)
	}
	if err := duration.Validate(); err != nil {
		t.Fatalf("duration overflow evidence validation = %v", err)
	}
}

func TestEvaluateRejectsRowCountBudgetArithmeticOverflow(t *testing.T) {
	maximum := int64(1 << 62)
	input := baseInput(rowsQuery(semanticquery.Rows{{"count": maximum}}))
	input.Bounds = Bounds{MaxQueries: 4, MaxRows: math.MaxInt64, MaxMillis: 1000}
	input.PreflightRows = maximum
	input.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "row_count", Maximum: &maximum}}}}}

	evidence, err := Evaluate(context.Background(), input)
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateUnavailable || !errors.Is(err, ErrGateBounds) {
		t.Fatalf("row-count arithmetic overflow error=%v evidence=%#v", err, evidence)
	}
	if !evidence.RowsExceeded || evidence.ObservedRows != math.MaxInt64 || evidence.Outcome != release.GateUnavailable {
		t.Fatalf("row-count arithmetic overflow evidence=%#v", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("row-count arithmetic overflow evidence invalid: %v", err)
	}
}

func TestEvaluateSaturatesElapsedBudgetArithmeticOverflow(t *testing.T) {
	input := baseInput(func(context.Context, semanticquery.Plan) (semanticquery.Rows, error) {
		time.Sleep(time.Millisecond)
		return nil, nil
	})
	input.Bounds = Bounds{MaxQueries: 2, MaxRows: 10, MaxMillis: math.MaxInt64}
	input.PreflightMillis = math.MaxInt64 - 1
	input.Models = []ModelInput{{ID: "model-1", Model: semanticmodel.Table{ModelName: "model-1", Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "id"}}}}}

	evidence, err := Evaluate(context.Background(), input)
	var evaluationErr *EvaluationError
	if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != release.GateTimeout {
		t.Fatalf("elapsed arithmetic overflow error=%v evidence=%#v", err, evidence)
	}
	if !evidence.DurationExceeded || evidence.DurationMillis != math.MaxInt64 || evidence.Outcome != release.GateTimeout {
		t.Fatalf("elapsed arithmetic overflow evidence=%#v", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("elapsed arithmetic overflow evidence invalid: %v", err)
	}
}

func TestEvaluateEvidenceNeverSerializesSourceSecretsOrLocations(t *testing.T) {
	input := baseInput(rowsQuery(nil))
	source := observedSource()
	source.Source.Path = "/private/sentinel-endpoint/path.parquet"
	source.Source.Object = "sentinel-object-location"
	sensitive := "sentinel-auth-token"
	source.Source.EffectivePathLocation = &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: source.Source.Path, Format: "csv"},
		Format:                 "csv",
		Options:                &projectcontracts.CSVReaderOptions{NullString: &sensitive},
	}}
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
	for _, sentinel := range []string{"sentinel-object-location", "sentinel-auth-token", "/private/sentinel"} {
		if strings.Contains(serialized, sentinel) {
			t.Fatalf("gate evidence serialized source sentinel %q: %s", sentinel, serialized)
		}
	}
}

func TestEvaluateSourceObservationFailuresProduceCanonicalEvidence(t *testing.T) {
	for _, test := range []struct {
		name      string
		failure   analyticsmaterialize.ObservationFailure
		expected  release.GateObservationFailure
		outcome   release.GateOutcome
		freshness bool
	}{
		{name: "unavailable", failure: analyticsmaterialize.ObservationUnavailable, expected: release.GateObservationFailureUnavailable, outcome: release.GateUnavailable},
		{name: "timeout", failure: analyticsmaterialize.ObservationTimeout, expected: release.GateObservationFailureTimeout, outcome: release.GateTimeout},
		{name: "bounds", failure: analyticsmaterialize.ObservationBounds, expected: release.GateObservationFailureBounds, outcome: release.GateUnavailable},
		{name: "freshness-bounds", failure: analyticsmaterialize.ObservationBounds, expected: release.GateObservationFailureBounds, outcome: release.GateUnavailable, freshness: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := observedSource()
			if test.freshness {
				source.FreshnessFailure = test.failure
			} else {
				source.SchemaFailure = test.failure
			}
			evidence, err := Evaluate(context.Background(), Input{CandidateID: "candidate-1", SourceDigest: testDigest, BindingGeneration: testBinding, RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: time.Unix(1_700_000_000, 0), Sources: []SourceInput{source}, Query: rowsQuery(nil)})
			var evaluationErr *EvaluationError
			if !errors.As(err, &evaluationErr) || evaluationErr.Outcome != test.outcome {
				t.Fatalf("failure evidence err=%v outcome=%v", err, evaluationErr)
			}
			if evidence.Digest == "" || evidence.Outcome != test.outcome {
				t.Fatalf("failure evidence=%#v", evidence)
			}
			if test.freshness {
				if got := evidence.Sources[0].FreshnessFailure; got != test.expected {
					t.Fatalf("freshness failure indicator=%q, want %q", got, test.expected)
				}
			} else if got := evidence.Sources[0].SchemaFailure; got != test.expected {
				t.Fatalf("schema failure indicator=%q, want %q", got, test.expected)
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
