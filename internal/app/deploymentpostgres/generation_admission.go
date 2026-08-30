package deploymentpostgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// GenerationAdmission is the application-composition capability for the
// native build-completion hand-off. The convenience method owns one
// PostgreSQL transaction; the Tx method composes into one supplied by the
// caller.
type GenerationAdmission interface {
	CompleteBuildAndAdmit(context.Context, GenerationAdmissionInput) (GenerationAdmissionResult, error)
	CompleteBuildAndAdmitTx(context.Context, deploymentnative.Tx, GenerationAdmissionInput) (GenerationAdmissionResult, error)
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
	if delivery == nil || serving == nil || !delivery.Configured() || !serving.Configured() {
		return nil, errors.New("generation admission requires configured PostgreSQL delivery and serving-state authorities")
	}
	if !configuredDuckLakeAuthority(ducklake) {
		return nil, errors.New("generation admission requires a configured DuckLake authority")
	}
	return &generationAdmitter{delivery: delivery, serving: serving, ducklake: ducklake}, nil
}

// CompleteBuildAndAdmit completes the build, allocates a generation revision,
// and admits the serving bundle in one PostgreSQL transaction. Every
// lower-level Tx method receives the exact same pgx transaction; this
// convenience method owns Begin, Commit and Rollback.
func (a *generationAdmitter) CompleteBuildAndAdmit(ctx context.Context, input GenerationAdmissionInput) (GenerationAdmissionResult, error) {
	if a == nil || a.delivery == nil || a.serving == nil || !configuredDuckLakeAuthority(a.ducklake) {
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
	result, err := a.CompleteBuildAndAdmitTx(ctx, tx, normalized)
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GenerationAdmissionResult{}, err
	}
	committed = true
	return result, nil
}

// CompleteBuildAndAdmitTx completes the build, allocates a generation
// revision, and admits the serving bundle in the caller-owned transaction.
// It never commits or rolls back tx.
func (a *generationAdmitter) CompleteBuildAndAdmitTx(ctx context.Context, tx deploymentnative.Tx, input GenerationAdmissionInput) (GenerationAdmissionResult, error) {
	if a == nil || a.delivery == nil || a.serving == nil || !configuredDuckLakeAuthority(a.ducklake) {
		return GenerationAdmissionResult{}, fmt.Errorf("%w: generation admission authorities are not configured", deploymentnative.ErrInvalid)
	}
	if tx == nil {
		return GenerationAdmissionResult{}, fmt.Errorf("%w: generation admission requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeInput(input)
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return GenerationAdmissionResult{}, fmt.Errorf("%w: generation admission requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}

	// The DuckLake ledger is written before delivery completion while the
	// caller-owned transaction still contains the running attempt. Any failure
	// in the subsequent delivery, generation, or serving steps rolls back this
	// evidence together with the rest of admission.
	duckAttempt, err := a.ducklake.CommitAttemptTx(ctx, tx, ducklakepostgres.CommitAttemptInput{
		AttemptID:    normalized.Commit.AttemptID,
		OwnerID:      normalized.Commit.OwnerID,
		FencingEpoch: normalized.Commit.FencingEpoch,
		Snapshot:     ducklakepostgres.SnapshotRef{PhysicalPoolID: normalized.Seal.PhysicalPoolID, CatalogID: normalized.Seal.CatalogID, SnapshotID: normalized.Commit.SnapshotID},
		CommitMarker: string(normalized.Commit.CommitMarker),
	})
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := verifyDuckLakeAttempt(duckAttempt, normalized); err != nil {
		return GenerationAdmissionResult{}, err
	}

	artifactBinding, err := a.delivery.BindBuildArtifactTx(ctx, tx, deploymentnative.BuildArtifactBindingInput{
		AttemptID: normalized.Commit.AttemptID, ServingArtifactID: normalized.Seal.ServingArtifactID,
		ServingArtifactDigest: normalized.Seal.ServingArtifactDigest, ServingStateID: normalized.Generation.GenerationID,
		OwnerID: normalized.Fence.OwnerID, FencingEpoch: normalized.Fence.FencingEpoch,
	})
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := verifyArtifactBinding(artifactBinding, normalized); err != nil {
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
	if err := verifyCompletedBuild(completed, normalized); err != nil {
		return GenerationAdmissionResult{}, err
	}
	generation, err := a.delivery.CreateGenerationAllocatedTx(ctx, tx, toNativeGeneration(normalized.Generation))
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := verifyGeneration(generation, normalized.Generation); err != nil {
		return GenerationAdmissionResult{}, err
	}
	bundle, err := a.serving.AdmitGenerationBundleTx(ctx, tx, toNativeBundle(normalized.Bundle), normalized.Graph)
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := verifyBundle(bundle, normalized); err != nil {
		return GenerationAdmissionResult{}, err
	}
	duckBinding, err := a.ducklake.BindGenerationTx(ctx, tx, ducklakepostgres.GenerationBinding{
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
	})
	if err != nil {
		return GenerationAdmissionResult{}, err
	}
	if err := verifyDuckLakeBinding(duckBinding, normalized); err != nil {
		return GenerationAdmissionResult{}, err
	}
	return GenerationAdmissionResult{
		AttemptID: completed.Attempt.AttemptID, SealID: completed.Seal.SealID, CandidateID: completed.Candidate.CandidateID,
		Generation: fromNativeGeneration(generation), Bundle: fromNativeBundle(bundle),
	}, nil
}

// configuredDuckLakeAuthority keeps interface-backed constructor checks safe
// for typed nil implementations. Calling a method on a typed nil is legal in
// Go but may panic in an implementation that does not guard its receiver.
func configuredDuckLakeAuthority(authority DuckLakeAuthority) bool {
	if authority == nil {
		return false
	}
	v := reflect.ValueOf(authority)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return false
		}
	}
	return authority.Configured()
}

func admissionEvidenceConflict(kind string) error {
	return fmt.Errorf("%w: persisted %s evidence differs", deploymentnative.ErrConflict, kind)
}

func verifyArtifactBinding(got deploymentnative.BuildArtifactBinding, input GenerationAdmissionInput) error {
	if got.AttemptID != input.Commit.AttemptID || got.ServingArtifactID != input.Seal.ServingArtifactID ||
		got.ServingArtifactDigest != input.Seal.ServingArtifactDigest || got.ServingStateID != input.Generation.GenerationID || got.BoundAt.IsZero() {
		return admissionEvidenceConflict("delivery artifact binding")
	}
	return nil
}

func verifyCompletedBuild(got deploymentnative.CompleteBuildResult, input GenerationAdmissionInput) error {
	attempt := got.Attempt
	if attempt.AttemptID != input.Commit.AttemptID || attempt.PlanID != input.Generation.PlanID || attempt.CandidateID != input.Generation.CandidateID ||
		attempt.OwnerID != input.Commit.OwnerID || attempt.PhysicalPoolID != input.Seal.PhysicalPoolID || attempt.FencingEpoch != input.Commit.FencingEpoch ||
		attempt.RequestDigest != input.Seal.RequestDigest || attempt.PlanDigest != input.Generation.PlanDigest || attempt.Namespace != input.Seal.RelationNamespace || attempt.State != deploymentnative.AttemptCommitted ||
		attempt.SnapshotID != input.Commit.SnapshotID || attempt.SessionIdentity == "" || attempt.LeaseExpiresAt.IsZero() || attempt.CreatedAt.IsZero() || attempt.UpdatedAt.IsZero() || attempt.FinishedAt.IsZero() ||
		len(attempt.TerminationEvidence) != 0 || !sameCommitMarker(attempt.CommitMarker, input.Commit.CommitMarker) {
		return admissionEvidenceConflict("delivery build completion")
	}
	if !sameSnapshotSeal(got.Seal, input.Seal) {
		return admissionEvidenceConflict("delivery snapshot seal")
	}
	candidate := got.Candidate
	if candidate.CandidateID != input.Generation.CandidateID || candidate.TargetID != input.Generation.TargetID || candidate.PlanID != input.Generation.PlanID ||
		candidate.AttemptID != input.Commit.AttemptID || candidate.SnapshotSealID != input.Seal.SealID || candidate.Status != "qualified" ||
		candidate.CandidateRevision <= 0 || candidate.ArtifactDigest != input.Generation.ServingArtifactDigest || candidate.QualificationDigest != input.QualificationDigest ||
		candidate.CreatedAt.IsZero() || candidate.QualifiedAt.IsZero() || !candidate.RetiredAt.IsZero() {
		return admissionEvidenceConflict("delivery candidate")
	}
	lease := got.Lease
	if lease.LeaseID != input.Fence.LeaseID || lease.TargetID != input.Fence.TargetID || lease.OwnerID != input.Fence.OwnerID ||
		lease.FencingEpoch != input.Fence.FencingEpoch || lease.State != "released" || lease.ExpiresAt.IsZero() || lease.AcquiredAt.IsZero() || lease.ReleasedAt.IsZero() {
		return admissionEvidenceConflict("delivery lease")
	}
	return nil
}

func sameSnapshotSeal(got deploymentnative.SnapshotSeal, want SnapshotSealEvidence) bool {
	return got.SealID == want.SealID && got.AttemptID == want.AttemptID && got.CandidateID == want.CandidateID &&
		got.PhysicalPoolID == want.PhysicalPoolID && got.TenantDomain == want.TenantDomain && got.Region == want.Region && got.EncryptionDomain == want.EncryptionDomain &&
		got.ObjectNamespace == want.ObjectNamespace && got.CatalogDatabase == want.CatalogDatabase && got.CatalogID == want.CatalogID && got.CatalogUUID == want.CatalogUUID &&
		got.CatalogVersion == want.CatalogVersion && got.DuckLakeSnapshotID == want.DuckLakeSnapshotID && got.RelationNamespace == want.RelationNamespace &&
		got.ObjectRoot == want.ObjectRoot && got.ObjectRootDigest == want.ObjectRootDigest && got.ArtifactRoot == want.ArtifactRoot && got.ArtifactRootDigest == want.ArtifactRootDigest &&
		got.RelationManifestDigest == want.RelationManifestDigest && got.ClosureDigest == want.ClosureDigest && got.CompiledGraphDigest == want.CompiledGraphDigest &&
		got.CompiledConfigDigest == want.CompiledConfigDigest && got.SecurityDomainFingerprint == want.SecurityDomainFingerprint && got.RequestDigest == want.RequestDigest &&
		got.PlanDigest == want.PlanDigest && got.CompatibilityDigest == want.CompatibilityDigest && got.ServingArtifactID == want.ServingArtifactID &&
		got.ServingArtifactDigest == want.ServingArtifactDigest && got.DuckDBVersion == want.DuckDBVersion && got.RuntimeVersion == want.RuntimeVersion &&
		got.DuckLakeExtensionVersion == want.DuckLakeExtensionVersion && got.DuckLakeSpecVersion == want.DuckLakeSpecVersion && got.CatalogSchemaVersion == want.CatalogSchemaVersion &&
		!got.QualifiedAt.IsZero() && sameJSON(got.QualificationEvidence, want.QualificationEvidence)
}

func verifyGeneration(got deploymentnative.DeliveryGeneration, want GenerationEvidence) error {
	if got.GenerationID != want.GenerationID || got.TargetID != want.TargetID || got.CandidateID != want.CandidateID || got.SnapshotSealID != want.SnapshotSealID ||
		got.PlanID != want.PlanID || got.PlanDigest != want.PlanDigest || got.ArtifactRoot != want.ArtifactRoot || got.ArtifactRootDigest != want.ArtifactRootDigest ||
		got.ServingArtifactDigest != want.ServingArtifactDigest || got.CompiledGraphDigest != want.CompiledGraphDigest || got.CompiledConfigDigest != want.CompiledConfigDigest ||
		got.SecurityDomainFingerprint != want.SecurityDomainFingerprint || got.GenerationRevision <= 0 || got.CreatedAt.IsZero() {
		return admissionEvidenceConflict("delivery generation")
	}
	return nil
}

func verifyBundle(got servingnative.Bundle, input GenerationAdmissionInput) error {
	want := input.Bundle
	if got.GenerationID != want.GenerationID || got.ProjectID != want.ProjectID || got.Environment != want.Environment ||
		got.ArtifactID != want.Artifact.ID || got.ArtifactDigest != want.Artifact.Digest || got.CompiledGraphDigest != input.Generation.CompiledGraphDigest ||
		got.ArtifactFormat != want.Artifact.Format || got.ArtifactLocator != want.ArtifactLocator || got.StorageSecurityDomain != want.StorageSecurityDomain ||
		got.ArtifactContentType != want.ArtifactContentType || got.ArtifactMetadataDigest != want.ArtifactMetadataDigest || got.SizeBytes != want.Artifact.SizeBytes ||
		got.DuckLakeSnapshotID != input.Commit.SnapshotID || got.CreatedBy != want.CreatedBy || !validPersistedTimestamp(got.CreatedAt) ||
		!sameJSON([]byte(got.ManifestJSON), []byte(want.Artifact.ManifestJSON)) || got.ProjectDigest != want.ProjectDigest ||
		!sameBundleObject(got.AccessPolicyJSON, want.AccessPolicyJSON) || !sameBundleObject(got.DashboardPublicationsJSON, want.DashboardPublicationsJSON) ||
		!sameBundleObject(got.DashboardAppearancesJSON, want.DashboardAppearancesJSON) {
		return admissionEvidenceConflict("serving generation bundle")
	}
	return nil
}

func verifyDuckLakeAttempt(got ducklakepostgres.AttemptEvidence, input GenerationAdmissionInput) error {
	if got.AttemptID != input.Commit.AttemptID || got.RequestDigest != input.Seal.RequestDigest || got.PlanDigest != input.Generation.PlanDigest ||
		got.PhysicalPoolID != input.Seal.PhysicalPoolID || got.CatalogID != input.Seal.CatalogID || got.OwnerID != input.Commit.OwnerID ||
		got.FencingEpoch != input.Commit.FencingEpoch || got.State != ducklakepostgres.AttemptCommitted || got.SnapshotID != input.Commit.SnapshotID ||
		got.SessionIdentity == "" || got.LeaseExpiresAt.IsZero() || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.TerminalAt.IsZero() || !got.TerminalAt.Equal(got.UpdatedAt) || len(got.TerminationEvidence) != 0 || !sameCommitMarker([]byte(got.CommitMarker), input.Commit.CommitMarker) {
		return admissionEvidenceConflict("DuckLake attempt ledger")
	}
	return nil
}

func verifyDuckLakeBinding(got ducklakepostgres.GenerationBinding, input GenerationAdmissionInput) error {
	if got.DeliveryID != input.Commit.DeliveryID || got.GenerationID != input.Generation.GenerationID || got.AttemptID != input.Commit.AttemptID ||
		got.PhysicalPoolID != input.Seal.PhysicalPoolID || got.CatalogID != input.Seal.CatalogID || got.SnapshotID != input.Commit.SnapshotID ||
		got.RelationManifestDigest != input.Seal.RelationManifestDigest || got.CompatibilityDigest != input.Seal.CompatibilityDigest ||
		got.ServingArtifactDigest != input.Seal.ServingArtifactDigest || got.RequestDigest != input.Seal.RequestDigest || got.PlanDigest != input.Seal.PlanDigest ||
		got.FencingEpoch != input.Commit.FencingEpoch || got.BoundAt.IsZero() {
		return admissionEvidenceConflict("DuckLake generation binding")
	}
	return nil
}

func sameCommitMarker(left, right []byte) bool {
	l, err := catalogartifact.DecodeCommitMarker(left)
	if err != nil {
		return false
	}
	r, err := catalogartifact.DecodeCommitMarker(right)
	return err == nil && l == r
}

func sameJSON(left, right []byte) bool {
	var l, r any
	if decodePreciseJSON(left, &l) != nil || decodePreciseJSON(right, &r) != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}

func sameBundleObject(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" || left == "null" {
		left = "{}"
	}
	if right == "" || right == "null" {
		right = "{}"
	}
	return sameJSON([]byte(left), []byte(right))
}

func decodePreciseJSON(raw []byte, target *any) error {
	// strictjson performs bounded, duplicate-key and trailing-data validation;
	// the second decode retains json.Number so large integer evidence cannot be
	// rounded through float64 before comparison.
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(raw, &validated, strictjson.Options{}); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(validated))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validPersistedTimestamp(value string) bool {
	if value == "" {
		return false
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !timestamp.IsZero()
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
	expectedNamespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{
		CandidateID: ctx.Seal.CandidateID, AttemptID: ctx.Commit.AttemptID, FencingEpoch: ctx.Commit.FencingEpoch,
	})
	if err != nil {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: derive relation namespace: %v", deploymentnative.ErrInvalid, err)
	}
	if ctx.Seal.RelationNamespace != expectedNamespace {
		return GenerationAdmissionInput{}, conflict("snapshot seal relation namespace differs from canonical candidate attempt identity")
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
