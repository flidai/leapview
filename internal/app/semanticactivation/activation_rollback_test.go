package semanticactivation

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRollbackUsesImmutableHistoricalPublicationEvidence(t *testing.T) {
	baseline := activationSemanticPublication(t, "1.0.0", `["east"]`)
	candidate := activationSemanticPublication(t, "1.1.0", `["east","west"]`)
	policy, err := contractpublication.DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	approval, err := contractpublication.PrepareWideningApproval(policy, "principal:reviewer", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	context := contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineExisting, Existing: &baseline}
	qualified, err := contractpublication.AttachPolicyEvidence(context, candidate, policy, &approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePublicationForActivation(context, qualified, now, false); !errors.Is(err, contractpublication.ErrApprovalExpired) {
		t.Fatalf("forward activation with expired approval error = %v, want approval expired", err)
	}
	if err := validatePublicationForActivation(context, qualified, now, true); err != nil {
		t.Fatalf("rollback rejected immutable historical approval: %v", err)
	}
	freshApproval, err := contractpublication.PrepareWideningApproval(policy, "principal:rollback-reviewer", now.Add(-time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	freshRollback, err := contractpublication.AttachPolicyEvidence(context, candidate, policy, &freshApproval)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePublicationForActivation(context, freshRollback, now, true); err != nil {
		t.Fatalf("freshly approved rollback rejected: %v", err)
	}

	tampered := qualified.Clone()
	tampered.Validation.PolicyEvidence.Candidate.Digest = baseline.Digest
	if err := validatePublicationForActivation(context, tampered, now, true); err == nil {
		t.Fatal("rollback accepted mismatched historical publication evidence")
	}
}

func activationSemanticPublication(t *testing.T, version, allowed string) contractpublication.ContractPublication {
	t.Helper()
	var authored projectcontracts.SemanticModel
	raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders_semantic"},"spec":{"datasets":{"orders":{"model":"orders_model","requiredAccessGrants":["region"],"accessFilters":[{"field":"region","userAttribute":"region"}]}},"accessGrants":{"region":{"userAttribute":"region","allowedValues":` + allowed + `}},"dimensions":{"region":{"datatype":"String","bindings":{"orders":{"field":"orders.region"}},"requiredAccessGrants":["region"]}},"metrics":{"orders":{"type":"aggregate","dataset":"orders","aggregation":"count","input":{"field":"orders.id"},"requiredAccessGrants":["region"]}}}}`
	if err := json.Unmarshal([]byte(raw), &authored); err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "model:orders", Name: "orders_model", Kind: projectgraph.KindModel}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	references, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSemanticModel(authored, contractprojection.Contract{Version: version, Compatibility: "backward"}, references)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := contractpublication.Prepare(contractpublication.ContractPublicationInput{
		InstanceID: "instance:test", Projection: projection,
		Validation: contractpublication.ValidationEvidence{Version: contractpublication.ValidationEvidenceVersion, Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "activation-rollback-test"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}
