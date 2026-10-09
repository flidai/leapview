package credential

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/access"
)

func (prepared PreparedActivation) CompletionAuditIntent(eventID string) (access.AuditIntent, error) {
	if prepared.Validate() != nil || prepared.CommittedAt.IsZero() || !prepared.CompletedAt.IsZero() || !canonicalPreparationID(eventID) {
		return access.AuditIntent{}, ErrInvalid
	}
	preparation := prepared.Preparation
	receipt := preparation.Receipt
	metadata, err := json.Marshal(struct {
		OperationID string `json:"operation_id"`
		VersionID   string `json:"version_id"`
		State       string `json:"state"`
	}{preparation.OperationID, receipt.Binding.VersionID, "completed"})
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
		Source: "credential", Operation: "completeCredentialActivation", Action: "credential.activation.completed",
		ResourceKind: "connection", ResourceID: receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey: "credential-activation:" + preparation.OperationID, AggregateSequence: 4, MetadataJSON: string(metadata),
	}).Canonicalize()
}
