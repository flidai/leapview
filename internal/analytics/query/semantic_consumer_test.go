package query

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSemanticConsumerDiscoveryAndPlannerUseSameDecision(t *testing.T) {
	model := semanticAccessTestModel(t)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var previous []byte
	for i := 0; i < 5; i++ {
		assets, err := consumer.Assets()
		if err != nil {
			t.Fatal(err)
		}
		if len(assets) == 0 {
			t.Fatal("empty discovery")
		}
		encoded, err := json.Marshal(assets)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && !bytes.Equal(previous, encoded) {
			t.Fatal("unstable discovery")
		}
		previous = encoded
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders", Metric: "revenue"}); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "unknown"}); err == nil {
		t.Fatal("unknown asset admitted")
	}
	plan, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.SQL = "SELECT * FROM orders"
	if err := consumer.ValidatePlan(plan); err == nil {
		t.Fatal("unadmitted SQL accepted")
	}
	authority.Control.State.Revision++
	if _, err := consumer.Assets(); err == nil {
		t.Fatal("stale authority accepted for discovery")
	}
}

func TestSemanticConsumerMissingAuthorityDoesNotBecomePublic(t *testing.T) {
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{}); err == nil {
		t.Fatal("missing authority accepted")
	}
	public := semanticAccessTestModel(t)
	public.AccessPolicy.AccessGrants = nil
	public.AccessPolicy.Datasets = nil
	public.AccessPolicy.Dimensions = nil
	public.AccessPolicy.Metrics = nil
	planner, err = NewCompiledPlanner(public)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Metric: "missing"}); err == nil {
		t.Fatal("unknown public asset admitted")
	}
}
