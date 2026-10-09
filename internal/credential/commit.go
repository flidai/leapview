package credential

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/access"
)

// ActivationPublication projects the immutable delivery intent at its final
// admission gate. It is not proof of a committed publication or live readiness.
type ActivationPublication struct {
	TargetID                string
	ExpectedTargetRevision  int64
	PredecessorGenerationID string
	CandidateID             string
	GenerationID            string
	PublicationID           string
	ActorID                 string
}

// Matches requires the publication to be exactly the one reserved by this
// receipt. Qualification, current authority and candidate pin verification
// remain the responsibility of the transaction-bound admission callback.
func (publication ActivationPublication) Matches(preparation ActivationPreparation) bool {
	return preparation.Validate() == nil &&
		publication.TargetID == preparation.Receipt.Binding.TargetID &&
		publication.ExpectedTargetRevision == preparation.ExpectedTargetRevision &&
		publication.PredecessorGenerationID == preparation.PredecessorGenerationID &&
		publication.CandidateID == preparation.CandidateID &&
		publication.GenerationID == preparation.GenerationID &&
		publication.PublicationID == preparation.PublicationID &&
		publication.ActorID == preparation.Receipt.ActorID
}

// CommitAuditIntent describes durable publication, never runtime readiness or
// permission to revoke the old credential. It is saved with the publication.
func (prepared PreparedActivation) CommitAuditIntent(eventID string) (access.AuditIntent, error) {
	if prepared.Validate() != nil || prepared.SwitchingAt.IsZero() ||
		!prepared.CommittedAt.IsZero() || !prepared.AbortedAt.IsZero() || !canonicalPreparationID(eventID) {
		return access.AuditIntent{}, ErrInvalid
	}
	preparation := prepared.Preparation
	receipt := preparation.Receipt
	metadata, err := json.Marshal(struct {
		OperationID   string `json:"operation_id"`
		ReceiptID     string `json:"receipt_id"`
		VersionID     string `json:"version_id"`
		CandidateID   string `json:"candidate_id"`
		GenerationID  string `json:"generation_id"`
		PublicationID string `json:"publication_id"`
	}{preparation.OperationID, receipt.ReceiptID, receipt.Binding.VersionID,
		preparation.CandidateID, preparation.GenerationID, preparation.PublicationID})
	if err != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	principalID := ""
	if canonicalPreparationID(receipt.ActorID) {
		principalID = receipt.ActorID
	}
	return (access.AuditIntent{
		EventID: eventID, DomainEventID: eventID,
		ScopeID: receipt.Binding.ProjectID, ActorID: receipt.ActorID, PrincipalID: principalID,
		Source: "credential", Operation: "commitCredentialActivation", Action: "credential.activation.committed",
		ResourceKind: "connection", ResourceID: receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey: "credential-activation:" + preparation.OperationID, AggregateSequence: 3,
		MetadataJSON: string(metadata),
	}).Canonicalize()
}
