package credential

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSwitchingAuditUsesStoredIntentAndOriginalActor(t *testing.T) {
	input := testActivationPreparation()
	prepared := PreparedActivation{Preparation: input, CreatedAt: input.Receipt.ValidatedAt}
	eventID := uuid.NewString()
	intent, err := prepared.SwitchingAuditIntent(eventID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.ActorID != input.Receipt.ActorID || intent.EventID != eventID ||
		intent.DomainEventID != eventID || intent.AggregateSequence != 2 ||
		intent.AggregateKey != "credential-activation:"+input.OperationID ||
		intent.Action != "credential.activation.switching" {
		t.Fatalf("wrong switching audit identity: %#v", intent)
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(intent.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"operation_id": input.OperationID, "receipt_id": input.Receipt.ReceiptID,
		"version_id": input.Receipt.Binding.VersionID, "candidate_id": input.CandidateID,
		"generation_id": input.GenerationID, "publication_id": input.PublicationID,
	}
	if len(metadata) != len(want) {
		t.Fatalf("switching audit exposes unexpected fields: %v", metadata)
	}
	for key, value := range want {
		if metadata[key] != value {
			t.Errorf("audit %s = %q, want %q", key, metadata[key], value)
		}
	}
	prepared.SwitchingAt = prepared.CreatedAt.Add(time.Second)
	if _, err := prepared.SwitchingAuditIntent(eventID); err == nil {
		t.Fatal("repeated switching audit accepted")
	}
	abort, err := prepared.AbortAuditIntent(uuid.NewString(), "current-operator")
	if err != nil {
		t.Fatal(err)
	}
	if abort.AggregateSequence != 3 {
		t.Fatalf("abort after switching sequence = %d", abort.AggregateSequence)
	}
	prepared.AbortedAt, prepared.AbortedBy = prepared.SwitchingAt, "current-operator"
	if _, err := prepared.SwitchingAuditIntent(eventID); err == nil {
		t.Fatal("aborted preparation allowed switching")
	}
}

func TestPreparedActivationRejectsImpossibleLifecycleTimes(t *testing.T) {
	input := testActivationPreparation()
	base := PreparedActivation{Preparation: input, CreatedAt: input.Receipt.ValidatedAt}
	for name, mutate := range map[string]func(*PreparedActivation){
		"missing creation":       func(p *PreparedActivation) { p.CreatedAt = time.Time{} },
		"switch before creation": func(p *PreparedActivation) { p.SwitchingAt = p.CreatedAt.Add(-time.Second) },
		"imprecise switch":       func(p *PreparedActivation) { p.SwitchingAt = p.CreatedAt.Add(time.Nanosecond) },
		"abort before switch": func(p *PreparedActivation) {
			p.SwitchingAt = p.CreatedAt.Add(time.Second)
			p.AbortedAt, p.AbortedBy = p.CreatedAt, "operator"
		},
		"abort actor without time": func(p *PreparedActivation) { p.AbortedBy = "operator" },
		"abort time without actor": func(p *PreparedActivation) { p.AbortedAt = p.CreatedAt },
	} {
		t.Run(name, func(t *testing.T) {
			prepared := base
			mutate(&prepared)
			if prepared.Validate() == nil {
				t.Fatal("invalid lifecycle evidence accepted")
			}
			if _, err := prepared.AbortAuditIntent(uuid.NewString(), "operator"); err == nil {
				t.Fatal("invalid lifecycle evidence authorized abort audit")
			}
		})
	}
	// Recovery validates identity and ordering without extending or requiring
	// current receipt freshness. An expired unfinished operation stays visible.
	base.CreatedAt = input.Receipt.ExpiresAt.Add(time.Hour)
	base.SwitchingAt = base.CreatedAt
	if err := base.Validate(); err != nil {
		t.Fatalf("expired recovery evidence rejected: %v", err)
	}
}
