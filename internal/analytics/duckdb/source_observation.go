package duckdb

import (
	"context"
	"fmt"
	"strings"
	"time"

	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// SourceObservations captures all source evidence while the resolved source
// session remains live. The returned record contains schemas, bounded check
// evidence, and target-owned timestamps/revision tokens; no relation text or
// credentials leave this seam.
func (p *PreparedSources) SourceObservations(ctx context.Context) ([]analyticsmaterialize.SourceObservation, error) {
	if p == nil || p.session == nil {
		return nil, fmt.Errorf("prepared source session is unavailable")
	}
	ids := sortedKeys(p.model.Sources)
	result := make([]analyticsmaterialize.SourceObservation, 0, len(ids))
	budget := analyticsmaterialize.ObservationBudgetFromContext(ctx)
	started := time.Now()
	if budget.MaxMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(budget.MaxMillis)*time.Millisecond)
		defer cancel()
	}
	queries := 0
	observedRows := int64(0)
	for _, id := range ids {
		if budget.MaxQueries > 0 && queries >= budget.MaxQueries || budget.MaxMillis > 0 && time.Since(started).Milliseconds() >= budget.MaxMillis {
			failure := analyticsmaterialize.ObservationBounds
			if budget.MaxMillis > 0 && (ctx.Err() != nil || time.Since(started).Milliseconds() >= budget.MaxMillis) {
				failure = analyticsmaterialize.ObservationTimeout
			}
			for _, remaining := range ids[len(result):] {
				result = append(result, analyticsmaterialize.SourceObservation{ID: remaining, SchemaFailure: failure})
			}
			break
		}
		sourceStarted := time.Now()
		sourceQueries := 0
		sourceRows := int64(0)
		source := p.model.Sources[id]
		relation := p.relationQueries[id]
		if relation == "" {
			result = append(result, analyticsmaterialize.SourceObservation{ID: id, SchemaFailure: analyticsmaterialize.ObservationUnavailable})
			continue
		}
		// Re-describe the prepared relation on this still-live session. The
		// schema carried on the semantic model is only a refresh aid; the gate
		// evidence must reflect the exact target relation acquired for this
		// candidate.
		columns, err := describeRelationSchema(ctx, p.session, relation)
		if err != nil {
			failure := sourceObservationFailure(ctx, budget, err)
			queries++
			result = append(result, analyticsmaterialize.SourceObservation{ID: id, SchemaFailure: failure, ObservationQueries: 1, ObservationMillis: time.Since(sourceStarted).Milliseconds()})
			if failure == analyticsmaterialize.ObservationTimeout || failure == analyticsmaterialize.ObservationBounds {
				for _, remaining := range ids[len(result):] {
					result = append(result, analyticsmaterialize.SourceObservation{ID: remaining, SchemaFailure: failure})
				}
				break
			}
			continue
		}
		queries++
		sourceQueries++
		sourceRows += int64(len(columns))
		observedRows += int64(len(columns))
		observation := analyticsmaterialize.SourceObservation{ID: id, Schema: append([]semanticmodel.ColumnSchema(nil), columns...)}
		observation.ObservationQueries = sourceQueries
		observation.ObservationRows = sourceRows
		if source.Freshness != nil && source.Freshness.Basis == "revision" {
			// The typed revision contract is a canonical UTC timestamp and thus
			// supplies the observation used for freshness age. Connectors may
			// replace it with equivalent target metadata when available.
			observation.Revision = source.Freshness.Revision
			if source.Freshness.RevisionAt != nil {
				observation.RevisionObserved = source.Freshness.RevisionAt.UTC()
				observation.FreshnessObserved = observation.RevisionObserved
			}
		} else if source.Freshness != nil && source.Freshness.Basis == "field" {
			field := source.Freshness.Field
			if relation == "" || field == "" {
				observation.FreshnessFailure = analyticsmaterialize.ObservationUnavailable
				observation.ObservationMillis = time.Since(sourceStarted).Milliseconds()
				result = append(result, observation)
				continue
			}
			if budget.MaxQueries > 0 && queries >= budget.MaxQueries || budget.MaxMillis > 0 && time.Since(started).Milliseconds() >= budget.MaxMillis {
				failure := analyticsmaterialize.ObservationBounds
				if budget.MaxMillis > 0 && (ctx.Err() != nil || time.Since(started).Milliseconds() >= budget.MaxMillis) {
					failure = analyticsmaterialize.ObservationTimeout
				}
				observation.FreshnessFailure = failure
				observation.ObservationMillis = time.Since(sourceStarted).Milliseconds()
				result = append(result, observation)
				for _, remaining := range ids[len(result):] {
					result = append(result, analyticsmaterialize.SourceObservation{ID: remaining, SchemaFailure: failure})
				}
				break
			}
			row := p.session.QueryRowContext(ctx, "SELECT MAX(\""+strings.ReplaceAll(field, "\"", "\"\"")+"\") FROM ("+relation+")")
			var value any
			if err := row.Scan(&value); err != nil {
				observation.FreshnessFailure = sourceObservationFailure(ctx, budget, err)
				queries++
				observation.ObservationQueries++
				observation.ObservationMillis = time.Since(sourceStarted).Milliseconds()
				result = append(result, observation)
				continue
			}
			queries++
			sourceQueries++
			observation.ObservationQueries++
			observation.FreshnessObserved = sourceObservationTime(value)
			observation.FreshnessEmpty = value == nil
		}
		if len(source.Checks) > 0 {
			evaluator := analyticsmaterialize.SourceCheckEvaluatorFromContext(ctx)
			if evaluator == nil {
				return nil, fmt.Errorf("source %q checks require a qualified evaluator", id)
			}
			refs := make(map[string]string, len(p.relationQueries))
			for name, sourceRelation := range p.relationQueries {
				refs[name] = sourceRelation
			}
			maxQueries := budget.MaxQueries
			if maxQueries <= 0 {
				maxQueries = 128
			}
			remainingQueries := maxQueries - queries
			maxRows := budget.MaxRows
			if maxRows <= 0 {
				maxRows = 10000
			}
			remainingRows := maxRows - observedRows
			maxMillis := budget.MaxMillis
			if maxMillis <= 0 {
				maxMillis = 5000
			}
			remainingMillis := maxMillis - time.Since(started).Milliseconds()
			if remainingQueries <= 0 || remainingRows <= 0 || remainingMillis <= 0 {
				return nil, fmt.Errorf("source %q check observation budget exhausted", id)
			}
			checks, checkErr := evaluator(ctx, id, relation, source.Checks, refs, analyticsmaterialize.ObservationBudget{MaxQueries: remainingQueries, MaxRows: remainingRows, MaxMillis: remainingMillis}, func(queryCtx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
				rows, err := p.session.QueryContext(queryCtx, plan.SQL, plan.Args...)
				if err != nil {
					return nil, err
				}
				defer rows.Close()
				result := semanticquery.Rows{}
				for rows.Next() {
					var value any
					if err := rows.Scan(&value); err != nil {
						return nil, err
					}
					result = append(result, semanticquery.Row{plan.Columns[0]: value})
				}
				return result, rows.Err()
			})
			if checkErr != nil {
				return nil, fmt.Errorf("source %q checks: %w", id, checkErr)
			}
			observation.CheckEvidence = checks
			for _, check := range checks {
				observation.ObservationQueries += check.Queries
				observation.ObservationRows += check.ObservedRows
				queries += check.Queries
				observedRows += check.ObservedRows
			}
		}
		result = append(result, observation)
		result[len(result)-1].ObservationMillis = time.Since(sourceStarted).Milliseconds()
	}
	return result, nil
}
