package credential

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/access"
)

// AbortAuditIntent describes the terminal cancellation of a prepared or
// switching activation. The event is constructed from the stored preparation and the
// current actor, so callers cannot attach secret material or caller-authored
// resource identities to the audit record.
func (prepared PreparedActivation) AbortAuditIntent(eventID, actorID string) (access.AuditIntent, error) {
	if prepared.Validate() != nil || !canonical(actorID) ||
		!prepared.AbortedAt.IsZero() || !prepared.CommittedAt.IsZero() || prepared.AbortedBy != "" || !canonicalPreparationID(eventID) {
		return access.AuditIntent{}, ErrInvalid
	}
	preparation := prepared.Preparation
	metadata, err := json.Marshal(struct {
		OperationID   string `json:"operation_id"`
		ReceiptID     string `json:"receipt_id"`
		CandidateID   string `json:"candidate_id"`
		GenerationID  string `json:"generation_id"`
		PublicationID string `json:"publication_id"`
		AbortedBy     string `json:"aborted_by"`
	}{preparation.OperationID, preparation.Receipt.ReceiptID, preparation.CandidateID,
		preparation.GenerationID, preparation.PublicationID, actorID})
	if err != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	principalID := ""
	if canonicalPreparationID(actorID) {
		principalID = actorID
	}
	sequence := int64(2)
	if !prepared.SwitchingAt.IsZero() {
		sequence = 3
	}
	intent, err := (access.AuditIntent{
		EventID: eventID, DomainEventID: eventID,
		ScopeID: preparation.Receipt.Binding.ProjectID, ActorID: actorID, PrincipalID: principalID,
		Source: "credential", Operation: "abortCredentialActivation", Action: "credential.activation.aborted",
		ResourceKind: "connection", ResourceID: preparation.Receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey: "credential-activation:" + preparation.OperationID, AggregateSequence: sequence,
		MetadataJSON: string(metadata),
	}).Canonicalize()
	if err != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	return intent, nil
}
