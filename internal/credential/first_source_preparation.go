package credential

import (
	"encoding/json"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
)

// FirstSourcePreparationIntent retains explicit source and normal publisher
// intent. The reserved validation receipt never confers publication authority.
type FirstSourcePreparationIntent struct {
	PreparationID           string            `json:"preparationId"`
	AdmissionOperationID    string            `json:"admissionOperationId"`
	AdmissionDigest         string            `json:"admissionDigest"`
	Receipt                 ValidationReceipt `json:"receipt"`
	PublisherID             string            `json:"publisherId"`
	SourceOwnerID           string            `json:"sourceOwnerId"`
	SourceDigest            string            `json:"sourceDigest"`
	SourceAttestationDigest string            `json:"sourceAttestationDigest"`
	PlanOperation           string            `json:"planOperation"`
	PlanIdempotencyKey      string            `json:"planIdempotencyKey"`
	PlanRequestDigest       string            `json:"planRequestDigest"`
	ExpectedTargetRevision  int64             `json:"expectedTargetRevision"`
}

func (i FirstSourcePreparationIntent) Validate() error {
	if !canonicalPreparationID(i.PreparationID) || !canonicalPreparationID(i.AdmissionOperationID) ||
		!destinationDigest(i.AdmissionDigest) || i.Receipt.Validate() != nil ||
		i.Receipt.Binding.ScopeKind != "connection" || i.Receipt.Binding.Provider != "postgres" ||
		i.Receipt.Binding.DeploymentID != i.Receipt.Binding.TargetID ||
		!canonicalPreparationID(i.Receipt.Binding.VersionID) ||
		!i.Receipt.ValidatedAt.Equal(i.Receipt.ValidatedAt.Truncate(time.Microsecond)) ||
		!i.Receipt.ExpiresAt.Equal(i.Receipt.ExpiresAt.Truncate(time.Microsecond)) ||
		!canonical(i.PublisherID) || !canonical(i.SourceOwnerID) ||
		!destinationDigest(i.SourceDigest) || !destinationDigest(i.SourceAttestationDigest) ||
		i.PlanOperation != "code_change" || !canonical(i.PlanIdempotencyKey) ||
		!destinationDigest(i.PlanRequestDigest) || i.ExpectedTargetRevision < 1 {
		return ErrInvalid
	}
	return nil
}

func (i FirstSourcePreparationIntent) Digest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return firstSourceDigest(i)
}

func (i FirstSourcePreparationIntent) MatchesAdmission(a FirstSourceAdmission) bool {
	b, admitted := i.Receipt.Binding, a.Intent
	return a.Validate() == nil && i.AdmissionOperationID == admitted.OperationID && i.AdmissionDigest == a.IntentDigest &&
		b.TargetID == admitted.TargetID && b.ProjectID == admitted.ProjectID && b.Environment == admitted.Environment &&
		b.OwnerID == admitted.CustomerOwnerID && b.ResourceID == admitted.ConnectionID &&
		i.Receipt.ActorID == admitted.OperatorPrincipalID && i.Receipt.BindingID == admitted.BindingID && i.Receipt.BindingRevision == 1
}

type FirstSourcePreparation struct {
	Intent       FirstSourcePreparationIntent
	IntentDigest string
	CreatedAt    time.Time
}

func (p FirstSourcePreparation) Validate() error {
	digest, err := p.Intent.Digest()
	if err != nil || digest != p.IntentDigest || p.CreatedAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

// FirstSourcePlanLink is selected in the normal CreatePlan transaction. Later
// build/recovery resolves this exact plan identity, never an ambient draft.
type FirstSourcePlanLink struct {
	TargetID      string `json:"targetId"`
	PreparationID string `json:"preparationId"`
	PlanID        string `json:"planId"`
	RequestDigest string `json:"requestDigest"`
	BindingDigest string `json:"bindingDigest"`
}

func (l FirstSourcePlanLink) Validate() error {
	plan, err := uuid.Parse(l.PlanID)
	if err != nil || plan == uuid.Nil || plan.String() != l.PlanID || plan.Version() != 7 ||
		!canonical(l.TargetID) || !canonicalPreparationID(l.PreparationID) ||
		!destinationDigest(l.RequestDigest) || !destinationDigest(l.BindingDigest) {
		return ErrInvalid
	}
	return nil
}

func (i FirstSourcePreparationIntent) PreparationAuditIntent() (access.AuditIntent, error) {
	digest, err := i.Digest()
	if err != nil {
		return access.AuditIntent{}, err
	}
	return i.auditIntent("credential.first_source.prepared", map[string]string{"preparationId": i.PreparationID, "intentDigest": digest})
}

func (i FirstSourcePreparationIntent) RenewalAuditIntent(receiptID string) (access.AuditIntent, error) {
	if !canonicalPreparationID(receiptID) {
		return access.AuditIntent{}, ErrInvalid
	}
	return i.auditIntent("credential.first_source.receipt_renewed", map[string]string{"preparationId": i.PreparationID, "receiptId": receiptID})
}

func (i FirstSourcePreparationIntent) PlanAuditIntent(link FirstSourcePlanLink) (access.AuditIntent, error) {
	if link.Validate() != nil || link.PreparationID != i.PreparationID || link.TargetID != i.Receipt.Binding.TargetID || link.RequestDigest != i.PlanRequestDigest {
		return access.AuditIntent{}, ErrInvalid
	}
	intent, err := i.auditIntent("credential.first_source.plan_selected", link)
	if err != nil {
		return intent, err
	}
	intent.ActorID = i.PublisherID
	intent.PrincipalID = ""
	if canonicalPreparationID(i.PublisherID) {
		intent.PrincipalID = i.PublisherID
	}
	return intent.Canonicalize()
}

func (i FirstSourcePreparationIntent) auditIntent(action string, metadata any) (access.AuditIntent, error) {
	if i.Validate() != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return access.AuditIntent{}, err
	}
	actor := i.Receipt.ActorID
	principal := ""
	if canonicalPreparationID(actor) {
		principal = actor
	}
	eventID := uuid.NewString()
	return (access.AuditIntent{EventID: eventID, AggregateKey: "credential-first-source-preparation:" + eventID, AggregateSequence: 1, ScopeID: i.Receipt.Binding.TargetID, ActorID: actor, PrincipalID: principal,
		Source: "credential", Operation: "prepareFirstSource", Action: action, ResourceKind: "connection", ResourceID: i.Receipt.Binding.ResourceID,
		Outcome: "success", MetadataJSON: string(body)}).Canonicalize()
}
