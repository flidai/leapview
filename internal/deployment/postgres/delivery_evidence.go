package postgres

import (
	"encoding/json"
	"time"
)

// SnapshotSeal is immutable qualification evidence. Every field that can
// affect execution or routing is relational, never hidden in evidence JSON.
type SnapshotSeal struct {
	SealID, AttemptID, CandidateID                                                                                   string
	PhysicalPoolID, TenantDomain, Region, EncryptionDomain, ObjectNamespace, CatalogDatabase, CatalogID, CatalogUUID string
	CatalogVersion, DuckLakeSnapshotID                                                                               int64
	RelationNamespace, ObjectRoot, ObjectRootDigest, ArtifactRoot, ArtifactRootDigest                                string
	RelationManifestDigest, ClosureDigest                                                                            string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint                                             string
	AuthorizationPolicyRevision                                                                                      int64
	AuthorizationPolicyDigest                                                                                        string
	// LegacyAuthorizationPolicy requests nullable policy identity columns while
	// finalizing a pre-017 indeterminate build. The repository accepts it only
	// when the persisted plan proves the historical policy shape; the marker is
	// never authoritative or persisted.
	LegacyAuthorizationPolicy                                                                          bool
	RequestDigest, PlanDigest, CompatibilityDigest, ServingArtifactID, ServingArtifactDigest           string
	DuckDBVersion, RuntimeVersion, DuckLakeExtensionVersion, DuckLakeSpecVersion, CatalogSchemaVersion string
	QualificationEvidence                                                                              json.RawMessage
	// ResolvedInputs is the compact, canonical build-time resolution of the
	// plan's data-input declarations. The full gate evidence remains in
	// QualificationEvidence; this record carries its digest and the resolved
	// values needed by status readers.
	ResolvedInputs       json.RawMessage
	ResolvedInputsDigest string
	CreatedAt            time.Time
	QualifiedAt          time.Time
}
type SnapshotSealInput = SnapshotSeal

type DeliveryCandidate struct {
	CandidateID, TargetID, PlanID, AttemptID, SnapshotSealID string
	Status                                                   string
	CandidateRevision                                        int64
	ArtifactDigest, QualificationDigest                      string
	ResolvedInputs                                           json.RawMessage
	ResolvedInputsDigest                                     string
	CreatedAt, QualifiedAt, RetiredAt                        time.Time
}
type CandidateInput = DeliveryCandidate
