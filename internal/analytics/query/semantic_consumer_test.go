package query

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func TestSemanticConsumerAdmissionDoesNotChangeSerializedPlan(t *testing.T) {
	planner, evaluation := securityPlannerFixture(t, false)
	consumer, err := NewSemanticAccessConsumer(planner, evaluation, "principal-1", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Metrics: []Field{{Field: "order_count"}}}
	before, err := planner.Plan(request)
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if err := planir.BindSecurityAdmission(before.IR, consumer.capability); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Fatalf("private admission changed the serialized plan representation\nbefore=%s\nafter=%s", original, after)
	}
}

func semanticAccessConsumerFixture(t *testing.T) (*SemanticAccessConsumer, SemanticAccessEvaluationContext) {
	t.Helper()
	planner, context := securityPlannerFixture(t, false)
	consumer, err := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	return consumer, context
}

func TestSemanticAccessConsumerBindsPlannerAndValidatesExactPlanEnvelope(t *testing.T) {
	consumer, _ := semanticAccessConsumerFixture(t)
	planner := consumer.Planner()
	if planner == nil {
		t.Fatal("Planner() returned nil")
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan(valid): %v", err)
	}
	if !consumer.Allows(SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("matching dataset was not discoverable")
	}
	if consumer.PrincipalID() != "principal-1" || consumer.GenerationIdentity() != "generation-1" {
		t.Fatalf("consumer identity = %q/%q", consumer.PrincipalID(), consumer.GenerationIdentity())
	}

	mutated := plan
	mutated.SQL += " "
	if err := consumer.ValidatePlan(mutated); err == nil || !strings.Contains(err.Error(), "SQL") {
		t.Fatalf("mutated SQL accepted: %v", err)
	}
	mutated = plan
	mutated.Args = append([]any(nil), plan.Args...)
	mutated.Args[0] = "tampered"
	if err := consumer.ValidatePlan(mutated); err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("mutated args accepted: %v", err)
	}
	mutated = plan
	mutated.Columns = append([]string(nil), plan.Columns...)
	mutated.Columns[0] = "tampered"
	if err := consumer.ValidatePlan(mutated); err == nil || !strings.Contains(err.Error(), "columns") {
		t.Fatalf("mutated columns accepted: %v", err)
	}
}

func TestSemanticAccessConsumerRejectsMissingContextIdentityAndForeignPlans(t *testing.T) {
	planner, context := securityPlannerFixture(t, false)
	if _, err := NewSemanticAccessConsumer(nil, context, "principal-1", "generation-1"); err == nil {
		t.Fatal("nil planner was accepted")
	}
	if _, err := NewSemanticAccessConsumer(planner, context, "", "generation-1"); err == nil {
		t.Fatal("empty principal was accepted")
	}
	if _, err := NewSemanticAccessConsumer(planner, context, "principal-1", ""); err == nil {
		t.Fatal("empty generation was accepted")
	}
	invalid := context
	invalid.RegistryState.Digest = "stale"
	if _, err := NewSemanticAccessConsumer(planner, invalid, "principal-1", "generation-1"); err == nil {
		t.Fatal("stale registry context was accepted")
	}
	invalid = context
	invalid.ControlState.Digest = ""
	if _, err := NewSemanticAccessConsumer(planner, invalid, "principal-1", "generation-1"); err == nil {
		t.Fatal("invalid control context was accepted")
	}

	first, _ := NewSemanticAccessConsumer(planner, context, "principal-1", "generation-1")
	second, _ := NewSemanticAccessConsumer(planner, context, "principal-2", "generation-1")
	plan, err := first.Planner().Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), "consumer") {
		t.Fatalf("foreign consumer plan accepted: %v", err)
	}
	if err := first.ValidatePlan(Plan{}); err == nil {
		t.Fatal("missing plan was accepted")
	}
}

func TestNeutralRegistryAwarePlannerCannotVerifyProtectedPlan(t *testing.T) {
	planner, _ := securityPlannerFixture(t, false)
	neutral, err := NewSemanticAccessPlanner(planner.CompiledModel(), SemanticAccessEvaluationContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := neutral.Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil {
		t.Fatal("neutral protected planner verified a plan without runtime context")
	}
}

func TestSemanticAccessConsumerRequiresPrivateAdmissionOnGraphCopies(t *testing.T) {
	consumer, _ := semanticAccessConsumerFixture(t)
	plan, err := consumer.Planner().Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	copyGraph := &planir.Graph{
		NodeMeta: plan.IR.NodeMeta,
		Nodes:    plan.IR.Nodes,
		Roots:    append([]string(nil), plan.IR.Roots...),
		Output:   plan.IR.Output,
	}
	copyPlan := plan
	copyPlan.IR = copyGraph
	if err := consumer.ValidatePlan(copyPlan); err == nil || !strings.Contains(err.Error(), "admitted") {
		t.Fatalf("graph without private admission accepted: %v", err)
	}
	mutatedGraph := *plan.IR
	mutatedGraph.Nodes = make(map[string]planir.Node, len(plan.IR.Nodes))
	for id, node := range plan.IR.Nodes {
		mutatedGraph.Nodes[id] = node
	}
	for id, node := range mutatedGraph.Nodes {
		barrier, ok := node.(planir.SecurityBarrier)
		if !ok {
			continue
		}
		barrier.Policy = "forged-policy"
		mutatedGraph.Nodes[id] = barrier
		break
	}
	forgedPlan := plan
	forgedPlan.IR = &mutatedGraph
	if err := consumer.ValidatePlan(forgedPlan); err == nil || !strings.Contains(err.Error(), "admitted") {
		t.Fatalf("forged barrier accepted: %v", err)
	}

	withTotal, err := planir.WithTotalRows(plan.IR, "__total_rows")
	if err != nil {
		// Aggregate plans are not valid total-row inputs; this assertion is
		// covered separately by the row-plan shape below.
		return
	}
	if reflect.ValueOf(withTotal).Pointer() == reflect.ValueOf(plan.IR).Pointer() {
		t.Fatal("WithTotalRows mutated the source graph")
	}
}

func TestSemanticAccessConsumerAcceptsAdmittedTotalRowsRewrite(t *testing.T) {
	consumer, _ := semanticAccessConsumerFixture(t)
	plan, err := consumer.Planner().PlanRows(RowRequest{
		Dataset: "orders", Dimensions: []Field{{Field: "orders.status"}}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	withTotal, err := planir.WithTotalRows(plan.IR, "__total_rows")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := planir.RenderDuckDB(withTotal)
	if err != nil {
		t.Fatal(err)
	}
	admitted := Plan{IR: withTotal, SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns}
	if err := consumer.ValidatePlan(admitted); err != nil {
		t.Fatalf("ValidatePlan(total rows): %v", err)
	}
	mutated := *plan.IR
	mutated.Nodes = make(map[string]planir.Node, len(plan.IR.Nodes))
	for id, node := range plan.IR.Nodes {
		mutated.Nodes[id] = node
	}
	sortNode := mutated.Nodes[mutated.Output].(planir.SortLimit)
	sortNode.Limit++
	mutated.Nodes[mutated.Output] = sortNode
	if err := mutated.Validate(); err != nil {
		t.Fatalf("mutated row graph should remain structurally valid: %v", err)
	}
	if _, err := planir.WithTotalRows(&mutated, "__total_rows"); err == nil || !strings.Contains(err.Error(), "changed after binding") {
		t.Fatalf("mutated admitted graph was eligible for total-row rewrite: %v", err)
	}
}

func TestSemanticAccessConsumerRejectsRerenderedProjectionMutation(t *testing.T) {
	consumer, _ := semanticAccessConsumerFixture(t)
	plan, err := consumer.Planner().PlanRows(RowRequest{
		Dataset: "orders", Dimensions: []Field{{Field: "orders.status"}}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutated := *plan.IR
	mutated.Nodes = make(map[string]planir.Node, len(plan.IR.Nodes))
	for id, node := range plan.IR.Nodes {
		mutated.Nodes[id] = node
	}
	sortNode := mutated.Nodes[mutated.Output].(planir.SortLimit)
	if len(sortNode.Projection) == 0 || len(sortNode.AvailableFields) == 0 {
		t.Fatal("row plan did not contain a projection")
	}
	sortNode.Projection = append([]planir.Projection(nil), sortNode.Projection...)
	sortNode.Projection[0].Name = "tampered_projection"
	sortNode.AvailableFields = append([]planir.Field(nil), sortNode.AvailableFields...)
	sortNode.AvailableFields[0].Name = "tampered_projection"
	mutated.Nodes[mutated.Output] = sortNode
	mutated.AvailableFields = append([]planir.Field(nil), mutated.AvailableFields...)
	mutated.AvailableFields[0].Name = "tampered_projection"
	if err := mutated.Validate(); err != nil {
		t.Fatalf("mutated projection graph should remain structurally valid: %v", err)
	}
	mutatedPlan := plan
	mutatedPlan.IR = &mutated
	rendered, err := planir.RenderDuckDB(&mutated)
	if err != nil {
		t.Fatal(err)
	}
	mutatedPlan.SQL, mutatedPlan.Args, mutatedPlan.Columns = rendered.SQL, rendered.Args, rendered.Columns
	if err := consumer.ValidatePlan(mutatedPlan); err == nil || !strings.Contains(err.Error(), "admitted") {
		t.Fatalf("rerendered projection mutation was accepted: %v", err)
	}
	if err := planir.BindSecurityAdmission(&mutated, consumer.capability); err == nil {
		t.Fatal("rebinding blessed the mutated projection")
	}
}
