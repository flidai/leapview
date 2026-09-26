package access

import (
	"errors"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

// GrantIssuancePolicy is the current target-policy head observed before
// resolving the issuer's effective authority. The writer locks and compares
// it again in the grant transaction, so a role revocation cannot commit
// between authority resolution and grant issuance.
type GrantIssuancePolicy struct {
	Scope    AuthorizationPolicyScope
	Revision int64
	Digest   string
}

func (p GrantIssuancePolicy) Validate() error {
	if err := ValidateAuthorizationPolicyScope(p.Scope); err != nil {
		return err
	}
	if p.Revision <= 0 || len(p.Digest) != len("sha256:")+64 || !strings.HasPrefix(p.Digest, "sha256:") {
		return fmt.Errorf("%w: issuance policy revision or digest is invalid", ErrGrantAuthorityInvalid)
	}
	for _, character := range p.Digest[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("%w: issuance policy digest is not canonical", ErrGrantAuthorityInvalid)
		}
	}
	return nil
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
