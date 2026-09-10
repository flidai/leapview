package authz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestProtectedSemanticCompositionRequiresCanonicalAuditRecorder(t *testing.T) {
	metrics, _, _ := semanticDiscoveryFixture(t)
	metrics.auditRecorder = nil
	if _, err := metrics.SemanticPlanner(context.Background(), "sales"); err == nil {
		t.Fatal("protected semantic consumer composed without canonical audit recorder")
	}
}

func TestSemanticAuditObserverPersistsRedactedDeterministicMetadata(t *testing.T) {
	metrics, _, _ := semanticDiscoveryFixture(t)
	recorder := &canonicalAuditRecorder{}
	metrics.auditRecorder = recorder
	ctx := dataquery.WithMetadata(context.Background(), dataquery.Metadata{RequestID: "request-1", CorrelationID: "correlation-1", Surface: "explore", Operation: "semanticAuthorize"})
	if err := metrics.AuthorizeSemanticTarget(ctx, "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.AuthorizeSemanticTarget(ctx, "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("audit events = %d, want 2", len(recorder.events))
	}
	first, second := recorder.events[0], recorder.events[1]
	if first.Identity != second.Identity || first.Resource != second.Resource || first.Capability != second.Capability || first.Action != semanticquery.SemanticAccessAuditAuthorization {
		t.Fatalf("event identity/action mismatch: first=%#v second=%#v", first, second)
	}
	if first.Status != "success" || second.Status != "success" {
		t.Fatalf("statuses = %q, %q", first.Status, second.Status)
	}
	if first.RequestID != "request-1" || first.CorrelationID != "correlation-1" {
		t.Fatalf("request metadata = %q/%q", first.RequestID, first.CorrelationID)
	}
	if first.MetadataJSON != second.MetadataJSON {
		t.Fatalf("metadata is not deterministic:\n%s\n%s", first.MetadataJSON, second.MetadataJSON)
	}
	var metadata struct {
		Source    string         `json:"source"`
		Allowed   bool           `json:"allowed"`
		Evidence  map[string]any `json:"evidence"`
		Reason    string         `json:"reason"`
		Operation string         `json:"operation"`
		ActorID   string         `json:"actorId"`
	}
	if err := json.Unmarshal([]byte(first.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Source != "semantic_access_consumer" || !metadata.Allowed || metadata.Operation != semanticquery.SemanticAccessAuditAuthorization || len(metadata.Evidence) == 0 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.ActorID != "alice" {
		t.Fatalf("actor metadata = %q", metadata.ActorID)
	}
	if strings.Contains(first.MetadataJSON, `"canonicalValues"`) || strings.Contains(first.MetadataJSON, `"us"`) || strings.Contains(first.MetadataJSON, `"predicate"`) {
		t.Fatalf("raw semantic authorization data leaked: %s", first.MetadataJSON)
	}
}

func TestSemanticAuditFailureBlocksAuthorization(t *testing.T) {
	metrics, _, _ := semanticDiscoveryFixture(t)
	recorder := &canonicalAuditRecorder{err: errors.New("audit unavailable")}
	metrics.auditRecorder = recorder
	if err := metrics.AuthorizeSemanticTarget(context.Background(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err == nil {
		t.Fatal("semantic authorization succeeded after canonical audit failure")
	}
}

func TestSemanticAuditRecordsUnavailableDecisionWithoutFabricatedEvidence(t *testing.T) {
	metrics, _, _ := semanticDiscoveryFixture(t)
	recorder, ok := metrics.auditRecorder.(*canonicalAuditRecorder)
	if !ok {
		t.Fatal("fixture did not install canonical recorder")
	}
	metrics.resolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) {
		return access.SemanticAttributeResolution{}, errors.New("authority unavailable")
	}
	if _, err := metrics.SemanticPlanner(context.Background(), "sales"); err == nil {
		t.Fatal("consumer composed without authoritative decision")
	}
	if len(recorder.events) != 1 {
		t.Fatalf("constructor denial events = %d", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Status != "denied" || event.Action != semanticquery.SemanticAccessAuditConsumerBind {
		t.Fatalf("constructor denial event = %#v", event)
	}
	var metadata struct {
		Source            string          `json:"source"`
		PrincipalID       string          `json:"principalId"`
		DecisionAvailable bool            `json:"decisionAvailable"`
		PolicyDigest      string          `json:"policyDigest"`
		DecisionDigest    string          `json:"decisionDigest"`
		Evidence          json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Source != "semantic_access_consumer" || metadata.PrincipalID != "alice" || metadata.DecisionAvailable || metadata.PolicyDigest != "" || metadata.DecisionDigest != "" || len(metadata.Evidence) != 0 {
		t.Fatalf("unavailable decision metadata = %#v (%s)", metadata, event.MetadataJSON)
	}
	if strings.Contains(event.MetadataJSON, "canonicalValues") || strings.Contains(event.MetadataJSON, "predicate") {
		t.Fatalf("unavailable decision leaked raw evidence: %s", event.MetadataJSON)
	}
}

func TestSemanticAuditRejectsAllowedObservationWithoutDecisionAvailability(t *testing.T) {
	metrics, _, _ := semanticDiscoveryFixture(t)
	snapshot, err := metrics.snapshotFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observer, err := metrics.semanticAuditObserver(context.Background(), snapshot, "sales", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := observer(semanticquery.SemanticAccessAuditObservation{
		Operation: semanticquery.SemanticAccessAuditAuthorization, Allowed: true,
		PrincipalID: "alice", ActorID: "alice", DecisionAvailable: false,
	}); err == nil {
		t.Fatal("allowed observation without decision availability was persisted")
	}
}

func TestSemanticAuditRecordsDeniedTargetBeforeReturningError(t *testing.T) {
	metrics, _, _ := semanticDiscoveryFixture(t)
	recorder := &canonicalAuditRecorder{}
	metrics.auditRecorder = recorder
	if err := metrics.AuthorizeSemanticTarget(context.Background(), "sales", semanticquery.SemanticAccessTarget{Dataset: "missing"}); err == nil {
		t.Fatal("unknown semantic target was accepted")
	}
	if len(recorder.events) != 1 || recorder.events[0].Status != "denied" {
		t.Fatalf("denied audit events = %#v", recorder.events)
	}
	if !strings.Contains(recorder.events[0].MetadataJSON, `"allowed":false`) || !strings.Contains(recorder.events[0].MetadataJSON, `"reason":"target_unknown"`) {
		t.Fatalf("denied metadata = %s", recorder.events[0].MetadataJSON)
	}
}

func TestPublicSemanticCompositionDoesNotRequireAuditRecorder(t *testing.T) {
	metrics := canonicalMetricsWithSnapshot(t, canonicalSnapshot(t, nil, nil), nil)
	metrics.auditRecorder = nil
	if err := metrics.AuthorizeSemanticTarget(context.Background(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
}
