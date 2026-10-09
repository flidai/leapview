package credential

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/google/uuid"
)

func testActivationPreparation() ActivationPreparation {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return ActivationPreparation{
		OperationID: uuid.NewString(), CandidateID: uuid.NewString(), GenerationID: uuid.NewString(), PublicationID: uuid.NewString(),
		ExpectedTargetRevision: 3, PredecessorGenerationID: uuid.NewString(),
		Receipt: ValidationReceipt{
			ReceiptID: uuid.NewString(), ActorID: uuid.NewString(), BindingID: "binding_warehouse", BindingRevision: 2,
			ConfigurationDigest: "sha256:" + strings.Repeat("b", 64), ValidatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
			Binding: encryption.Binding{
				DeploymentID: "instance", OwnerID: "customer", ScopeKind: "connection", TargetID: "instance",
				ProjectID: "sales", Environment: "prod", ResourceID: "warehouse", Purpose: "connection-authentication",
				Provider: "postgres", Destination: "sha256:" + strings.Repeat("a", 64), VersionID: uuid.NewString(),
			},
		},
	}
}

func TestActivationPreparationRequiresExactCanonicalIntent(t *testing.T) {
	for name, mutate := range map[string]func(*ActivationPreparation){
		"missing operation":                       func(v *ActivationPreparation) { v.OperationID = "" },
		"nil candidate":                           func(v *ActivationPreparation) { v.CandidateID = uuid.Nil.String() },
		"malformed generation":                    func(v *ActivationPreparation) { v.GenerationID = "generation" },
		"noncanonical publication":                func(v *ActivationPreparation) { v.PublicationID = " " + v.PublicationID },
		"invalid predecessor":                     func(v *ActivationPreparation) { v.PredecessorGenerationID = uuid.Nil.String() },
		"invalid revision":                        func(v *ActivationPreparation) { v.ExpectedTargetRevision = 0 },
		"invalid receipt":                         func(v *ActivationPreparation) { v.Receipt.BindingRevision = 0 },
		"other provider":                          func(v *ActivationPreparation) { v.Receipt.Binding.Provider = "s3" },
		"invalid version":                         func(v *ActivationPreparation) { v.Receipt.Binding.VersionID = "provider-version" },
		"target differs from instance deployment": func(v *ActivationPreparation) { v.Receipt.Binding.TargetID = "another-target" },
		"invalid destination":                     func(v *ActivationPreparation) { v.Receipt.Binding.Destination = "endpoint" },
		"timestamp alias": func(v *ActivationPreparation) {
			v.Receipt.ValidatedAt = v.Receipt.ValidatedAt.Add(time.Nanosecond)
			v.Receipt.ExpiresAt = v.Receipt.ExpiresAt.Add(time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := testActivationPreparation()
			mutate(&value)
			if value.Validate() == nil {
				t.Fatal("invalid preparation accepted")
			}
			if _, err := value.AuditIntent(); err == nil {
				t.Fatal("invalid preparation produced audit")
			}
		})
	}
	value := testActivationPreparation()
	value.PredecessorGenerationID = ""
	if err := value.Validate(); err != nil {
		t.Fatalf("initial publication intent: %v", err)
	}
}

func TestActivationPreparationAuditDescribesIntentWithoutReadiness(t *testing.T) {
	value := testActivationPreparation()
	intent, err := value.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if intent.ActorID != value.Receipt.ActorID || intent.DomainEventID != value.OperationID ||
		intent.ResourceID != value.Receipt.Binding.ResourceID || intent.Action != "credential.activation.prepared" ||
		intent.AggregateSequence != 1 || intent.RequestDigest != "" {
		t.Fatalf("incorrect preparation audit: %#v", intent)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(intent.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{
		"operation_id": value.OperationID, "receipt_id": value.Receipt.ReceiptID, "version_id": value.Receipt.Binding.VersionID,
		"candidate_id": value.CandidateID, "generation_id": value.GenerationID, "publication_id": value.PublicationID,
		"predecessor_generation_id": value.PredecessorGenerationID,
	} {
		if metadata[key] != expected {
			t.Errorf("metadata %s = %v", key, metadata[key])
		}
	}
	for _, forbidden := range []string{"password", "ciphertext", "in_use", "committed", "active_write_key"} {
		if strings.Contains(intent.MetadataJSON, forbidden) {
			t.Errorf("audit contains %s", forbidden)
		}
	}
}
