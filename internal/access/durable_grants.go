package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// DurableGrantProfile is the profile of the independent, target-bound grant
// records. It is deliberately different from the generation-bound
// authorization snapshot profile and from the async job envelope profile.
const DurableGrantProfile = "leapview.durable-grants/v1"

const (
	GrantCredentialClassSession  = "session"
	GrantCredentialClassAPIToken = "api_token"
)

const (
	DurableGrantKindResourceShare GrantKind = "resource_share"
	DurableGrantKindExecution     GrantKind = "execution"
	DurableGrantKindAdminEnvelope GrantKind = "grant_admin_envelope"
)

var (
	ErrInvalidDurableGrant       = errors.New("invalid durable grant")
	ErrGrantIdempotencyConflict  = errors.New("durable grant idempotency key conflicts with a different request")
	ErrGrantNotFound             = errors.New("durable grant was not found")
	ErrGrantRevoked              = errors.New("durable grant is revoked")
	ErrGrantExpired              = errors.New("durable grant is expired")
	ErrGrantPrincipalInactive    = errors.New("durable grant principal is inactive")
	ErrGrantResourceInactive     = errors.New("durable grant resource is inactive")
	ErrGrantCredentialInvalid    = errors.New("durable grant credential evidence is invalid")
	ErrGrantNoOnwardDelegation   = errors.New("durable grant does not permit onward delegation")
	ErrGrantResourceUIDMismatch  = errors.New("durable grant resource UID does not match its exact target")
	ErrGrantPermissionCeiling    = errors.New("durable grant permission exceeds its issuer ceiling")
	ErrGrantMissingExecutionPair = errors.New("execution grant requires an exact pipeline.run pair")
)

// GrantKind distinguishes the durable authorities. The three kinds are
// separate records and must not be interpreted interchangeably.
type GrantKind string

// GrantCredentialEvidence is a non-secret binding to the credential that
// issued a grant. Fingerprint is evidence only; no bearer token or secret is
// accepted by these types.
type GrantCredentialEvidence struct {
	Class       string `json:"class"`
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

func (e GrantCredentialEvidence) Validate() error {
	if e.Class != GrantCredentialClassSession && e.Class != GrantCredentialClassAPIToken {
		return fmt.Errorf("%w: unsupported credential class %q", ErrGrantCredentialInvalid, e.Class)
	}
	if !durableIdentity(e.ID, 512) || !durableFingerprint(e.Fingerprint) {
		return fmt.Errorf("%w: credential id and fingerprint are required", ErrGrantCredentialInvalid)
	}
	return nil
}

// GrantIssuerEvidence binds the issuing principal and credential together.
// The issuer's current authority is checked by the repository at issuance;
// the captured values remain immutable historical evidence afterward.
type GrantIssuerEvidence struct {
	PrincipalID string                  `json:"principalId"`
	Credential  GrantCredentialEvidence `json:"credential"`
}

func (e GrantIssuerEvidence) Validate() error {
	if !canonicalUUID(e.PrincipalID) {
		return fmt.Errorf("%w: issuer principal is required", ErrInvalidDurableGrant)
	}
	if err := e.Credential.Validate(); err != nil {
		return fmt.Errorf("%w: issuer credential: %v", ErrInvalidDurableGrant, err)
	}
	return nil
}

// DurableGrantTarget is an exact instance/project/resource identity. A UID
// is mandatory for resource grants so deleting and recreating an authored ID
// cannot resurrect the old authority.
type DurableGrantTarget struct {
	InstanceID   string                  `json:"instanceId"`
	ProjectID    projectgraph.ResourceID `json:"projectId"`
	ResourceUID  string                  `json:"resourceUid"`
	ResourceID   projectgraph.ResourceID `json:"resourceId"`
	ResourceKind projectgraph.Kind       `json:"resourceKind"`
}

// GrantTarget is the shorter spelling used by callers composing grant
// envelopes.
type GrantTarget = DurableGrantTarget

func (t DurableGrantTarget) Validate() error {
	if !durableIdentity(t.InstanceID, 255) || !durableIdentity(t.ProjectID.String(), 255) ||
		!durableIdentity(t.ResourceID.String(), 255) || !canonicalUUID(t.ResourceUID) {
		return fmt.Errorf("%w: exact resource target is incomplete", ErrInvalidDurableGrant)
	}
	if !durableResourceKind(t.ResourceKind) {
		return fmt.Errorf("%w: unsupported resource kind %q", ErrInvalidDurableGrant, t.ResourceKind)
	}
	return nil
}

func (t DurableGrantTarget) pairTargetMatches(pair PermissionPair) bool {
	return pair.Target.Scope == PermissionScopeResource &&
		pair.Target.ProjectID == t.ProjectID && pair.Target.ResourceID == t.ResourceID &&
		pair.Target.ResourceKind == t.ResourceKind && !pair.Target.IncludeFuture
}

// ResourceShareGrant is an independently durable exact-resource share. It is
// not a generation-bound authorization_grant and remains valid across
// generation changes only while its UID target remains active.
type ResourceShareGrant struct {
	ID                   string
	Profile              string
	Target               DurableGrantTarget
	Issuer               GrantIssuerEvidence
	Recipient            SubjectRef
	RecipientPrincipalID string
	Permissions          []PermissionPair
	// IssuancePermissions is the already-resolved current intersection of the
	// issuer principal and credential authority. It is required because this
	// foundation does not guess authority from a browser or queue envelope.
	IssuancePermissions   []PermissionPair
	IssuedAt              time.Time
	ExpiresAt             time.Time
	Fingerprint           string
	IdempotencyKey        string
	RequestDigest         string
	AllowOnwardDelegation bool
	RevokedAt             time.Time
	RevokedByPrincipalID  string
	RevocationReason      string
}

// ShareGrant is retained as a concise API alias.
type ShareGrant = ResourceShareGrant

type ResourceShareGrantInput struct {
	ID                    string
	Profile               string
	Target                DurableGrantTarget
	Issuer                GrantIssuerEvidence
	Recipient             SubjectRef
	RecipientPrincipalID  string
	Permissions           []PermissionPair
	IssuancePermissions   []PermissionPair
	IssuedAt              time.Time
	ExpiresAt             time.Time
	TTL                   time.Duration
	IdempotencyKey        string
	RequestDigest         string
	AllowOnwardDelegation bool
}

func (in ResourceShareGrantInput) Validate() error {
	if in.Profile != "" && in.Profile != DurableGrantProfile {
		return fmt.Errorf("%w: unsupported profile %q", ErrInvalidDurableGrant, in.Profile)
	}
	if in.ID != "" && !durableIdentity(in.ID, 255) {
		return fmt.Errorf("%w: grant ID is invalid", ErrInvalidDurableGrant)
	}
	if err := in.Target.Validate(); err != nil {
		return err
	}
	if err := in.Issuer.Validate(); err != nil {
		return err
	}
	recipient, recipientErr := shareRecipient(in.Recipient, in.RecipientPrincipalID)
	if recipientErr != nil || recipient.ID == in.Issuer.PrincipalID {
		return fmt.Errorf("%w: recipient principal is invalid", ErrInvalidDurableGrant)
	}
	if err := validateIssuancePermissions(in.IssuancePermissions, in.Target, DurableGrantKindResourceShare, in.Permissions); err != nil {
		return err
	}
	if err := validateGrantPermissions(in.Permissions, in.Target, false); err != nil {
		return err
	}
	if in.TTL < 0 || in.TTL > maxDurableGrantTTL {
		return fmt.Errorf("%w: share TTL must be between zero and %s", ErrInvalidDurableGrant, maxDurableGrantTTL)
	}
	if !durableIdentity(in.IdempotencyKey, 256) {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidDurableGrant)
	}
	if in.RequestDigest != "" && !durableDigest(in.RequestDigest) {
		return fmt.Errorf("%w: request digest is invalid", ErrInvalidDurableGrant)
	}
	if in.ExpiresAt.IsZero() && in.TTL == 0 {
		// Shares may be non-expiring. The zero value is intentional.
		return nil
	}
	if !in.ExpiresAt.IsZero() && !in.IssuedAt.IsZero() && !in.ExpiresAt.After(in.IssuedAt) {
		return fmt.Errorf("%w: expiry must be after issuance", ErrInvalidDurableGrant)
	}
	return nil
}

func shareRecipient(subject SubjectRef, principalID string) (SubjectRef, error) {
	if subject.ID == "" {
		if !canonicalUUID(principalID) {
			return SubjectRef{}, fmt.Errorf("%w: recipient is required", ErrInvalidDurableGrant)
		}
		return NewSubjectRef(SubjectKindPrincipal, principalID)
	}
	if err := subject.Validate(); err != nil || !canonicalUUID(subject.ID) {
		return SubjectRef{}, fmt.Errorf("%w: recipient subject is invalid", ErrInvalidDurableGrant)
	}
	if principalID != "" && principalID != subject.ID {
		return SubjectRef{}, fmt.Errorf("%w: recipient aliases disagree", ErrInvalidDurableGrant)
	}
	return subject, nil
}

// ShareRecipient resolves the principal-ID compatibility field into the
// explicit principal/group subject used by durable share persistence.
func ShareRecipient(subject SubjectRef, principalID string) (SubjectRef, error) {
	return shareRecipient(subject, principalID)
}

// ExecutionGrant is an exact, bounded workload authority. Every digest is
// immutable evidence for the executable closure and its external boundaries.
type ExecutionGrant struct {
	ID                   string
	Profile              string
	Target               DurableGrantTarget
	Issuer               GrantIssuerEvidence
	ExecutionPrincipalID string
	Permissions          []PermissionPair
	WorkflowID           string
	WorkflowRevision     string
	ClosureDigest        string
	BindingDigest        string
	DestinationDigest    string
	TriggerDigest        string
	IssuedAt             time.Time
	ExpiresAt            time.Time
	Fingerprint          string
	IdempotencyKey       string
	RequestDigest        string
	RevokedAt            time.Time
	RevokedByPrincipalID string
	RevocationReason     string
}

type ExecutionGrantInput struct {
	ID                   string
	Profile              string
	Target               DurableGrantTarget
	Issuer               GrantIssuerEvidence
	ExecutionPrincipalID string
	Permissions          []PermissionPair
	IssuancePermissions  []PermissionPair
	WorkflowID           string
	WorkflowRevision     string
	ClosureDigest        string
	BindingDigest        string
	DestinationDigest    string
	TriggerDigest        string
	IssuedAt             time.Time
	ExpiresAt            time.Time
	TTL                  time.Duration
	IdempotencyKey       string
	RequestDigest        string
}

func (in ExecutionGrantInput) Validate() error {
	if in.Profile != "" && in.Profile != DurableGrantProfile {
		return fmt.Errorf("%w: unsupported profile %q", ErrInvalidDurableGrant, in.Profile)
	}
	if in.ID != "" && !durableIdentity(in.ID, 255) {
		return fmt.Errorf("%w: grant ID is invalid", ErrInvalidDurableGrant)
	}
	if err := in.Target.Validate(); err != nil {
		return err
	}
	if err := in.Issuer.Validate(); err != nil {
		return err
	}
	if !canonicalUUID(in.ExecutionPrincipalID) || in.ExecutionPrincipalID == in.Issuer.PrincipalID {
		return fmt.Errorf("%w: execution principal is invalid", ErrInvalidDurableGrant)
	}
	if in.Target.ResourceKind != projectgraph.KindPipeline {
		return fmt.Errorf("%w: execution target must be a pipeline", ErrInvalidDurableGrant)
	}
	if err := validateGrantPermissions(in.Permissions, in.Target, true); err != nil {
		return err
	}
	if err := validateIssuancePermissions(in.IssuancePermissions, in.Target, DurableGrantKindExecution, in.Permissions); err != nil {
		return err
	}
	hasRun := false
	for _, pair := range in.Permissions {
		if pair.Action == ActionPipelineRun && in.Target.pairTargetMatches(pair) {
			hasRun = true
		}
	}
	if !hasRun {
		return ErrGrantMissingExecutionPair
	}
	for label, value := range map[string]string{"workflow": in.WorkflowID, "workflow revision": in.WorkflowRevision, "closure digest": in.ClosureDigest, "binding digest": in.BindingDigest, "destination digest": in.DestinationDigest, "trigger digest": in.TriggerDigest} {
		if !durableIdentity(value, 512) && !durableDigest(value) {
			return fmt.Errorf("%w: %s evidence is required", ErrInvalidDurableGrant, label)
		}
	}
	if in.TTL < 0 || in.TTL > maxDurableGrantTTL || (in.TTL == 0 && in.ExpiresAt.IsZero()) {
		return fmt.Errorf("%w: execution TTL must be greater than zero and at most %s", ErrInvalidDurableGrant, maxDurableGrantTTL)
	}
	if !durableIdentity(in.IdempotencyKey, 256) {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidDurableGrant)
	}
	if in.RequestDigest != "" && !durableDigest(in.RequestDigest) {
		return fmt.Errorf("%w: request digest is invalid", ErrInvalidDurableGrant)
	}
	if !in.ExpiresAt.IsZero() && !in.IssuedAt.IsZero() && !in.ExpiresAt.After(in.IssuedAt) {
		return fmt.Errorf("%w: expiry must be after issuance", ErrInvalidDurableGrant)
	}
	return nil
}

// GrantAdminEnvelope bounds access administration itself. Onward delegation
// is explicit and defaults to false; a missing/false value cannot be treated
// as a wildcard.
type GrantAdminEnvelope struct {
	ID                    string
	Profile               string
	Issuer                GrantIssuerEvidence
	BoundPrincipalID      string
	Permissions           []PermissionPair
	TargetProjectID       projectgraph.ResourceID
	TargetResourceKind    projectgraph.Kind
	TargetResourceID      projectgraph.ResourceID
	RecipientSelector     string
	RoleVersion           string
	IssuedAt              time.Time
	ExpiresAt             time.Time
	Fingerprint           string
	IdempotencyKey        string
	RequestDigest         string
	AllowOnwardDelegation bool
	RevokedAt             time.Time
	RevokedByPrincipalID  string
	RevocationReason      string
}

type GrantAdminEnvelopeInput struct {
	ID                    string
	Profile               string
	Issuer                GrantIssuerEvidence
	BoundPrincipalID      string
	Permissions           []PermissionPair
	IssuancePermissions   []PermissionPair
	TargetProjectID       projectgraph.ResourceID
	TargetResourceKind    projectgraph.Kind
	TargetResourceID      projectgraph.ResourceID
	RecipientSelector     string
	RoleVersion           string
	IssuedAt              time.Time
	ExpiresAt             time.Time
	TTL                   time.Duration
	IdempotencyKey        string
	RequestDigest         string
	AllowOnwardDelegation bool
}

func (in GrantAdminEnvelopeInput) Validate() error {
	if in.Profile != "" && in.Profile != DurableGrantProfile {
		return fmt.Errorf("%w: unsupported profile %q", ErrInvalidDurableGrant, in.Profile)
	}
	if in.ID != "" && !durableIdentity(in.ID, 255) {
		return fmt.Errorf("%w: envelope ID is invalid", ErrInvalidDurableGrant)
	}
	if err := in.Issuer.Validate(); err != nil {
		return err
	}
	if !canonicalUUID(in.BoundPrincipalID) || in.BoundPrincipalID != in.Issuer.PrincipalID {
		return fmt.Errorf("%w: envelope principal must equal its issuer", ErrInvalidDurableGrant)
	}
	if err := ValidatePermissionPairs(in.Permissions); err != nil || len(in.Permissions) == 0 {
		if err == nil {
			err = ErrTokenPermissionsNeeded
		}
		return fmt.Errorf("%w: envelope permissions: %v", ErrInvalidDurableGrant, err)
	}
	if err := validateIssuancePermissions(in.IssuancePermissions, DurableGrantTarget{ProjectID: in.TargetProjectID}, DurableGrantKindAdminEnvelope, in.Permissions); err != nil {
		return err
	}
	if in.TargetProjectID == "" || in.TargetProjectID.Validate() != nil {
		return fmt.Errorf("%w: envelope target project is required", ErrInvalidDurableGrant)
	}
	if in.TargetResourceKind != "" && !durableResourceKind(in.TargetResourceKind) {
		return fmt.Errorf("%w: envelope target resource kind is invalid", ErrInvalidDurableGrant)
	}
	if (in.TargetResourceKind == "") != (in.TargetResourceID == "") {
		return fmt.Errorf("%w: envelope resource selector is incomplete", ErrInvalidDurableGrant)
	}
	if in.TargetResourceID != "" && in.TargetResourceID.Validate() != nil {
		return fmt.Errorf("%w: envelope target resource is invalid", ErrInvalidDurableGrant)
	}
	if !durableIdentity(in.RecipientSelector, 1024) || !durableIdentity(in.RoleVersion, 255) {
		return fmt.Errorf("%w: recipient selector and role version are required", ErrInvalidDurableGrant)
	}
	if in.TTL < 0 || in.TTL > maxDurableGrantTTL || (in.TTL == 0 && in.ExpiresAt.IsZero()) {
		return fmt.Errorf("%w: envelope TTL must be greater than zero and at most %s", ErrInvalidDurableGrant, maxDurableGrantTTL)
	}
	if !durableIdentity(in.IdempotencyKey, 256) {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidDurableGrant)
	}
	if in.RequestDigest != "" && !durableDigest(in.RequestDigest) {
		return fmt.Errorf("%w: request digest is invalid", ErrInvalidDurableGrant)
	}
	return nil
}

const maxDurableGrantTTL = 365 * 24 * time.Hour

func validateGrantPermissions(pairs []PermissionPair, target DurableGrantTarget, execution bool) error {
	if err := ValidatePermissionPairs(pairs); err != nil {
		return fmt.Errorf("%w: permission pairs: %v", ErrInvalidDurableGrant, err)
	}
	if len(pairs) == 0 {
		return fmt.Errorf("%w: at least one permission pair is required", ErrInvalidDurableGrant)
	}
	for _, pair := range pairs {
		if !execution && !target.pairTargetMatches(pair) {
			return fmt.Errorf("%w: every grant pair must bind the exact target", ErrInvalidDurableGrant)
		}
		if execution && (pair.Target.Scope != PermissionScopeResource || pair.Target.ProjectID != target.ProjectID || pair.Target.IncludeFuture) {
			return fmt.Errorf("%w: execution pairs must be exact resources in the target project", ErrInvalidDurableGrant)
		}
		definition, ok := Permission(pair.Action)
		if !ok {
			return fmt.Errorf("%w: unknown action %q", ErrInvalidDurableGrant, pair.Action)
		}
		if !definition.Delegable && !(execution && pair.Action == ActionPipelineRun) {
			return fmt.Errorf("%w: action %q is not delegable", ErrGrantPermissionCeiling, pair.Action)
		}
	}
	return nil
}

func validateIssuancePermissions(authority []PermissionPair, target DurableGrantTarget, kind GrantKind, requested []PermissionPair) error {
	if authority == nil {
		return fmt.Errorf("%w: explicit current issuance authority is required", ErrGrantPermissionCeiling)
	}
	if err := ValidatePermissionPairs(authority); err != nil {
		return fmt.Errorf("%w: issuance authority: %v", ErrGrantPermissionCeiling, err)
	}
	for _, pair := range requested {
		if !PermissionSetAllows(authority, pair) {
			return fmt.Errorf("%w: requested action %q is outside current issuer authority", ErrGrantPermissionCeiling, pair.Action)
		}
	}
	if kind == DurableGrantKindAdminEnvelope {
		manage, err := NewProjectPermissionPair(ActionProjectAccessManage, target.ProjectID)
		if err != nil || !PermissionSetAllows(authority, manage) {
			return fmt.Errorf("%w: project.access.manage is required for envelope issuance", ErrGrantPermissionCeiling)
		}
		delegate, err := NewProjectPermissionPair(ActionProjectAccessDelegate, target.ProjectID)
		if err != nil || !PermissionSetAllows(authority, delegate) {
			return fmt.Errorf("%w: project.access.delegate is required for envelope issuance", ErrGrantPermissionCeiling)
		}
		return nil
	}
	resource, err := NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		return err
	}
	action := ActionResourceShare
	if kind == DurableGrantKindExecution {
		action = ActionWorkloadDelegate
	}
	required, err := NewExactPermissionPair(action, target.ProjectID, resource)
	if err != nil || !PermissionSetAllows(authority, required) {
		return fmt.Errorf("%w: required issuance action %q is unavailable", ErrGrantPermissionCeiling, action)
	}
	return nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func durableIdentity(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}

func durableFingerprint(value string) bool {
	if !durableIdentity(value, 512) {
		return false
	}
	// Accept either the native 64-hex credential fingerprint or a test/adapter
	// opaque evidence ID. Neither form can be used as a bearer credential.
	return len(value) >= 16
}

func durableDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func durableResourceKind(kind projectgraph.Kind) bool {
	switch kind {
	case projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard:
		return true
	default:
		return false
	}
}

func grantDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// ResourceShareGrantFingerprint computes the stable fingerprint for a grant
// after validation. Repositories use it as immutable evidence and never
// expose a bearer secret.
func ResourceShareGrantFingerprint(in ResourceShareGrantInput, issuedAt time.Time, expiresAt time.Time) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	encoded, err := EncodePermissionPairs(in.Permissions)
	if err != nil {
		return "", err
	}
	return grantDigest(struct {
		Kind        string
		Profile     string
		Target      DurableGrantTarget
		Issuer      GrantIssuerEvidence
		Recipient   SubjectRef
		Permissions json.RawMessage
		IssuedAt    time.Time
		ExpiresAt   time.Time
		Onward      bool
	}{string(DurableGrantKindResourceShare), in.Profile, in.Target, in.Issuer, func() SubjectRef { subject, _ := shareRecipient(in.Recipient, in.RecipientPrincipalID); return subject }(), encoded, issuedAt.UTC(), expiresAt.UTC(), in.AllowOnwardDelegation}), nil
}

func ExecutionGrantFingerprint(in ExecutionGrantInput, issuedAt, expiresAt time.Time) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	encoded, err := EncodePermissionPairs(in.Permissions)
	if err != nil {
		return "", err
	}
	return grantDigest(struct {
		Kind, Profile                                                  string
		Target                                                         DurableGrantTarget
		Issuer                                                         GrantIssuerEvidence
		ExecutionPrincipalID                                           string
		Permissions                                                    json.RawMessage
		WorkflowID, WorkflowRevision                                   string
		ClosureDigest, BindingDigest, DestinationDigest, TriggerDigest string
		IssuedAt, ExpiresAt                                            time.Time
	}{string(DurableGrantKindExecution), in.Profile, in.Target, in.Issuer, in.ExecutionPrincipalID, encoded, in.WorkflowID, in.WorkflowRevision, in.ClosureDigest, in.BindingDigest, in.DestinationDigest, in.TriggerDigest, issuedAt.UTC(), expiresAt.UTC()}), nil
}

func GrantAdminEnvelopeFingerprint(in GrantAdminEnvelopeInput, issuedAt, expiresAt time.Time) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	encoded, err := EncodePermissionPairs(in.Permissions)
	if err != nil {
		return "", err
	}
	return grantDigest(struct {
		Kind, Profile                  string
		Issuer                         GrantIssuerEvidence
		BoundPrincipalID               string
		Permissions                    json.RawMessage
		TargetProjectID                projectgraph.ResourceID
		TargetResourceKind             projectgraph.Kind
		TargetResourceID               projectgraph.ResourceID
		RecipientSelector, RoleVersion string
		IssuedAt, ExpiresAt            time.Time
		Onward                         bool
	}{string(DurableGrantKindAdminEnvelope), in.Profile, in.Issuer, in.BoundPrincipalID, encoded, in.TargetProjectID, in.TargetResourceKind, in.TargetResourceID, in.RecipientSelector, in.RoleVersion, issuedAt.UTC(), expiresAt.UTC(), in.AllowOnwardDelegation}), nil
}
