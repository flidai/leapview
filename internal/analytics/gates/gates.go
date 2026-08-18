// Package gates evaluates the closed ADR-0010 source and model assertions
// against a disposable candidate relation namespace. It only accepts typed
// specs and a value-only query capability; callers cannot supply authored SQL.
package gates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/release"
)

var (
	ErrGateBlocking    = errors.New("candidate gate blocked activation")
	ErrGateUnavailable = errors.New("candidate gate evidence unavailable")
	ErrGateBounds      = errors.New("candidate gate execution bound exceeded")
)

const evidenceVersion = 1

type Bounds struct {
	MaxRows    int64
	MaxQueries int
	MaxMillis  int64
}

func (b Bounds) normalized() Bounds {
	if b.MaxRows <= 0 {
		b.MaxRows = 10000
	}
	if b.MaxQueries <= 0 {
		b.MaxQueries = 128
	}
	if b.MaxMillis <= 0 {
		b.MaxMillis = 5000
	}
	return b
}

// Query is intentionally narrower than a database handle. Implementations
// must enforce the candidate writer lease before executing each plan.
type Query func(context.Context, semanticquery.Plan) (semanticquery.Rows, error)

type SourceInput struct {
	ID       string
	Source   semanticmodel.Source
	Relation string
	// SourceDigest may be supplied by the compiler when the source artifact
	// identity is already known. When omitted it is derived from the lowered,
	// non-secret source contract.
	SourceDigest string
	Observed     []semanticmodel.ColumnSchema
	// Revision and RevisionObserved are target-owned, non-secret freshness
	// evidence. A raw revision value is never copied to the evidence record.
	Revision         string
	RevisionObserved time.Time
	// FreshnessObserved is captured by the live source session. A non-zero
	// value is preferred over running another query after that session closes.
	FreshnessObserved  time.Time
	FreshnessEmpty     bool
	SchemaFailure      string
	FreshnessFailure   string
	ObservationQueries int
	ObservationRows    int64
	ObservationMillis  int64
}

type ModelInput struct {
	ID    string
	Model semanticmodel.Table
}

type Input struct {
	CandidateID       string
	SourceDigest      string
	BindingGeneration string
	RuntimeVersion    string
	DuckDBVersion     string
	Now               time.Time
	Bounds            Bounds
	Sources           []SourceInput
	Models            []ModelInput
	Query             Query
	PreflightQueries  int
	PreflightRows     int64
	PreflightMillis   int64
}

type EvaluationError struct {
	Outcome  release.GateOutcome
	Identity string
	Evidence release.GateEvidence
	Cause    error
}

func (e *EvaluationError) Error() string {
	if e == nil {
		return "candidate gate failed"
	}
	if e.Cause == nil {
		return fmt.Sprintf("candidate gate %s: %s", e.Outcome, e.Identity)
	}
	return fmt.Sprintf("candidate gate %s (%s): %v", e.Outcome, e.Identity, e.Cause)
}

func (e *EvaluationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type budget struct {
	Bounds          Bounds
	Queries         int
	Rows            int64
	Query           Query
	PreflightMillis int64
	Started         time.Time
	QueriesExceeded bool
	RowsExceeded    bool
}

func Evaluate(ctx context.Context, input Input) (release.GateEvidence, error) {
	bounds := input.Bounds.normalized()
	if strings.TrimSpace(input.CandidateID) == "" || strings.TrimSpace(input.SourceDigest) == "" || strings.TrimSpace(input.BindingGeneration) == "" || strings.TrimSpace(input.RuntimeVersion) == "" || strings.TrimSpace(input.DuckDBVersion) == "" || input.Query == nil {
		return release.GateEvidence{}, fmt.Errorf("%w: candidate identity, source/runtime evidence, and query capability are required", ErrGateUnavailable)
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	input.Now = input.Now.UTC()
	if input.PreflightQueries < 0 || input.PreflightRows < 0 || input.PreflightMillis < 0 || input.PreflightQueries > bounds.MaxQueries || input.PreflightRows > bounds.MaxRows || input.PreflightMillis >= bounds.MaxMillis {
		evidence := release.GateEvidence{Version: evidenceVersion, CandidateID: input.CandidateID, SourceDigest: input.SourceDigest, BindingGeneration: input.BindingGeneration, RuntimeVersion: input.RuntimeVersion, DuckDBVersion: input.DuckDBVersion, EvaluatedAt: input.Now.UTC(), Bounds: release.GateBounds{MaxRows: bounds.MaxRows, MaxQueries: bounds.MaxQueries, MaxMillis: bounds.MaxMillis}, Queries: input.PreflightQueries, ObservedRows: input.PreflightRows, DurationMillis: minInt64(input.PreflightMillis, bounds.MaxMillis), DurationExceeded: input.PreflightMillis >= bounds.MaxMillis, QueriesExceeded: input.PreflightQueries > bounds.MaxQueries, RowsExceeded: input.PreflightRows > bounds.MaxRows}
		outcome := release.GateUnavailable
		if input.PreflightMillis >= bounds.MaxMillis {
			outcome = release.GateTimeout
		}
		err := gateError("preflight", outcome, ErrGateBounds, nil)
		return finishFailure(evidence, nil, err)
	}
	remaining := time.Duration(bounds.MaxMillis-input.PreflightMillis) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	evidence := release.GateEvidence{Version: evidenceVersion, CandidateID: input.CandidateID, SourceDigest: input.SourceDigest, BindingGeneration: input.BindingGeneration, RuntimeVersion: input.RuntimeVersion, DuckDBVersion: input.DuckDBVersion, EvaluatedAt: input.Now, Bounds: release.GateBounds{MaxRows: bounds.MaxRows, MaxQueries: bounds.MaxQueries, MaxMillis: bounds.MaxMillis}}
	state := &budget{Bounds: bounds, Query: input.Query, Queries: input.PreflightQueries, Rows: input.PreflightRows, PreflightMillis: input.PreflightMillis, Started: time.Now()}

	sources := append([]SourceInput(nil), input.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	for _, source := range sources {
		result, err := evaluateSource(ctx, input.Now, state, source)
		evidence.Sources = append(evidence.Sources, result)
		if err != nil {
			return finishFailure(evidence, state, err)
		}
	}

	models := append([]ModelInput(nil), input.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	for _, model := range models {
		checks := impliedChecks(model.Model)
		checks = append(checks, model.Model.Checks...)
		checks = canonicalChecks(checks)
		for _, check := range checks {
			result, err := evaluateCheck(ctx, state, model.ID, model.Model, check)
			evidence.Checks = append(evidence.Checks, result)
			if err != nil {
				return finishFailure(evidence, state, err)
			}
		}
	}
	return finish(evidence, state)
}

func withEvidence(evidence release.GateEvidence, err error) release.GateEvidence {
	if evaluation, ok := err.(*EvaluationError); ok {
		evaluation.Evidence = evidence
	}
	return evidence
}

func finishFailure(evidence release.GateEvidence, state *budget, cause error) (release.GateEvidence, error) {
	canonical, err := finish(evidence, state)
	if err != nil {
		return release.GateEvidence{}, errors.Join(cause, err)
	}
	return withEvidence(canonical, cause), cause
}

func finish(evidence release.GateEvidence, state *budget) (release.GateEvidence, error) {
	evidence.Sources = append([]release.GateSourceEvidence(nil), evidence.Sources...)
	evidence.Checks = append([]release.GateCheckEvidence(nil), evidence.Checks...)
	sort.Slice(evidence.Sources, func(i, j int) bool { return evidence.Sources[i].ID < evidence.Sources[j].ID })
	sort.Slice(evidence.Checks, func(i, j int) bool { return evidence.Checks[i].Identity < evidence.Checks[j].Identity })
	if state != nil {
		evidence.Queries = state.Queries
		evidence.ObservedRows = state.Rows
		elapsed := state.PreflightMillis + time.Since(state.Started).Milliseconds()
		if elapsed > state.Bounds.MaxMillis {
			evidence.DurationExceeded = true
			elapsed = state.Bounds.MaxMillis
		}
		evidence.QueriesExceeded = state.QueriesExceeded
		evidence.RowsExceeded = state.RowsExceeded
		evidence.DurationMillis = elapsed
	}
	computedOutcome := aggregateOutcome(evidence)
	if evidence.Outcome != "" {
		computedOutcome = higherOutcome(computedOutcome, evidence.Outcome)
	}
	evidence.Outcome = computedOutcome
	return evidence.Canonical()
}

func aggregateOutcome(evidence release.GateEvidence) release.GateOutcome {
	outcome := release.GateSuccess
	for _, source := range evidence.Sources {
		outcome = higherOutcome(outcome, source.SchemaOutcome)
		outcome = higherOutcome(outcome, source.FreshnessOutcome)
	}
	for _, check := range evidence.Checks {
		outcome = higherOutcome(outcome, check.Outcome)
	}
	if evidence.DurationExceeded {
		outcome = higherOutcome(outcome, release.GateTimeout)
	} else if evidence.QueriesExceeded || evidence.RowsExceeded {
		outcome = higherOutcome(outcome, release.GateUnavailable)
	}
	return outcome
}

func higherOutcome(current, candidate release.GateOutcome) release.GateOutcome {
	rank := func(value release.GateOutcome) int {
		switch value {
		case release.GateTimeout:
			return 6
		case release.GateBlocking:
			return 5
		case release.GateUnavailable:
			return 4
		case release.GateEmpty:
			return 3
		case release.GateWarning:
			return 2
		case release.GateSuccess:
			return 1
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

func evaluateSource(ctx context.Context, now time.Time, state *budget, source SourceInput) (release.GateSourceEvidence, error) {
	result := release.GateSourceEvidence{ID: source.ID, Mode: source.Source.SchemaMode, ObservationQueries: source.ObservationQueries, ObservationRows: source.ObservationRows, ObservationMillis: source.ObservationMillis}
	if result.Mode == "" {
		result.Mode = "inferred"
	}
	result.SchemaOutcome = release.GateSuccess
	if strings.TrimSpace(source.ID) == "" {
		return result, gateError("source", release.GateUnavailable, ErrGateUnavailable, nil)
	}
	result.SourceDigest = source.SourceDigest
	if result.SourceDigest == "" {
		result.SourceDigest = digest(sourceDigestInputFrom(source.ID, source.Source))
	}
	observed := append([]semanticmodel.ColumnSchema(nil), source.Observed...)
	sort.Slice(observed, func(i, j int) bool {
		if observed[i].Name != observed[j].Name {
			return observed[i].Name < observed[j].Name
		}
		return observed[i].Ordinal < observed[j].Ordinal
	})
	result.ObservedSchema = observed
	if source.SchemaFailure != "" {
		result.SchemaOutcome = observationFailureOutcome(source.SchemaFailure)
		result.ObservationDigest = digest(struct{ Failure string }{source.SchemaFailure})
		return result, gateError(result.ID+":schema", result.SchemaOutcome, failureCause(result.SchemaOutcome), nil)
	}
	if len(observed) == 0 {
		result.SchemaOutcome = release.GateUnavailable
		return result, gateError(result.ID+":schema", release.GateUnavailable, ErrGateUnavailable, nil)
	}
	checked := source.Source
	checked.Schema = semanticmodel.TableSchema{Columns: observed}
	checker := semanticmodel.Model{Sources: map[string]semanticmodel.Source{source.ID: checked}}
	if err := checker.ValidateDiscoveredSourceSchemas(); err != nil {
		result.SchemaOutcome = release.GateBlocking
		return result, gateError(result.ID+":schema", release.GateBlocking, ErrGateBlocking, err)
	}
	if source.Source.Freshness != nil && source.Source.Freshness.Basis == "field" {
		found := false
		for _, column := range observed {
			if column.Name == source.Source.Freshness.Field {
				found = true
				break
			}
		}
		if !found {
			result.FreshnessOutcome = release.GateUnavailable
			return result, gateError(result.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
		}
	}
	result.SchemaDigest = digest(observed)
	if source.FreshnessFailure != "" {
		result.FreshnessOutcome = observationFailureOutcome(source.FreshnessFailure)
		result.ObservationDigest = digest(struct{ Failure string }{source.FreshnessFailure})
		return result, gateError(result.ID+":freshness", result.FreshnessOutcome, failureCause(result.FreshnessOutcome), nil)
	}
	if source.Source.Freshness != nil {
		if source.Source.Freshness.Basis == "field" {
			result.ObservedAt = source.FreshnessObserved.UTC()
		} else if source.Source.Freshness.Basis == "revision" {
			result.ObservedAt = source.RevisionObserved.UTC()
		}
		freshness, ageMillis, observationDigest, err := evaluateFreshness(ctx, now, state, source)
		result.FreshnessOutcome = freshness
		result.FreshnessAgeMillis = ageMillis
		result.ObservationDigest = observationDigest
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func observationFailureOutcome(value string) release.GateOutcome {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "timeout":
		return release.GateTimeout
	case "bounds", "unavailable":
		return release.GateUnavailable
	default:
		return release.GateUnavailable
	}
}

func failureCause(outcome release.GateOutcome) error {
	if outcome == release.GateTimeout {
		return context.DeadlineExceeded
	}
	return ErrGateUnavailable
}

func evaluateFreshness(ctx context.Context, now time.Time, state *budget, source SourceInput) (release.GateOutcome, int64, string, error) {
	freshness := source.Source.Freshness
	var observed time.Time
	if freshness.Basis == "revision" {
		if source.Revision == "" || source.RevisionObserved.IsZero() {
			return release.GateUnavailable, 0, "", gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
		}
		observed = source.RevisionObserved
		if freshness.RevisionAt != nil && !observed.UTC().Equal(freshness.RevisionAt.UTC()) {
			return release.GateUnavailable, 0, "", gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
		}
	} else if freshness.Basis == "field" {
		if freshness.Field == "" {
			return release.GateUnavailable, 0, "", gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
		}
		if !validField(freshness.Field) {
			return release.GateUnavailable, 0, "", gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
		}
		if source.FreshnessEmpty {
			return release.GateEmpty, 0, digest(struct{ Empty bool }{true}), gateError(source.ID+":freshness", release.GateEmpty, nil, nil)
		}
		observed = source.FreshnessObserved
		if observed.IsZero() {
			return release.GateUnavailable, 0, "", gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
		}
	} else {
		return release.GateUnavailable, 0, "", gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
	}
	age := now.Sub(observed.UTC())
	if age < 0 {
		return release.GateUnavailable, 0, digest(struct {
			Observed time.Time
			Future   bool
		}{Observed: observed.UTC(), Future: true}), gateError(source.ID+":freshness", release.GateUnavailable, ErrGateUnavailable, nil)
	}
	if freshness.ErrorAfter != nil && age >= durationOf(*freshness.ErrorAfter) {
		return release.GateBlocking, age.Milliseconds(), digest(struct {
			Observed  time.Time
			AgeMillis int64
			Revision  string
		}{Observed: observed.UTC(), AgeMillis: age.Milliseconds(), Revision: source.Revision}), gateError(source.ID+":freshness", release.GateBlocking, ErrGateBlocking, nil)
	}
	if freshness.WarningAfter != nil && age >= durationOf(*freshness.WarningAfter) {
		return release.GateWarning, age.Milliseconds(), digest(struct {
			Observed  time.Time
			AgeMillis int64
			Revision  string
		}{Observed: observed.UTC(), AgeMillis: age.Milliseconds(), Revision: source.Revision}), nil
	}
	return release.GateSuccess, age.Milliseconds(), digest(struct {
		Observed  time.Time
		AgeMillis int64
		Revision  string
	}{Observed: observed.UTC(), AgeMillis: age.Milliseconds(), Revision: source.Revision}), nil
}

func evaluateCheck(ctx context.Context, state *budget, modelID string, table semanticmodel.Table, check semanticmodel.ModelCheckSpec) (result release.GateCheckEvidence, retErr error) {
	identity := checkIdentity(modelID, check)
	result = release.GateCheckEvidence{Identity: identity, Kind: check.Type, ResourceID: modelID, Severity: severity(check.Severity)}
	queriesBefore := state.Queries
	defer func() { result.Queries = state.Queries - queriesBefore }()
	relation := modelRelation(table, modelID)
	var rows semanticquery.Rows
	var err error
	switch check.Type {
	case "non_null":
		if !validField(check.Field) {
			return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
		}
		rows, err = run(ctx, state, relation, "1", fmt.Sprintf("%s IS NULL", quoteIdent(check.Field)))
	case "unique":
		fields := canonicalFields(check.Fields)
		if len(fields) == 0 {
			return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
		}
		parts := make([]string, len(fields))
		for i, field := range fields {
			parts[i] = quoteIdent(field)
		}
		rows, err = runPlan(ctx, state, fmt.Sprintf("SELECT 1 AS value FROM %s GROUP BY %s HAVING COUNT(*) > 1 LIMIT %d", relation, strings.Join(parts, ", "), state.Bounds.MaxRows), nil)
	case "accepted_values":
		if !validField(check.Field) || len(check.Values) == 0 {
			return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
		}
		values := append([]string(nil), check.Values...)
		sort.Strings(values)
		args := make([]any, len(values))
		placeholders := make([]string, len(values))
		for i, value := range values {
			args[i] = value
			placeholders[i] = "?"
		}
		rows, err = run(ctx, state, relation, "1", fmt.Sprintf("%s IS NOT NULL AND %s NOT IN (%s)", quoteIdent(check.Field), quoteIdent(check.Field), strings.Join(placeholders, ",")), args)
	case "relationship":
		toTable, toField, ok := splitReference(check.To)
		if !ok || !validField(check.Field) {
			return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
		}
		if !validField(toField) {
			return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
		}
		where := fmt.Sprintf("child.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s AS parent WHERE parent.%s = child.%s)", quoteIdent(check.Field), modelRelationName(toTable), quoteIdent(toField), quoteIdent(check.Field))
		rows, err = runQualified(ctx, state, relation+" AS child", "1", where, nil)
	case "row_count":
		limit := check.Maximum
		if limit == nil || (check.Minimum != nil && *check.Minimum > *limit) {
			limit = check.Minimum
		}
		if limit == nil || *limit < 0 || *limit >= state.Bounds.MaxRows {
			return result, gateError(identity, release.GateUnavailable, ErrGateBounds, nil)
		}
		rows, err = runPlan(ctx, state, fmt.Sprintf("SELECT COUNT(*) AS count FROM (SELECT 1 FROM %s LIMIT ?) AS bounded", relation), []any{*limit + 1})
		if err == nil {
			count, ok := int64Value(firstValue(rows, "count"))
			if !ok {
				result.Outcome = release.GateUnavailable
				return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
			}
			result.ObservedRows = count
			result.ObservationDigest = digest(rows)
			if check.Minimum != nil && count < *check.Minimum {
				result.Outcome = checkOutcome(check.Severity, true)
				return result, checkError(identity, result.Outcome)
			}
			if check.Maximum != nil && count > *check.Maximum {
				result.Outcome = checkOutcome(check.Severity, true)
				return result, checkError(identity, result.Outcome)
			}
			result.Outcome = release.GateSuccess
			return result, nil
		}
	default:
		return result, gateError(identity, release.GateUnavailable, ErrGateUnavailable, nil)
	}
	if err != nil {
		outcome, gateErr := outcomeForError(identity, err)
		result.Outcome = outcome
		return result, gateErr
	}
	result.ObservedRows = int64(len(rows))
	result.ObservationDigest = digest(rows)
	if len(rows) > 0 {
		result.Outcome = checkOutcome(check.Severity, true)
		return result, checkError(identity, result.Outcome)
	}
	result.Outcome = release.GateSuccess
	return result, nil
}

func checkError(identity string, outcome release.GateOutcome) error {
	if outcome == release.GateWarning {
		return nil
	}
	return gateError(identity, outcome, ErrGateBlocking, nil)
}

func checkOutcome(severity string, failed bool) release.GateOutcome {
	if !failed {
		return release.GateSuccess
	}
	if strings.EqualFold(severity, "warning") {
		return release.GateWarning
	}
	return release.GateBlocking
}

func severity(value string) string {
	if strings.EqualFold(value, "warning") {
		return "warning"
	}
	return "error"
}

func run(ctx context.Context, state *budget, relation, projection, predicate string, args ...[]any) (semanticquery.Rows, error) {
	if predicate == "" {
		predicate = "TRUE"
	}
	limit := state.Bounds.MaxRows
	if limit <= 0 {
		limit = 1
	}
	sql := fmt.Sprintf("SELECT %s AS value FROM %s WHERE %s LIMIT %d", projection, relation, predicate, limit)
	return runPlan(ctx, state, sql, firstArgs(args))
}

func runQualified(ctx context.Context, state *budget, relation, projection, predicate string, args []any) (semanticquery.Rows, error) {
	limit := state.Bounds.MaxRows
	if limit <= 0 {
		limit = 1
	}
	return runPlan(ctx, state, fmt.Sprintf("SELECT %s AS value FROM %s WHERE %s LIMIT %d", projection, relation, predicate, limit), args)
}

func runPlan(ctx context.Context, state *budget, sql string, args []any) (semanticquery.Rows, error) {
	if state.Queries >= state.Bounds.MaxQueries {
		state.QueriesExceeded = true
		return nil, ErrGateBounds
	}
	state.Queries++
	rows, err := currentQuery(ctx, state, sql, args)
	if err != nil {
		return nil, err
	}
	state.Rows += int64(len(rows))
	if state.Rows > state.Bounds.MaxRows {
		state.RowsExceeded = true
		return nil, ErrGateBounds
	}
	return rows, nil
}

func currentQuery(ctx context.Context, state *budget, sql string, args []any) (semanticquery.Rows, error) {
	if state == nil || state.Query == nil {
		return nil, ErrGateUnavailable
	}
	return state.Query(ctx, semanticquery.Plan{SQL: sql, Args: args})
}

func firstArgs(args [][]any) []any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

func gateError(identity string, outcome release.GateOutcome, cause, detail error) error {
	if detail != nil {
		cause = detail
	}
	if cause == nil && outcome == release.GateBlocking {
		cause = ErrGateBlocking
	}
	return &EvaluationError{Identity: identity, Outcome: outcome, Cause: cause}
}

func outcomeForError(identity string, err error) (release.GateOutcome, error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return release.GateTimeout, gateError(identity, release.GateTimeout, context.DeadlineExceeded, err)
	}
	if errors.Is(err, ErrGateBounds) {
		return release.GateUnavailable, gateError(identity, release.GateUnavailable, ErrGateBounds, err)
	}
	return release.GateUnavailable, gateError(identity, release.GateUnavailable, ErrGateUnavailable, err)
}

func impliedChecks(table semanticmodel.Table) []semanticmodel.ModelCheckSpec {
	result := []semanticmodel.ModelCheckSpec{}
	for name, entity := range table.Entities {
		if entity.Type == "primary" || entity.Type == "unique" {
			fields := canonicalFields(entity.Fields)
			result = append(result, semanticmodel.ModelCheckSpec{Type: "unique", Fields: fields, Severity: "error"})
		}
		if name == table.GrainEntity {
			for _, field := range entity.Fields {
				result = append(result, semanticmodel.ModelCheckSpec{Type: "non_null", Field: field, Severity: "error"})
			}
		}
	}
	return result
}

func canonicalChecks(values []semanticmodel.ModelCheckSpec) []semanticmodel.ModelCheckSpec {
	type item struct {
		check semanticmodel.ModelCheckSpec
		key   string
	}
	items := make([]item, 0, len(values))
	for _, check := range values {
		check.Fields = canonicalFields(check.Fields)
		sort.Strings(check.Values)
		key := checkIdentity("", check)
		found := false
		for i := range items {
			if items[i].key == key {
				if severity(items[i].check.Severity) == "warning" && severity(check.Severity) == "error" {
					items[i].check.Severity = "error"
				}
				found = true
				break
			}
		}
		if !found {
			items = append(items, item{check: check, key: key})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	result := make([]semanticmodel.ModelCheckSpec, len(items))
	for i, value := range items {
		result[i] = value.check
	}
	return result
}

func checkIdentity(modelID string, check semanticmodel.ModelCheckSpec) string {
	fields := canonicalFields(check.Fields)
	values := append([]string(nil), check.Values...)
	sort.Strings(values)
	return strings.Join([]string{modelID, check.Type, check.Field, strings.Join(fields, ","), check.To, strings.Join(values, "\x1f"), ptrInt(check.Minimum), ptrInt(check.Maximum)}, "\x00")
}
func ptrInt(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func canonicalFields(values []string) []string {
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	sort.Strings(out)
	return out
}
func validField(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func normalizeCompilerRelation(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 3 {
		return "", false
	}
	quoted := make([]string, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"`))
		if !validField(part) {
			return "", false
		}
		quoted[i] = quoteIdent(part)
	}
	return strings.Join(quoted, "."), true
}
func quoteIdent(value string) string                            { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
func modelRelation(table semanticmodel.Table, id string) string { return modelRelationName(id) }
func modelRelationName(id string) string {
	return `"model".` + quoteIdent(strings.TrimPrefix(id, "model:"))
}
func splitReference(value string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 2 || !validField(parts[0]) || !validField(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type sourceDigestInput struct {
	ID           string
	Connection   string
	Object       string
	Path         string
	Format       string
	LocationType string
	Catalog      string
	Schema       string
	Relation     string
	SchemaMode   string
	Fields       map[string]semanticmodel.SourceField
	Effective    map[string]any
	Freshness    *semanticmodel.SourceFreshnessSpec
}

func sourceDigestInputFrom(id string, source semanticmodel.Source) sourceDigestInput {
	return sourceDigestInput{
		ID: id, Connection: source.Connection, Object: source.Object, Path: source.Path, Format: source.Format,
		LocationType: source.LocationType, Catalog: source.Catalog, Schema: source.SchemaName, Relation: source.RelationName,
		SchemaMode: source.SchemaMode, Fields: source.Fields, Effective: source.EffectiveOptions,
		Freshness: source.Freshness,
	}
}
func durationOf(value semanticmodel.FreshnessDurationSpec) time.Duration {
	switch value.Unit {
	case "second":
		return time.Duration(value.Amount) * time.Second
	case "minute":
		return time.Duration(value.Amount) * time.Minute
	case "hour":
		return time.Duration(value.Amount) * time.Hour
	case "day":
		return time.Duration(value.Amount) * 24 * time.Hour
	}
	return 0
}
func timeValue(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, v); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}
func firstValue(rows semanticquery.Rows, key string) any {
	if len(rows) == 0 {
		return nil
	}
	if value, ok := rows[0][key]; ok {
		return value
	}
	if value, ok := rows[0]["value"]; ok {
		return value
	}
	return nil
}
func int64Value(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int16:
		return int64(v), true
	case int8:
		return int64(v), true
	case uint64:
		if v > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(v), true
	case uint:
		if uint64(v) > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(v), true
	case uint32:
		return int64(v), true
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	}
	return 0, false
}
