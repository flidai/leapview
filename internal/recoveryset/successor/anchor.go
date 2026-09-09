package successor

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
)

// ObjectRoot repeats the owner root projection with provider recovery
// frontier explicit. The owner v1 tag is omitempty, so this wire type prevents
// an intentional local empty value from disappearing from the successor hash.
type ObjectRoot struct {
	Kind                     string `json:"kind"`
	URI                      string `json:"uri"`
	VersionID                string `json:"version_id"`
	Digest                   string `json:"digest"`
	ProviderRecoveryFrontier string `json:"provider_recovery_frontier"`
}

type SourceAnchor struct {
	AnchorVersion         int32                              `json:"anchor_version"`
	SetID                 string                             `json:"set_id"`
	ClusterPoints         []recoveryset.ClusterRecoveryPoint `json:"cluster_points"`
	Delivery              recoveryset.DeliveryPointer        `json:"delivery"`
	Serving               recoveryset.SnapshotSeal           `json:"serving"`
	Catalog               recoveryset.CatalogCommit          `json:"catalog"`
	ObjectRoots           []ObjectRoot                       `json:"object_roots"`
	Compatibility         recoveryset.CompatibilityTuple     `json:"compatibility"`
	ManagedClosureDigest  string                             `json:"managed_closure_digest"`
	ProviderProfileDigest string                             `json:"provider_profile_digest"`
}

func (a SourceAnchor) Validate() error {
	if a.AnchorVersion != SourceAnchorVersion {
		return fmt.Errorf("%w: source anchor version %d", ErrUnsupportedVersion, a.AnchorVersion)
	}
	if !canonicalUUID(a.SetID) {
		return invalid("source anchor set_id must be a canonical UUID")
	}
	if !digest(a.ManagedClosureDigest) || !digest(a.ProviderProfileDigest) {
		return invalid("source anchor digest is malformed")
	}
	if a.ClusterPoints == nil || a.ObjectRoots == nil {
		return invalid("source anchor arrays must be explicit")
	}
	if len(a.ObjectRoots) != 2 {
		return invalid("source anchor requires exactly two object roots")
	}
	roots := make([]recoveryset.ObjectRoot, len(a.ObjectRoots))
	for i, root := range a.ObjectRoots {
		if root.ProviderRecoveryFrontier == "" && root.Kind != recoveryset.ObjectRootDuckLake && root.Kind != recoveryset.ObjectRootServingArtifact {
			return invalid("unknown root with empty provider frontier")
		}
		roots[i] = recoveryset.ObjectRoot{Kind: root.Kind, URI: root.URI, VersionID: root.VersionID, Digest: root.Digest, ProviderRecoveryFrontier: root.ProviderRecoveryFrontier}
	}
	owner := recoveryset.RecoverySet{
		ID: a.SetID, SchemaVersion: recoveryset.SchemaVersion, ClusterPoints: append([]recoveryset.ClusterRecoveryPoint(nil), a.ClusterPoints...),
		Delivery: a.Delivery, Serving: a.Serving, Catalog: a.Catalog, ObjectRoots: roots, Compatibility: a.Compatibility,
		FenceEpoch: 1, AuditIdentity: "successor-anchor", Status: recoveryset.StatusPrepared, CreatedBy: "successor-anchor",
		CreatedAt: anchorTime,
	}
	if err := owner.Validate(); err != nil {
		return invalid("source anchor owner projection: %v", err)
	}
	for _, root := range a.ObjectRoots {
		if root.Kind != recoveryset.ObjectRootDuckLake && root.Kind != recoveryset.ObjectRootServingArtifact {
			return invalid("unsupported source anchor root kind")
		}
	}
	return nil
}

var anchorTime = recoverysetAnchorTime()

func recoverysetAnchorTime() (value time.Time) { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

// AnchorProjection is the immutable source projection used by set3. Lifecycle
// status, fence, attempt, audit, creation metadata, M and F are intentionally
// excluded.
func (a SourceAnchor) Normalize() (SourceAnchor, error) {
	if err := a.Validate(); err != nil {
		return SourceAnchor{}, err
	}
	copy := a
	copy.ClusterPoints = append([]recoveryset.ClusterRecoveryPoint(nil), a.ClusterPoints...)
	sort.Slice(copy.ClusterPoints, func(i, j int) bool { return copy.ClusterPoints[i].DatabaseRole < copy.ClusterPoints[j].DatabaseRole })
	copy.ObjectRoots = append([]ObjectRoot(nil), a.ObjectRoots...)
	sort.Slice(copy.ObjectRoots, func(i, j int) bool { return rootLess(copy.ObjectRoots[i], copy.ObjectRoots[j]) })
	return copy, nil
}

func rootLess(a, b ObjectRoot) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.URI != b.URI {
		return a.URI < b.URI
	}
	return a.VersionID < b.VersionID
}

func (a SourceAnchor) CanonicalJSON() ([]byte, error) {
	normalized, err := a.Normalize()
	if err != nil {
		return nil, err
	}
	return marshal(normalized, MaxDocumentBytes)
}
func (a SourceAnchor) Digest() (string, error) {
	raw, err := a.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return hash(anchorDomain, raw), nil
}

func ParseSourceAnchor(raw []byte) (SourceAnchor, error) {
	var anchor SourceAnchor
	if err := strictDecode(raw, &anchor, MaxDocumentBytes); err != nil {
		return anchor, err
	}
	if _, err := exactObject(raw, map[string]bool{"anchor_version": true, "set_id": true, "cluster_points": true, "delivery": true, "serving": true, "catalog": true, "object_roots": true, "compatibility": true, "managed_closure_digest": true, "provider_profile_digest": true}, map[string]bool{"anchor_version": true, "set_id": true, "cluster_points": true, "delivery": true, "serving": true, "catalog": true, "object_roots": true, "compatibility": true, "managed_closure_digest": true, "provider_profile_digest": true}, "source anchor"); err != nil {
		return anchor, err
	}
	canonical, err := anchor.CanonicalJSON()
	if err != nil {
		return anchor, err
	}
	if !bytes.Equal(raw, canonical) {
		return anchor, invalid("source anchor is not canonical JSON")
	}
	return anchor, nil
}
