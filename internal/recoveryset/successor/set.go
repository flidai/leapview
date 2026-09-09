package successor

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
)

// RecoverySet3 is a wire-isolated successor frontier. It is an in-memory
// contract only: status and fencing fields here do not authorize publication.
type RecoverySet3 struct {
	ID                                string                             `json:"id"`
	SchemaVersion                     int32                              `json:"schema_version"`
	ClusterPoints                     []recoveryset.ClusterRecoveryPoint `json:"cluster_points"`
	Delivery                          recoveryset.DeliveryPointer        `json:"delivery"`
	Serving                           recoveryset.SnapshotSeal           `json:"serving"`
	Catalog                           recoveryset.CatalogCommit          `json:"catalog"`
	ObjectRoots                       []ObjectRoot                       `json:"object_roots"`
	Compatibility                     recoveryset.CompatibilityTuple     `json:"compatibility"`
	SourceFrontierAnchorDigest        string                             `json:"source_frontier_anchor_digest"`
	ManagedObservationManifestVersion int32                              `json:"managed_observation_manifest_version"`
	ManagedObservationManifestDigest  string                             `json:"managed_observation_manifest_digest"`
	FrontierDigest                    string                             `json:"frontier_digest"`
	FenceEpoch                        int64                              `json:"fence_epoch"`
	AuditIdentity                     string                             `json:"audit_identity"`
	Status                            recoveryset.Status                 `json:"status"`
	PublishedValidationAttemptID      string                             `json:"published_validation_attempt_id,omitempty"`
	CreatedBy                         string                             `json:"created_by"`
	CreatedAt                         string                             `json:"created_at"`
}

type Evidence struct {
	Anchor        SourceAnchor
	Manifest      ManagedManifest2
	Receipt       SignedReceipt
	Profiles      ProviderProfileSet
	Authorities   AuthorityRegistry
	ExpectedScope ExpectedScope
	// VerificationTime is an independently selected clock value, never a
	// timestamp supplied by the receipt. It bounds the capture's latest end.
	VerificationTime time.Time
}

func (s RecoverySet3) Validate() error {
	return s.validate(true)
}

func (s RecoverySet3) validate(requireCommitment bool) error {
	if s.SchemaVersion != RecoverySetVersion {
		return fmt.Errorf("%w: recovery set version %d", ErrUnsupportedVersion, s.SchemaVersion)
	}
	if !canonicalUUID(s.ID) {
		return invalid("recovery set id must be a canonical UUID")
	}
	if s.ManagedObservationManifestVersion != ManagedManifestVersion {
		return fmt.Errorf("%w: managed manifest reference version %d", ErrUnsupportedVersion, s.ManagedObservationManifestVersion)
	}
	if !digest(s.SourceFrontierAnchorDigest) || !digest(s.ManagedObservationManifestDigest) {
		return invalid("recovery set managed evidence references are malformed")
	}
	if err := canonicalTimeOnly(s.CreatedAt); err != nil {
		return err
	}
	if s.ClusterPoints == nil || s.ObjectRoots == nil {
		return invalid("recovery set arrays must be explicit")
	}
	if s.FenceEpoch <= 0 {
		return invalid("recovery set fence epoch must be positive")
	}
	if s.Status != recoveryset.StatusPrepared && s.Status != recoveryset.StatusPublished && s.Status != recoveryset.StatusSuperseded && s.Status != recoveryset.StatusInvalid {
		return invalid("recovery set status is unsupported")
	}
	if s.Status == recoveryset.StatusPrepared && s.PublishedValidationAttemptID != "" {
		return invalid("prepared recovery set cannot carry a validation attempt")
	}
	if s.Status == recoveryset.StatusPublished || s.Status == recoveryset.StatusSuperseded {
		if !canonicalUUID(s.PublishedValidationAttemptID) {
			return invalid("terminal recovery set requires validation attempt")
		}
	}
	if s.PublishedValidationAttemptID != "" && !canonicalUUID(s.PublishedValidationAttemptID) {
		return invalid("published validation attempt is not a canonical UUID")
	}
	if err := id(s.AuditIdentity, "recovery set audit identity"); err != nil {
		return err
	}
	if err := id(s.CreatedBy, "recovery set created_by"); err != nil {
		return err
	}
	roots := make([]recoveryset.ObjectRoot, len(s.ObjectRoots))
	for i, root := range s.ObjectRoots {
		roots[i] = toOwnerRoot(root)
	}
	owner := recoveryset.RecoverySet{ID: s.ID, SchemaVersion: recoveryset.SchemaVersion, ClusterPoints: s.ClusterPoints, Delivery: s.Delivery, Serving: s.Serving, Catalog: s.Catalog, ObjectRoots: roots, Compatibility: s.Compatibility, FenceEpoch: s.FenceEpoch, AuditIdentity: s.AuditIdentity, Status: s.Status, PublishedValidationAttemptID: s.PublishedValidationAttemptID, CreatedBy: s.CreatedBy, CreatedAt: parseSetTime(s.CreatedAt)}
	if err := owner.Validate(); err != nil {
		return invalid("recovery set owner projection: %v", err)
	}
	if requireCommitment && !digest(s.FrontierDigest) {
		return invalid("recovery set frontier digest is required")
	}
	if requireCommitment && s.FrontierDigest != "" {
		commitment, err := s.Commitment()
		if err != nil || commitment != s.FrontierDigest {
			return invalid("recovery set frontier digest does not match commitment")
		}
	}
	return nil
}

func parseSetTime(value string) time.Time  { parsed, _ := canonicalTime(value); return parsed }
func canonicalTimeOnly(value string) error { _, err := canonicalTime(value); return err }
func toOwnerRoot(root ObjectRoot) recoveryset.ObjectRoot {
	return recoveryset.ObjectRoot{Kind: root.Kind, URI: root.URI, VersionID: root.VersionID, Digest: root.Digest, ProviderRecoveryFrontier: root.ProviderRecoveryFrontier}
}

func (s RecoverySet3) Normalize() (RecoverySet3, error) {
	if err := s.Validate(); err != nil {
		return RecoverySet3{}, err
	}
	copy := s
	copy.ClusterPoints = append([]recoveryset.ClusterRecoveryPoint(nil), s.ClusterPoints...)
	sort.Slice(copy.ClusterPoints, func(i, j int) bool { return copy.ClusterPoints[i].DatabaseRole < copy.ClusterPoints[j].DatabaseRole })
	copy.ObjectRoots = append([]ObjectRoot(nil), s.ObjectRoots...)
	sort.Slice(copy.ObjectRoots, func(i, j int) bool { return rootLess(copy.ObjectRoots[i], copy.ObjectRoots[j]) })
	return copy, nil
}

func (s RecoverySet3) CanonicalJSON() ([]byte, error) {
	normalized, err := s.Normalize()
	if err != nil {
		return nil, err
	}
	return marshal(normalized, MaxSetBytes)
}

func (s RecoverySet3) Commitment() (string, error) {
	raw, err := s.CommitmentBytes()
	if err != nil {
		return "", err
	}
	return hash(frontierDomain, raw), nil
}

// CommitmentBytes exposes the frozen owner projection for persistence integrity
// checks. Storage adapters must not reproduce its field ordering themselves.
func (s RecoverySet3) CommitmentBytes() ([]byte, error) {
	if err := s.ValidateWithoutCommitment(); err != nil {
		return nil, err
	}
	type projection struct {
		SchemaVersion                     int32  `json:"schema_version"`
		SetID                             string `json:"set_id"`
		SourceFrontierAnchorDigest        string `json:"source_frontier_anchor_digest"`
		ManagedObservationManifestVersion int32  `json:"managed_observation_manifest_version"`
		ManagedObservationManifestDigest  string `json:"managed_observation_manifest_digest"`
	}
	return marshal(projection{s.SchemaVersion, s.ID, s.SourceFrontierAnchorDigest, s.ManagedObservationManifestVersion, s.ManagedObservationManifestDigest}, MaxSetBytes)
}

func (s RecoverySet3) ValidateWithoutCommitment() error { return s.validate(false) }
func (s RecoverySet3) Digest() (string, error)          { return s.Commitment() }

func (s RecoverySet3) ValidateAgainst(anchor SourceAnchor, manifestDigest string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	anchorDigest, err := anchor.Digest()
	if err != nil {
		return err
	}
	if anchorDigest != s.SourceFrontierAnchorDigest || manifestDigest == "" || manifestDigest != s.ManagedObservationManifestDigest {
		return invalid("recovery set does not match selected anchor or manifest")
	}
	if s.ID != anchor.SetID || !sameOwnerProjection(s, anchor) {
		return invalid("recovery set owner projection differs from source anchor")
	}
	return nil
}

func sameOwnerProjection(s RecoverySet3, a SourceAnchor) bool {
	if len(s.ClusterPoints) != len(a.ClusterPoints) || len(s.ObjectRoots) != len(a.ObjectRoots) || s.Delivery != a.Delivery || s.Serving != a.Serving || s.Catalog != a.Catalog || s.Compatibility != a.Compatibility {
		return false
	}
	left, _ := s.Normalize()
	right, _ := a.Normalize()
	for i := range left.ClusterPoints {
		if left.ClusterPoints[i] != right.ClusterPoints[i] {
			return false
		}
	}
	for i := range left.ObjectRoots {
		root := right.ObjectRoots[i]
		if left.ObjectRoots[i].Kind != root.Kind || left.ObjectRoots[i].URI != root.URI || left.ObjectRoots[i].VersionID != root.VersionID || left.ObjectRoots[i].Digest != root.Digest || left.ObjectRoots[i].ProviderRecoveryFrontier != root.ProviderRecoveryFrontier {
			return false
		}
	}
	return true
}

func (s RecoverySet3) ValidateEvidence(e Evidence) error {
	if e.VerificationTime.IsZero() {
		return invalid("independent verification time is required")
	}
	anchorDigest, err := e.Anchor.Digest()
	if err != nil {
		return err
	}
	if err := e.Anchor.Validate(); err != nil {
		return err
	}
	if e.Anchor.SetID != e.Manifest.SetID || e.Anchor.SetID != s.ID {
		return invalid("evidence set identity mismatch")
	}
	if e.Anchor.ManagedClosureDigest != e.Manifest.ManagedClosureDigest || e.Anchor.ManagedClosureDigest != e.ExpectedScope.ManagedClosureDigest {
		return invalid("evidence closure binding mismatch")
	}
	profileDigest, err := e.Profiles.Digest()
	if err != nil {
		return err
	}
	if e.Anchor.ProviderProfileDigest != profileDigest {
		return invalid("evidence profile binding mismatch")
	}
	if e.ExpectedScope.SourceFrontierAnchorDigest != anchorDigest {
		return invalid("evidence source anchor differs from selected source")
	}
	manifestDigest, err := e.Manifest.Digest()
	if err != nil {
		return err
	}
	if err := s.ValidateAgainst(e.Anchor, manifestDigest); err != nil {
		return err
	}
	if err := e.Profiles.ValidateManifest(e.Manifest, e.ExpectedScope); err != nil {
		return err
	}
	if err := e.Receipt.Verify(e.Manifest, e.ExpectedScope, e.Authorities, profileDigest); err != nil {
		return err
	}
	completed, err := canonicalTime(e.Receipt.Core.CompletedAt)
	if err != nil || completed.After(e.VerificationTime) {
		return invalid("receipt capture is later than verification time")
	}
	if anchorDigest != s.SourceFrontierAnchorDigest {
		return invalid("evidence anchor digest mismatch")
	}
	return nil
}

func ParseRecoverySet3(raw []byte) (RecoverySet3, error) {
	var set RecoverySet3
	if err := strictDecode(raw, &set, MaxSetBytes); err != nil {
		return set, err
	}
	if _, err := exactObject(raw, map[string]bool{"id": true, "schema_version": true, "cluster_points": true, "delivery": true, "serving": true, "catalog": true, "object_roots": true, "compatibility": true, "source_frontier_anchor_digest": true, "managed_observation_manifest_version": true, "managed_observation_manifest_digest": true, "frontier_digest": true, "fence_epoch": true, "audit_identity": true, "status": true, "published_validation_attempt_id": true, "created_by": true, "created_at": true}, map[string]bool{"id": true, "schema_version": true, "cluster_points": true, "delivery": true, "serving": true, "catalog": true, "object_roots": true, "compatibility": true, "source_frontier_anchor_digest": true, "managed_observation_manifest_version": true, "managed_observation_manifest_digest": true, "frontier_digest": true, "fence_epoch": true, "audit_identity": true, "status": true, "created_by": true, "created_at": true}, "recovery set"); err != nil {
		return set, err
	}
	canonical, err := set.CanonicalJSON()
	if err != nil {
		return set, err
	}
	if !bytes.Equal(raw, canonical) {
		return set, invalid("recovery set is not canonical JSON")
	}
	return set, nil
}
