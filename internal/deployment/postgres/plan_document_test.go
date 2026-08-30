package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/project/graph"
)

func richPlanDocumentFixture(t *testing.T, id, target, project string) (deployment.DeliveryPlan, json.RawMessage) {
	t.Helper()
	d := func(ch byte) string { return testDigest(ch) }
	created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: id, TargetID: target, ProjectID: graph.ResourceID(project), Environment: "prod",
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: d('e'), BaseTargetRevision: 0,
		Execution: deployment.DeliveryExecutionInputs{
			SourceArtifactDigest: d('e'), CompilerDigest: d('b'), ExecutableDigest: d('c'),
			DependencyDigest: d('d'), ConfigDigest: d('c'), BindingDigest: d('f'),
			RuntimeDigest: d('0'), CapabilityDigest: d('1'),
		},
		Provenance: deployment.DeliveryProvenance{Builder: "test"},
		Governance: deployment.DeliveryGovernance{
			PolicyDigest: d('2'), AuthorizationDigest: d('d'), QualificationDigest: d('3'),
			ExpiresAt: created.Add(time.Hour), RequiresApproval: true,
		},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement: "impact", PhysicalWorkStatement: "work", ReuseStatement: "reuse",
			Qualification: deployment.DeliveryQualificationEvidence{Policy: "contract", Steps: []deployment.DeliveryQualificationStep{{ID: "schema", Kind: "contract", Description: "schema", Required: true, Blocking: true}}},
			StalePolicy:   deployment.DeliveryStalePolicy{Mode: "reject"},
			Rollback:      deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe},
		},
		CreatedAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan, document
}

func planDocumentProjectionFixture(t *testing.T, rich deployment.DeliveryPlan, document json.RawMessage) PlanInput {
	t.Helper()
	return PlanInput{
		PlanID: rich.ID, TargetID: rich.TargetID, PlanRevision: 1, PlanDigest: rich.Digest,
		CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: rich.Execution.ConfigDigest,
		SecurityDomainFingerprint: rich.Governance.AuthorizationDigest, ArtifactDigest: rich.SourceDigest,
		QualificationDigest: rich.Governance.QualificationDigest, PlanDocument: document,
		Evidence: json.RawMessage(`{"review":"ok"}`),
	}
}

func richPlanInputFixture(t *testing.T, id, target, project string) PlanInput {
	t.Helper()
	rich, document := richPlanDocumentFixture(t, id, target, project)
	return planDocumentProjectionFixture(t, rich, document)
}

func TestCanonicalPlanDocumentRequiresCanonicalValidatedRichPlan(t *testing.T) {
	rich, document := richPlanDocumentFixture(t, "0198f2c0-7c7a-7f00-8a11-000000000901", "target_document", "project_document")
	canonical, decoded, err := canonicalPlanDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, document) || decoded.Digest != rich.Digest || decoded.Execution.ConfigDigest != rich.Execution.ConfigDigest {
		t.Fatalf("canonical plan document changed: got=%s decoded=%#v want=%s", canonical, decoded, document)
	}
	if _, _, err := canonicalPlanDocument(append([]byte(" "), document...)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-canonical whitespace document error = %v", err)
	}
	if _, _, err := canonicalPlanDocument([]byte(`{"id":"unknown"}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete rich plan document error = %v", err)
	}
}

func TestRichPlanRehydratesAndKeepsApprovalSeparateFromQualificationProjection(t *testing.T) {
	rich, document := richPlanDocumentFixture(t, "0198f2c0-7c7a-7f00-8a11-000000000902", "target_document", "project_document")
	input := planDocumentProjectionFixture(t, rich, document)
	persisted := DeliveryPlan(input)
	rehydrated, err := persisted.RichPlan()
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated.Digest != rich.Digest || !reflect.DeepEqual(rehydrated.Execution, rich.Execution) || !reflect.DeepEqual(rehydrated.Governance, rich.Governance) {
		t.Fatalf("rehydrated rich plan = %#v, want %#v", rehydrated, rich)
	}

	changed := rich
	changed.Governance.RequiresApproval = !changed.Governance.RequiresApproval
	changed, err = deployment.NewDeliveryPlan(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedDocument, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedInput := planDocumentProjectionFixture(t, changed, changedDocument)
	if !planDocumentProjectionMatches(changed, changedInput) {
		t.Fatal("approval-policy change unexpectedly changed qualification projection identity")
	}

	servingRich := rich
	servingRich.ServingArtifactDigest = testDigest('9')
	servingRich, err = deployment.NewDeliveryPlan(servingRich)
	if err != nil {
		t.Fatal(err)
	}
	servingDocument, err := json.Marshal(servingRich)
	if err != nil {
		t.Fatal(err)
	}
	servingDigestInput := planDocumentProjectionFixture(t, servingRich, servingDocument)
	servingDigestInput.ArtifactDigest = servingRich.ServingArtifactDigest
	if !planDocumentProjectionMatches(servingRich, servingDigestInput) || servingDigestInput.ArtifactDigest == servingRich.SourceDigest {
		t.Fatal("serving artifact digest was incorrectly coupled to source digest")
	}
	for name, mutate := range map[string]func(*PlanInput){
		"authorization": func(input *PlanInput) { input.SecurityDomainFingerprint = testDigest('9') },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := input
			mutate(&drifted)
			if planDocumentProjectionMatches(rich, drifted) {
				t.Fatalf("%s projection drift was accepted", name)
			}
		})
	}
}

func TestCreatePlanRejectsRichDocumentOutsideTargetScope(t *testing.T) {
	db := deliveryTestDB(t)
	repository := New(db)
	const targetID = "target_document_scope"
	if _, err := repository.CreateTarget(t.Context(), TargetInput{TargetID: targetID, ProjectID: "project_document", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	input := richPlanInputFixture(t, "0198f2c0-7c7a-7f00-8a11-000000000903", targetID, "project_other")
	if _, err := repository.CreatePlan(t.Context(), input); !errors.Is(err, ErrConflict) {
		t.Fatalf("out-of-scope plan error = %v, want conflict", err)
	}
	if _, err := repository.Plan(t.Context(), input.PlanID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope plan was persisted, read error = %v", err)
	}
}
