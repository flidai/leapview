package query

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSemanticAccessObserverReceivesDetachedDecision(t *testing.T) {
	planner, context := securityPlannerFixture(t, false)
	var seen SemanticAccessObservation
	var serialized []byte
	observer := func(observation SemanticAccessObservation) error {
		seen = observation
		serialized, _ = json.Marshal(observation)
		if len(observation.Grants) > 0 {
			observation.Grants[0] = "mutated-grant"
		}
		if len(observation.AppliedFilters) > 0 {
			observation.AppliedFilters[0].UserAttribute = "mutated-attribute"
		}
		observation.Allowed = false
		return nil
	}
	consumer, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1", observer)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := consumer.Evaluate(SemanticAccessTarget{Dataset: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed {
		t.Fatal("observer mutation changed evaluator decision")
	}
	if len(decision.Predicates) == 0 {
		t.Fatal("expected protected dataset predicate")
	}
	if decision.Grants[0] == "mutated-grant" || decision.AppliedFilters[0].UserAttribute == "mutated-attribute" {
		t.Fatal("observer mutation changed retained evaluator evidence")
	}
	if seen.Target != (SemanticAccessTarget{Dataset: "orders"}) || !seen.Allowed {
		t.Fatalf("observation = %#v", seen)
	}
	if strings.Contains(string(serialized), "Predicates") || strings.Contains(string(serialized), `"us"`) {
		t.Fatalf("observation serialized forbidden decision detail: %s", serialized)
	}

	if _, err := consumer.Planner().Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err != nil {
		t.Fatalf("planner observer callback rejected an allowed plan: %v", err)
	}
}

func TestSemanticAccessObserverReceivesDeniedPlannerDecision(t *testing.T) {
	planner, context := securityPlannerFixture(t, false)
	context.Attributes[0] = semanticAccessAttribute(t, planner.compiled.semanticAccess.definitions["region"], "eu")
	var seen []SemanticAccessObservation
	observer := func(observation SemanticAccessObservation) error {
		seen = append(seen, observation)
		return nil
	}
	consumer, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1", observer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Planner().Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil {
		t.Fatal("denied plan was admitted")
	}
	if len(seen) == 0 || seen[0].Allowed || seen[0].Reason != "required access grant is not satisfied" {
		t.Fatalf("denied observations = %#v", seen)
	}
}

func TestSemanticAccessObserverFailurePreventsPlanAndDiscovery(t *testing.T) {
	planner, context := securityPlannerFixture(t, false)
	wantErr := errors.New("observer unavailable")
	observer := func(SemanticAccessObservation) error { return wantErr }
	consumer, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1", observer)
	if err != nil {
		t.Fatal(err)
	}
	if consumer.Allows(SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("observer failure widened discovery")
	}
	if _, err := consumer.Planner().Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("observer failure did not prevent plan: %v", err)
	}
}

func TestSemanticAccessObserverRedactsEvaluatorErrors(t *testing.T) {
	planner, context := securityPlannerFixture(t, false)
	var seen SemanticAccessObservation
	consumer, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1", func(observation SemanticAccessObservation) error {
		seen = observation
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Evaluate(SemanticAccessTarget{Dataset: "unvalidated-private-selector"}); err == nil {
		t.Fatal("malformed target was accepted")
	}
	if seen.Allowed || seen.Reason != semanticAccessObservationEvaluationError {
		t.Fatalf("malformed target observation = %#v", seen)
	}
	if seen.Target != (SemanticAccessTarget{}) {
		t.Fatal("unvalidated selector entered decision observation")
	}
}

func TestNewSemanticAccessConsumerBoundsObservers(t *testing.T) {
	planner, context := securityPlannerFixture(t, false)
	observer := SemanticAccessDecisionObserver(func(SemanticAccessObservation) error { return nil })
	if _, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1", observer, observer); err == nil {
		t.Fatal("accepted multiple observers")
	}
	if _, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1", nil); err == nil {
		t.Fatal("accepted nil observer")
	}
}
