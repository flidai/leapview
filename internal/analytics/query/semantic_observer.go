package query

import "fmt"

// SemanticAccessObservation is the redacted projection of one semantic-access
// decision. It intentionally excludes predicates, attribute values, and SQL.
// The slices are detached from the evaluator's decision before delivery to an
// observer, so an observer cannot alter the decision used by query planning.
type SemanticAccessObservation struct {
	Target         SemanticAccessTarget
	Allowed        bool
	Grants         []string
	GrantOutcomes  []SemanticAccessGrantEvidence
	AppliedFilters []SemanticAccessFilterEvidence
	Reason         string
}

// SemanticAccessDecisionObserver receives one redacted semantic-access
// decision. Returning an error fails closed at the observing query boundary.
type SemanticAccessDecisionObserver func(SemanticAccessObservation) error

const semanticAccessObservationEvaluationError = "semantic access evaluation failed"

func observeSemanticAccessDecision(observer SemanticAccessDecisionObserver, target SemanticAccessTarget, decision SemanticAccessDecision, evaluationErr error) error {
	if observer == nil {
		return nil
	}
	reason := decision.Reason
	if evaluationErr != nil {
		// An invalid selector is not a resolved semantic identity. Do not copy
		// unvalidated request text into audit evidence. The strict durable
		// observer will reject this incomplete target and the consumer fails
		// closed; no successful semantic decision is claimed for it.
		reason = semanticAccessObservationEvaluationError
		target = SemanticAccessTarget{}
	}
	observation := SemanticAccessObservation{
		Target:         target,
		Allowed:        decision.Allowed,
		Grants:         append([]string(nil), decision.Grants...),
		GrantOutcomes:  append([]SemanticAccessGrantEvidence(nil), decision.GrantOutcomes...),
		AppliedFilters: append([]SemanticAccessFilterEvidence(nil), decision.AppliedFilters...),
		Reason:         reason,
	}
	if err := observer(observation); err != nil {
		return fmt.Errorf("semantic access decision observer: %w", err)
	}
	return nil
}
