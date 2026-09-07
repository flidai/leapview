package query

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func TestSemanticConsumerAdmissionRejectsCrossConsumerAndRerenderedMutation(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	plan, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	other := newDiscoveryConsumer(t, snapshot, authority)
	if err := other.ValidatePlan(plan); err == nil {
		t.Fatal("another consumer accepted admitted plan")
	}
	copyPlan := plan
	graphCopy := *plan.IR
	copyPlan.IR = &graphCopy
	if err := consumer.ValidatePlan(copyPlan); err == nil {
		t.Fatal("copied graph accepted without admission")
	}
	output, ok := plan.IR.Nodes[plan.IR.Output].(planir.SortLimit)
	if !ok {
		t.Fatalf("output = %T", plan.IR.Nodes[plan.IR.Output])
	}
	output.Limit = 10
	plan.IR.Nodes[plan.IR.Output] = output
	rendered, err := planir.RenderDuckDB(plan.IR)
	if err != nil {
		t.Fatal(err)
	}
	plan.SQL, plan.Args, plan.Columns = rendered.SQL, rendered.Args, rendered.Columns
	if err := consumer.ValidatePlan(plan); err == nil {
		t.Fatal("re-rendered graph mutation accepted")
	}
}

func TestSemanticConsumerDoesNotMixValidDecisions(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID, Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
		return snapshot, authority, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Assets(); err != nil {
		t.Fatal(err)
	}
	// Another internally consistent snapshot is not the original request's
	// authority. The consumer must not combine decisions across observations.
	snapshot, authority = semanticAccessSnapshot(t, nil)
	if _, err := consumer.Assets(); err == nil {
		t.Fatal("changed valid decision mixed into discovery")
	}
	if _, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}}); err == nil {
		t.Fatal("changed decision admitted a plan")
	}
}

func TestSemanticConsumerDiscoveryPlannerCannotBecomeExecutionCapability(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewSemanticAccessDiscovery(planner.CompiledModel(), SemanticAccessConsumerConfig{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID, Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
		return snapshot, authority, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Assets(); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Planner().Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}}); err == nil {
		t.Fatal("discovery-only consumer emitted executable plan")
	}
}
