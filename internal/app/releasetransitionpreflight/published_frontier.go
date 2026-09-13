package releasetransitionpreflight

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

// PublishedRecoveryFrontierReader is the read-only recovery-set projection
// needed to resolve a published frontier. Implementations must read the exact
// IDs supplied by the caller; no latest/current selector is part of this
// boundary.
type PublishedRecoveryFrontierReader interface {
	ReadRecoveryFrontierSnapshot(context.Context, string) (recoveryset.RecoverySet, recoveryset.ValidationAttempt, recoveryset.ValidationResult, error)
}

// PublishedRecoveryFrontierAuthority adapts the recovery-set owner's
// read-only API to the transition preflight resolver contract. It performs no
// writes and deliberately retains no mutable/latest state.
type PublishedRecoveryFrontierAuthority struct {
	reader PublishedRecoveryFrontierReader
}

// NewPublishedRecoveryFrontierAuthority constructs an authority over a
// read-only recovery-set owner. A nil reader is retained as an unusable
// authority and fails closed from ResolveRecoveryFrontier.
func NewPublishedRecoveryFrontierAuthority(reader PublishedRecoveryFrontierReader) *PublishedRecoveryFrontierAuthority {
	return &PublishedRecoveryFrontierAuthority{reader: reader}
}

// ResolveRecoveryFrontier proves that setRef names one exact, currently
// published recovery set and its exact passed validation evidence for target.
// The returned reference is always freshly bound to the digest computed from
// target; caller-provided target binding is never trusted without comparison.
func (a *PublishedRecoveryFrontierAuthority) ResolveRecoveryFrontier(ctx context.Context, setRef transitionpreflight.RecoveryFrontierRef, target transitionpreflight.TargetIdentity) (transitionpreflight.RecoveryFrontierRef, error) {
	if a == nil || a.reader == nil {
		return transitionpreflight.RecoveryFrontierRef{}, fmt.Errorf("%w: recovery frontier reader is unavailable", transitionpreflight.ErrResolutionNotFound)
	}
	targetDigest, err := target.Digest()
	if err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("target identity", err)
	}
	// A resolver caller may supply an unbound set reference. Validate its
	// canonical set/digest identity after filling the binding that this
	// authority computes from target, while retaining a supplied binding for
	// the explicit mismatch check below.
	refToValidate := setRef
	refToValidate.TargetIdentityDigest = targetDigest
	if err := refToValidate.Validate(); err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier reference", err)
	}
	set, attempt, result, err := a.reader.ReadRecoveryFrontierSnapshot(ctx, setRef.SetID)
	if err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, mapFrontierReadError("recovery frontier", err)
	}
	if set.ID != setRef.SetID {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier set identity", nil)
	}
	if err := set.Validate(); err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier", err)
	}
	if set.Status != recoveryset.StatusPublished {
		return transitionpreflight.RecoveryFrontierRef{}, fmt.Errorf("%w: recovery frontier is %s", transitionpreflight.ErrStaleFrontier, set.Status)
	}
	computedDigest, err := set.Digest()
	if err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier digest", err)
	}
	if strings.TrimSpace(set.FrontierDigest) == "" || set.FrontierDigest != computedDigest {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier digest", nil)
	}
	if setRef.Digest != set.FrontierDigest {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier reference digest", nil)
	}
	if set.Delivery.TargetID != target.TargetID || set.Delivery.TargetRevision != target.TargetRevision {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier target", nil)
	}
	if setRef.TargetIdentityDigest != "" && setRef.TargetIdentityDigest != targetDigest {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("recovery frontier target digest", nil)
	}

	attemptID := set.PublishedValidationAttemptID
	if attemptID == "" {
		return transitionpreflight.RecoveryFrontierRef{}, fmt.Errorf("%w: published recovery frontier has no validation attempt", transitionpreflight.ErrResolutionMismatch)
	}
	if attempt.AttemptID != attemptID || attempt.SetID != set.ID || attempt.FenceEpoch != set.FenceEpoch {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("validation attempt identity", nil)
	}
	if attempt.Status != recoveryset.ValidationPassed || attempt.Validate() != nil || attempt.ResultDigest == "" {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("validation attempt", nil)
	}

	if result.AttemptID != attemptID || result.ResultDigest != attempt.ResultDigest || result.Validate() != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("validation result identity", nil)
	}
	envelope, err := recoveryset.ParseValidationEvidenceEnvelope(result.Evidence)
	if err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("validation evidence", err)
	}
	if err := envelope.ValidateFor(set, attemptID); err != nil {
		return transitionpreflight.RecoveryFrontierRef{}, resolutionMismatch("validation evidence binding", err)
	}
	return transitionpreflight.RecoveryFrontierRef{SetID: set.ID, Digest: computedDigest, TargetIdentityDigest: targetDigest}, nil
}

func resolutionMismatch(scope string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", transitionpreflight.ErrResolutionMismatch, scope)
	}
	return fmt.Errorf("%w: %s: %v", transitionpreflight.ErrResolutionMismatch, scope, cause)
}

func mapFrontierReadError(scope string, err error) error {
	if errors.Is(err, recoveryset.ErrNotFound) {
		return fmt.Errorf("%w: %s", transitionpreflight.ErrResolutionNotFound, scope)
	}
	if errors.Is(err, recoveryset.ErrInvalid) {
		return resolutionMismatch(scope, err)
	}
	return fmt.Errorf("%s: %w", scope, err)
}
