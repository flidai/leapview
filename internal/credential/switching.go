package credential

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/access"
)

// SwitchingAuditIntent records the intent to pause and drain, never an
// acknowledgment of drain, publication, or live readiness. The original
// receipt actor must still authorize this transition; the receipt is not a grant.
func (prepared PreparedActivation) SwitchingAuditIntent(eventID string) (access.AuditIntent, error) {
	if prepared.Validate() != nil || !prepared.SwitchingAt.IsZero() ||
		!prepared.AbortedAt.IsZero() || !canonicalPreparationID(eventID) {
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
		Source: "credential", Operation: "beginCredentialActivationSwitching", Action: "credential.activation.switching",
		ResourceKind: "connection", ResourceID: receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey: "credential-activation:" + preparation.OperationID, AggregateSequence: 2,
		MetadataJSON: string(metadata),
	}).Canonicalize()
}
