package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type nativeDeliveryAuthorizationReaderFake struct {
	deploymentmodule.NativeDeliveryReader
	plan           deploymentpostgres.DeliveryPlan
	generation     deploymentpostgres.DeliveryGeneration
	publication    deploymentpostgres.DeliveryPublication
	publicationErr error
	calls          []string
}

func (r *nativeDeliveryAuthorizationReaderFake) Plan(_ context.Context, id string) (deploymentpostgres.DeliveryPlan, error) {
	r.calls = append(r.calls, "plan:"+id)
	return r.plan, nil
}

func (r *nativeDeliveryAuthorizationReaderFake) Generation(_ context.Context, id string) (deploymentpostgres.DeliveryGeneration, error) {
	r.calls = append(r.calls, "generation:"+id)
	return r.generation, nil
}

func (r *nativeDeliveryAuthorizationReaderFake) Publication(_ context.Context, id string) (deploymentpostgres.DeliveryPublication, error) {
	r.calls = append(r.calls, "publication:"+id)
	if r.publicationErr != nil {
		return deploymentpostgres.DeliveryPublication{}, r.publicationErr
	}
	return r.publication, nil
}

func TestNativeDeliveryAuthorizationPlanResolvesPublicationThroughGeneration(t *testing.T) {
	plan := nativeDeliveryAuthorizationPlanFixture(t)
	reader := &nativeDeliveryAuthorizationReaderFake{
		plan:        plan,
		generation:  deploymentpostgres.DeliveryGeneration{GenerationID: "generation-id", PlanID: plan.PlanID},
		publication: deploymentpostgres.DeliveryPublication{PublicationID: "publication-id", GenerationID: "generation-id"},
	}

	got, err := deliveryAuthorizationPlan(context.Background(), nil, reader, "getDeliveryPublicationEvidence", "publication-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != plan.PlanID {
		t.Fatalf("resolved plan ID = %q, want %q", got.ID, plan.PlanID)
	}
	wantCalls := []string{"publication:publication-id", "generation:generation-id", "plan:" + plan.PlanID}
	if strings.Join(reader.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("native read calls = %v, want %v", reader.calls, wantCalls)
	}
}

func TestNativeDeliveryAuthorizationPlanMapsUnresolvablePublicationToNoRows(t *testing.T) {
	for _, readErr := range []error{deploymentpostgres.ErrNotFound, deploymentpostgres.ErrInvalid} {
		reader := &nativeDeliveryAuthorizationReaderFake{publicationErr: readErr}
		_, err := deliveryAuthorizationPlan(context.Background(), nil, reader, "getDeliveryPublicationEvidence", "unresolvable-publication")
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("read error %v mapped to %v, want sql.ErrNoRows", readErr, err)
		}
	}
}

func nativeDeliveryAuthorizationPlanFixture(t *testing.T) deploymentpostgres.DeliveryPlan {
	t.Helper()
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 6, 0, 0, 0, time.UTC)
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID:                    "0198f2c0-7c7a-7f00-8a11-000000000301",
		TargetID:              "target",
		ProjectID:             projectID,
		Environment:           "prod",
		Operation:             deployment.DeliveryOperationCodeChange,
		ActorID:               "actor",
		SourceOwnerID:         "owner",
		SourceDigest:          digest('a'),
		ServingArtifactDigest: digest('b'),
		CreatedAt:             now,
		Execution: deployment.DeliveryExecutionInputs{
			SourceArtifactDigest: digest('a'),
			CompilerDigest:       digest('c'),
			ExecutableDigest:     digest('d'),
			DependencyDigest:     digest('e'),
			ConfigDigest:         digest('f'),
			BindingDigest:        digest('1'),
			RuntimeDigest:        digest('2'),
			CapabilityDigest:     digest('3'),
		},
		Provenance: deployment.DeliveryProvenance{Builder: "native-authorization-test"},
		Governance: deployment.DeliveryGovernance{
			PolicyDigest:           digest('4'),
			AuthorizationDigest:    digest('5'),
			QualificationDigest:    digest('6'),
			ApprovalPolicyRevision: 1,
			ExpiresAt:              time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement:       "authorization fixture impact",
			PhysicalWorkStatement: "authorization fixture physical work",
			ReuseStatement:        "authorization fixture has no reuse",
			Qualification: deployment.DeliveryQualificationEvidence{
				Policy: "required",
				Steps:  []deployment.DeliveryQualificationStep{{ID: "snapshot", Kind: "contract", Description: "qualify", Required: true, Blocking: true}},
			},
			StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"},
			Rollback:    deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return deploymentpostgres.DeliveryPlan{
		PlanID:                    plan.ID,
		TargetID:                  plan.TargetID,
		PlanDigest:                plan.Digest,
		CompiledConfigDigest:      plan.Execution.ConfigDigest,
		SecurityDomainFingerprint: plan.Governance.AuthorizationDigest,
		ArtifactDigest:            plan.ServingArtifactDigest,
		QualificationDigest:       plan.Governance.QualificationDigest,
		ApprovalPolicyRevision:    plan.Governance.ApprovalPolicyRevision,
		PlanDocument:              document,
		CreatedAt:                 now,
	}
}
