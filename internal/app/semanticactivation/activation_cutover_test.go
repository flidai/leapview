package semanticactivation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

type activationAuditRecorder struct {
	events []access.CanonicalAuditEvent
	err    error
}

type auditTestTx struct{ accesspostgres.Tx }

func (r *activationAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func (r *activationAuditRecorder) RecordCanonicalAuditEventTx(ctx context.Context, _ accesspostgres.Tx, event access.CanonicalAuditEvent) error {
	return r.RecordCanonicalAuditEvent(ctx, event)
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
		policy: deployment.SemanticActivationModelEvidence{PolicyDefinitionDigest: digest, PublicationPolicy: resultidentity.PublicationPolicyIdentity{
			Candidate: resultidentity.PublicationIdentity{Digest: digest},
		}},
	}
	evidence := deployment.SemanticActivationEvidence{Digest: digest, RegistryDigest: digest, RegistryRevision: 3, ControlDigest: digest, ControlRevision: 5}
	input := activationCutoverInput{GenerationID: "generation-1", Actor: "publisher-1", Rollback: true}
	recorder := &activationAuditRecorder{}
	fence := &Fence{audit: recorder}
	for range 2 {
		if err := fence.auditActivation(t.Context(), auditTestTx{}, projectgraph.ResourceID("project_demo"), "prod", input, evidence, item, semanticquery.SemanticAccessActivationPolicyIdentity{DefinitionDigest: digest, GenerationDigest: digest}); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.events) != 2 || recorder.events[0].MetadataJSON != recorder.events[1].MetadataJSON {
		t.Fatalf("activation audit is not deterministic: %#v", recorder.events)
	}
	if strings.Contains(recorder.events[0].MetadataJSON, "publisher-1") || strings.Contains(recorder.events[0].MetadataJSON, "attribute") {
		t.Fatalf("activation audit metadata is not redacted: %s", recorder.events[0].MetadataJSON)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(recorder.events[0].MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["policyDefinitionDigest"] != digest || metadata["generationPolicyDigest"] != digest {
		t.Fatalf("activation audit policy identity = %#v", metadata)
	}
	wantErr := errors.New("audit unavailable")
	fence.audit = &activationAuditRecorder{err: wantErr}
	if err := fence.auditActivation(t.Context(), auditTestTx{}, projectgraph.ResourceID("project_demo"), "prod", input, evidence, item, semanticquery.SemanticAccessActivationPolicyIdentity{DefinitionDigest: digest, GenerationDigest: digest}); !errors.Is(err, wantErr) {
		t.Fatalf("audit failure = %v, want %v", err, wantErr)
	}
}

func TestActivatedPolicyDefinitionMustMatchApprovedEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	matching := semanticquery.SemanticAccessActivationPolicyIdentity{DefinitionDigest: digest, GenerationDigest: "sha256:" + strings.Repeat("b", 64)}
	if err := validatePolicyDefinitionIdentity(digest, matching); err != nil {
		t.Fatalf("matching deployed policy rejected: %v", err)
	}
	for name, identity := range map[string]semanticquery.SemanticAccessActivationPolicyIdentity{
		"definition mismatch": {DefinitionDigest: "sha256:" + strings.Repeat("c", 64), GenerationDigest: matching.GenerationDigest},
		"missing definition":  {GenerationDigest: matching.GenerationDigest},
		"missing generation":  {DefinitionDigest: digest},
	} {
		if err := validatePolicyDefinitionIdentity(digest, identity); !errors.Is(err, deployment.ErrDeliveryStale) {
			t.Errorf("%s error = %v, want delivery stale", name, err)
		}
	}
	recorder := &activationAuditRecorder{}
	fence := &Fence{audit: recorder}
	item := semanticActivationResolvedModel{id: projectgraph.ResourceID("semantic_orders"), policy: deployment.SemanticActivationModelEvidence{PolicyDefinitionDigest: digest}}
	mismatch := semanticquery.SemanticAccessActivationPolicyIdentity{DefinitionDigest: "sha256:" + strings.Repeat("c", 64), GenerationDigest: matching.GenerationDigest}
	if err := fence.admitPolicyActivation(t.Context(), auditTestTx{}, projectgraph.ResourceID("project_demo"), "prod", activationCutoverInput{GenerationID: "generation-1", Actor: "publisher-1"}, deployment.SemanticActivationEvidence{}, item, mismatch); !errors.Is(err, deployment.ErrDeliveryStale) {
		t.Fatalf("mismatched deployed policy admission error = %v, want delivery stale", err)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("mismatched deployed policy emitted audit evidence: %#v", recorder.events)
	}
}

func TestTransactionBoundAuthorityMutationRejectsActivationEvidence(t *testing.T) {
	planned := &deployment.SemanticActivationEvidence{RegistryRevision: 7, RegistryDigest: "sha256:" + strings.Repeat("a", 64), ControlRevision: 11, ControlDigest: "sha256:" + strings.Repeat("b", 64)}
	current := *planned
	current.ControlRevision++
	current.ControlDigest = "sha256:" + strings.Repeat("c", 64)
	if err := validatePlannedActivationEvidence(planned, &current); !errors.Is(err, deployment.ErrDeliveryStale) {
		t.Fatalf("authority mutation error = %v, want delivery stale", err)
	}
	if err := validatePlannedActivationEvidence(planned, planned); err != nil {
		t.Fatalf("unchanged authority evidence rejected: %v", err)
	}
}
