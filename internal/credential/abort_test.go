package credential

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestActivationPreparationAbortAuditIsRedactedAndCanonical(t *testing.T) {
	prepared := PreparedActivation{Preparation: testActivationPreparation(), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	actorID, eventID := uuid.NewString(), uuid.NewString()
	intent, err := prepared.AbortAuditIntent(eventID, actorID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.EventID != eventID || intent.DomainEventID != eventID || intent.ActorID != actorID ||
		intent.Action != "credential.activation.aborted" || intent.Outcome != "success" ||
		intent.AggregateKey != "credential-activation:"+prepared.Preparation.OperationID || intent.AggregateSequence != 2 {
		t.Fatalf("incorrect abort audit identity: %#v", intent)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(intent.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{
		"operation_id":   prepared.Preparation.OperationID,
		"receipt_id":     prepared.Preparation.Receipt.ReceiptID,
		"candidate_id":   prepared.Preparation.CandidateID,
		"generation_id":  prepared.Preparation.GenerationID,
		"publication_id": prepared.Preparation.PublicationID,
		"aborted_by":     actorID,
	} {
		if metadata[key] != expected {
			t.Errorf("metadata %s = %v, want %s", key, metadata[key], expected)
		}
	}
	for _, forbidden := range []string{"password", "ciphertext", "secret", "credential_value"} {
		if strings.Contains(strings.ToLower(intent.MetadataJSON), forbidden) {
			t.Errorf("abort audit contains %q", forbidden)
		}
	}

	for name, mutate := range map[string]func(*PreparedActivation){
		"noncanonical actor": func(*PreparedActivation) {},
		"already aborted": func(value *PreparedActivation) {
			value.AbortedAt = value.CreatedAt.Add(time.Second)
			value.AbortedBy = actorID
		},
		"invalid creation time": func(value *PreparedActivation) { value.CreatedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			value := prepared
			mutate(&value)
			candidateActor, candidateEvent := actorID, eventID
			if name == "noncanonical actor" {
				candidateActor = " " + actorID
			}
			if _, err := value.AbortAuditIntent(candidateEvent, candidateActor); err == nil {
				t.Fatal("invalid abort audit input accepted")
			}
		})
	}
}
