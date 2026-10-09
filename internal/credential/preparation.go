package credential

import (
	"encoding/json"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
)

// ActivationPreparation identifies one intended source publication and the
// exact observation it reserves. IDs are allocated by the coordinating service,
// not inferred from a provider-version string. This is preparation intent only:
// it proves neither candidate qualification nor authority to publish or use it.
type ActivationPreparation struct {
	OperationID             string
	Receipt                 ValidationReceipt
	ExpectedTargetRevision  int64
	PredecessorGenerationID string
	CandidateID             string
	GenerationID            string
	PublicationID           string
}

// PreparedActivation is the durable receipt reservation. Reading it after a
// crash does not renew its receipt or establish live runtime readiness.
type PreparedActivation struct {
	Preparation ActivationPreparation
	CreatedAt   time.Time
	SwitchingAt time.Time
	CommittedAt time.Time
	CompletedAt time.Time
	AbortedAt   time.Time
	AbortedBy   string
}

// Validate checks durable identity and transition ordering, not current
// authority or receipt freshness. Recovery must still see expired operations.
func (prepared PreparedActivation) Validate() error {
	if prepared.Preparation.Validate() != nil || prepared.CreatedAt.IsZero() ||
		!prepared.CreatedAt.Equal(prepared.CreatedAt.Truncate(time.Microsecond)) {
		return ErrInvalid
	}
	latest := prepared.CreatedAt
	if !prepared.SwitchingAt.IsZero() {
		if prepared.SwitchingAt.Before(latest) || !prepared.SwitchingAt.Equal(prepared.SwitchingAt.Truncate(time.Microsecond)) {
			return ErrInvalid
		}
		latest = prepared.SwitchingAt
	}
	if !prepared.CommittedAt.IsZero() {
		if prepared.SwitchingAt.IsZero() || prepared.CommittedAt.Before(latest) ||
			!prepared.CommittedAt.Equal(prepared.CommittedAt.Truncate(time.Microsecond)) ||
			!prepared.AbortedAt.IsZero() || prepared.AbortedBy != "" {
			return ErrInvalid
		}
	}
	if prepared.AbortedAt.IsZero() {
		if prepared.AbortedBy != "" {
			return ErrInvalid
		}
	} else if !canonical(prepared.AbortedBy) || prepared.AbortedAt.Before(latest) ||
		!prepared.AbortedAt.Equal(prepared.AbortedAt.Truncate(time.Microsecond)) {
		return ErrInvalid
	}
	if !prepared.CompletedAt.IsZero() && (prepared.CommittedAt.IsZero() ||
		prepared.CompletedAt.Before(prepared.CommittedAt) ||
		!prepared.CompletedAt.Equal(prepared.CompletedAt.Truncate(time.Microsecond))) {
		return ErrInvalid
	}
	return nil
}

func (preparation ActivationPreparation) Validate() error {
	// Saved receipts use PostgreSQL's microsecond precision. Reject finer
	// timestamps rather than letting a SQL parameter round to another instant.
	if preparation.Receipt.Validate() != nil || preparation.Receipt.Binding.Provider != "postgres" ||
		preparation.Receipt.Binding.TargetID != preparation.Receipt.Binding.DeploymentID ||
		!canonicalPreparationID(preparation.Receipt.Binding.VersionID) ||
		!destinationDigest(preparation.Receipt.Binding.Destination) ||
		!preparation.Receipt.ValidatedAt.Equal(preparation.Receipt.ValidatedAt.Truncate(time.Microsecond)) ||
		!preparation.Receipt.ExpiresAt.Equal(preparation.Receipt.ExpiresAt.Truncate(time.Microsecond)) ||
		preparation.ExpectedTargetRevision < 1 {
		return ErrInvalid
	}
	for _, id := range []string{preparation.OperationID, preparation.CandidateID, preparation.GenerationID, preparation.PublicationID} {
		if !canonicalPreparationID(id) {
			return ErrInvalid
		}
	}
	if preparation.PredecessorGenerationID != "" && !canonicalPreparationID(preparation.PredecessorGenerationID) {
		return ErrInvalid
	}
	return nil
}

func canonicalPreparationID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

// AuditIntent records only the intended identities and non-secret configuration
// evidence. The store constructs this itself rather than accepting audit fields
// supplied separately from the preparation.
func (preparation ActivationPreparation) AuditIntent() (access.AuditIntent, error) {
	if err := preparation.Validate(); err != nil {
		return access.AuditIntent{}, err
	}
	receipt := preparation.Receipt
	metadata, err := json.Marshal(struct {
		OperationID             string `json:"operation_id"`
		ReceiptID               string `json:"receipt_id"`
		VersionID               string `json:"version_id"`
		TargetID                string `json:"target_id"`
		BindingID               string `json:"binding_id"`
		BindingRevision         int64  `json:"binding_revision"`
		ConfigurationDigest     string `json:"configuration_digest"`
		ExpectedTargetRevision  int64  `json:"expected_target_revision"`
		PredecessorGenerationID string `json:"predecessor_generation_id"`
		CandidateID             string `json:"candidate_id"`
		GenerationID            string `json:"generation_id"`
		PublicationID           string `json:"publication_id"`
	}{
		preparation.OperationID, receipt.ReceiptID, receipt.Binding.VersionID, receipt.Binding.TargetID,
		receipt.BindingID, receipt.BindingRevision, receipt.ConfigurationDigest,
		preparation.ExpectedTargetRevision, preparation.PredecessorGenerationID,
		preparation.CandidateID, preparation.GenerationID, preparation.PublicationID,
	})
	if err != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	principalID := ""
	if canonicalPreparationID(receipt.ActorID) {
		principalID = receipt.ActorID
	}
	return (access.AuditIntent{
		EventID: preparation.OperationID, DomainEventID: preparation.OperationID,
		ScopeID: receipt.Binding.ProjectID, ActorID: receipt.ActorID, PrincipalID: principalID,
		Source: "credential", Operation: "prepareCredentialActivation", Action: "credential.activation.prepared",
		ResourceKind: "connection", ResourceID: receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey: "credential-activation:" + preparation.OperationID, AggregateSequence: 1,
		MetadataJSON: string(metadata),
	}).Canonicalize()
}
