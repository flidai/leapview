package deploymentpostgres

// This file owns the small application boundary for a native physical build.
// It deliberately contains no legacy catalog behavior:
// one invocation opens one marker-scoped PostgreSQL DuckLake writer, performs
// one governed materialization, reads value-only evidence, and closes it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// NativePhysicalBuildEnvironment is the complete capability used by one
// physical build. Implementations must bind all methods to the same
// PostgreSQL-backed DuckLake writer and must not expose a catalog file.
type NativePhysicalBuildEnvironment interface {
	analyticsmaterialization.Executor
	SnapshotSealEvidence(context.Context, int64) (ducklake.PostgresSnapshotSealEvidence, error)
	NativeSnapshotClosureEvidence(context.Context, ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error)
	Close() error
}

// NativePhysicalBuildEnvironmentFactory opens an attempt-scoped environment.
// The marker is passed by value so an implementation cannot mutate the
// caller's identity or silently open a second attempt.
type NativePhysicalBuildEnvironmentFactory interface {
	Open(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error)
}

// NativePhysicalBuildEnvironmentFactoryFunc adapts a function to the factory
// boundary, making orchestration tests independent of DuckDB and PostgreSQL.
type NativePhysicalBuildEnvironmentFactoryFunc func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error)

var _ NativePhysicalBuildEnvironmentFactory = NativePhysicalBuildEnvironmentFactoryFunc(nil)

func (f NativePhysicalBuildEnvironmentFactoryFunc) Open(ctx context.Context, marker catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
	if f == nil {
		return nil, errors.New("native physical build environment factory is not configured")
	}
	return f(ctx, marker)
}

// DuckLakePhysicalBuildEnvironmentFactory is the production adapter shape for
// the boundary. Config supplies the process-owned PostgreSQL catalog/pool
// admission and MaterializerFactory supplies the analytics module policy. It
// does not participate in application composition; callers can inject the
// function factory above in tests.
type DuckLakePhysicalBuildEnvironmentFactory struct {
	Config              ducklake.Config
	MaterializerFactory func(*ducklake.Environment) (analyticsmaterialization.Executor, error)
}

// Open creates one PostgreSQL writer with the exact supplied marker. The
// marker is copied into DuckLake.Config so the environment's one-shot commit
// guard and commit_extra_info use the same attempt identity.
func (f DuckLakePhysicalBuildEnvironmentFactory) Open(ctx context.Context, marker catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
	if f.Config.PostgresCatalog == nil || f.Config.PostgresCatalog.Mode != ducklake.PostgresCatalogWriter {
		return nil, fmt.Errorf("%w: native physical build requires a PostgreSQL DuckLake writer configuration", deploymentnative.ErrInvalid)
	}
	if f.MaterializerFactory == nil {
		return nil, fmt.Errorf("%w: native physical build materializer factory is not configured", deploymentnative.ErrInvalid)
	}
	config := f.Config
	postgres := *config.PostgresCatalog
	config.PostgresCatalog = &postgres
	config.CatalogPath = ""
	markerCopy := marker
	config.CommitMarker = &markerCopy
	environment, err := ducklake.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	materializer, err := f.MaterializerFactory(environment)
	if err != nil {
		return nil, errors.Join(err, environment.Close())
	}
	if materializer == nil {
		return nil, errors.Join(fmt.Errorf("%w: native physical build materializer factory returned nil executor", deploymentnative.ErrInvalid), environment.Close())
	}
	return &duckLakePhysicalBuildEnvironment{environment: environment, materializer: materializer}, nil
}

type duckLakePhysicalBuildEnvironment struct {
	environment  *ducklake.Environment
	materializer analyticsmaterialization.Executor
}

var _ NativePhysicalBuildEnvironment = (*duckLakePhysicalBuildEnvironment)(nil)

func (e *duckLakePhysicalBuildEnvironment) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
	if e == nil || e.environment == nil || e.materializer == nil {
		return 0, fmt.Errorf("%w: native physical build environment is not initialized", deploymentnative.ErrInvalid)
	}
	return e.materializer.Materialize(ctx, request)
}

func (e *duckLakePhysicalBuildEnvironment) SnapshotSealEvidence(ctx context.Context, snapshotID int64) (ducklake.PostgresSnapshotSealEvidence, error) {
	if e == nil || e.environment == nil {
		return ducklake.PostgresSnapshotSealEvidence{}, fmt.Errorf("%w: native physical build environment is not initialized", deploymentnative.ErrInvalid)
	}
	return e.environment.SnapshotSealEvidence(ctx, snapshotID)
}

func (e *duckLakePhysicalBuildEnvironment) NativeSnapshotClosureEvidence(ctx context.Context, request ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	if e == nil || e.environment == nil {
		return ducklake.NativeSnapshotClosureEvidence{}, fmt.Errorf("%w: native physical build environment is not initialized", deploymentnative.ErrInvalid)
	}
	return e.environment.NativeSnapshotClosureEvidence(ctx, request)
}

func (e *duckLakePhysicalBuildEnvironment) Close() error {
	if e == nil || e.environment == nil {
		return nil
	}
	return e.environment.Close()
}

// NativePhysicalBuildInput is the immutable identity and execution request
// for one native physical build. Attempt is the durable running-attempt proof;
// Marker is the exact identity written to DuckLake commit metadata.
type NativePhysicalBuildInput struct {
	Attempt    deploymentnative.DeliveryBuildAttempt
	Marker     catalogartifact.CommitMarker
	Request    analyticsmaterialization.Request
	CatalogID  string
	ObjectRoot string
}

// NativePhysicalBuildEvidence is value-only evidence returned after one
// successful materialization. No environment, database, SQL handle, or
// catalog path escapes this boundary.
type NativePhysicalBuildEvidence struct {
	AttemptID           string
	CatalogID           string
	ObjectRoot          string
	SnapshotID          int64
	Marker              catalogartifact.CommitMarker
	CanonicalMarkerJSON json.RawMessage
	Seal                ducklake.PostgresSnapshotSealEvidence
	Closure             ducklake.NativeSnapshotClosureEvidence
}

// PhysicalBuildInput and PhysicalBuildEvidence are concise aliases for
// callers that do not need to repeat the native qualifier.
type PhysicalBuildInput = NativePhysicalBuildInput
type PhysicalBuildEvidence = NativePhysicalBuildEvidence

// BuildNativePhysical performs exactly one marker-scoped native materialize
// operation and verifies the resulting catalog/object/snapshot evidence.
// Validation is complete before the factory is called. If an environment is
// opened, it is always closed; a close failure is joined without replacing a
// preceding operation error.
func BuildNativePhysical(ctx context.Context, input NativePhysicalBuildInput, factory NativePhysicalBuildEnvironmentFactory) (evidence NativePhysicalBuildEvidence, err error) {
	ctx = contextOrBackground(ctx)
	normalized, canonicalMarker, canonicalRoot, err := validateNativePhysicalBuildInput(input)
	if err != nil {
		return NativePhysicalBuildEvidence{}, err
	}
	if factory == nil {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: native physical build environment factory is not configured", deploymentnative.ErrInvalid)
	}

	environment, openErr := factory.Open(ctx, normalized.Marker)
	if openErr != nil {
		// A defensive close handles factories that return a usable environment
		// together with an error while preserving the open error as primary.
		if environment != nil {
			return NativePhysicalBuildEvidence{}, errors.Join(openErr, environment.Close())
		}
		return NativePhysicalBuildEvidence{}, openErr
	}
	if environment == nil {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: native physical build environment factory returned nil environment", deploymentnative.ErrInvalid)
	}
	defer func() {
		closeErr := environment.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
			if err != nil {
				evidence = NativePhysicalBuildEvidence{}
			}
		}
	}()

	snapshotID, materializeErr := environment.Materialize(ctx, normalized.Request)
	if materializeErr != nil {
		return NativePhysicalBuildEvidence{}, materializeErr
	}
	if snapshotID <= 0 {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: materialization returned non-positive snapshot", deploymentnative.ErrInvalid)
	}

	seal, sealErr := environment.SnapshotSealEvidence(ctx, snapshotID)
	if sealErr != nil {
		return NativePhysicalBuildEvidence{}, sealErr
	}
	closure, closureErr := environment.NativeSnapshotClosureEvidence(ctx, ducklake.NativeSnapshotClosureRequest{CatalogID: normalized.CatalogID, SnapshotID: snapshotID, ObjectRoot: canonicalRoot})
	if closureErr != nil {
		return NativePhysicalBuildEvidence{}, closureErr
	}
	if err := verifyNativePhysicalEvidence(seal, closure, normalized, canonicalMarker, canonicalRoot, snapshotID); err != nil {
		return NativePhysicalBuildEvidence{}, err
	}

	return NativePhysicalBuildEvidence{
		AttemptID: normalized.Attempt.AttemptID, CatalogID: normalized.CatalogID, ObjectRoot: canonicalRoot,
		SnapshotID: snapshotID, Marker: normalized.Marker,
		CanonicalMarkerJSON: append(json.RawMessage(nil), canonicalMarker...), Seal: cloneSealEvidence(seal), Closure: cloneClosureEvidence(closure),
	}, nil
}

// RunNativePhysicalBuild is an explicit verb alias for callers that prefer a
// command-style name.
func RunNativePhysicalBuild(ctx context.Context, input NativePhysicalBuildInput, factory NativePhysicalBuildEnvironmentFactory) (NativePhysicalBuildEvidence, error) {
	return BuildNativePhysical(ctx, input, factory)
}

func validateNativePhysicalBuildInput(input NativePhysicalBuildInput) (NativePhysicalBuildInput, []byte, string, error) {
	marker, err := input.Marker.Normalize()
	if err != nil {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	canonicalMarker, err := marker.CanonicalJSON()
	if err != nil || len(canonicalMarker) > catalogartifact.MaxCommitMarkerBytes {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: canonical commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	if err := validateAttempt(input.Attempt); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	if marker.AttemptID != input.Attempt.AttemptID || marker.PlanDigest != input.Attempt.PlanDigest || marker.RequestDigest != input.Attempt.RequestDigest || marker.PhysicalPoolID != input.Attempt.PhysicalPoolID || marker.LeaseEpoch != input.Attempt.FencingEpoch {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: marker and build attempt identity differs", deploymentnative.ErrConflict)
	}
	request := input.Request
	if err := request.Identity.Validate(); err != nil {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization serving identity: %v", deploymentnative.ErrInvalid, err)
	}
	if marker.Project != request.Identity.ProjectID.String() || marker.Environment != request.Identity.Environment || marker.GenerationID != request.Identity.GenerationID {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: marker and materialization serving identity differs", deploymentnative.ErrConflict)
	}
	if string(request.Environment) != request.Identity.Environment {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization environment differs from serving identity", deploymentnative.ErrConflict)
	}
	if request.CandidateID == "" || request.CandidateID != input.Attempt.CandidateID {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization candidate identity differs from build attempt", deploymentnative.ErrConflict)
	}
	if _, err := canonicalUUID(request.CandidateID, "materialization candidate id"); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	if err := validateResourceID(request.TargetID, "materialization target id"); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	if err := validateTextField(request.TargetType, "materialization target type", 255); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	if len(request.Models) == 0 || len(request.ModelTables) == 0 || len(request.Tables) == 0 {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization requires non-empty models, model tables, and tables", deploymentnative.ErrInvalid)
	}
	for name, model := range request.Models {
		if err := validateTextField(name, "materialization model id", 255); err != nil || model == nil {
			if err == nil {
				err = errors.New("model is nil")
			}
			return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization model %q: %v", deploymentnative.ErrInvalid, name, err)
		}
	}
	for name := range request.ModelTables {
		if err := validateTextField(name, "materialization model-table id", 255); err != nil {
			return NativePhysicalBuildInput{}, nil, "", err
		}
	}
	for _, table := range request.Tables {
		if err := validateTextField(table, "materialization table", 255); err != nil {
			return NativePhysicalBuildInput{}, nil, "", err
		}
	}
	if err := validateTextField(input.CatalogID, "catalog id", ducklake.NativeSnapshotClosureMaxFieldBytes); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	canonicalRoot, err := ducklake.CanonicalDataPath(input.ObjectRoot)
	if err != nil {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: object root: %v", deploymentnative.ErrInvalid, err)
	}
	if err := validateTextField(canonicalRoot, "object root", ducklake.NativeSnapshotClosureMaxFieldBytes); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	input.Marker = marker
	input.Request = request
	return input, []byte(canonicalMarker), canonicalRoot, nil
}

func validateAttempt(attempt deploymentnative.DeliveryBuildAttempt) error {
	for label, value := range map[string]string{"attempt id": attempt.AttemptID, "plan id": attempt.PlanID, "owner id": attempt.OwnerID, "physical pool id": attempt.PhysicalPoolID} {
		if err := validateTextField(value, label, 512); err != nil {
			return err
		}
	}
	if _, err := canonicalUUID(attempt.AttemptID, "attempt id"); err != nil {
		return err
	}
	if _, err := canonicalUUID(attempt.PlanID, "plan id"); err != nil {
		return err
	}
	if attempt.CandidateID != "" {
		if _, err := canonicalUUID(attempt.CandidateID, "candidate id"); err != nil {
			return err
		}
	}
	if attempt.FencingEpoch <= 0 {
		return fmt.Errorf("%w: build attempt fencing epoch must be positive", deploymentnative.ErrInvalid)
	}
	if attempt.State != deploymentnative.AttemptRunning {
		return fmt.Errorf("%w: build attempt must be running before physical materialization", deploymentnative.ErrConflict)
	}
	if attempt.LeaseExpiresAt.IsZero() || !attempt.LeaseExpiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("%w: build attempt lease is expired", deploymentnative.ErrConflict)
	}
	for label, value := range map[string]string{"request digest": attempt.RequestDigest, "plan digest": attempt.PlanDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: %s: %v", deploymentnative.ErrInvalid, label, err)
		}
	}
	return nil
}

func verifyNativePhysicalEvidence(seal ducklake.PostgresSnapshotSealEvidence, closure ducklake.NativeSnapshotClosureEvidence, input NativePhysicalBuildInput, canonicalMarker []byte, canonicalRoot string, snapshotID int64) error {
	if seal.SnapshotID != snapshotID {
		return fmt.Errorf("%w: snapshot seal evidence snapshot %d differs from materialized snapshot %d", deploymentnative.ErrConflict, seal.SnapshotID, snapshotID)
	}
	if seal.CatalogType != "postgres" || seal.DataPath != canonicalRoot || seal.CommitMarker != string(canonicalMarker) {
		return fmt.Errorf("%w: snapshot seal catalog/object/marker evidence differs", deploymentnative.ErrConflict)
	}
	if seal.MetadataSchema != ducklake.MetadataSchemaForPool(input.Attempt.PhysicalPoolID) {
		return fmt.Errorf("%w: snapshot seal metadata schema differs from physical pool", deploymentnative.ErrConflict)
	}
	for label, value := range map[string]string{"catalog type": seal.CatalogType, "metadata schema": seal.MetadataSchema, "data path": seal.DataPath, "extension version": seal.ExtensionVersion, "catalog version": seal.CatalogVersion, "commit marker": seal.CommitMarker} {
		if err := validateTextField(value, "snapshot seal "+label, ducklake.MaxCommitMarkerFieldBytes); err != nil {
			return err
		}
	}
	if closure.CatalogID != input.CatalogID || closure.SnapshotID != snapshotID || closure.ObjectRoot != canonicalRoot {
		return fmt.Errorf("%w: native snapshot closure catalog/object/snapshot evidence differs", deploymentnative.ErrConflict)
	}
	if len(closure.CanonicalJSON) == 0 || len(closure.CanonicalJSON) > ducklake.NativeSnapshotClosureMaxBytes {
		return fmt.Errorf("%w: native snapshot closure canonical evidence is missing or oversized", deploymentnative.ErrInvalid)
	}
	if err := verifyCanonicalClosureJSON(closure, canonicalRoot); err != nil {
		return err
	}
	for label, value := range map[string]string{"relation manifest digest": closure.RelationManifestDigest, "closure digest": closure.ClosureDigest, "object root digest": closure.ObjectRootDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: closure %s: %v", deploymentnative.ErrInvalid, label, err)
		}
	}
	if closure.RelationManifestDigest != nativeEvidenceDigest(closure.RelationManifestJSON) || closure.ClosureDigest != nativeEvidenceDigest(closure.ClosureJSON) || closure.ObjectRootDigest != nativeEvidenceDigest([]byte(closure.ObjectRoot)) {
		return fmt.Errorf("%w: native snapshot closure digests differ from canonical evidence", deploymentnative.ErrConflict)
	}
	return nil
}

func nativeEvidenceDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func verifyCanonicalClosureJSON(closure ducklake.NativeSnapshotClosureEvidence, canonicalRoot string) error {
	type relationManifest struct {
		Relations []ducklake.BaseTable `json:"relations"`
	}
	type closureManifest struct {
		Objects []ducklake.NativeSnapshotObject `json:"objects"`
	}
	type envelope struct {
		SchemaVersion          int                             `json:"schema_version"`
		CatalogID              string                          `json:"catalog_id"`
		SnapshotID             int64                           `json:"snapshot_id"`
		ObjectRoot             string                          `json:"object_root"`
		Relations              []ducklake.BaseTable            `json:"relations"`
		Objects                []ducklake.NativeSnapshotObject `json:"objects"`
		RelationManifestDigest string                          `json:"relation_manifest_digest"`
		ClosureDigest          string                          `json:"closure_digest"`
		ObjectRootDigest       string                          `json:"object_root_digest"`
	}
	relationJSON, err := json.Marshal(relationManifest{Relations: closure.Relations})
	if err != nil {
		return fmt.Errorf("%w: marshal closure relation manifest: %v", deploymentnative.ErrInvalid, err)
	}
	closureJSON, err := json.Marshal(closureManifest{Objects: closure.Objects})
	if err != nil {
		return fmt.Errorf("%w: marshal closure object manifest: %v", deploymentnative.ErrInvalid, err)
	}
	if len(closure.RelationManifestJSON) == 0 || !bytes.Equal(closure.RelationManifestJSON, relationJSON) || len(closure.ClosureJSON) == 0 || !bytes.Equal(closure.ClosureJSON, closureJSON) {
		return fmt.Errorf("%w: native snapshot closure manifests are not canonical", deploymentnative.ErrConflict)
	}
	expected, err := json.Marshal(envelope{SchemaVersion: ducklake.NativeSnapshotClosureSchemaVersion, CatalogID: closure.CatalogID, SnapshotID: closure.SnapshotID, ObjectRoot: canonicalRoot, Relations: closure.Relations, Objects: closure.Objects, RelationManifestDigest: closure.RelationManifestDigest, ClosureDigest: closure.ClosureDigest, ObjectRootDigest: closure.ObjectRootDigest})
	if err != nil {
		return fmt.Errorf("%w: marshal closure evidence: %v", deploymentnative.ErrInvalid, err)
	}
	if !bytes.Equal(closure.CanonicalJSON, expected) {
		return fmt.Errorf("%w: native snapshot closure canonical evidence differs from value fields", deploymentnative.ErrConflict)
	}
	return nil
}

func validateResourceID(value projectgraph.ResourceID, label string) error {
	if err := value.Validate(); err != nil || value.String() != strings.TrimSpace(value.String()) {
		return fmt.Errorf("%w: %s is not canonical", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func validateTextField(value, label string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) > max || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %s is invalid", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func cloneSealEvidence(value ducklake.PostgresSnapshotSealEvidence) ducklake.PostgresSnapshotSealEvidence {
	return value
}

func cloneClosureEvidence(value ducklake.NativeSnapshotClosureEvidence) ducklake.NativeSnapshotClosureEvidence {
	if value.Relations != nil {
		relations := make([]ducklake.BaseTable, len(value.Relations))
		copy(relations, value.Relations)
		value.Relations = relations
	}
	if value.Objects != nil {
		objects := make([]ducklake.NativeSnapshotObject, len(value.Objects))
		copy(objects, value.Objects)
		value.Objects = objects
	}
	value.RelationManifestJSON = append(json.RawMessage(nil), value.RelationManifestJSON...)
	value.ClosureJSON = append(json.RawMessage(nil), value.ClosureJSON...)
	value.CanonicalJSON = append(json.RawMessage(nil), value.CanonicalJSON...)
	return value
}
