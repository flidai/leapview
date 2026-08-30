package deploymentpostgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// GenerationAdmission is the application-composition capability for the
// native build-completion hand-off. Implementations own one PostgreSQL
// transaction and commit only after both delivery and serving-state evidence
// have been admitted.
type GenerationAdmission interface {
	CompleteBuildAndAdmit(context.Context, GenerationAdmissionInput) (GenerationAdmissionResult, error)
}

// GenerationAdmissionInput carries all immutable build, seal, generation,
// artifact and graph evidence needed by the native admission boundary. The
// nested records intentionally use app/domain types rather than PostgreSQL
// adapter types, keeping callers independent of storage implementation.
type GenerationAdmissionInput struct {
	Commit              CommitEvidence
	Seal                SnapshotSealEvidence
	QualificationDigest string
	Fence               LeaseFenceEvidence
	Generation          GenerationEvidence
	Bundle              BundleEvidenceInput
	Graph               projectgraph.ProjectGraph
}

// CommitEvidence is the immutable attempt completion proof written by the
// DuckLake writer.
type CommitEvidence struct {
	// DeliveryID is the external idempotency identity. It must exactly match
	// the commit marker and the DuckLake generation binding.
	DeliveryID   string
	AttemptID    string
	OwnerID      string
	FencingEpoch int64
	SnapshotID   int64
	CommitMarker json.RawMessage
}

// LeaseFenceEvidence is the exact target lease fence used by completion.
type LeaseFenceEvidence struct {
	LeaseID, TargetID, OwnerID string
	FencingEpoch               int64
}

// SnapshotSealEvidence is the complete immutable qualification evidence
// required by the delivery PostgreSQL authority.
type SnapshotSealEvidence struct {
	SealID, AttemptID, CandidateID                                                                                   string
	PhysicalPoolID, TenantDomain, Region, EncryptionDomain, ObjectNamespace, CatalogDatabase, CatalogID, CatalogUUID string
	CatalogVersion, DuckLakeSnapshotID                                                                               int64
	RelationNamespace, ObjectRoot, ObjectRootDigest, ArtifactRoot, ArtifactRootDigest                                string
	RelationManifestDigest, ClosureDigest                                                                            string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint                                             string
	RequestDigest, PlanDigest, CompatibilityDigest, ServingArtifactID, ServingArtifactDigest                         string
	DuckDBVersion, RuntimeVersion, DuckLakeExtensionVersion, DuckLakeSpecVersion, CatalogSchemaVersion               string
	QualificationEvidence                                                                                            json.RawMessage
}

// GenerationEvidence is the immutable serving-generation identity. A caller
// supplies GenerationRevision as zero; the native authority allocates the
// target-owned revision in the same transaction.
type GenerationEvidence struct {
	GenerationID, TargetID, CandidateID, SnapshotSealID, PlanID          string
	PlanDigest, ArtifactRoot, ArtifactRootDigest, ServingArtifactDigest  string
	CompiledGraphDigest, CompiledConfigDigest, SecurityDomainFingerprint string
	GenerationRevision                                                   int64
}

// BundleEvidenceInput contains the object-backed serving artifact and the
// policy/publication/appearance documents persisted with its generation.
// Artifact.Path is deliberately not consulted: ArtifactLocator is the sole
// immutable object locator accepted by native admission.
type BundleEvidenceInput struct {
	GenerationID                                                                                    string
	ProjectID                                                                                       projectgraph.ResourceID
	Environment                                                                                     servingstate.Environment
	Artifact                                                                                        servingstate.Artifact
	ArtifactLocator, StorageSecurityDomain, ArtifactContentType, ArtifactMetadataDigest             string
	ProjectDigest, AccessPolicyJSON, DashboardPublicationsJSON, DashboardAppearancesJSON, CreatedBy string
}

// GenerationAdmissionResult is a storage-neutral projection of the durable
// completion and bundle evidence returned after commit. It is safe for
// callers to retain and use for subsequent publication orchestration.
type GenerationAdmissionResult struct {
	AttemptID, SealID, CandidateID string
	Generation                     GenerationEvidence
	Bundle                         BundleEvidence
}

// BundleEvidence is the immutable bundle projection returned by admission.
type BundleEvidence struct {
	GenerationID                                                                                       string
	ProjectID                                                                                          projectgraph.ResourceID
	Environment                                                                                        servingstate.Environment
	ArtifactID, ArtifactDigest, CompiledGraphDigest, ArtifactFormat, ArtifactLocator                   string
	StorageSecurityDomain, ArtifactContentType, ArtifactMetadataDigest                                 string
	ManifestJSON, ProjectDigest, AccessPolicyJSON, DashboardPublicationsJSON, DashboardAppearancesJSON string
	SizeBytes                                                                                          int64
	DuckLakeSnapshotID                                                                                 int64
	CreatedBy, CreatedAt                                                                               string
}

// generationAdmitter is the concrete app-composition implementation. Both
// repositories are retained privately; callers depend only on
// GenerationAdmission.
type generationAdmitter struct {
	delivery *deploymentnative.Repository
	serving  *servingnative.Repository
	ducklake DuckLakeAuthority
}

// DuckLakeAuthority is the narrow app-composition surface needed to admit
// the immutable external commit into DuckLake's PostgreSQL ledger. Both
// methods operate on the caller-owned transaction; the authority must never
// begin or commit a second transaction.
type DuckLakeAuthority interface {
	Configured() bool
	CommitAttemptTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.CommitAttemptInput) (ducklakepostgres.AttemptEvidence, error)
	BindGenerationTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.GenerationBinding) (ducklakepostgres.GenerationBinding, error)
}

var _ GenerationAdmission = (*generationAdmitter)(nil)
var _ DuckLakeAuthority = (*ducklakepostgres.Repository)(nil)

// NewGenerationAdmission constructs the native capability from the three
// process-owned PostgreSQL authorities. DuckLake is required so an external
// physical commit cannot be admitted without its ledger, binding, and
// retention evidence. It does not begin a transaction or perform schema work.
func NewGenerationAdmission(delivery *deploymentnative.Repository, serving *servingnative.Repository, ducklake DuckLakeAuthority) (GenerationAdmission, error) {
	if !delivery.Configured() || !serving.Configured() {
		return nil, errors.New("generation admission requires configured PostgreSQL delivery and serving-state authorities")
	}
	if ducklake == nil || !ducklake.Configured() {
		return nil, errors.New("generation admission requires a configured DuckLake authority")
	}
	return &generationAdmitter{delivery: delivery, serving: serving, ducklake: ducklake}, nil
}

// CompleteBuildAndAdmit completes the build, allocates a generation revision,
// and admits the serving bundle in one caller-owned PostgreSQL transaction.
// Every lower-level Tx method receives the exact same pgx transaction; this
// method alone owns Begin, Commit and Rollback.
func (a *generationAdmitter) CompleteBuildAndAdmit(ctx context.Context, input GenerationAdmissionInput) (GenerationAdmissionResult, error) {
	if a == nil || a.delivery == nil || a.serving == nil || a.ducklake == nil || !a.ducklake.Configured() {
		return GenerationAdmissionResult{}, fmt.Errorf("%w: generation admission authorities are not configured", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeInput(input)
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	tx, err := a.delivery.Begin(ctx)
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, ok := tx.(pgx.Tx); !ok {
		return GenerationAdmissionResult{}, fmt.Errorf("%w: generation admission requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}

	// The DuckLake ledger is written before delivery completion while the
	// caller-owned transaction still contains the running attempt. Any failure
	// in the subsequent delivery, generation, or serving steps rolls back this
	// evidence together with the rest of admission.
	if _, err := a.ducklake.CommitAttemptTx(ctx, tx, ducklakepostgres.CommitAttemptInput{
		AttemptID:    normalized.Commit.AttemptID,
		OwnerID:      normalized.Commit.OwnerID,
		FencingEpoch: normalized.Commit.FencingEpoch,
		Snapshot:     ducklakepostgres.SnapshotRef{PhysicalPoolID: normalized.Seal.PhysicalPoolID, CatalogID: normalized.Seal.CatalogID, SnapshotID: normalized.Commit.SnapshotID},
		CommitMarker: string(normalized.Commit.CommitMarker),
	}); err != nil {
		return GenerationAdmissionResult{}, err
	}

	if _, err := a.delivery.BindBuildArtifactTx(ctx, tx, deploymentnative.BuildArtifactBindingInput{
		AttemptID: normalized.Commit.AttemptID, ServingArtifactID: normalized.Seal.ServingArtifactID,
		ServingArtifactDigest: normalized.Seal.ServingArtifactDigest, ServingStateID: normalized.Generation.GenerationID,
		OwnerID: normalized.Fence.OwnerID, FencingEpoch: normalized.Fence.FencingEpoch,
	}); err != nil {
		return GenerationAdmissionResult{}, err
	}
	completed, err := a.delivery.CompleteBuildTx(ctx, tx, deploymentnative.CommitAttemptInput{
		AttemptID: normalized.Commit.AttemptID, OwnerID: normalized.Commit.OwnerID,
		FencingEpoch: normalized.Commit.FencingEpoch, SnapshotID: normalized.Commit.SnapshotID,
		CommitMarker: append(json.RawMessage(nil), normalized.Commit.CommitMarker...),
	}, toNativeSeal(normalized.Seal), normalized.QualificationDigest, toNativeFence(normalized.Fence))
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	generation, err := a.delivery.CreateGenerationAllocatedTx(ctx, tx, toNativeGeneration(normalized.Generation))
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	bundle, err := a.serving.AdmitGenerationBundleTx(ctx, tx, toNativeBundle(normalized.Bundle), normalized.Graph)
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if _, err := a.ducklake.BindGenerationTx(ctx, tx, ducklakepostgres.GenerationBinding{
		DeliveryID:             normalized.Commit.DeliveryID,
		GenerationID:           normalized.Generation.GenerationID,
		AttemptID:              normalized.Commit.AttemptID,
		PhysicalPoolID:         normalized.Seal.PhysicalPoolID,
		CatalogID:              normalized.Seal.CatalogID,
		SnapshotID:             normalized.Commit.SnapshotID,
		RelationManifestDigest: normalized.Seal.RelationManifestDigest,
		CompatibilityDigest:    normalized.Seal.CompatibilityDigest,
		ServingArtifactDigest:  normalized.Seal.ServingArtifactDigest,
		RequestDigest:          normalized.Seal.RequestDigest,
		PlanDigest:             normalized.Seal.PlanDigest,
		FencingEpoch:           normalized.Commit.FencingEpoch,
	}); err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GenerationAdmissionResult{}, err
	}
	committed = true
	return GenerationAdmissionResult{
		AttemptID: completed.Attempt.AttemptID, SealID: completed.Seal.SealID, CandidateID: completed.Candidate.CandidateID,
		Generation: fromNativeGeneration(generation), Bundle: fromNativeBundle(bundle),
	}, nil
}

func normalizeInput(input GenerationAdmissionInput) (GenerationAdmissionInput, error) {
	ctx := input
	genID, err := canonicalUUID(ctx.Generation.GenerationID, "generation id")
	if err != nil {
		return GenerationAdmissionInput{}, err
	}
	ctx.Generation.GenerationID = genID
	if ctx.Bundle.GenerationID != genID {
		return GenerationAdmissionInput{}, conflict("bundle and generation ids differ")
	}
	if ctx.Bundle.Artifact.ServingStateID != servingstate.ID(genID) {
		return GenerationAdmissionInput{}, conflict("artifact serving-state id differs from generation id")
	}
	if err := canonicalProjectAndEnvironment(ctx.Bundle.ProjectID, ctx.Bundle.Environment); err != nil {
		return GenerationAdmissionInput{}, err
	}
	if err := ctx.Graph.Validate(); err != nil || ctx.Graph.ProjectID() != ctx.Bundle.ProjectID {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: serving graph is invalid or project-mismatched", deploymentnative.ErrInvalid)
	}
	if ctx.Generation.CompiledGraphDigest != ctx.Graph.Digest() {
		return GenerationAdmissionInput{}, conflict("generation and graph digests differ")
	}
	if ctx.Bundle.Artifact.Digest != ctx.Generation.ServingArtifactDigest || ctx.Bundle.Artifact.Digest != ctx.Seal.ServingArtifactDigest {
		return GenerationAdmissionInput{}, conflict("artifact and generation digests differ")
	}
	if ctx.Seal.CompiledGraphDigest != ctx.Generation.CompiledGraphDigest || ctx.Seal.CompiledConfigDigest != ctx.Generation.CompiledConfigDigest || ctx.Seal.SecurityDomainFingerprint != ctx.Generation.SecurityDomainFingerprint || ctx.Seal.PlanDigest != ctx.Generation.PlanDigest || ctx.Seal.ArtifactRoot != ctx.Generation.ArtifactRoot || ctx.Seal.ArtifactRootDigest != ctx.Generation.ArtifactRootDigest {
		return GenerationAdmissionInput{}, conflict("generation and seal evidence differ")
	}
	if ctx.Seal.ServingArtifactID != ctx.Bundle.Artifact.ID {
		return GenerationAdmissionInput{}, conflict("artifact ids differ")
	}
	if ctx.Seal.SealID != ctx.Generation.SnapshotSealID || ctx.Seal.CandidateID != ctx.Generation.CandidateID || ctx.Seal.AttemptID != ctx.Commit.AttemptID {
		return GenerationAdmissionInput{}, conflict("seal and generation evidence differ")
	}
	if ctx.Generation.TargetID != ctx.Fence.TargetID {
		return GenerationAdmissionInput{}, conflict("generation and lease target ids differ")
	}
	if ctx.Commit.OwnerID != ctx.Fence.OwnerID || ctx.Commit.FencingEpoch != ctx.Fence.FencingEpoch {
		return GenerationAdmissionInput{}, conflict("commit and lease fences differ")
	}
	if ctx.Commit.SnapshotID != ctx.Seal.DuckLakeSnapshotID {
		return GenerationAdmissionInput{}, conflict("commit snapshot and seal snapshot differ")
	}
	if ctx.Commit.SnapshotID <= 0 || ctx.Commit.FencingEpoch <= 0 || ctx.Fence.FencingEpoch <= 0 {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: commit snapshot and lease fence must be positive", deploymentnative.ErrInvalid)
	}
	if ctx.Generation.GenerationRevision != 0 {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: generation revision must be allocated by the target", deploymentnative.ErrInvalid)
	}
	for label, value := range map[string]string{
		"commit owner id": ctx.Commit.OwnerID, "lease owner id": ctx.Fence.OwnerID,
		"lease target id": ctx.Fence.TargetID, "generation target id": ctx.Generation.TargetID,
		"physical pool id": ctx.Seal.PhysicalPoolID, "tenant domain": ctx.Seal.TenantDomain,
		"region": ctx.Seal.Region, "encryption domain": ctx.Seal.EncryptionDomain,
		"object namespace": ctx.Seal.ObjectNamespace, "catalog database": ctx.Seal.CatalogDatabase,
		"catalog id": ctx.Seal.CatalogID, "relation namespace": ctx.Seal.RelationNamespace,
		"object root": ctx.Seal.ObjectRoot, "artifact root": ctx.Seal.ArtifactRoot,
		"duckdb version": ctx.Seal.DuckDBVersion, "runtime version": ctx.Seal.RuntimeVersion,
		"ducklake extension version":     ctx.Seal.DuckLakeExtensionVersion,
		"ducklake specification version": ctx.Seal.DuckLakeSpecVersion,
		"catalog schema version":         ctx.Seal.CatalogSchemaVersion,
	} {
		if err := validateText(value, label, 512); err != nil {
			return GenerationAdmissionInput{}, err
		}
	}
	if ctx.Seal.CatalogVersion <= 0 || ctx.Seal.DuckLakeSnapshotID <= 0 {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: catalog and DuckLake snapshot versions must be positive", deploymentnative.ErrInvalid)
	}
	if err := validateUUIDFields(ctx); err != nil {
		return GenerationAdmissionInput{}, err
	}
	for label, value := range map[string]string{
		"qualification digest": ctx.QualificationDigest, "plan digest": ctx.Generation.PlanDigest,
		"artifact digest": ctx.Bundle.Artifact.Digest, "project digest": ctx.Bundle.ProjectDigest,
		"artifact root digest":    ctx.Generation.ArtifactRootDigest,
		"seal object root digest": ctx.Seal.ObjectRootDigest, "seal artifact root digest": ctx.Seal.ArtifactRootDigest,
		"seal relation manifest digest": ctx.Seal.RelationManifestDigest, "seal closure digest": ctx.Seal.ClosureDigest,
		"seal compiled graph digest": ctx.Seal.CompiledGraphDigest, "seal compiled config digest": ctx.Seal.CompiledConfigDigest,
		"seal security fingerprint": ctx.Seal.SecurityDomainFingerprint, "seal request digest": ctx.Seal.RequestDigest,
		"seal plan digest": ctx.Seal.PlanDigest, "seal compatibility digest": ctx.Seal.CompatibilityDigest,
	} {
		if err := validateDigest(value, label); err != nil {
			return GenerationAdmissionInput{}, err
		}
	}
	for label, value := range map[string]string{
		"generation serving artifact digest": ctx.Generation.ServingArtifactDigest,
		"generation compiled graph digest":   ctx.Generation.CompiledGraphDigest,
		"generation compiled config digest":  ctx.Generation.CompiledConfigDigest,
		"generation security fingerprint":    ctx.Generation.SecurityDomainFingerprint,
	} {
		if err := validateDigest(value, label); err != nil {
			return GenerationAdmissionInput{}, err
		}
	}
	if err := validateArtifact(ctx.Bundle); err != nil {
		return GenerationAdmissionInput{}, err
	}
	if err := validateObjects(ctx.Bundle); err != nil {
		return GenerationAdmissionInput{}, err
	}
	if err := validateRequiredObject(ctx.Seal.QualificationEvidence, "qualification evidence"); err != nil {
		return GenerationAdmissionInput{}, err
	}
	marker, canonical, err := decodeCanonicalMarker(ctx.Commit.CommitMarker)
	if err != nil {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: invalid commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	deliveryID := ctx.Commit.DeliveryID
	if deliveryID == "" || deliveryID != strings.TrimSpace(deliveryID) {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: commit delivery id is not canonical", deploymentnative.ErrInvalid)
	}
	if err := validateText(deliveryID, "delivery id", 255); err != nil {
		return GenerationAdmissionInput{}, err
	}
	ctx.Commit.CommitMarker = canonical
	if marker.DeliveryID != deliveryID || marker.GenerationID != genID || marker.AttemptID != ctx.Commit.AttemptID || marker.Project != ctx.Bundle.ProjectID.String() || marker.Environment != string(ctx.Bundle.Environment) || marker.PhysicalPoolID != ctx.Seal.PhysicalPoolID || marker.PlanDigest != ctx.Generation.PlanDigest || marker.RequestDigest != ctx.Seal.RequestDigest || marker.LeaseEpoch != ctx.Fence.FencingEpoch {
		return GenerationAdmissionInput{}, conflict("commit marker identity differs")
	}
	return ctx, nil
}

func validateUUIDFields(input GenerationAdmissionInput) error {
	for label, value := range map[string]string{
		"attempt id": input.Commit.AttemptID, "seal id": input.Seal.SealID,
		"seal attempt id": input.Seal.AttemptID, "candidate id": input.Seal.CandidateID,
		"generation candidate id": input.Generation.CandidateID, "generation seal id": input.Generation.SnapshotSealID,
		"plan id": input.Generation.PlanID, "lease id": input.Fence.LeaseID, "catalog uuid": input.Seal.CatalogUUID,
	} {
		if _, err := canonicalUUID(value, label); err != nil {
			return err
		}
	}
	return nil
}

func canonicalProjectAndEnvironment(projectID projectgraph.ResourceID, environment servingstate.Environment) error {
	if err := projectID.Validate(); err != nil || projectID.String() != strings.TrimSpace(projectID.String()) {
		return fmt.Errorf("%w: project id must be canonical", deploymentnative.ErrInvalid)
	}
	if err := servingstate.ValidateEnvironment(environment); err != nil || string(environment) != strings.TrimSpace(string(environment)) {
		return fmt.Errorf("%w: environment must be canonical", deploymentnative.ErrInvalid)
	}
	return nil
}

func validateArtifact(bundle BundleEvidenceInput) error {
	artifact := bundle.Artifact
	if artifact.ID == "" || artifact.ID != strings.TrimSpace(artifact.ID) || strings.ContainsAny(artifact.ID, "\x00\r\n") || artifact.Format != projectbundle.BundleFormat || artifact.Path != "" || artifact.SizeBytes <= 0 || artifact.SizeBytes > projectbundle.MaxBundleBytes {
		return fmt.Errorf("%w: artifact identity is invalid", deploymentnative.ErrInvalid)
	}
	if err := validateDigest(artifact.Digest, "artifact digest"); err != nil {
		return err
	}
	if artifact.ID != "artifact-"+strings.TrimPrefix(artifact.Digest, "sha256:") {
		return fmt.Errorf("%w: artifact id does not match digest", deploymentnative.ErrConflict)
	}
	locator := bundle.ArtifactLocator
	wantLocator := "serving-artifacts/" + strings.TrimPrefix(artifact.Digest, "sha256:") + ".tar.gz"
	if locator != wantLocator || locator != strings.TrimSpace(locator) || strings.ContainsAny(locator, "\x00\r\n") {
		return fmt.Errorf("%w: artifact object locator is invalid", deploymentnative.ErrInvalid)
	}
	if err := validateText(bundle.StorageSecurityDomain, "storage security domain", 512); err != nil {
		return err
	}
	if bundle.ArtifactContentType != projectbundle.BundleContentType {
		return fmt.Errorf("%w: artifact content type must be %q", deploymentnative.ErrInvalid, projectbundle.BundleContentType)
	}
	if err := validateDigest(bundle.ArtifactMetadataDigest, "artifact metadata digest"); err != nil {
		return err
	}
	if err := validateText(bundle.CreatedBy, "created by", 255); err != nil {
		return err
	}
	return nil
}

func validateText(value, label string, max int) error {
	if value == "" || !utf8.ValidString(value) || value != strings.TrimSpace(value) || len(value) > max || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %s is invalid", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func validateRequiredObject(raw json.RawMessage, label string) error {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return fmt.Errorf("%w: %s is required", deploymentnative.ErrInvalid, label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil || len(object) == 0 {
		return fmt.Errorf("%w: %s must be a non-empty JSON object", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func validateObjects(bundle BundleEvidenceInput) error {
	if err := validateRequiredObject(json.RawMessage(bundle.Artifact.ManifestJSON), "artifact manifest"); err != nil {
		return err
	}
	for label, value := range map[string]string{
		"access policy": bundle.AccessPolicyJSON, "dashboard publications": bundle.DashboardPublicationsJSON,
		"dashboard appearances": bundle.DashboardAppearancesJSON,
	} {
		value = strings.TrimSpace(value)
		if value == "" || value == "null" {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
			return fmt.Errorf("%w: %s must be a JSON object", deploymentnative.ErrInvalid, label)
		}
	}
	return nil
}

func decodeCanonicalMarker(raw json.RawMessage) (catalogartifact.CommitMarker, json.RawMessage, error) {
	if len(raw) == 0 {
		return catalogartifact.CommitMarker{}, nil, errors.New("commit marker is empty")
	}
	marker, err := catalogartifact.DecodeCommitMarker(raw)
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, err
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, err
	}
	if !bytes.Equal(raw, []byte(canonical)) {
		return catalogartifact.CommitMarker{}, nil, errors.New("commit marker is not canonical JSON")
	}
	return marker, json.RawMessage(canonical), nil
}

func canonicalUUID(value, label string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%w: %s must be a canonical UUID", deploymentnative.ErrInvalid, label)
	}
	u, err := uuid.Parse(value)
	if err != nil || u.String() != value {
		return "", fmt.Errorf("%w: %s must be a canonical UUID", deploymentnative.ErrInvalid, label)
	}
	return value, nil
}

func validateDigest(value, label string) error {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%w: %s must be a SHA-256 digest", deploymentnative.ErrInvalid, label)
	}
	if _, err := hex.DecodeString(value[7:]); err != nil || strings.ToLower(value[7:]) != value[7:] {
		return fmt.Errorf("%w: %s must be a lowercase SHA-256 digest", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func conflict(message string) error {
	return fmt.Errorf("%w: %s", deploymentnative.ErrConflict, message)
}

func toNativeFence(f LeaseFenceEvidence) deploymentnative.LeaseFence {
	return deploymentnative.LeaseFence{LeaseID: f.LeaseID, TargetID: f.TargetID, OwnerID: f.OwnerID, FencingEpoch: f.FencingEpoch}
}

func toNativeSeal(s SnapshotSealEvidence) deploymentnative.SnapshotSealInput {
	return deploymentnative.SnapshotSealInput{SealID: s.SealID, AttemptID: s.AttemptID, CandidateID: s.CandidateID, PhysicalPoolID: s.PhysicalPoolID, TenantDomain: s.TenantDomain, Region: s.Region, EncryptionDomain: s.EncryptionDomain, ObjectNamespace: s.ObjectNamespace, CatalogDatabase: s.CatalogDatabase, CatalogID: s.CatalogID, CatalogUUID: s.CatalogUUID, CatalogVersion: s.CatalogVersion, DuckLakeSnapshotID: s.DuckLakeSnapshotID, RelationNamespace: s.RelationNamespace, ObjectRoot: s.ObjectRoot, ObjectRootDigest: s.ObjectRootDigest, ArtifactRoot: s.ArtifactRoot, ArtifactRootDigest: s.ArtifactRootDigest, RelationManifestDigest: s.RelationManifestDigest, ClosureDigest: s.ClosureDigest, CompiledGraphDigest: s.CompiledGraphDigest, CompiledConfigDigest: s.CompiledConfigDigest, SecurityDomainFingerprint: s.SecurityDomainFingerprint, RequestDigest: s.RequestDigest, PlanDigest: s.PlanDigest, CompatibilityDigest: s.CompatibilityDigest, ServingArtifactID: s.ServingArtifactID, ServingArtifactDigest: s.ServingArtifactDigest, DuckDBVersion: s.DuckDBVersion, RuntimeVersion: s.RuntimeVersion, DuckLakeExtensionVersion: s.DuckLakeExtensionVersion, DuckLakeSpecVersion: s.DuckLakeSpecVersion, CatalogSchemaVersion: s.CatalogSchemaVersion, QualificationEvidence: append(json.RawMessage(nil), s.QualificationEvidence...)}
}

func toNativeGeneration(g GenerationEvidence) deploymentnative.GenerationInput {
	return deploymentnative.GenerationInput{GenerationID: g.GenerationID, TargetID: g.TargetID, CandidateID: g.CandidateID, SnapshotSealID: g.SnapshotSealID, PlanID: g.PlanID, PlanDigest: g.PlanDigest, ArtifactRoot: g.ArtifactRoot, ArtifactRootDigest: g.ArtifactRootDigest, ServingArtifactDigest: g.ServingArtifactDigest, CompiledGraphDigest: g.CompiledGraphDigest, CompiledConfigDigest: g.CompiledConfigDigest, SecurityDomainFingerprint: g.SecurityDomainFingerprint, GenerationRevision: g.GenerationRevision}
}

func toNativeBundle(b BundleEvidenceInput) servingnative.GenerationBundleInput {
	artifact := b.Artifact
	artifact.Path = ""
	return servingnative.GenerationBundleInput{GenerationID: b.GenerationID, ProjectID: b.ProjectID, Environment: b.Environment, Artifact: artifact, ArtifactLocator: b.ArtifactLocator, StorageSecurityDomain: b.StorageSecurityDomain, ArtifactContentType: b.ArtifactContentType, ArtifactMetadataDigest: b.ArtifactMetadataDigest, ProjectDigest: b.ProjectDigest, AccessPolicyJSON: b.AccessPolicyJSON, DashboardPublicationsJSON: b.DashboardPublicationsJSON, DashboardAppearancesJSON: b.DashboardAppearancesJSON, CreatedBy: b.CreatedBy}
}

func fromNativeGeneration(g deploymentnative.DeliveryGeneration) GenerationEvidence {
	return GenerationEvidence{GenerationID: g.GenerationID, TargetID: g.TargetID, CandidateID: g.CandidateID, SnapshotSealID: g.SnapshotSealID, PlanID: g.PlanID, PlanDigest: g.PlanDigest, ArtifactRoot: g.ArtifactRoot, ArtifactRootDigest: g.ArtifactRootDigest, ServingArtifactDigest: g.ServingArtifactDigest, CompiledGraphDigest: g.CompiledGraphDigest, CompiledConfigDigest: g.CompiledConfigDigest, SecurityDomainFingerprint: g.SecurityDomainFingerprint, GenerationRevision: g.GenerationRevision}
}

func fromNativeBundle(b servingnative.Bundle) BundleEvidence {
	return BundleEvidence{GenerationID: b.GenerationID, ProjectID: b.ProjectID, Environment: b.Environment, ArtifactID: b.ArtifactID, ArtifactDigest: b.ArtifactDigest, CompiledGraphDigest: b.CompiledGraphDigest, ArtifactFormat: b.ArtifactFormat, ArtifactLocator: b.ArtifactLocator, StorageSecurityDomain: b.StorageSecurityDomain, ArtifactContentType: b.ArtifactContentType, ArtifactMetadataDigest: b.ArtifactMetadataDigest, ManifestJSON: b.ManifestJSON, ProjectDigest: b.ProjectDigest, AccessPolicyJSON: b.AccessPolicyJSON, DashboardPublicationsJSON: b.DashboardPublicationsJSON, DashboardAppearancesJSON: b.DashboardAppearancesJSON, SizeBytes: b.SizeBytes, DuckLakeSnapshotID: b.DuckLakeSnapshotID, CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
