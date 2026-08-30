package deploymentpostgres

import (
	"encoding/json"
	"testing"
	"time"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// nativePlanFixture turns the relational plan projections used by native
// admission tests into the complete immutable execution document required by
// the PostgreSQL authority. The rich plan owns PlanDigest; callers must use
// the returned value when constructing dependent candidate/attempt evidence.
func nativePlanFixture(t *testing.T, input deploymentnative.PlanInput, projectID string) deploymentnative.PlanInput {
	t.Helper()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	plan, err := deploymentdomain.NewDeliveryPlan(deploymentdomain.DeliveryPlan{
		ID: input.PlanID, TargetID: input.TargetID, ProjectID: projectgraph.ResourceID(projectID), Environment: "prod",
		Operation: deploymentdomain.DeliveryOperationCodeChange, SourceDigest: input.ArtifactDigest,
		Execution: deploymentdomain.DeliveryExecutionInputs{
			SourceArtifactDigest: input.ArtifactDigest,
			CompilerDigest:       input.CompiledGraphDigest,
			ExecutableDigest:     admissionDigest('4'),
			DependencyDigest:     admissionDigest('5'),
			ConfigDigest:         input.CompiledConfigDigest,
			BindingDigest:        input.SecurityDomainFingerprint,
			RuntimeDigest:        admissionDigest('0'),
			CapabilityDigest:     admissionDigest('9'),
		},
		Provenance: deploymentdomain.DeliveryProvenance{Builder: "native-test"},
		Governance: deploymentdomain.DeliveryGovernance{
			PolicyDigest: admissionDigest('2'), AuthorizationDigest: input.SecurityDomainFingerprint,
			QualificationDigest: input.QualificationDigest, ExpiresAt: now.Add(time.Hour),
		},
		Evidence: deploymentdomain.DeliveryPlanEvidence{
			ImpactStatement:       "native test plan impact",
			PhysicalWorkStatement: "native test physical work",
			ReuseStatement:        "native test does not reuse physical state",
			Qualification: deploymentdomain.DeliveryQualificationEvidence{
				Policy: "native exact snapshot qualification",
				Steps:  []deploymentdomain.DeliveryQualificationStep{{ID: "snapshot", Kind: "contract", Description: "qualify exact snapshot", Required: true, Blocking: true}},
			},
			StalePolicy: deploymentdomain.DeliveryStalePolicy{Mode: "reject"},
			Rollback:    deploymentdomain.DeliveryRollbackEvidence{Class: deploymentdomain.DeliveryServingSafe},
		},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("construct native plan fixture: %v", err)
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal native plan fixture: %v", err)
	}
	input.PlanDigest = plan.Digest
	input.PlanDocument = document
	input.QualificationRequired = true
	return input
}
