package module

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
)

func policyImpactGraph(t *testing.T) projectgraph.ProjectGraph {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "source_orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "dashboard_sales", Kind: projectgraph.KindDashboard, Name: "sales"},
		{ID: "model_unrelated", Kind: projectgraph.KindModel, Name: "unrelated"},
	}, []projectgraph.Edge{
		{From: "model_orders", To: "source_orders"},
		{From: "dashboard_sales", To: "model_orders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func policyPlanPublication(t *testing.T) identityledger.ContractPublication {
	t.Helper()
	makePublication := func(version string, extra bool) identityledger.ContractPublication {
		var document map[string]any
		if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source_orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"/private/orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{"id":{"datatype":"String","nullable":false}}}}}`), &document); err != nil {
			t.Fatal(err)
		}
		if extra {
			document["spec"].(map[string]any)["schema"].(map[string]any)["fields"].(map[string]any)["note"] = map[string]any{"datatype": "String", "nullable": true}
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		var source projectcontracts.Source
		if err := json.Unmarshal(encoded, &source); err != nil {
			t.Fatal(err)
		}
		projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: version, Compatibility: "backward"})
		if err != nil {
			t.Fatal(err)
		}
		publication, err := identityledger.PrepareContractPublication(identityledger.ContractPublicationInput{
			InstanceID: "instance_plan", Projection: projection,
			Validation: identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{{Name: "projection", Outcome: identityledger.ValidationPassed, Reference: "test"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return publication
	}
	baseline, candidate := makePublication("1.0.0", false), makePublication("1.1.0", true)
	return attachPlanPolicyEvidence(t, baseline, candidate)
}

func attachPlanPolicyEvidence(t *testing.T, baseline, candidate identityledger.ContractPublication) identityledger.ContractPublication {
	t.Helper()
	classification, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	baselineIdentity := identityledger.PolicyPublicationIdentity{
		InstanceID: baseline.InstanceID, AuthoredID: baseline.AuthoredID, ResourceKind: baseline.ResourceKind,
		Version: baseline.Version, VersionBaseline: baseline.VersionBaseline, ProjectionProfile: baseline.ProjectionProfile, Digest: baseline.Digest,
	}
	evidence, err := identityledger.NewPolicyEvidence(identityledger.PolicyContext{
		BaselineKind: identityledger.PolicyBaselineExisting, Baseline: &baselineIdentity, ExpectedLifecycleSequence: 1,
	}, &baseline, candidate, 1, "bundle_plan", classification)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Validation.PolicyEvidence = &evidence
	return candidate
}

func TestContractPolicyApprovalInputPreservesSecurityWidening(t *testing.T) {
	makePublication := func(version string, widen bool) identityledger.ContractPublication {
		var document map[string]any
		if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic_sales","name":"sales"},"spec":{"datasets":{"orders":{"model":"orders","requiredAccessGrants":["region_access"]}},"accessGrants":{"region_access":{"userAttribute":"region","allowedValues":["emea"]}},"dimensions":{},"filters":{},"metrics":{}}}`), &document); err != nil {
			t.Fatal(err)
		}
		if widen {
			document["spec"].(map[string]any)["accessGrants"].(map[string]any)["region_access"].(map[string]any)["allowedValues"] = []any{"emea", "amer"}
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		var source projectcontracts.SemanticModel
		if err := json.Unmarshal(encoded, &source); err != nil {
			t.Fatal(err)
		}
		projection, err := contractprojection.ProjectSemanticModel(source, contractprojection.Contract{Version: version, Compatibility: "backward"})
		if err != nil {
			t.Fatal(err)
		}
		publication, err := identityledger.PrepareContractPublication(identityledger.ContractPublicationInput{
			InstanceID: "instance_plan", Projection: projection,
			Validation: identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{{Name: "projection", Outcome: identityledger.ValidationPassed, Reference: "test"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return publication
	}
	publication := attachPlanPolicyEvidence(t, makePublication("1.0.0", false), makePublication("1.1.0", true))
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "dashboard_sales", Kind: projectgraph.KindDashboard, Name: "sales_dashboard"},
	}, []projectgraph.Edge{{From: "dashboard_sales", To: "semantic_sales"}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanContractPolicyEvidence("instance_plan", publication, graph)
	if err != nil {
		t.Fatal(err)
	}
	input, err := plan.ApprovalInput()
	if err != nil {
		t.Fatal(err)
	}
	if !input.Decision.ApprovalRequired || input.Decision.ApprovalState != identityledger.PolicyApprovalRequired || input.Decision.Classification.SecurityImpact != contractversion.SecurityWidening || len(input.Decision.ChangedDimensions) == 0 {
		t.Fatalf("widening signal lost: %#v", input)
	}
	input.Decision.ApprovalRequired = false
	again, err := plan.ApprovalInput()
	if err != nil || !again.Decision.ApprovalRequired {
		t.Fatalf("caller cleared retained approval requirement: %v", err)
	}
}

func TestContractPolicyPlanPreservesEvidenceAndDetachesApprovalInput(t *testing.T) {
	publication := policyPlanPublication(t)
	graph := policyImpactGraph(t)
	plan, err := PlanContractPolicyEvidence("instance_plan", publication, graph)
	if err != nil {
		t.Fatal(err)
	}
	want, err := publication.PolicyDecision()
	if err != nil {
		t.Fatal(err)
	}
	input, err := plan.ApprovalInput()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Decision, want) || input.GraphDigest != graph.Digest() || len(input.DependentResources) != 2 {
		t.Fatalf("approval input lost publication/graph evidence: %#v", input)
	}
	input.Decision.Classification.Changes[0].Path = "mutated"
	input.DependentResources[0].AuthoredID = "mutated"
	publication.Validation.PolicyEvidence.Classification.Changes[0].Path = "mutated original"
	again, err := plan.ApprovalInput()
	if err != nil || !reflect.DeepEqual(again.Decision, want) || again.DependentResources[0].AuthoredID != "dashboard_sales" {
		t.Fatalf("caller mutation escaped planning boundary: %#v, %v", again, err)
	}
}

func TestContractPolicyPlanRejectsUnqualifiedEvidence(t *testing.T) {
	publication := policyPlanPublication(t)
	graph := policyImpactGraph(t)
	if _, err := PlanContractPolicyEvidence("other_instance", publication, graph); err == nil {
		t.Fatal("cross-instance publication admitted")
	}
	publication.Validation.PolicyEvidence.EvidenceDigest = ""
	if _, err := PlanContractPolicyEvidence("instance_plan", publication, graph); err == nil {
		t.Fatal("missing evidence digest admitted")
	}
	publication.Validation.PolicyEvidence = nil
	if _, err := PlanContractPolicyEvidence("instance_plan", publication, graph); err == nil {
		t.Fatal("historical checks-only publication admitted")
	}
	if _, err := (ContractPolicyPlan{}).ApprovalInput(); err == nil {
		t.Fatal("zero plan produced approval input")
	}
}

func policyPlanClassification(t *testing.T) contractversion.Result {
	t.Helper()
	baseline := []byte(`{"apiVersion":"leapview.dev/v1","profile":"leapview.contract/v1","kind":"Source","metadata":{"id":"source_orders","contract":{"version":"1.0.0","compatibility":"backward"}},"contract":{"schema":{"fields":{"id":{"datatype":"String","nullable":false}}}}}`)
	var candidate map[string]any
	if err := json.Unmarshal(baseline, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["metadata"].(map[string]any)["contract"].(map[string]any)["version"] = "1.1.0"
	candidate["contract"].(map[string]any)["schema"].(map[string]any)["fields"].(map[string]any)["note"] = map[string]any{"datatype": "String", "nullable": true}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	result, err := contractversion.ValidateVersionTransition(baseline, encoded)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestContractPolicyImpactUsesExistingDependencyGraph(t *testing.T) {
	graph := policyImpactGraph(t)
	classification := policyPlanClassification(t)
	direct, dependents, err := contractPolicyImpact(graph, "source_orders", projectgraph.KindSource, classification)
	if err != nil {
		t.Fatal(err)
	}
	if direct.AuthoredID != "source_orders" || direct.Kind != projectgraph.KindSource {
		t.Fatalf("direct resource evidence = %#v", direct)
	}
	var affected []string
	for _, resource := range dependents {
		affected = append(affected, resource.AuthoredID.String())
	}
	if !reflect.DeepEqual(affected, []string{"dashboard_sales", "model_orders"}) {
		t.Fatalf("dependent resources = %v", affected)
	}
	secondDirect, secondDependents, err := contractPolicyImpact(graph, "source_orders", projectgraph.KindSource, classification)
	if err != nil || direct != secondDirect || !reflect.DeepEqual(dependents, secondDependents) {
		t.Fatalf("planning is not deterministic: %v", err)
	}
}

func TestContractPolicyImpactRejectsMissingOrWrongAuthority(t *testing.T) {
	graph := policyImpactGraph(t)
	classification := policyPlanClassification(t)
	for _, input := range []struct {
		id     projectgraph.ResourceID
		kind   projectgraph.Kind
		result contractversion.Result
	}{
		{"missing", projectgraph.KindSource, classification},
		{"source_orders", projectgraph.KindModel, classification},
		{"source_orders", projectgraph.KindSource, contractversion.Result{}},
	} {
		if _, _, err := contractPolicyImpact(graph, input.id, input.kind, input.result); err == nil {
			t.Fatalf("invalid planning input accepted: %#v", input)
		}
	}
}
