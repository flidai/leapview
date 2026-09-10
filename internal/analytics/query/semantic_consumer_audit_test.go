package query

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestSemanticConsumerObserverAuditsBoundariesAndInvalidation(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	currentAuthority := authority
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, currentAuthority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var observations []SemanticAccessAuditObservation
	consumer.config.Observer = func(observation SemanticAccessAuditObservation) error {
		observation.EvidenceJSON = append([]byte(nil), observation.EvidenceJSON...)
		observations = append(observations, observation)
		return nil
	}
	if _, err := consumer.Assets(); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "missing"}); err == nil {
		t.Fatal("unknown target accepted")
	}
	plan, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	changed := authority
	changed.Control.State.Revision++
	currentAuthority = changed
	if err := consumer.ValidatePlan(plan); err == nil {
		t.Fatal("stale authority accepted during plan validation")
	}
	if _, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}}); err == nil {
		t.Fatal("stale authority accepted during plan admission")
	}
	if len(observations) == 0 {
		t.Fatal("semantic audit observer was not called")
	}
	var foundDiscovery, foundAuthorization, foundDenied, foundAdmission, foundValidation, foundInvalidation bool
	for _, observation := range observations {
		if observation.Allowed && len(observation.EvidenceJSON) == 0 {
			t.Fatalf("allowed observation lacks evidence: %#v", observation)
		}
		switch observation.Operation {
		case SemanticAccessAuditDiscovery:
			foundDiscovery = true
		case SemanticAccessAuditAuthorization:
			foundAuthorization = true
			if !observation.Allowed {
				foundDenied = true
				if observation.Reason != semanticAccessAuditReasonTargetUnknown {
					t.Fatalf("denied target observation = %#v", observation)
				}
			}
		case SemanticAccessAuditPlanAdmission:
			foundAdmission = true
		case SemanticAccessAuditPlanValidation:
			foundValidation = true
		case SemanticAccessAuditPlanInvalidation:
			foundInvalidation = true
			if observation.Allowed || observation.Reason != semanticAccessAuditReasonAuthorityChanged {
				t.Fatalf("invalidation observation = %#v", observation)
			}
		}
		if strings.Contains(string(observation.EvidenceJSON), "canonicalValues") || strings.Contains(string(observation.EvidenceJSON), "us") {
			t.Fatalf("raw semantic value leaked into evidence: %s", observation.EvidenceJSON)
		}
	}
	if !foundDiscovery || !foundAuthorization || !foundDenied || !foundAdmission || !foundValidation || !foundInvalidation {
		t.Fatalf("boundary observations = %#v", observations)
	}
}

func TestSemanticConsumerAuditFailureBlocksAdmissionAndOutput(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	wantErr := errors.New("audit unavailable")
	consumer.config.Observer = func(SemanticAccessAuditObservation) error { return wantErr }
	if err := consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: SemanticAccessAuditAuthorization, Allowed: true}); err == nil {
		t.Fatal("authorization succeeded after audit failure")
	}
	if _, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}}); err == nil {
		t.Fatal("plan admission succeeded after audit failure")
	}
	consumer.mu.RLock()
	admissions := len(consumer.admissions)
	consumer.mu.RUnlock()
	if admissions != 0 {
		t.Fatalf("audit-failed plan remained admitted: %d", admissions)
	}
	assets, err := consumer.Assets()
	if err == nil || assets != nil {
		t.Fatalf("discovery released after audit failure: assets=%#v err=%v", assets, err)
	}
}

func TestSemanticConsumerAuditFailureBlocksPreviouslyAdmittedValidation(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	plan, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
	if err != nil {
		t.Fatal(err)
	}
	consumer.config.Observer = func(SemanticAccessAuditObservation) error { return errors.New("audit unavailable") }
	if err := consumer.ValidatePlan(plan); err == nil {
		t.Fatal("previously admitted plan released after audit failure")
	}
}

func TestSemanticConsumerRejectsOversizedAuditEvidence(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	consumer.config.Observer = func(SemanticAccessAuditObservation) error { return nil }
	consumer.mu.Lock()
	consumer.auditEvidenceJSON = []byte(strings.Repeat("x", maxSemanticAccessAuditEvidenceBytes+1))
	consumer.mu.Unlock()
	if err := consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: SemanticAccessAuditAuthorization, Allowed: true}); err == nil {
		t.Fatal("oversized audit evidence was released")
	}
}

func TestSemanticConsumerPublicObserverIsUnused(t *testing.T) {
	model := semanticAccessTestModel(t)
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{}
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{Observer: func(SemanticAccessAuditObservation) error {
		called = true
		return errors.New("must not be called")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("public consumer invoked semantic audit observer")
	}
}

func TestSemanticConsumerAuditEvidenceIsStable(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	var first, second []byte
	consumer.config.Observer = func(observation SemanticAccessAuditObservation) error {
		if first == nil {
			first = append([]byte(nil), observation.EvidenceJSON...)
		} else if second == nil {
			second = append([]byte(nil), observation.EvidenceJSON...)
		}
		return nil
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatalf("evidence changed across replay: first=%s second=%s", first, second)
	}
}

func TestSemanticConsumerAuditRecordsHiddenDiscoveryDenials(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, nil)
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	var denied []SemanticAccessAuditObservation
	consumer.config.Observer = func(observation SemanticAccessAuditObservation) error {
		if !observation.Allowed {
			denied = append(denied, observation)
		}
		return nil
	}
	assets, err := consumer.Assets()
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 0 || len(denied) == 0 {
		t.Fatalf("discovery assets/denials = %d/%d", len(assets), len(denied))
	}
	for _, observation := range denied {
		if observation.Operation != SemanticAccessAuditDiscovery || observation.Reason != semanticAccessAuditReasonAccessDenied {
			t.Fatalf("hidden denial = %#v", observation)
		}
	}
}

func TestSemanticConsumerAuditRecordsUnauthorizedMemberPlannerDenial(t *testing.T) {
	allAttributes := semanticAccessEffective(t, semanticAccessDefinitions())
	attributes := make([]access.EffectiveSemanticAttribute, 0, 1)
	for _, attribute := range allAttributes {
		if attribute.DefinitionName == "department" {
			attributes = append(attributes, attribute)
		}
	}
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	var denied *SemanticAccessAuditObservation
	consumer.config.Observer = func(observation SemanticAccessAuditObservation) error {
		if !observation.Allowed && observation.Operation == SemanticAccessAuditPlanAdmission {
			copy := observation
			denied = &copy
		}
		return nil
	}
	if _, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}}); err == nil {
		t.Fatal("unauthorized semantic member was admitted")
	}
	if denied == nil || denied.Target.Dataset != "orders" || denied.Reason != semanticAccessAuditReasonAccessDenied {
		t.Fatalf("planner denial observation = %#v", denied)
	}
}
