package credential

import (
	"encoding/json"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
)

// ActivationRequestRecord is reserved before candidate construction so a
// process interruption can neither lose the operation nor bypass its fence.
// Receipt is the original immutable request; later observations are appended.
type ActivationRequestRecord struct {
	Request                                          ActivationRequest
	Receipt                                          ValidationReceipt
	State                                            string
	Revision                                         int64
	CreatedAt, UpdatedAt                             time.Time
	PlanID, CandidateID, GenerationID, PublicationID string
	ConfigurationRevision                            int64
}

func (record ActivationRequestRecord) Resource() Resource {
	b := record.Receipt.Binding
	return Resource{ScopeKind: b.ScopeKind, TargetID: b.TargetID, ProjectID: b.ProjectID, Environment: b.Environment, ResourceID: b.ResourceID}
}

func (record ActivationRequestRecord) ActivationRecord() ActivationRecord {
	return ActivationRecord{Resource: record.Resource(), Request: record.Request, Status: ActivationStatus{
		OperationID: record.Request.OperationID, VersionID: record.Request.VersionID, State: record.State,
		BindingRevision: record.Request.ExpectedBindingRevision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		CandidateID: record.CandidateID, GenerationID: record.GenerationID, PublicationID: record.PublicationID,
	}}
}

func (record ActivationRequestRecord) Validate() error {
	if record.Request.Validate() != nil || record.Receipt.Validate() != nil || record.Request.VersionID != record.Receipt.Binding.VersionID ||
		record.Request.ReceiptID != record.Receipt.ReceiptID || record.Request.ExpectedBindingRevision != record.Receipt.BindingRevision || record.Revision < 1 ||
		record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return ErrInvalid
	}
	switch record.State {
	case "preparing", "prepared", "switching", "committed", "completed", "aborted":
	default:
		return ErrInvalid
	}
	if record.State != "preparing" && record.State != "aborted" {
		if record.Resource().ScopeKind == "connection" {
			for _, id := range []string{record.PlanID, record.CandidateID, record.GenerationID, record.PublicationID} {
				if !activationUUID(id) {
					return ErrInvalid
				}
			}
		} else if record.ConfigurationRevision < 1 {
			return ErrInvalid
		}
	}
	return nil
}

func (record ActivationRequestRecord) CommandAudit(actor, action string) (access.AuditIntent, error) {
	if record.Validate() != nil || !canonical(actor) {
		return access.AuditIntent{}, ErrInvalid
	}
	operation := ""
	switch action {
	case "credential.activation.requested":
		operation = "startCredentialActivation"
	case "credential.activation.retried":
		operation = "retryCredentialActivation"
	case "credential.activation.aborted":
		operation = "abortCredentialActivation"
	default:
		return access.AuditIntent{}, ErrInvalid
	}
	metadata, _ := json.Marshal(struct {
		OperationID string `json:"operation_id"`
		VersionID   string `json:"version_id"`
		State       string `json:"state"`
	}{record.Request.OperationID, record.Request.VersionID, record.State})
	principal := ""
	if activationUUID(actor) {
		principal = actor
	}
	scopeID, resourceKind := credentialAuditScope(record.Receipt.Binding)
	return (access.AuditIntent{EventID: uuid.NewString(), DomainEventID: uuid.NewString(), ScopeID: scopeID,
		ActorID: actor, PrincipalID: principal, Source: "credential", Operation: operation, Action: action,
		ResourceKind: resourceKind, ResourceID: record.Receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey: "credential-activation-command:" + record.Request.OperationID, AggregateSequence: record.Revision, MetadataJSON: string(metadata),
	}).Canonicalize()
}
