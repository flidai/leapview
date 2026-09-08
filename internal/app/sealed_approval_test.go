package app

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func sealedApprovalDigest(seed byte) string {
	return deployment.CanonicalDeliveryDigest([]byte{seed})
}

func sealedApprovalPlan(t *testing.T) deployment.DeliveryPlan {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	digest := sealedApprovalDigest
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: "plan-1", TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod",
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: digest('a'),
		Execution: deployment.DeliveryExecutionInputs{
			SourceArtifactDigest: digest('a'), CompilerDigest: digest('b'), ExecutableDigest: digest('c'),
			DependencyDigest: digest('d'), ConfigDigest: digest('e'), BindingDigest: digest('f'),
			RuntimeDigest: digest('g'), CapabilityDigest: digest('h'),
		},
		Provenance: deployment.DeliveryProvenance{Builder: "test"},
		Governance: deployment.DeliveryGovernance{
			PolicyDigest: digest('i'), AuthorizationDigest: digest('j'), QualificationDigest: digest('k'),
			ExpiresAt: now.Add(time.Hour), RequiresApproval: true,
		},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement: "impact", PhysicalWorkStatement: "work", ReuseStatement: "reuse",
			Qualification: deployment.DeliveryQualificationEvidence{Policy: "protected", Steps: []deployment.DeliveryQualificationStep{{ID: "contracts", Kind: "contract", Description: "validate", Required: true, Blocking: true}}},
			StalePolicy:   deployment.DeliveryStalePolicy{Mode: "reject"},
			Rollback:      deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe},
		},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func sealedApprovalPublication(t *testing.T, plan deployment.DeliveryPlan) deployment.PublicationIntent {
	t.Helper()
	publication, err := deployment.NewPublicationIntent(deployment.PublicationIntent{
		ID: "publication-1", RequestDigest: sealedApprovalDigest('z'), TargetID: plan.TargetID,
		ProjectID: plan.ProjectID, Environment: plan.Environment, PlanID: plan.ID, PlanDigest: plan.Digest,
		CandidateID: "candidate-1", GenerationID: "generation-1", ExpectedTargetRevision: 0, CreatedAt: plan.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestValidateSealedPublicationPlanBinding(t *testing.T) {
	plan := sealedApprovalPlan(t)
	publication := sealedApprovalPublication(t, plan)
	binding := sealedcontrol.SealBinding{
		DeploymentID: publication.ID, ProjectID: publication.ProjectID.String(), Environment: publication.Environment,
		TargetID: publication.TargetID, CandidateID: publication.CandidateID, GenerationID: publication.GenerationID, PlanDigest: plan.Digest,
	}
	now := plan.CreatedAt.Add(30 * time.Minute)
	if err := validateSealedPublicationPlanBinding(plan, binding, publication, now); err != nil {
		t.Fatalf("exact plan binding rejected: %v", err)
	}
	if err := validateSealedPublicationPlanBinding(plan, binding, publication, plan.Governance.ExpiresAt); !errors.Is(err, deployment.ErrDeliveryPlanExpired) {
		t.Fatalf("expiry boundary error = %v, want ErrDeliveryPlanExpired", err)
	}

	tests := map[string]struct {
		plan        deployment.DeliveryPlan
		binding     sealedcontrol.SealBinding
		publication deployment.PublicationIntent
	}{
		"tampered plan evidence": {
			plan: func() deployment.DeliveryPlan {
				copy := plan
				copy.Evidence.ImpactStatement = "tampered"
				return copy
			}(), binding: binding, publication: publication,
		},
		"stale publication plan digest": {
			plan: plan, binding: binding,
			publication: func() deployment.PublicationIntent {
				copy := publication
				copy.PlanDigest = sealedApprovalDigest('q')
				return copy
			}(),
		},
		"scope substitution": {
			plan: plan,
			binding: func() sealedcontrol.SealBinding {
				copy := binding
				copy.TargetID = "target-2"
				return copy
			}(), publication: publication,
		},
		"candidate substitution": {
			plan: plan,
			binding: func() sealedcontrol.SealBinding {
				copy := binding
				copy.CandidateID = "candidate-2"
				return copy
			}(), publication: publication,
		},
		"generation substitution": {
			plan: plan,
			binding: func() sealedcontrol.SealBinding {
				copy := binding
				copy.GenerationID = "generation-2"
				return copy
			}(), publication: publication,
		},
		"base generation substitution": {
			plan: plan,
			publication: func() deployment.PublicationIntent {
				copy := publication
				copy.ExpectedBaseGenerationID = "generation-base"
				return copy
			}(), binding: binding,
		},
		"base target revision substitution": {
			plan: plan,
			publication: func() deployment.PublicationIntent {
				copy := publication
				copy.ExpectedTargetRevision = 1
				return copy
			}(), binding: binding,
		},
		"expired plan row": {
			plan: func() deployment.DeliveryPlan {
				copy := plan
				copy.Status = deployment.DeliveryPlanExpired
				return copy
			}(), binding: binding, publication: publication,
		},
		"missing verification time": {
			plan: plan, binding: binding, publication: publication,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			verificationNow := now
			if name == "expired plan row" {
				verificationNow = test.plan.Governance.ExpiresAt
			}
			if name == "missing verification time" {
				verificationNow = time.Time{}
			}
			err := validateSealedPublicationPlanBinding(test.plan, test.binding, test.publication, verificationNow)
			if name == "expired plan row" {
				if !errors.Is(err, sealedcontrol.ErrSealUnverified) {
					t.Fatalf("binding error = %v, want ErrSealUnverified", err)
				}
				return
			}
			if name == "missing verification time" {
				if !errors.Is(err, sealedcontrol.ErrSealUnverified) {
					t.Fatalf("binding error = %v, want ErrSealUnverified", err)
				}
				return
			}
			if !errors.Is(err, sealedcontrol.ErrSealUnverified) {
				t.Fatalf("binding error = %v, want ErrSealUnverified", err)
			}
		})
	}
}
