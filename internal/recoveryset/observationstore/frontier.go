package observationstore

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
)

// ReadFrontier verifies both immutable frontier keys and returns the exact
// schema-2 recovery set.
func (s *Store) ReadFrontier(ctx context.Context, ref FrontierRef, horizons ...time.Time) (recoveryset.RecoverySet, error) {
	if err := validContext(ctx); err != nil {
		return recoveryset.RecoverySet{}, err
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if ref.SchemaVersion != SchemaVersion || ref.SetID == "" || !digest(ref.Digest) || ref.Frontier.Validate() != nil || ref.SetIDObject.Validate() != nil || ref.Evidence.Validate() != nil {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: invalid frontier reference", ErrInvalid)
	}
	now := s.now()
	required := time.Time{}
	if len(horizons) > 1 {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: frontier reload accepts one required horizon", ErrInvalid)
	}
	if len(horizons) == 1 && !horizons[0].IsZero() {
		required = ceilSecond(horizons[0].UTC())
	}
	if s.setIDKey(ref.SetID) != ref.SetIDObject.Key {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: frontier set-ID key does not match set ID", ErrIntegrity)
	}
	if s.frontierKey(ref.Digest) != ref.Frontier.Key {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: frontier content key does not match digest", ErrIntegrity)
	}
	loaded, err := s.Reload(ctx, ref.Evidence, required)
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if required.IsZero() {
		required = loaded.Descriptor.RequiredRetainUntil
	}
	a, _, err := s.getEvidence(ctx, ref.Frontier, now, required)
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	b, _, err := s.getEvidence(ctx, ref.SetIDObject, now, required)
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if !bytes.Equal(a, b) || normalizedDigest(a) != ref.Digest || ref.Frontier.SHA256 != rawDigest(ref.Digest) || ref.SetIDObject.SHA256 != rawDigest(ref.Digest) {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: frontier object refs do not match", ErrIntegrity)
	}
	set, err := recoveryset.ParseRecoverySet(a)
	if err != nil || set.ID != ref.SetID || set.SchemaVersion != recoveryset.EvidenceSchemaVersion {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: parse persisted frontier", ErrIntegrity)
	}
	if set.ManagedEvidence == nil || set.ManagedEvidence.ManifestDigest != "sha256:"+ref.Evidence.Manifest.SHA256 || set.ManagedEvidence.BoundaryDigest != "sha256:"+ref.Evidence.Boundary.SHA256 || set.ManagedEvidence.DescriptorDigest != "sha256:"+ref.Evidence.Descriptor.SHA256 || !set.ManagedEvidence.Boundary.Matches(loaded.Boundary) {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: frontier managed evidence does not match evidence reference", ErrIntegrity)
	}
	return set, nil
}
