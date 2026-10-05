package access

import (
	"encoding/json"
	"fmt"
	"time"
)

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

type ResourceShareGrantInput struct {
	ID                    string
	Profile               string
	Target                DurableGrantTarget
	Issuer                GrantIssuerEvidence
	IssuancePolicy        GrantIssuancePolicy
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
	if in.AllowOnwardDelegation {
		return ErrGrantNoOnwardDelegation
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

// ShareRecipient resolves the principal-ID field into the explicit
// principal/group subject used by durable share persistence.
func ShareRecipient(subject SubjectRef, principalID string) (SubjectRef, error) {
	return shareRecipient(subject, principalID)
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
