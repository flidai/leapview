package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// DurableGrantWriter is the mutation-only port used by DurableGrantService.
// It intentionally does not expose reads or authority lookup: callers must
// resolve current authority through CurrentAuthorityResolver and the writer
// remains responsible for durable transaction and audit semantics.
type DurableGrantWriter interface {
	CreateResourceShareGrant(context.Context, ResourceShareGrantInput) (ResourceShareGrant, error)
	CreateExecutionGrant(context.Context, ExecutionGrantInput) (ExecutionGrant, error)
	CreateGrantAdminEnvelope(context.Context, GrantAdminEnvelopeInput) (GrantAdminEnvelope, error)
	RevokeResourceShareGrant(context.Context, string, string, string) error
	RevokeExecutionGrant(context.Context, string, string, string) error
	RevokeGrantAdminEnvelope(context.Context, string, string, string) error
}

// CurrentAuthorityRequest identifies the exact target for which an authority
// snapshot is needed. The principal and credential are deliberately absent:
// they must come from the authenticated server-side context owned by the
// resolver, never from an HTTP body or picker payload.
type CurrentAuthorityRequest struct {
	Target DurableGrantTarget
}

// CurrentAuthorityResolver is the only authority input accepted by the
// durable grant service. Implementations must resolve the principal and all
// applicable groups from one coherent current authorization snapshot at the
// mutation boundary. A resolver must return an error when that snapshot or
// current credential evidence cannot be established.
type CurrentAuthorityResolver interface {
	ResolveCurrentAuthority(context.Context, CurrentAuthorityRequest) (CurrentAuthoritySnapshot, error)
}

// CurrentAuthorityResolverFunc adapts a function to CurrentAuthorityResolver.
type CurrentAuthorityResolverFunc func(context.Context, CurrentAuthorityRequest) (CurrentAuthoritySnapshot, error)

func (f CurrentAuthorityResolverFunc) ResolveCurrentAuthority(ctx context.Context, request CurrentAuthorityRequest) (CurrentAuthoritySnapshot, error) {
	if f == nil {
		return CurrentAuthoritySnapshot{}, ErrGrantAuthorityUnavailable
	}
	return f(ctx, request)
}

// CurrentAuthoritySnapshot is the resolver's single coherent view of the
// issuing principal, its group subjects, typed principal+group authority, and
// the current credential ceiling. Permissions is the union produced by the
// same principal/group snapshot. CredentialPermissions is required for API
// tokens and is intersected with Permissions before any grant input is built.
// For a browser session, credential attenuation is not yet a separate typed
// set in the current session contract; the resolver must instead provide
// current session evidence and the coherent typed snapshot together. If a
// resolver does provide a non-nil session ceiling, it is intersected too.
type CurrentAuthoritySnapshot struct {
	Principal             Principal
	Groups                []SubjectRef
	Permissions           []PermissionPair
	Credential            CredentialEvidence
	CredentialPermissions []PermissionPair
}

// AuthoritySnapshot is a concise compatibility alias for callers that use
// the shorter name.
type AuthoritySnapshot = CurrentAuthoritySnapshot

var (
	ErrGrantAuthorityUnavailable = errors.New("current grant authority is unavailable")
	ErrGrantAuthorityInvalid     = errors.New("current grant authority is invalid")
	ErrGrantAuthorityAttenuated  = errors.New("current grant authority is outside the credential ceiling")
	ErrGrantRevokeInvalid        = errors.New("invalid durable grant revocation")
)

// ResourceShareGrantRequest contains only user-controlled share parameters.
// Issuer and IssuancePermissions are intentionally not fields on this type.
type ResourceShareGrantRequest struct {
	ID                    string
	Profile               string
	Target                DurableGrantTarget
	Recipient             SubjectRef
	RecipientPrincipalID  string
	Permissions           []PermissionPair
	IssuedAt              time.Time
	ExpiresAt             time.Time
	TTL                   time.Duration
	IdempotencyKey        string
	RequestDigest         string
	AllowOnwardDelegation bool
}

// ResourceShareRequest and ShareGrantRequest retain concise names for
// adapters without exposing the repository's authority-bearing input type.
type ResourceShareRequest = ResourceShareGrantRequest
type ShareGrantRequest = ResourceShareGrantRequest

// ExecutionGrantRequest contains only user-controlled bounded execution
// parameters. The issuer, bound principal, and issuance ceiling are derived
// by DurableGrantService.
type ExecutionGrantRequest struct {
	ID                   string
	Profile              string
	Target               DurableGrantTarget
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
	TTL                  time.Duration
	IdempotencyKey       string
	RequestDigest        string
}

// GrantAdminEnvelopeRequest contains only the bounded envelope requested by
// the caller. BoundPrincipalID is always the current resolved principal and
// cannot be selected by the HTTP/body caller.
type GrantAdminEnvelopeRequest struct {
	ID                    string
	Profile               string
	Permissions           []PermissionPair
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

// DurableGrantService is the narrow internal issuance/revocation service.
// It owns all authority-bearing fields passed to DurableGrantWriter.
type DurableGrantService struct {
	writer   DurableGrantWriter
	resolver CurrentAuthorityResolver
	now      func() time.Time
}

// NewDurableGrantService creates a service with explicit authority and durable
// mutation ports. Both are required so no caller can accidentally obtain a
// service that falls back to request-supplied authority.
func NewDurableGrantService(writer DurableGrantWriter, resolver CurrentAuthorityResolver) (*DurableGrantService, error) {
	if writer == nil {
		return nil, fmt.Errorf("durable grant writer is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("current authority resolver is required")
	}
	return &DurableGrantService{writer: writer, resolver: resolver, now: func() time.Time { return time.Now().UTC() }}, nil
}

// IssueResourceShare resolves current authority exactly once and constructs
// the repository input, including IssuancePermissions, internally.
func (s *DurableGrantService) IssueResourceShare(ctx context.Context, request ResourceShareGrantRequest) (ResourceShareGrant, error) {
	if s == nil || s.writer == nil || s.resolver == nil {
		return ResourceShareGrant{}, ErrGrantAuthorityUnavailable
	}
	if err := validateShareRequest(request); err != nil {
		return ResourceShareGrant{}, err
	}
	if request.AllowOnwardDelegation {
		return ResourceShareGrant{}, ErrGrantNoOnwardDelegation
	}
	authority, err := s.resolve(ctx, request.Target)
	if err != nil {
		return ResourceShareGrant{}, err
	}
	issuer, ceiling, err := authority.issuance(request.Target, DurableGrantKindResourceShare, request.Permissions, s.currentTime())
	if err != nil {
		return ResourceShareGrant{}, err
	}
	in := ResourceShareGrantInput{
		ID: request.ID, Profile: request.Profile, Target: request.Target, Issuer: issuer,
		Recipient: request.Recipient, RecipientPrincipalID: request.RecipientPrincipalID,
		Permissions: ClonePermissionPairs(request.Permissions), IssuancePermissions: ceiling,
		IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt, TTL: request.TTL,
		IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest,
		AllowOnwardDelegation: false,
	}
	if err := in.Validate(); err != nil {
		return ResourceShareGrant{}, err
	}
	return s.writer.CreateResourceShareGrant(ctx, in)
}

// IssueExecutionGrant issues a bounded pipeline execution grant using only
// authority resolved by the service. Execution recipients are always exact
// principals; group execution subjects are not accepted by the durable model.
func (s *DurableGrantService) IssueExecutionGrant(ctx context.Context, request ExecutionGrantRequest) (ExecutionGrant, error) {
	if s == nil || s.writer == nil || s.resolver == nil {
		return ExecutionGrant{}, ErrGrantAuthorityUnavailable
	}
	if err := validateExecutionRequest(request); err != nil {
		return ExecutionGrant{}, err
	}
	authority, err := s.resolve(ctx, request.Target)
	if err != nil {
		return ExecutionGrant{}, err
	}
	issuer, ceiling, err := authority.issuance(request.Target, DurableGrantKindExecution, request.Permissions, s.currentTime())
	if err != nil {
		return ExecutionGrant{}, err
	}
	in := ExecutionGrantInput{
		ID: request.ID, Profile: request.Profile, Target: request.Target, Issuer: issuer,
		ExecutionPrincipalID: request.ExecutionPrincipalID, Permissions: ClonePermissionPairs(request.Permissions),
		IssuancePermissions: ceiling, WorkflowID: request.WorkflowID, WorkflowRevision: request.WorkflowRevision,
		ClosureDigest: request.ClosureDigest, BindingDigest: request.BindingDigest,
		DestinationDigest: request.DestinationDigest, TriggerDigest: request.TriggerDigest,
		IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt, TTL: request.TTL,
		IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest,
	}
	if err := in.Validate(); err != nil {
		return ExecutionGrant{}, err
	}
	return s.writer.CreateExecutionGrant(ctx, in)
}

// IssueGrantAdminEnvelope binds the envelope to the currently resolved
// principal and requires both project.access.manage and project.access.delegate
// through the same coherent authority snapshot.
func (s *DurableGrantService) IssueGrantAdminEnvelope(ctx context.Context, request GrantAdminEnvelopeRequest) (GrantAdminEnvelope, error) {
	if s == nil || s.writer == nil || s.resolver == nil {
		return GrantAdminEnvelope{}, ErrGrantAuthorityUnavailable
	}
	if request.AllowOnwardDelegation {
		return GrantAdminEnvelope{}, ErrGrantNoOnwardDelegation
	}
	target := DurableGrantTarget{ProjectID: request.TargetProjectID, ResourceKind: request.TargetResourceKind, ResourceID: request.TargetResourceID}
	authority, err := s.resolve(ctx, target)
	if err != nil {
		return GrantAdminEnvelope{}, err
	}
	issuer, ceiling, err := authority.issuance(target, DurableGrantKindAdminEnvelope, request.Permissions, s.currentTime())
	if err != nil {
		return GrantAdminEnvelope{}, err
	}
	in := GrantAdminEnvelopeInput{
		ID: request.ID, Profile: request.Profile, Issuer: issuer, BoundPrincipalID: authority.Principal.ID,
		Permissions: ClonePermissionPairs(request.Permissions), IssuancePermissions: ceiling,
		TargetProjectID: request.TargetProjectID, TargetResourceKind: request.TargetResourceKind, TargetResourceID: request.TargetResourceID,
		RecipientSelector: request.RecipientSelector, RoleVersion: request.RoleVersion,
		IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt, TTL: request.TTL,
		IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest,
		AllowOnwardDelegation: false,
	}
	if err := in.Validate(); err != nil {
		return GrantAdminEnvelope{}, err
	}
	return s.writer.CreateGrantAdminEnvelope(ctx, in)
}

// RevokeResourceShareGrant performs the monotonic active-to-revoked mutation
// using the current resolved principal as actor. The actor is never accepted
// from a request body and a repeated repository revoke cannot restore state.
func (s *DurableGrantService) RevokeResourceShareGrant(ctx context.Context, id, reason string) error {
	return s.revoke(ctx, DurableGrantKindResourceShare, id, reason)
}

func (s *DurableGrantService) RevokeExecutionGrant(ctx context.Context, id, reason string) error {
	return s.revoke(ctx, DurableGrantKindExecution, id, reason)
}

func (s *DurableGrantService) RevokeGrantAdminEnvelope(ctx context.Context, id, reason string) error {
	return s.revoke(ctx, DurableGrantKindAdminEnvelope, id, reason)
}

func (s *DurableGrantService) revoke(ctx context.Context, kind GrantKind, id, reason string) error {
	if s == nil || s.writer == nil || s.resolver == nil {
		return ErrGrantAuthorityUnavailable
	}
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || strings.ContainsAny(id, "\x00\r\n") || len(id) > 255 || len(reason) > 1024 || strings.ContainsAny(reason, "\x00\r\n") {
		return ErrGrantRevokeInvalid
	}
	authority, err := s.resolve(ctx, DurableGrantTarget{})
	if err != nil {
		return err
	}
	if _, err := authority.issuerEvidence(s.currentTime()); err != nil {
		return err
	}
	switch kind {
	case DurableGrantKindResourceShare:
		return s.writer.RevokeResourceShareGrant(ctx, id, authority.Principal.ID, reason)
	case DurableGrantKindExecution:
		return s.writer.RevokeExecutionGrant(ctx, id, authority.Principal.ID, reason)
	case DurableGrantKindAdminEnvelope:
		return s.writer.RevokeGrantAdminEnvelope(ctx, id, authority.Principal.ID, reason)
	default:
		return ErrGrantRevokeInvalid
	}
}

func (s *DurableGrantService) resolve(ctx context.Context, target DurableGrantTarget) (CurrentAuthoritySnapshot, error) {
	authority, err := s.resolver.ResolveCurrentAuthority(ctx, CurrentAuthorityRequest{Target: target})
	if err != nil {
		return CurrentAuthoritySnapshot{}, fmt.Errorf("%w: %w", ErrGrantAuthorityUnavailable, err)
	}
	return authority, nil
}

func (s *DurableGrantService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (authority CurrentAuthoritySnapshot) issuance(target DurableGrantTarget, kind GrantKind, requested []PermissionPair, now time.Time) (GrantIssuerEvidence, []PermissionPair, error) {
	issuer, err := authority.issuerEvidence(now)
	if err != nil {
		return GrantIssuerEvidence{}, nil, err
	}
	seenGroups := make(map[string]struct{}, len(authority.Groups))
	for _, group := range authority.Groups {
		if group.Kind != SubjectKindGroup || !canonicalUUID(group.ID) || group.ID == authority.Principal.ID {
			return GrantIssuerEvidence{}, nil, fmt.Errorf("%w: group subject is invalid", ErrGrantAuthorityInvalid)
		}
		if _, exists := seenGroups[group.ID]; exists {
			return GrantIssuerEvidence{}, nil, fmt.Errorf("%w: duplicate group subject", ErrGrantAuthorityInvalid)
		}
		seenGroups[group.ID] = struct{}{}
	}
	if authority.Permissions == nil {
		return GrantIssuerEvidence{}, nil, fmt.Errorf("%w: coherent typed principal/group snapshot is required", ErrGrantPermissionCeiling)
	}
	if err := ValidatePermissionPairs(authority.Permissions); err != nil {
		return GrantIssuerEvidence{}, nil, fmt.Errorf("%w: coherent typed snapshot: %v", ErrGrantAuthorityInvalid, err)
	}
	if err := ValidatePermissionPairs(requested); err != nil {
		return GrantIssuerEvidence{}, nil, err
	}

	credential := authority.Credential
	ceiling := authority.Permissions
	if credential.Class == GrantCredentialClassAPIToken && authority.CredentialPermissions == nil {
		return GrantIssuerEvidence{}, nil, ErrTokenPermissionAttenuationNeeded
	}
	if authority.CredentialPermissions != nil {
		if err := ValidatePermissionPairs(authority.CredentialPermissions); err != nil {
			return GrantIssuerEvidence{}, nil, fmt.Errorf("%w: credential permissions: %v", ErrGrantAuthorityAttenuated, err)
		}
		ceiling = IntersectPermissionPairs(authority.CredentialPermissions, authority.Permissions)
	}
	if err := ValidatePermissionPairsAgainstAuthority(ceiling, requested); err != nil {
		return GrantIssuerEvidence{}, nil, fmt.Errorf("%w: %v", ErrGrantPermissionCeiling, err)
	}
	// This check is intentionally made here as well as by each durable input's
	// validator so the service's derived ceiling cannot be bypassed by a future
	// writer implementation.
	if err := validateIssuancePermissions(ceiling, target, kind, requested); err != nil {
		return GrantIssuerEvidence{}, nil, err
	}
	return issuer, ClonePermissionPairs(ceiling), nil
}

func (authority CurrentAuthoritySnapshot) issuerEvidence(now time.Time) (GrantIssuerEvidence, error) {
	if authority.Principal.AccessDisabled() || authority.Principal.ID == "" || !canonicalUUID(authority.Principal.ID) {
		return GrantIssuerEvidence{}, fmt.Errorf("%w: principal is unavailable", ErrGrantAuthorityInvalid)
	}
	principalSubject, err := NewSubjectRef(SubjectKindPrincipal, authority.Principal.ID)
	if err != nil {
		return GrantIssuerEvidence{}, fmt.Errorf("%w: principal identity is invalid", ErrGrantAuthorityInvalid)
	}
	credential := authority.Credential
	if credential.PrincipalID != "" && credential.PrincipalID != authority.Principal.ID {
		return GrantIssuerEvidence{}, fmt.Errorf("%w: credential principal differs from authority principal", ErrGrantCredentialInvalid)
	}
	if credential.Class != GrantCredentialClassSession && credential.Class != GrantCredentialClassAPIToken {
		return GrantIssuerEvidence{}, fmt.Errorf("%w: unsupported credential class %q", ErrGrantCredentialInvalid, credential.Class)
	}
	if credential.ID == "" || credential.Fingerprint == "" || credential.ExpiresAt.IsZero() || !credential.ExpiresAt.After(now) {
		return GrantIssuerEvidence{}, fmt.Errorf("%w: current credential evidence is required", ErrGrantCredentialInvalid)
	}
	issuer := GrantIssuerEvidence{PrincipalID: principalSubject.ID, Credential: GrantCredentialEvidence{Class: credential.Class, ID: credential.ID, Fingerprint: credential.Fingerprint}}
	if err := issuer.Validate(); err != nil {
		return GrantIssuerEvidence{}, fmt.Errorf("%w: issuer evidence: %v", ErrGrantCredentialInvalid, err)
	}
	return issuer, nil
}

func validateShareRequest(request ResourceShareGrantRequest) error {
	if err := request.Target.Validate(); err != nil {
		return err
	}
	if _, err := ShareRecipient(request.Recipient, request.RecipientPrincipalID); err != nil {
		return err
	}
	if err := ValidatePermissionPairs(request.Permissions); err != nil {
		return fmt.Errorf("%w: requested permissions: %v", ErrInvalidDurableGrant, err)
	}
	if err := validateGrantPermissions(request.Permissions, request.Target, false); err != nil {
		return err
	}
	return nil
}

func validateExecutionRequest(request ExecutionGrantRequest) error {
	if err := request.Target.Validate(); err != nil {
		return err
	}
	if !canonicalUUID(request.ExecutionPrincipalID) {
		return fmt.Errorf("%w: execution principal is invalid", ErrInvalidDurableGrant)
	}
	if err := ValidatePermissionPairs(request.Permissions); err != nil {
		return fmt.Errorf("%w: requested permissions: %v", ErrInvalidDurableGrant, err)
	}
	return validateGrantPermissions(request.Permissions, request.Target, true)
}
