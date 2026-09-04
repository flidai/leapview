package deploymentpostgres

// This file owns the read-only qualification boundary for a native
// PostgreSQL-backed DuckLake build. A build has already committed one exact
// snapshot and captured its relation/object closure. Qualification consumes
// that value evidence, opens the same catalog at that snapshot in serving
// mode, and runs the existing source/model gates against the authority-derived
// relation namespace. No catalog file or physical-pool directory is opened
// or enumerated here.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/gates"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/release"
)

const (
	NativeQualificationSchemaVersion = 1
	NativeQualificationMaxBytes      = 128 * 1024
	NativeQualificationMaxFieldBytes = 4096
)

var (
	ErrNativeQualificationInvalid = errors.New("native DuckLake qualification input is invalid")
	ErrNativeQualificationFailed  = errors.New("native DuckLake qualification failed")
	ErrNativeQualificationRuntime = errors.New("native DuckLake runtime compatibility evidence is unavailable")
)

// NativeQualificationEnvironment is deliberately read-only. Query is the
// existing value-only analytics gate capability; no Exec, Commit, refresh, or
// catalog maintenance operation can escape this boundary.
type NativeQualificationEnvironment interface {
	Query(context.Context, semanticQueryPlan) (semanticRows, error)
	RuntimeCompatibility(context.Context) (NativeRuntimeCompatibilityEvidence, error)
	NativeSnapshotClosureEvidence(context.Context, ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error)
	Close() error
}

// These aliases keep the public qualification interface independent from the
// concrete semantic-query package in comments and make fake environments
// straightforward to implement. They are aliases, not wrappers.
type semanticQueryPlan = semanticquery.Plan
type semanticRows = semanticquery.Rows

// NativeQualificationOpenRequest identifies the one exact read-only attach.
// Every field is derived from the physical build evidence or the admitted
// target contract; callers cannot ask a factory to attach a different state.
type NativeQualificationOpenRequest struct {
	PhysicalPoolID    string
	CatalogID         string
	SnapshotID        int64
	ObjectRoot        string
	RelationNamespace string
	CommitMarker      catalogartifact.CommitMarker
	Compatibility     ducklakepostgres.RuntimeCompatibility
}

type NativeQualificationEnvironmentFactory interface {
	Open(context.Context, NativeQualificationOpenRequest) (NativeQualificationEnvironment, error)
}

// NativeQualificationCompatibilityAuthority reads the current PostgreSQL
// catalog/runtime tuple. Qualification must bind the attached engine values
// to this independently persisted authority rather than echoing request data.
type NativeQualificationCompatibilityAuthority interface {
	LoadCatalogRuntimeCompatibility(context.Context, string) (ducklakepostgres.CatalogRuntimeCompatibility, error)
}

type NativeQualificationEnvironmentFactoryFunc func(context.Context, NativeQualificationOpenRequest) (NativeQualificationEnvironment, error)

func (f NativeQualificationEnvironmentFactoryFunc) Open(ctx context.Context, request NativeQualificationOpenRequest) (NativeQualificationEnvironment, error) {
	if f == nil {
		return nil, ErrNativeQualificationRuntime
	}
	return f(ctx, request)
}

// NativeRuntimeCompatibilityEvidence is observed from the attached DuckLake
// runtime, not copied from a caller's expected tuple. The target compatibility
// digest and catalog schema version are included as admitted identities after
// the observed runtime/catalog values match.
type NativeRuntimeCompatibilityEvidence struct {
	SnapshotID           int64  `json:"snapshot_id"`
	CatalogType          string `json:"catalog_type"`
	DataPath             string `json:"data_path"`
	MetadataSchema       string `json:"metadata_schema"`
	DuckDBRuntime        string `json:"duckdb_runtime"`
	DuckLakeExtension    string `json:"ducklake_extension"`
	CatalogFormat        string `json:"catalog_format"`
	CompatibilityDigest  string `json:"compatibility_digest"`
	CatalogSchemaVersion string `json:"catalog_schema_version"`
}

// NativeQualificationRequest supplies the compiled contract and exact
// physical build evidence. Sources and models are the existing gates' typed
// inputs; they are never decoded from authored SQL at this boundary.
type NativeQualificationRequest struct {
	Build             NativePhysicalBuildEvidence
	CandidateID       string
	SourceDigest      string
	BindingGeneration string
	RuntimeVersion    string
	Compatibility     ducklakepostgres.RuntimeCompatibility
	Sources           []gates.SourceInput
	Models            []gates.ModelInput
	Bounds            gates.Bounds
	Now               time.Time
}

// NativeQualificationEvidence is bounded, canonical, and content addressed.
// The closure digest is the exact digest returned by the physical build;
// closure membership itself remains in DuckLake's authoritative evidence.
type NativeQualificationEvidence struct {
	SchemaVersion          int                                `json:"schema_version"`
	CandidateID            string                             `json:"candidate_id"`
	AttemptID              string                             `json:"attempt_id"`
	PhysicalPoolID         string                             `json:"physical_pool_id"`
	CatalogID              string                             `json:"catalog_id"`
	SnapshotID             int64                              `json:"snapshot_id"`
	ObjectRoot             string                             `json:"object_root"`
	RelationNamespace      string                             `json:"relation_namespace"`
	RelationManifestDigest string                             `json:"relation_manifest_digest"`
	ClosureDigest          string                             `json:"closure_digest"`
	Runtime                NativeRuntimeCompatibilityEvidence `json:"runtime"`
	Gates                  release.GateEvidence               `json:"gates"`
	Digest                 string                             `json:"digest"`
}

// Canonical returns the stable JSON bytes and digest for this evidence. It
// revalidates the digest-bearing component records before encoding.
func (e NativeQualificationEvidence) Canonical() ([]byte, string, error) {
	if err := validateNativeQualificationEvidence(e); err != nil {
		return nil, "", err
	}
	suppliedDigest := e.Digest
	e.Digest = ""
	encoded, err := json.Marshal(e)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) > NativeQualificationMaxBytes {
		return nil, "", fmt.Errorf("%w: canonical evidence exceeds %d bytes", ErrNativeQualificationInvalid, NativeQualificationMaxBytes)
	}
	digestValue := nativeQualificationDigest(encoded)
	if suppliedDigest != "" && suppliedDigest != digestValue {
		return nil, "", fmt.Errorf("%w: qualification digest mismatch", ErrNativeQualificationInvalid)
	}
	e.Digest = digestValue
	encoded, err = json.Marshal(e)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) > NativeQualificationMaxBytes {
		return nil, "", fmt.Errorf("%w: canonical evidence exceeds %d bytes", ErrNativeQualificationInvalid, NativeQualificationMaxBytes)
	}
	return encoded, digestValue, nil
}

// QualifyNativeSnapshot opens one exact read-only snapshot, evaluates the
// existing schema/source/analytical gates, and returns canonical evidence.
// Open environments are always closed, including when a gate fails.
func QualifyNativeSnapshot(ctx context.Context, request NativeQualificationRequest, factory NativeQualificationEnvironmentFactory) (result NativeQualificationEvidence, resultErr error) {
	if err := validateNativeQualificationRequest(request); err != nil {
		return NativeQualificationEvidence{}, err
	}
	preflightQueries, preflightRows, preflightMillis, err := nativeQualificationPreflight(request.Sources)
	if err != nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: source observation metrics: %v", ErrNativeQualificationInvalid, err)
	}
	if factory == nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: environment factory is required", ErrNativeQualificationRuntime)
	}
	build := request.Build
	openRequest := NativeQualificationOpenRequest{
		PhysicalPoolID: build.Marker.PhysicalPoolID, CatalogID: build.CatalogID,
		SnapshotID: build.SnapshotID, ObjectRoot: build.ObjectRoot,
		RelationNamespace: build.Closure.RelationNamespace, CommitMarker: build.Marker, Compatibility: request.Compatibility,
	}
	env, err := factory.Open(ctx, openRequest)
	if err != nil {
		openErr := fmt.Errorf("%w: open exact snapshot: %v", ErrNativeQualificationFailed, err)
		if env != nil {
			if closeErr := env.Close(); closeErr != nil {
				openErr = errors.Join(openErr, fmt.Errorf("%w: close partially opened snapshot: %v", ErrNativeQualificationFailed, closeErr))
			}
		}
		return NativeQualificationEvidence{}, openErr
	}
	if env == nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: factory returned nil environment", ErrNativeQualificationRuntime)
	}
	defer func() {
		// An attach that cannot be closed is not a complete qualification. The
		// environment may otherwise retain a live PostgreSQL session past the
		// evidence boundary.
		if closeErr := env.Close(); closeErr != nil {
			result = NativeQualificationEvidence{}
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: close read-only snapshot: %v", ErrNativeQualificationFailed, closeErr))
		}
	}()
	compatibility, err := env.RuntimeCompatibility(ctx)
	if err != nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: %v", ErrNativeQualificationRuntime, err)
	}
	if err := validateObservedRuntimeCompatibility(compatibility, openRequest, request.Compatibility); err != nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: %v", ErrNativeQualificationFailed, err)
	}
	liveClosure, err := env.NativeSnapshotClosureEvidence(ctx, ducklake.NativeSnapshotClosureRequest{CatalogID: build.CatalogID, SnapshotID: build.SnapshotID, ObjectRoot: build.ObjectRoot, RelationNamespace: build.Closure.RelationNamespace})
	if err != nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: read exact physical closure: %v", ErrNativeQualificationFailed, err)
	}
	if err := validateExactClosure(build.Closure, liveClosure); err != nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: physical closure changed: %v", ErrNativeQualificationFailed, err)
	}
	gateInput := gates.Input{
		CandidateID: request.CandidateID, SourceDigest: request.SourceDigest,
		BindingGeneration: request.BindingGeneration, RuntimeVersion: request.RuntimeVersion,
		DuckDBVersion: compatibility.DuckDBRuntime, Now: request.Now, Bounds: request.Bounds,
		Sources: request.Sources, Models: request.Models, RelationNamespace: build.Closure.RelationNamespace,
		PreflightQueries: preflightQueries, PreflightRows: preflightRows, PreflightMillis: preflightMillis,
		Query: func(queryCtx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
			return env.Query(queryCtx, plan)
		},
	}
	gatesEvidence, gateErr := gates.Evaluate(ctx, gateInput)
	if gateErr != nil {
		return NativeQualificationEvidence{}, fmt.Errorf("%w: analytical gates: %v", ErrNativeQualificationFailed, gateErr)
	}
	result = NativeQualificationEvidence{
		SchemaVersion: NativeQualificationSchemaVersion, CandidateID: request.CandidateID,
		AttemptID: build.AttemptID, PhysicalPoolID: build.Marker.PhysicalPoolID,
		CatalogID: build.CatalogID, SnapshotID: build.SnapshotID, ObjectRoot: build.ObjectRoot,
		RelationNamespace:      build.Closure.RelationNamespace,
		RelationManifestDigest: build.Closure.RelationManifestDigest, ClosureDigest: build.Closure.ClosureDigest,
		Runtime: compatibility, Gates: gatesEvidence,
	}
	canonical, _, err := result.Canonical()
	if err != nil {
		return NativeQualificationEvidence{}, err
	}
	if err := json.Unmarshal(canonical, &result); err != nil {
		return NativeQualificationEvidence{}, err
	}
	return result, nil
}

// nativeQualificationPreflight totals the source-session work that has
// already happened before the closed gate evaluator starts. Checked
// aggregation prevents malformed evidence from wrapping around and evading
// the evaluator's bounds checks; Evaluate remains responsible for deciding
// whether a valid total exhausts the configured budget.
func nativeQualificationPreflight(sources []gates.SourceInput) (queries int, rows, millis int64, err error) {
	for _, source := range sources {
		if source.ObservationQueries < 0 || source.ObservationRows < 0 || source.ObservationMillis < 0 {
			return 0, 0, 0, fmt.Errorf("source %q observation counters cannot be negative", source.ID)
		}
		queries, err = checkedNativeQualificationIntAdd(queries, source.ObservationQueries)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("source %q observation queries overflow: %w", source.ID, err)
		}
		rows, err = checkedNativeQualificationInt64Add(rows, source.ObservationRows)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("source %q observation rows overflow: %w", source.ID, err)
		}
		millis, err = checkedNativeQualificationInt64Add(millis, source.ObservationMillis)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("source %q observation millis overflow: %w", source.ID, err)
		}
	}
	return queries, rows, millis, nil
}

func checkedNativeQualificationIntAdd(total, value int) (int, error) {
	if value > int(^uint(0)>>1)-total {
		return 0, errors.New("integer overflow")
	}
	return total + value, nil
}

func checkedNativeQualificationInt64Add(total, value int64) (int64, error) {
	if value > int64(^uint64(0)>>1)-total {
		return 0, errors.New("integer overflow")
	}
	return total + value, nil
}

// QualifyNative is a command-style alias retained for callers which use the
// build/qualify verb pair.
func QualifyNative(ctx context.Context, request NativeQualificationRequest, factory NativeQualificationEnvironmentFactory) (NativeQualificationEvidence, error) {
	return QualifyNativeSnapshot(ctx, request, factory)
}

// DuckLakeNativeQualificationEnvironmentFactory is the executable production
// adapter. It forces a PostgreSQL serving attachment and ReadOnly=true, with
// SNAPSHOT_VERSION supplied only from build evidence. Config must carry the
// admitted pool contract and extension admission; no catalog path is read.
type DuckLakeNativeQualificationEnvironmentFactory struct {
	Config                 ducklake.Config
	CatalogID              string
	CompatibilityAuthority NativeQualificationCompatibilityAuthority
}

func (f DuckLakeNativeQualificationEnvironmentFactory) Open(ctx context.Context, request NativeQualificationOpenRequest) (NativeQualificationEnvironment, error) {
	if f.Config.PoolContract == nil || f.Config.ExtensionAdmission == nil {
		return nil, fmt.Errorf("%w: pool contract and extension admission are required", ErrNativeQualificationRuntime)
	}
	marker, err := request.CommitMarker.Normalize()
	if err != nil || marker.PhysicalPoolID != request.PhysicalPoolID {
		return nil, fmt.Errorf("%w: exact commit marker is required", ErrNativeQualificationInvalid)
	}
	request.CommitMarker = marker
	if request.SnapshotID <= 0 || strings.TrimSpace(request.PhysicalPoolID) == "" || request.PhysicalPoolID != f.Config.PoolContract.Pool.ID.String() {
		return nil, fmt.Errorf("%w: physical pool or snapshot identity mismatch", ErrNativeQualificationInvalid)
	}
	if strings.TrimSpace(f.CatalogID) == "" || request.CatalogID != f.CatalogID {
		return nil, fmt.Errorf("%w: catalog identity mismatch", ErrNativeQualificationInvalid)
	}
	if nativeBuildAuthorityNil(f.CompatibilityAuthority) {
		return nil, fmt.Errorf("%w: PostgreSQL runtime compatibility authority is required", ErrNativeQualificationRuntime)
	}
	current, err := f.CompatibilityAuthority.LoadCatalogRuntimeCompatibility(ctx, request.PhysicalPoolID)
	if err != nil {
		return nil, fmt.Errorf("%w: load PostgreSQL runtime compatibility: %v", ErrNativeQualificationRuntime, err)
	}
	if current.PhysicalPoolID != request.PhysicalPoolID || current.CatalogID != request.CatalogID || !sameNativeRuntimeCompatibility(current.RuntimeCompatibility, request.Compatibility) {
		return nil, fmt.Errorf("%w: PostgreSQL runtime compatibility identity mismatch", ErrNativeQualificationInvalid)
	}
	if err := f.Config.PoolContract.Validate(); err != nil {
		return nil, fmt.Errorf("%w: pool admission: %v", ErrNativeQualificationInvalid, err)
	}
	if err := ducklake.ValidateRelationNamespace(request.RelationNamespace); err != nil {
		return nil, fmt.Errorf("%w: relation namespace: %v", ErrNativeQualificationInvalid, err)
	}
	if _, err := ducklake.CanonicalDataPath(request.ObjectRoot); err != nil {
		return nil, fmt.Errorf("%w: object root: %v", ErrNativeQualificationInvalid, err)
	}
	config := f.Config
	config.CatalogPath = ""
	config.ReadOnly = true
	config.PhysicalPoolID = request.PhysicalPoolID
	config.SharedPool = true
	postgres := config.PostgresCatalog
	if postgres == nil {
		return nil, fmt.Errorf("%w: PostgreSQL catalog configuration is required", ErrNativeQualificationRuntime)
	}
	postgresCopy := *postgres
	postgresCopy.Mode = ducklake.PostgresCatalogServing
	postgresCopy.PhysicalPoolID = request.PhysicalPoolID
	postgresCopy.SnapshotVersion = request.SnapshotID
	postgresCopy.DataPath = ""
	config.PostgresCatalog = &postgresCopy
	config.CommitMarker = &marker
	env, err := ducklake.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	return &duckLakeNativeQualificationEnvironment{environment: env, request: request, expected: current.RuntimeCompatibility}, nil
}

type duckLakeNativeQualificationEnvironment struct {
	environment *ducklake.Environment
	request     NativeQualificationOpenRequest
	expected    ducklakepostgres.RuntimeCompatibility
}

func (e *duckLakeNativeQualificationEnvironment) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	if e == nil || e.environment == nil {
		return nil, ErrNativeQualificationRuntime
	}
	return e.environment.Query(ctx, plan)
}

func (e *duckLakeNativeQualificationEnvironment) Close() error {
	if e == nil || e.environment == nil {
		return nil
	}
	return e.environment.Close()
}

func (e *duckLakeNativeQualificationEnvironment) SnapshotSealEvidence(ctx context.Context, snapshotID int64) (ducklake.PostgresSnapshotSealEvidence, error) {
	if e == nil || e.environment == nil {
		return ducklake.PostgresSnapshotSealEvidence{}, ErrNativeQualificationRuntime
	}
	return e.environment.SnapshotSealEvidence(ctx, snapshotID)
}

func (e *duckLakeNativeQualificationEnvironment) NativeSnapshotClosureEvidence(ctx context.Context, request ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	if e == nil || e.environment == nil {
		return ducklake.NativeSnapshotClosureEvidence{}, ErrNativeQualificationRuntime
	}
	return e.environment.NativeSnapshotClosureEvidence(ctx, request)
}

func (e *duckLakeNativeQualificationEnvironment) RuntimeCompatibility(ctx context.Context) (NativeRuntimeCompatibilityEvidence, error) {
	if e == nil || e.environment == nil {
		return NativeRuntimeCompatibilityEvidence{}, ErrNativeQualificationRuntime
	}
	query := func(sql string, columns ...string) (semanticquery.Rows, error) {
		return e.environment.Query(ctx, semanticquery.Plan{SQL: sql, Columns: columns})
	}
	versionRows, err := query("SELECT version() AS version", "version")
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, fmt.Errorf("read DuckDB version: %w", err)
	}
	if len(versionRows) != 1 {
		return NativeRuntimeCompatibilityEvidence{}, fmt.Errorf("read DuckDB version: expected one row, got %d", len(versionRows))
	}
	duckdbRuntime, err := canonicalRuntimeComponent("duckdb", stringValue(versionRows[0]["version"]))
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, err
	}
	settings, err := query("SELECT catalog_type, extension_version, data_path FROM lake.settings() LIMIT 1", "catalog_type", "extension_version", "data_path")
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, fmt.Errorf("read DuckLake settings: %w", err)
	}
	if len(settings) != 1 {
		return NativeRuntimeCompatibilityEvidence{}, fmt.Errorf("read DuckLake settings: expected one row, got %d", len(settings))
	}
	catalogType, extensionVersion := stringValue(settings[0]["catalog_type"]), stringValue(settings[0]["extension_version"])
	dataPath, err := ducklake.CanonicalDataPath(stringValue(settings[0]["data_path"]))
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, err
	}
	ducklakeExtension, err := canonicalRuntimeComponent("ducklake", extensionVersion)
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, err
	}
	versionRows, err = query("SELECT CAST(value AS VARCHAR) AS value FROM lake.options() WHERE lower(option_name) = 'version' AND upper(scope) = 'GLOBAL' LIMIT 1", "value")
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, fmt.Errorf("read DuckLake catalog version: %w", err)
	}
	if len(versionRows) != 1 {
		return NativeRuntimeCompatibilityEvidence{}, fmt.Errorf("read DuckLake catalog version: expected one row, got %d", len(versionRows))
	}
	catalogFormat, err := canonicalCatalogVersion(stringValue(versionRows[0]["value"]))
	if err != nil {
		return NativeRuntimeCompatibilityEvidence{}, err
	}
	return NativeRuntimeCompatibilityEvidence{
		SnapshotID: e.request.SnapshotID, CatalogType: strings.TrimSpace(catalogType), DataPath: dataPath,
		MetadataSchema: ducklake.MetadataSchemaForPool(e.request.PhysicalPoolID), DuckDBRuntime: duckdbRuntime,
		DuckLakeExtension: ducklakeExtension, CatalogFormat: catalogFormat,
		CompatibilityDigest: e.expected.CompatibilityDigest, CatalogSchemaVersion: e.expected.CatalogSchemaVersion,
	}, nil
}

func validateNativeQualificationRequest(request NativeQualificationRequest) error {
	build := request.Build
	if strings.TrimSpace(request.CandidateID) == "" || strings.TrimSpace(request.SourceDigest) == "" || strings.TrimSpace(request.BindingGeneration) == "" || strings.TrimSpace(request.RuntimeVersion) == "" {
		return fmt.Errorf("%w: candidate/runtime identities are required", ErrNativeQualificationInvalid)
	}
	for name, value := range map[string]string{"source digest": request.SourceDigest, "binding generation": request.BindingGeneration} {
		if err := digest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrNativeQualificationInvalid, name, err)
		}
	}
	if request.Compatibility.DuckDBRuntime == "" || request.Compatibility.DuckLakeExtension == "" || request.Compatibility.CatalogFormat == "" || request.Compatibility.CompatibilityDigest == "" || request.Compatibility.CatalogSchemaVersion == "" {
		return fmt.Errorf("%w: runtime compatibility contract is incomplete", ErrNativeQualificationInvalid)
	}
	canonicalDuckDB, duckDBErr := canonicalRuntimeComponent("duckdb", request.Compatibility.DuckDBRuntime)
	canonicalDuckLake, duckLakeErr := canonicalRuntimeComponent("ducklake", request.Compatibility.DuckLakeExtension)
	if duckDBErr != nil || duckLakeErr != nil || canonicalDuckDB != request.Compatibility.DuckDBRuntime || canonicalDuckLake != request.Compatibility.DuckLakeExtension {
		return fmt.Errorf("%w: runtime compatibility components are not canonical", ErrNativeQualificationInvalid)
	}
	if err := digest.ValidateSHA256Identity(request.Compatibility.CompatibilityDigest); err != nil {
		return fmt.Errorf("%w: compatibility digest: %v", ErrNativeQualificationInvalid, err)
	}
	if build.AttemptID == "" || build.CatalogID == "" || build.Marker.PhysicalPoolID == "" || build.SnapshotID <= 0 || build.ObjectRoot == "" || build.Closure.RelationNamespace == "" {
		return fmt.Errorf("%w: physical build evidence is incomplete", ErrNativeQualificationInvalid)
	}
	if len(request.Models) == 0 {
		return fmt.Errorf("%w: at least one compiled model is required", ErrNativeQualificationInvalid)
	}
	if build.Marker.AttemptID != build.AttemptID || build.Marker.PhysicalPoolID == "" || build.Seal.SnapshotID != build.SnapshotID || build.Seal.CatalogType != "postgres" || build.Seal.CommitMarker == "" {
		return fmt.Errorf("%w: physical build identity evidence does not match", ErrNativeQualificationInvalid)
	}
	if build.Seal.MetadataSchema != ducklake.MetadataSchemaForPool(build.Marker.PhysicalPoolID) {
		return fmt.Errorf("%w: physical build metadata schema does not match the admitted pool", ErrNativeQualificationInvalid)
	}
	buildCatalogVersion, buildCatalogErr := canonicalCatalogVersion(build.Seal.CatalogVersion)
	expectedCatalogVersion, expectedCatalogErr := canonicalCatalogVersion(request.Compatibility.CatalogFormat)
	buildExtension, buildExtensionErr := canonicalRuntimeComponent("ducklake", build.Seal.ExtensionVersion)
	if buildCatalogErr != nil || expectedCatalogErr != nil || buildCatalogVersion != expectedCatalogVersion || buildExtensionErr != nil || buildExtension != request.Compatibility.DuckLakeExtension {
		return fmt.Errorf("%w: physical build runtime versions do not match admitted compatibility", ErrNativeQualificationInvalid)
	}
	markerJSON, err := build.Marker.CanonicalJSON()
	if err != nil || len(build.CanonicalMarkerJSON) == 0 || string(build.CanonicalMarkerJSON) != markerJSON || build.Seal.CommitMarker != markerJSON {
		return fmt.Errorf("%w: physical build commit marker evidence is not canonical", ErrNativeQualificationInvalid)
	}
	canonicalRoot, err := ducklake.CanonicalDataPath(build.ObjectRoot)
	if err != nil || canonicalRoot != build.ObjectRoot || build.Seal.DataPath != canonicalRoot || build.Closure.ObjectRoot != canonicalRoot {
		return fmt.Errorf("%w: physical object root evidence does not match", ErrNativeQualificationInvalid)
	}
	if build.Closure.CatalogID != build.CatalogID || build.Closure.SnapshotID != build.SnapshotID || build.Closure.RelationNamespace == "" {
		return fmt.Errorf("%w: physical closure identity does not match", ErrNativeQualificationInvalid)
	}
	if err := ducklake.VerifyNativeSnapshotClosureEvidence(build.Closure); err != nil {
		return fmt.Errorf("%w: closure evidence: %v", ErrNativeQualificationInvalid, err)
	}
	if len(build.Closure.CanonicalJSON) == 0 || len(build.Closure.CanonicalJSON) > ducklake.NativeSnapshotClosureMaxBytes {
		return fmt.Errorf("%w: closure canonical JSON is missing or oversized", ErrNativeQualificationInvalid)
	}
	return nil
}

func validateObservedRuntimeCompatibility(observed NativeRuntimeCompatibilityEvidence, request NativeQualificationOpenRequest, expected ducklakepostgres.RuntimeCompatibility) error {
	if observed.SnapshotID != request.SnapshotID || observed.CatalogType != "postgres" || observed.DataPath != request.ObjectRoot || observed.MetadataSchema != ducklake.MetadataSchemaForPool(request.PhysicalPoolID) {
		return fmt.Errorf("attached runtime identity differs from exact physical build")
	}
	expectedCatalogFormat, err := canonicalCatalogVersion(expected.CatalogFormat)
	if err != nil {
		return fmt.Errorf("admitted catalog format is invalid")
	}
	if observed.DuckDBRuntime != expected.DuckDBRuntime || observed.DuckLakeExtension != expected.DuckLakeExtension || observed.CatalogFormat != expectedCatalogFormat || observed.CompatibilityDigest != expected.CompatibilityDigest || observed.CatalogSchemaVersion != expected.CatalogSchemaVersion {
		return fmt.Errorf("attached runtime compatibility differs from admitted tuple")
	}
	return nil
}

func sameNativeRuntimeCompatibility(left, right ducklakepostgres.RuntimeCompatibility) bool {
	return left.DuckDBRuntime == right.DuckDBRuntime && left.DuckLakeExtension == right.DuckLakeExtension && left.CatalogFormat == right.CatalogFormat && left.CompatibilityDigest == right.CompatibilityDigest && left.CatalogSchemaVersion == right.CatalogSchemaVersion
}

func validateExactClosure(expected, actual ducklake.NativeSnapshotClosureEvidence) error {
	if err := ducklake.VerifyNativeSnapshotClosureEvidence(actual); err != nil {
		return err
	}
	if expected.CatalogID != actual.CatalogID || expected.SnapshotID != actual.SnapshotID || expected.ObjectRoot != actual.ObjectRoot || expected.RelationNamespace != actual.RelationNamespace || expected.RelationManifestDigest != actual.RelationManifestDigest || expected.ClosureDigest != actual.ClosureDigest || expected.ObjectRootDigest != actual.ObjectRootDigest {
		return fmt.Errorf("closure identity or digest differs")
	}
	if len(expected.Relations) != len(actual.Relations) || len(expected.Objects) != len(actual.Objects) {
		return fmt.Errorf("closure membership differs")
	}
	for i := range expected.Relations {
		if expected.Relations[i] != actual.Relations[i] {
			return fmt.Errorf("relation closure differs")
		}
	}
	for i := range expected.Objects {
		if expected.Objects[i] != actual.Objects[i] {
			return fmt.Errorf("object closure differs")
		}
	}
	if string(expected.RelationManifestJSON) != string(actual.RelationManifestJSON) || string(expected.ClosureJSON) != string(actual.ClosureJSON) {
		return fmt.Errorf("canonical closure manifests differ")
	}
	return nil
}

func validateNativeQualificationEvidence(e NativeQualificationEvidence) error {
	if e.SchemaVersion != NativeQualificationSchemaVersion || e.CandidateID == "" || e.AttemptID == "" || e.PhysicalPoolID == "" || e.CatalogID == "" || e.SnapshotID <= 0 || e.ObjectRoot == "" || e.RelationNamespace == "" || e.RelationManifestDigest == "" || e.ClosureDigest == "" {
		return fmt.Errorf("%w: qualification identity is incomplete", ErrNativeQualificationInvalid)
	}
	if err := digest.ValidateSHA256Identity(e.RelationManifestDigest); err != nil {
		return fmt.Errorf("%w: relation manifest digest: %v", ErrNativeQualificationInvalid, err)
	}
	if err := digest.ValidateSHA256Identity(e.ClosureDigest); err != nil {
		return fmt.Errorf("%w: closure digest: %v", ErrNativeQualificationInvalid, err)
	}
	if err := ducklake.ValidateRelationNamespace(e.RelationNamespace); err != nil {
		return fmt.Errorf("%w: relation namespace: %v", ErrNativeQualificationInvalid, err)
	}
	canonicalRoot, err := ducklake.CanonicalDataPath(e.ObjectRoot)
	if err != nil || canonicalRoot != e.ObjectRoot {
		return fmt.Errorf("%w: object root is not canonical", ErrNativeQualificationInvalid)
	}
	if e.Runtime.SnapshotID != e.SnapshotID || e.Runtime.DataPath != e.ObjectRoot || e.Runtime.CatalogType != "postgres" {
		return fmt.Errorf("%w: runtime evidence identity differs", ErrNativeQualificationInvalid)
	}
	if err := digest.ValidateSHA256Identity(e.Runtime.CompatibilityDigest); err != nil || e.Runtime.DuckDBRuntime == "" || e.Runtime.DuckLakeExtension == "" || e.Runtime.CatalogFormat == "" || e.Runtime.CatalogSchemaVersion == "" {
		return fmt.Errorf("%w: runtime compatibility evidence is incomplete", ErrNativeQualificationInvalid)
	}
	if err := e.Gates.Validate(); err != nil || (e.Gates.Outcome != release.GateSuccess && e.Gates.Outcome != release.GateWarning) {
		return fmt.Errorf("%w: gate evidence is not qualifying", ErrNativeQualificationInvalid)
	}
	return nil
}

func canonicalRuntimeComponent(prefix, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrNativeQualificationRuntime
	}
	if index := strings.IndexByte(value, ':'); index >= 0 {
		if value[:index] != prefix {
			return "", fmt.Errorf("runtime component prefix %q is invalid", value)
		}
		value = value[index+1:]
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrNativeQualificationRuntime
	}
	return prefix + ":" + value, nil
}

func canonicalCatalogVersion(value string) (string, error) {
	canonical, err := ducklake.CanonicalCatalogVersion(value)
	if err != nil {
		return "", ErrNativeQualificationRuntime
	}
	return canonical, nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

func nativeQualificationDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
