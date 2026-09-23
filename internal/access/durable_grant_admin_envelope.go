package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

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
	IssuancePolicy        GrantIssuancePolicy
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

// CurrentGrantAdminEnvelopeReader resolves a live grant-administration
// envelope for a mutation. Implementations used by write paths must serialize
// this read with concurrent revocation so the mutation and revocation have a
// deterministic commit order.
type CurrentGrantAdminEnvelopeReader interface {
	CurrentGrantAdminEnvelopeForMutation(context.Context, string, string) (GrantAdminEnvelope, error)
}

var ErrGrantAdminEnvelopeMismatch = errors.New("grant administration envelope does not authorize the mutation")

// PermissionRoleVersion is the canonical, catalog-pinned identity used when
// a grant-administration envelope authorizes one named role expansion.
func PermissionRoleVersion(role PermissionRole) string {
	return PermissionCatalogProfile + ":role:" + string(role)
}

// ValidateGrantAdminEnvelopeRoleBinding proves that a live envelope is bound
// to the actor, Project, exact recipient, catalog-pinned role expansion, and
// every permission the binding would make effective. The envelope is a
// ceiling, not a wildcard, and resource-scoped envelopes cannot administer a
// Project-wide role binding.
func ValidateGrantAdminEnvelopeRoleBinding(envelope GrantAdminEnvelope, actorID string, projectID projectgraph.ResourceID, subject SubjectRef, role PermissionRole, permissions []PermissionPair) error {
	if envelope.BoundPrincipalID != actorID || envelope.TargetProjectID != projectID || envelope.TargetResourceKind != "" || envelope.TargetResourceID != "" {
		return ErrGrantAdminEnvelopeMismatch
	}
	if envelope.RecipientSelector != string(subject.Kind)+":"+subject.ID || envelope.RoleVersion != PermissionRoleVersion(role) {
		return ErrGrantAdminEnvelopeMismatch
	}
	if err := ValidatePermissionPairs(permissions); err != nil || len(permissions) == 0 {
		return ErrGrantAdminEnvelopeMismatch
	}
	for _, pair := range permissions {
		matched := false
		for _, ceiling := range envelope.Permissions {
			if ceiling.Key() == pair.Key() {
				matched = true
				break
			}
		}
		if !matched {
			return ErrGrantAdminEnvelopeMismatch
		}
	}
	return nil
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
