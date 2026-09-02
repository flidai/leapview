package deploymentpostgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

var ErrNativePhysicalRecoveryUnresolved = errors.New("native physical build commit outcome is unresolved")

// ErrNativePhysicalMarkerAbsent identifies the one safe, positive result of a
// marker lookup that completed successfully and found no exact marker.  It is
// deliberately distinct from ErrNativePhysicalRecoveryUnresolved: resolver,
// close, quarantine, snapshot, and evidence errors all remain unresolved but
// must never authorize a successor admission.
var ErrNativePhysicalMarkerAbsent = errors.New("native physical build commit marker is absent")

// NativePhysicalSnapshotInspector is the least-privilege read-only view used
// during recovery. It deliberately omits Query, materialization, and all
// mutation capabilities exposed by the normal qualification environment.
type NativePhysicalSnapshotInspector interface {
	SnapshotSealEvidence(context.Context, int64) (ducklake.PostgresSnapshotSealEvidence, error)
	NativeSnapshotClosureEvidence(context.Context, ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error)
	Close() error
}

type NativePhysicalSnapshotInspectorFactory interface {
	Open(context.Context, NativeQualificationOpenRequest) (NativePhysicalSnapshotInspector, error)
}

type NativePhysicalSnapshotInspectorFactoryFunc func(context.Context, NativeQualificationOpenRequest) (NativePhysicalSnapshotInspector, error)

func (f NativePhysicalSnapshotInspectorFactoryFunc) Open(ctx context.Context, request NativeQualificationOpenRequest) (NativePhysicalSnapshotInspector, error) {
	if f == nil {
		return nil, ErrNativeQualificationRuntime
	}
	return f(ctx, request)
}

// NativeQualificationSnapshotInspectorFactory adapts the normal pinned
// qualification opener while erasing its Query capability at this boundary.
// Production and tests can instead provide a native inspector directly.
type NativeQualificationSnapshotInspectorFactory struct {
	QualificationFactory NativeQualificationEnvironmentFactory
}

type nativeQualificationSnapshotSealReader interface {
	SnapshotSealEvidence(context.Context, int64) (ducklake.PostgresSnapshotSealEvidence, error)
}

func (f NativeQualificationSnapshotInspectorFactory) Open(ctx context.Context, request NativeQualificationOpenRequest) (NativePhysicalSnapshotInspector, error) {
	if nativeBuildAuthorityNil(f.QualificationFactory) {
		return nil, ErrNativeQualificationRuntime
	}
	env, err := f.QualificationFactory.Open(ctx, request)
	if err != nil {
		if !nativeBuildAuthorityNil(env) {
			return nil, errors.Join(err, env.Close())
		}
		return nil, err
	}
	if nativeBuildAuthorityNil(env) {
		return nil, ErrNativeQualificationRuntime
	}
	sealReader, ok := env.(nativeQualificationSnapshotSealReader)
	if !ok || nativeBuildAuthorityNil(sealReader) {
		return nil, errors.Join(ErrNativeQualificationRuntime, env.Close())
	}
	return qualificationSnapshotInspector{env: env, sealReader: sealReader}, nil
}

type qualificationSnapshotInspector struct {
	env        NativeQualificationEnvironment
	sealReader nativeQualificationSnapshotSealReader
}

func (i qualificationSnapshotInspector) SnapshotSealEvidence(ctx context.Context, snapshotID int64) (ducklake.PostgresSnapshotSealEvidence, error) {
	return i.sealReader.SnapshotSealEvidence(ctx, snapshotID)
}

func (i qualificationSnapshotInspector) NativeSnapshotClosureEvidence(ctx context.Context, request ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	return i.env.NativeSnapshotClosureEvidence(ctx, request)
}

func (i qualificationSnapshotInspector) Close() error { return i.env.Close() }

type NativeSourceObservationReader interface {
	LoadSourceObservationCapture(context.Context, string) (ducklakepostgres.SourceObservationCapture, error)
}

// NativeMarkerQuarantineWriter is the durable DuckLake control authority used
// when the read-only physical resolver finds marker evidence that cannot be
// selected safely. Recovery must persist that evidence before returning the
// anomaly; merely logging the resolver error would allow later admission to
// treat the pool as healthy again.
type NativeMarkerQuarantineWriter interface {
	QuarantineMarker(context.Context, ducklakepostgres.MarkerQuarantineInput) (ducklakepostgres.MarkerQuarantine, error)
}

// NativePhysicalRecoveryInput binds every identity and read-only capability
// required to reconstruct one exact attempted native build. No recovery path
// may infer an attempt, snapshot, source observation, or catalog environment.
type NativePhysicalRecoveryInput struct {
	Attempt       deploymentnative.DeliveryBuildAttempt
	Marker        catalogartifact.CommitMarker
	Request       analyticsmaterialization.Request
	CatalogID     string
	ObjectRoot    string
	Compatibility ducklakepostgres.RuntimeCompatibility

	MarkerResolverFactory NativePhysicalMarkerResolverFactory
	MarkerQuarantine      NativeMarkerQuarantineWriter
	ObservationReader     NativeSourceObservationReader
	SnapshotFactory       NativePhysicalSnapshotInspectorFactory
}

// RecoverNativePhysicalBuild reconstructs value-only build evidence from a
// marker-qualified committed snapshot. It never materializes, opens authored
// sources, or invokes analytical qualification gates.
func RecoverNativePhysicalBuild(ctx context.Context, input NativePhysicalRecoveryInput) (result NativePhysicalBuildEvidence, resultErr error) {
	ctx = contextOrBackground(ctx)
	normalized, canonicalMarker, canonicalRoot, err := validateNativePhysicalRecoveryInput(input)
	if err != nil {
		return NativePhysicalBuildEvidence{}, err
	}
	if nativeBuildAuthorityNil(input.MarkerResolverFactory) || nativeBuildAuthorityNil(input.MarkerQuarantine) || nativeBuildAuthorityNil(input.ObservationReader) || nativeBuildAuthorityNil(input.SnapshotFactory) {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: recovery authorities are required", deploymentnative.ErrInvalid)
	}

	resolver, err := input.MarkerResolverFactory.OpenReadOnly(ctx)
	if err != nil {
		if !nativeBuildAuthorityNil(resolver) {
			err = errors.Join(err, resolver.Close())
		}
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, err)
	}
	if nativeBuildAuthorityNil(resolver) {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: marker resolver factory returned nil resolver", deploymentnative.ErrInvalid)
	}
	resolution, resolveErr := resolver.ResolveCommittedMarker(ctx, normalized.Marker)
	closeErr := resolver.Close()
	if resolution.Anomaly != "" {
		if resolution.Found || resolution.SnapshotID != 0 {
			contradiction := fmt.Errorf("%w: marker resolver returned anomaly with a positive snapshot", deploymentnative.ErrConflict)
			if closeErr != nil {
				contradiction = errors.Join(contradiction, fmt.Errorf("close marker resolver: %w", closeErr))
			}
			return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, contradiction, resolveErr)
		}
		quarantineErr := persistNativeMarkerQuarantine(ctx, input, resolution)
		if closeErr != nil {
			quarantineErr = errors.Join(quarantineErr, fmt.Errorf("close marker resolver: %w", closeErr))
		}
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, ducklakepostgres.ErrMarkerQuarantined, resolveErr, quarantineErr)
	}
	if resolveErr != nil {
		if closeErr != nil {
			resolveErr = errors.Join(resolveErr, closeErr)
		}
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, resolveErr)
	}
	if closeErr != nil {
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, fmt.Errorf("close marker resolver: %w", closeErr))
	}
	if !resolution.Found && resolution.SnapshotID != 0 || resolution.Found && resolution.SnapshotID <= 0 {
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, fmt.Errorf("%w: marker resolver returned contradictory resolution", deploymentnative.ErrConflict))
	}
	if !resolution.Found {
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, ErrNativePhysicalMarkerAbsent)
	}

	capture, err := input.ObservationReader.LoadSourceObservationCapture(ctx, normalized.Attempt.AttemptID)
	if err != nil {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: load source observation capture: %w", ErrNativePhysicalRecoveryUnresolved, err)
	}
	observations, err := validateRecoveredSourceObservationCapture(capture, normalized.Attempt.AttemptID, canonicalMarker)
	if err != nil {
		return NativePhysicalBuildEvidence{}, err
	}

	openRequest := NativeQualificationOpenRequest{
		PhysicalPoolID:    normalized.Marker.PhysicalPoolID,
		CatalogID:         normalized.CatalogID,
		SnapshotID:        resolution.SnapshotID,
		ObjectRoot:        canonicalRoot,
		RelationNamespace: normalized.Attempt.Namespace,
		CommitMarker:      normalized.Marker,
		Compatibility:     input.Compatibility,
	}
	inspector, err := input.SnapshotFactory.Open(ctx, openRequest)
	if err != nil {
		if !nativeBuildAuthorityNil(inspector) {
			err = errors.Join(err, inspector.Close())
		}
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, err)
	}
	if nativeBuildAuthorityNil(inspector) {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: snapshot inspector factory returned nil inspector", deploymentnative.ErrInvalid)
	}
	defer func() {
		if closeErr := inspector.Close(); closeErr != nil {
			result = NativePhysicalBuildEvidence{}
			resultErr = errors.Join(resultErr, ErrNativePhysicalRecoveryUnresolved, fmt.Errorf("close read-only snapshot inspector: %w", closeErr))
		}
	}()

	seal, err := inspector.SnapshotSealEvidence(ctx, resolution.SnapshotID)
	if err != nil {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: read snapshot seal evidence: %w", ErrNativePhysicalRecoveryUnresolved, err)
	}
	closure, err := inspector.NativeSnapshotClosureEvidence(ctx, ducklake.NativeSnapshotClosureRequest{
		CatalogID: normalized.CatalogID, SnapshotID: resolution.SnapshotID,
		ObjectRoot: canonicalRoot, RelationNamespace: normalized.Attempt.Namespace,
	})
	if err != nil {
		return NativePhysicalBuildEvidence{}, fmt.Errorf("%w: read snapshot closure evidence: %w", ErrNativePhysicalRecoveryUnresolved, err)
	}
	if err := verifyNativePhysicalEvidence(seal, closure, normalized, canonicalMarker, canonicalRoot, resolution.SnapshotID); err != nil {
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, fmt.Errorf("verify recovered physical evidence: %w", err))
	}
	if err := validateRuntimeAndCatalogVersions(seal.CatalogVersion, seal.ExtensionVersion, input.Compatibility); err != nil {
		return NativePhysicalBuildEvidence{}, errors.Join(ErrNativePhysicalRecoveryUnresolved, fmt.Errorf("verify recovered runtime compatibility: %w", err))
	}
	return NativePhysicalBuildEvidence{
		AttemptID: normalized.Attempt.AttemptID, CatalogID: normalized.CatalogID,
		ObjectRoot: canonicalRoot, SnapshotID: resolution.SnapshotID,
		Marker: normalized.Marker, CanonicalMarkerJSON: append(json.RawMessage(nil), canonicalMarker...),
		Seal: cloneSealEvidence(seal), Closure: cloneClosureEvidence(closure),
		SourceObservations: cloneSourceObservations(observations),
	}, nil
}

type nativeMarkerQuarantineEvidence struct {
	SchemaVersion         int      `json:"schema_version"`
	Anomaly               string   `json:"anomaly"`
	PhysicalPoolID        string   `json:"physical_pool_id"`
	CatalogID             string   `json:"catalog_id"`
	AttemptID             string   `json:"attempt_id"`
	RequestDigest         string   `json:"request_digest"`
	PlanDigest            string   `json:"plan_digest"`
	ObservedMarkerDigests []string `json:"observed_marker_digests"`
	ObservedSnapshotIDs   []int64  `json:"observed_snapshot_ids"`
}

func persistNativeMarkerQuarantine(ctx context.Context, input NativePhysicalRecoveryInput, resolution ducklake.PhysicalMarkerResolution) error {
	var reason ducklakepostgres.MarkerQuarantineReason
	switch resolution.Anomaly {
	case ducklake.PhysicalMarkerAnomalyDuplicate:
		reason = ducklakepostgres.MarkerQuarantineDuplicate
	case ducklake.PhysicalMarkerAnomalyDigestMismatch:
		reason = ducklakepostgres.MarkerQuarantineDigestMismatch
	case ducklake.PhysicalMarkerAnomalyIdentityMismatch:
		reason = ducklakepostgres.MarkerQuarantineIdentityMismatch
	default:
		return fmt.Errorf("%w: unknown physical marker anomaly %q", deploymentnative.ErrConflict, resolution.Anomaly)
	}
	digests := make([]string, 0, len(resolution.ObservedMarkerDigests))
	for _, digest := range resolution.ObservedMarkerDigests {
		if digest != "" {
			digests = append(digests, digest)
		}
	}
	snapshotIDs := make([]int64, 0, len(resolution.ObservedSnapshotIDs))
	for _, snapshotID := range resolution.ObservedSnapshotIDs {
		if snapshotID > 0 {
			snapshotIDs = append(snapshotIDs, snapshotID)
		}
	}
	if len(digests) == 0 || len(snapshotIDs) == 0 {
		return fmt.Errorf("%w: physical marker anomaly evidence is incomplete", deploymentnative.ErrConflict)
	}
	evidence, err := json.Marshal(nativeMarkerQuarantineEvidence{
		SchemaVersion: 1, Anomaly: string(resolution.Anomaly),
		PhysicalPoolID: input.Attempt.PhysicalPoolID, CatalogID: input.CatalogID,
		AttemptID: input.Attempt.AttemptID, RequestDigest: input.Attempt.RequestDigest, PlanDigest: input.Attempt.PlanDigest,
		ObservedMarkerDigests: digests, ObservedSnapshotIDs: snapshotIDs,
	})
	if err != nil {
		return err
	}
	_, err = input.MarkerQuarantine.QuarantineMarker(ctx, ducklakepostgres.MarkerQuarantineInput{
		PhysicalPoolID: input.Attempt.PhysicalPoolID, CatalogID: input.CatalogID,
		AttemptID: input.Attempt.AttemptID, RequestDigest: input.Attempt.RequestDigest, PlanDigest: input.Attempt.PlanDigest,
		Reason: reason, Evidence: evidence, ObservedMarkerDigest: digests[0], ObservedSnapshotIDs: snapshotIDs,
	})
	return err
}

// RecoverNativePhysical is a concise alias for command-style callers.
func RecoverNativePhysical(ctx context.Context, input NativePhysicalRecoveryInput) (NativePhysicalBuildEvidence, error) {
	return RecoverNativePhysicalBuild(ctx, input)
}

func validateNativePhysicalRecoveryInput(input NativePhysicalRecoveryInput) (NativePhysicalBuildInput, []byte, string, error) {
	if input.Attempt.State != deploymentnative.AttemptIndeterminate {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: recovery requires an indeterminate attempt", deploymentnative.ErrConflict)
	}
	if input.Attempt.LeaseExpiresAt.IsZero() {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: recovery attempt lease timestamp is missing", deploymentnative.ErrInvalid)
	}
	if err := validateNativeBuildContractRuntimeCompatibility(input.Compatibility); err != nil {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: recovery runtime compatibility: %v", deploymentnative.ErrInvalid, err)
	}
	buildInput := NativePhysicalBuildInput{
		Attempt: input.Attempt, Marker: input.Marker, Request: input.Request,
		CatalogID: input.CatalogID, ObjectRoot: input.ObjectRoot,
	}
	normalized, markerJSON, root, err := validateNativePhysicalBuildInputWithPolicy(buildInput, time.Now().UTC(), deploymentnative.AttemptIndeterminate, true)
	if err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	normalized.Attempt = input.Attempt
	return normalized, markerJSON, root, nil
}

func validateRecoveredSourceObservationCapture(capture ducklakepostgres.SourceObservationCapture, attemptID string, canonicalMarker []byte) ([]analyticsmaterialize.SourceObservation, error) {
	if capture.AttemptID != attemptID || !bytes.Equal(capture.CommitMarker, canonicalMarker) {
		return nil, errors.Join(ErrNativePhysicalRecoveryUnresolved, fmt.Errorf("%w: source observation capture marker differs from requested marker", deploymentnative.ErrConflict))
	}
	if capture.CapturedAt.IsZero() || capture.CapturedAt.Location() != time.UTC || !capture.CapturedAt.Equal(capture.CapturedAt.UTC()) || capture.CreatedAt.IsZero() || capture.CreatedAt.Location() != time.UTC || !capture.CreatedAt.Equal(capture.CreatedAt.UTC()) {
		return nil, fmt.Errorf("%w: source observation capture timestamp is invalid", ErrNativePhysicalRecoveryUnresolved)
	}
	observations, err := capture.Observations()
	if err != nil {
		return nil, fmt.Errorf("%w: decode source observation capture: %w", ErrNativePhysicalRecoveryUnresolved, err)
	}
	canonicalEnvelope, err := ducklakepostgres.CanonicalSourceObservationEnvelope(observations)
	if err != nil || !bytes.Equal(canonicalEnvelope, capture.ObservationEnvelope) {
		return nil, fmt.Errorf("%w: source observation capture envelope is not canonical", ErrNativePhysicalRecoveryUnresolved)
	}
	digest := sha256.Sum256(canonicalEnvelope)
	expectedDigest := "sha256:" + hex.EncodeToString(digest[:])
	if capture.ContentDigest != expectedDigest || platformdigest.ValidateSHA256Identity(capture.ContentDigest) != nil {
		return nil, fmt.Errorf("%w: source observation capture digest is invalid", ErrNativePhysicalRecoveryUnresolved)
	}
	return cloneSourceObservations(observations), nil
}

// NativePhysicalMarkerResolver is the application recovery boundary for
// exact DuckLake build markers. It intentionally does not embed the physical
// build environment or materializer: recovery may inspect a marker but must
// never rerun Materialize.
type NativePhysicalMarkerResolver interface {
	ResolveCommittedMarker(context.Context, catalogartifact.CommitMarker) (ducklake.PhysicalMarkerResolution, error)
	Close() error
}

// NativePhysicalMarkerResolverFactory opens read-only physical recovery
// environments. Implementations must ensure every resolver call gets a fresh
// DuckLake session rather than reusing connection-local commit state.
type NativePhysicalMarkerResolverFactory interface {
	OpenReadOnly(context.Context) (NativePhysicalMarkerResolver, error)
}

// NativePhysicalMarkerResolverFactoryFunc adapts a constructor for tests and
// embedders without exposing a DuckDB or PostgreSQL handle.
type NativePhysicalMarkerResolverFactoryFunc func(context.Context) (NativePhysicalMarkerResolver, error)

var _ NativePhysicalMarkerResolverFactory = NativePhysicalMarkerResolverFactoryFunc(nil)

func (f NativePhysicalMarkerResolverFactoryFunc) OpenReadOnly(ctx context.Context) (NativePhysicalMarkerResolver, error) {
	if f == nil {
		return nil, errors.New("native physical marker resolver factory is not configured")
	}
	return f(ctx)
}

// DuckLakePhysicalMarkerResolverFactory is the production app adapter. It
// keeps DuckLake's config and credential bootstrap at composition while
// exporting only the recovery resolver capability to orchestration.
// ResolverFactory is optional and exists for package-level composition tests;
// production callers should provide Config and use the DuckLake adapter.
type DuckLakePhysicalMarkerResolverFactory struct {
	Config          ducklake.Config
	ResolverFactory ducklake.PhysicalMarkerResolverFactory
}

var _ NativePhysicalMarkerResolverFactory = DuckLakePhysicalMarkerResolverFactory{}

// OpenReadOnly creates one application-owned read-only resolver. Config is
// copied by the DuckLake factory, which forces marker-reconciliation attach
// mode and removes any caller commit marker or catalog-file path.
func (f DuckLakePhysicalMarkerResolverFactory) OpenReadOnly(ctx context.Context) (NativePhysicalMarkerResolver, error) {
	resolverFactory := f.ResolverFactory
	if nativeBuildAuthorityNil(resolverFactory) {
		resolverFactory = ducklake.DuckLakePhysicalMarkerResolverFactory{Config: f.Config}
	}
	resolver, err := resolverFactory.OpenReadOnly(ctx)
	if err != nil {
		if !nativeBuildAuthorityNil(resolver) {
			return nil, errors.Join(err, resolver.Close())
		}
		return nil, err
	}
	if nativeBuildAuthorityNil(resolver) {
		return nil, fmt.Errorf("native physical marker resolver factory returned nil resolver")
	}
	return &duckLakeNativePhysicalMarkerResolver{resolver: resolver}, nil
}

type duckLakeNativePhysicalMarkerResolver struct {
	resolver ducklake.PhysicalMarkerResolver
}

var _ NativePhysicalMarkerResolver = (*duckLakeNativePhysicalMarkerResolver)(nil)

func (r *duckLakeNativePhysicalMarkerResolver) ResolveCommittedMarker(ctx context.Context, marker catalogartifact.CommitMarker) (ducklake.PhysicalMarkerResolution, error) {
	if r == nil || r.resolver == nil {
		return ducklake.PhysicalMarkerResolution{}, errors.New("native physical marker resolver is not initialized")
	}
	return r.resolver.ResolveCommittedMarker(ctx, marker)
}

func (r *duckLakeNativePhysicalMarkerResolver) Close() error {
	if r == nil || r.resolver == nil {
		return nil
	}
	return r.resolver.Close()
}
