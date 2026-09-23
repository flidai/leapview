package access

import (
	"encoding/json"
	"fmt"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

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
	IssuancePolicy       GrantIssuancePolicy
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
