package semanticactivation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

type activationAuditRecorder struct {
	events []access.CanonicalAuditEvent
	err    error
}

func (r *activationAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func TestLegacyDataPolicyCutoverRejectsNewAuthoringAndPreservesRollback(t *testing.T) {
	legacy := `{"dataPolicies":{"regional":{"id":"regional","name":"regional","object":{"kind":"model","id":"orders"},"policyType":"row_filter","expressionJson":"{\"field\":\"region\"}"}}}`
	if err := validateLegacyDataPolicyCutover(legacy, false); !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("new legacy DataPolicy activation error = %v, want delivery invalid", err)
	}
	if err := validateLegacyDataPolicyCutover(legacy, true); err != nil {
		t.Fatalf("historical rollback lost retained DataPolicy restrictions: %v", err)
	}
	for _, raw := range []string{"", `{}`} {
		if err := validateLegacyDataPolicyCutover(raw, false); err != nil {
			t.Fatalf("policy-free activation %q: %v", raw, err)
		}
	}
	if err := validateLegacyDataPolicyCutover(`{"dataPolicies":`, true); !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("malformed historical evidence error = %v, want delivery invalid", err)
	}
}

func TestServingActivationLifecycleFailsClosed(t *testing.T) {
	for _, status := range []servingstate.Status{servingstate.StatusPending, servingstate.StatusDraining, servingstate.StatusFailed, servingstate.StatusDeleted} {
		if err := validateServingActivationState(servingstate.State{Status: status}); !errors.Is(err, deployment.ErrDeliveryConflict) {
			t.Errorf("status %q error = %v, want delivery conflict", status, err)
		}
	}
	for _, status := range []servingstate.Status{servingstate.StatusValidated, servingstate.StatusInactive, servingstate.StatusActive} {
		if err := validateServingActivationState(servingstate.State{Status: status}); err != nil {
			t.Errorf("activatable status %q: %v", status, err)
		}
	}
}

func TestActivationAuditIsDeterministicRedactedAndFailClosed(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	item := semanticActivationResolvedModel{
		id: projectgraph.ResourceID("semantic_orders"),
		policy: deployment.SemanticActivationModelEvidence{PublicationPolicy: resultidentity.PublicationPolicyIdentity{
			Candidate: resultidentity.PublicationIdentity{Digest: digest},
		}},
	}
	evidence := deployment.SemanticActivationEvidence{Digest: digest, RegistryDigest: digest, RegistryRevision: 3, ControlDigest: digest, ControlRevision: 5}
	input := deploymentmodule.ActivationCutoverInput{GenerationID: "generation-1", Actor: "publisher-1", Rollback: true}
	recorder := &activationAuditRecorder{}
	fence := &Fence{audit: recorder}
	for range 2 {
		if err := fence.auditActivation(t.Context(), projectgraph.ResourceID("project_demo"), "prod", input, evidence, item, digest); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.events) != 2 || recorder.events[0].MetadataJSON != recorder.events[1].MetadataJSON {
		t.Fatalf("activation audit is not deterministic: %#v", recorder.events)
	}
	if strings.Contains(recorder.events[0].MetadataJSON, "publisher-1") || strings.Contains(recorder.events[0].MetadataJSON, "attribute") {
		t.Fatalf("activation audit metadata is not redacted: %s", recorder.events[0].MetadataJSON)
	}
	wantErr := errors.New("audit unavailable")
	fence.audit = &activationAuditRecorder{err: wantErr}
	if err := fence.auditActivation(t.Context(), projectgraph.ResourceID("project_demo"), "prod", input, evidence, item, digest); !errors.Is(err, wantErr) {
		t.Fatalf("audit failure = %v, want %v", err, wantErr)
	}
}
