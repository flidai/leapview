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
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// NativePhysicalBuildEnvironment is the complete capability used by one
// physical build. Implementations must bind all methods to the same
// PostgreSQL-backed DuckLake writer and must not expose a catalog file.
type NativePhysicalBuildEnvironment interface {
	analyticsmaterialization.Executor
	CatalogID() string
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

// NativePhysicalBuildPhase identifies the boundary at which a native physical
// build failed. The phases are intentionally coarse: callers use them to
// decide whether an attempt can be safely failed or needs indeterminate
// recovery, not to expose implementation details of the writer.
type NativePhysicalBuildPhase string

const (
	NativePhysicalBuildPhaseValidation  NativePhysicalBuildPhase = "validation"
	NativePhysicalBuildPhaseOpen        NativePhysicalBuildPhase = "open"
	NativePhysicalBuildPhaseMaterialize NativePhysicalBuildPhase = "materialize"
	NativePhysicalBuildPhaseEvidence    NativePhysicalBuildPhase = "evidence"
	NativePhysicalBuildPhaseClose       NativePhysicalBuildPhase = "close"
)

// NativePhysicalFailureClassification describes whether a failed build has a
// positive no-commit conclusion. Once the environment Open boundary has been
// crossed, failures are indeterminate because this boundary has no proof that
// an external writer did not commit.
type NativePhysicalFailureClassification string

const (
	NativePhysicalFailureDeterministic NativePhysicalFailureClassification = "deterministic_no_commit"
	NativePhysicalFailureIndeterminate NativePhysicalFailureClassification = "indeterminate"
)

// NativePhysicalBuildError annotates a build failure with its lifecycle phase
// and commit outcome. It wraps Err so errors.Is and errors.As continue to find
// the original sentinel or typed operation error.
type NativePhysicalBuildError struct {
	Phase          NativePhysicalBuildPhase
	Classification NativePhysicalFailureClassification
	Err            error
}

func (e *NativePhysicalBuildError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("native physical build %s failure (%s)", e.Phase, e.Classification)
	}
	return fmt.Sprintf("native physical build %s failure (%s): %v", e.Phase, e.Classification, e.Err)
}

func (e *NativePhysicalBuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NativePhysicalBuildFailureOf returns the first lifecycle classification in
// err, following the same wrapping tree as errors.As. It is useful to
// coordinators that need both the phase and commit outcome without repeating
// the type assertion boilerplate.
func NativePhysicalBuildFailureOf(err error) (*NativePhysicalBuildError, bool) {
	var failure *NativePhysicalBuildError
	if !errors.As(err, &failure) {
		return nil, false
	}
	return failure, true
}

// NativePhysicalBuildFailureIsIndeterminate reports whether err carries an
// indeterminate native physical build classification.
func NativePhysicalBuildFailureIsIndeterminate(err error) bool {
	failure, ok := NativePhysicalBuildFailureOf(err)
	return ok && failure.Classification == NativePhysicalFailureIndeterminate
}

// NativePhysicalBuildFailureIsDeterministic reports whether err carries a
// deterministic no-commit native physical build classification.
func NativePhysicalBuildFailureIsDeterministic(err error) bool {
	failure, ok := NativePhysicalBuildFailureOf(err)
	return ok && failure.Classification == NativePhysicalFailureDeterministic
}

func nativePhysicalBuildFailure(phase NativePhysicalBuildPhase, classification NativePhysicalFailureClassification, err error) error {
	if err == nil {
		return nil
	}
	return &NativePhysicalBuildError{Phase: phase, Classification: classification, Err: err}
}

func nativePhysicalBuildDeterministicFailure(phase NativePhysicalBuildPhase, err error) error {
	return nativePhysicalBuildFailure(phase, NativePhysicalFailureDeterministic, err)
}

func nativePhysicalBuildIndeterminateFailure(phase NativePhysicalBuildPhase, err error) error {
	return nativePhysicalBuildFailure(phase, NativePhysicalFailureIndeterminate, err)
}

// DuckLakePhysicalBuildEnvironmentFactory is the production adapter shape for
// the boundary. Config supplies the process-owned PostgreSQL catalog/pool
// admission and MaterializerFactory supplies the analytics module policy. It
// does not participate in application composition; callers can inject the
// function factory above in tests.
type DuckLakePhysicalBuildEnvironmentFactory struct {
	Config              ducklake.Config
	CatalogID           string
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
	if err := validateTextField(f.CatalogID, "DuckLake catalog id", ducklake.MaxCommitMarkerFieldBytes); err != nil {
		return nil, err
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
	return &duckLakePhysicalBuildEnvironment{environment: environment, materializer: materializer, catalogID: f.CatalogID}, nil
}

type duckLakePhysicalBuildEnvironment struct {
	environment  *ducklake.Environment
	materializer analyticsmaterialization.Executor
	catalogID    string
}

var _ NativePhysicalBuildEnvironment = (*duckLakePhysicalBuildEnvironment)(nil)

func (e *duckLakePhysicalBuildEnvironment) CatalogID() string {
	if e == nil {
		return ""
	}
	return e.catalogID
}

func (e *duckLakePhysicalBuildEnvironment) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
	if e == nil || e.environment == nil || e.materializer == nil {
		return 0, fmt.Errorf("%w: native physical build environment is not initialized", deploymentnative.ErrInvalid)
	}
	return e.materializer.Materialize(ctx, request)
}

// MaterializeWithObservations preserves the single-call correlation when the
// underlying materializer supports the optional atomic observation extension.
// A getter-only provider is deliberately not consulted: a separate read could
// observe a concurrent run and falsely bind its source evidence to this
// snapshot. Materializers without the atomic extension remain executable but
// return no source observations, causing any evidence-dependent qualification
// gate to fail closed.
func (e *duckLakePhysicalBuildEnvironment) MaterializeWithObservations(ctx context.Context, request analyticsmaterialization.Request) (int64, []analyticsmaterialize.SourceObservation, error) {
	if e == nil || e.environment == nil || e.materializer == nil {
		return 0, nil, fmt.Errorf("%w: native physical build environment is not initialized", deploymentnative.ErrInvalid)
	}
	if executor, ok := e.materializer.(analyticsmaterialization.ObservationExecutor); ok {
		snapshotID, observations, err := executor.MaterializeWithObservations(ctx, request)
		if err != nil {
			return 0, nil, err
		}
		return snapshotID, cloneSourceObservations(observations), nil
	}
	snapshotID, err := e.materializer.Materialize(ctx, request)
	if err != nil {
		return 0, nil, err
	}
	return snapshotID, nil, nil
}

// MaterializeWithObservationWriter preserves the exact source-session and
// DuckLake transaction ordering required by native physical builds. There is
// intentionally no compatibility fallback: a configured writer requires the
// pre-commit capture extension.
func (e *duckLakePhysicalBuildEnvironment) MaterializeWithObservationWriter(ctx context.Context, request analyticsmaterialization.Request, writer analyticsmaterialization.ObservationWriter) (int64, []analyticsmaterialize.SourceObservation, error) {
	if e == nil || e.environment == nil || e.materializer == nil {
		return 0, nil, fmt.Errorf("%w: native physical build environment is not initialized", deploymentnative.ErrInvalid)
	}
	executor, ok := e.materializer.(analyticsmaterialization.ObservationWriterExecutor)
	if !ok {
		return 0, nil, fmt.Errorf("%w: native physical build materializer does not support pre-commit source observation capture", deploymentnative.ErrInvalid)
	}
	return executor.MaterializeWithObservationWriter(ctx, request, writer)
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
	// CaptureClock supplies the authoritative UTC timestamp for observation
	// persistence. Lower-level adapters may leave it nil to use wall time.
	CaptureClock func() time.Time
	// ObservationWriter is invoked inside the DuckLake CommitTransaction
	// callback. It is optional for lower-level test adapters; the production
	// native coordinator supplies it and fails closed when unavailable.
	ObservationWriter ducklakepostgres.SourceObservationWriter
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
	SourceObservations  []analyticsmaterialize.SourceObservation
}

const (
	maxNativeSourceObservations              = 4096
	maxNativeObservationSchemaColumns        = 16384
	maxNativeObservationTotalColumns         = 65536
	maxNativeObservationTotalTextBytes       = 8 << 20
	maxNativeObservationIDBytes              = 512
	maxNativeObservationRevisionBytes        = 4096
	maxNativeObservationQueries              = 1 << 31
	maxNativeObservationRows           int64 = 1 << 62
	maxNativeObservationMillis         int64 = 7 * 24 * 60 * 60 * 1000
)

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
	normalized, canonicalMarker, canonicalRoot, err := validateNativePhysicalBuildInput(input)
	if err != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildDeterministicFailure(NativePhysicalBuildPhaseValidation, err)
	}
	if !nativePhysicalBuildFactoryConfigured(factory) {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildDeterministicFailure(
			NativePhysicalBuildPhaseValidation,
			fmt.Errorf("%w: native physical build environment factory is not configured", deploymentnative.ErrInvalid),
		)
	}
	if normalized.ObservationWriter != nil && nativeBuildAuthorityNil(normalized.ObservationWriter) {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildDeterministicFailure(NativePhysicalBuildPhaseValidation, fmt.Errorf("%w: source observation writer is typed nil", deploymentnative.ErrInvalid))
	}

	environment, openErr := factory.Open(ctx, normalized.Marker)
	if openErr != nil {
		// A defensive close handles factories that return a usable environment
		// together with an error while preserving the open error as primary.
		if environment != nil {
			return NativePhysicalBuildEvidence{}, errors.Join(
				nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseOpen, openErr),
				nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseClose, environment.Close()),
			)
		}
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseOpen, openErr)
	}
	if environment == nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(
			NativePhysicalBuildPhaseOpen,
			fmt.Errorf("%w: native physical build environment factory returned nil environment", deploymentnative.ErrInvalid),
		)
	}
	defer func() {
		closeErr := environment.Close()
		if closeErr != nil {
			closeFailure := nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseClose, closeErr)
			if err == nil {
				err = closeFailure
			} else {
				err = errors.Join(err, closeFailure)
			}
			evidence = NativePhysicalBuildEvidence{}
		}
	}()
	if environment.CatalogID() != normalized.CatalogID {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(
			NativePhysicalBuildPhaseOpen,
			fmt.Errorf("%w: native physical build environment catalog identity differs", deploymentnative.ErrConflict),
		)
	}

	var observations []analyticsmaterialize.SourceObservation
	var snapshotID int64
	var materializeErr error
	if normalized.ObservationWriter != nil {
		executor, ok := environment.(interface {
			MaterializeWithObservationWriter(context.Context, analyticsmaterialization.Request, analyticsmaterialization.ObservationWriter) (int64, []analyticsmaterialize.SourceObservation, error)
		})
		if !ok {
			return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(
				NativePhysicalBuildPhaseMaterialize,
				fmt.Errorf("%w: native physical build environment does not support pre-commit source observation capture", deploymentnative.ErrInvalid),
			)
		}
		captureClock := normalized.CaptureClock
		if captureClock == nil {
			captureClock = func() time.Time { return time.Now().UTC() }
		}
		// DuckLake may retry its transaction callback. Sample once so every
		// callback writes the exact same immutable attempt identity; differing
		// observations still conflict through the envelope digest.
		capturedAt := captureClock().UTC()
		var callbackEnvelope []byte
		snapshotID, observations, materializeErr = executor.MaterializeWithObservationWriter(ctx, normalized.Request, func(writerCtx context.Context, captured []analyticsmaterialize.SourceObservation) error {
			capture, captureErr := ducklakepostgres.NewSourceObservationCapture(normalized.Attempt.AttemptID, normalized.Marker, captured, capturedAt)
			if captureErr != nil {
				return captureErr
			}
			persisted, writeErr := normalized.ObservationWriter.RecordSourceObservationCapture(writerCtx, capture)
			if writeErr != nil {
				return writeErr
			}
			if persisted.AttemptID != capture.AttemptID || persisted.ContentDigest != capture.ContentDigest || !bytes.Equal(persisted.CommitMarker, capture.CommitMarker) || !bytes.Equal(persisted.ObservationEnvelope, capture.ObservationEnvelope) || !persisted.CapturedAt.Equal(capture.CapturedAt) {
				return fmt.Errorf("%w: persisted source observation capture differs from callback evidence", deploymentnative.ErrConflict)
			}
			if callbackEnvelope != nil && !bytes.Equal(callbackEnvelope, capture.ObservationEnvelope) {
				return fmt.Errorf("%w: source observations changed across materialization callback retries", deploymentnative.ErrConflict)
			}
			callbackEnvelope = append(callbackEnvelope[:0], capture.ObservationEnvelope...)
			return nil
		})
		if materializeErr == nil {
			returnedEnvelope, envelopeErr := ducklakepostgres.CanonicalSourceObservationEnvelope(observations)
			switch {
			case envelopeErr != nil:
				materializeErr = envelopeErr
			case callbackEnvelope == nil:
				materializeErr = fmt.Errorf("%w: materializer did not invoke source observation capture", deploymentnative.ErrConflict)
			case !bytes.Equal(returnedEnvelope, callbackEnvelope):
				materializeErr = fmt.Errorf("%w: materializer returned source observations different from persisted callback evidence", deploymentnative.ErrConflict)
			}
		}
	} else if executor, ok := environment.(analyticsmaterialization.ObservationExecutor); ok {
		snapshotID, observations, materializeErr = executor.MaterializeWithObservations(ctx, normalized.Request)
	} else {
		snapshotID, materializeErr = environment.Materialize(ctx, normalized.Request)
	}
	if materializeErr != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseMaterialize, materializeErr)
	}
	if snapshotID <= 0 {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(
			NativePhysicalBuildPhaseMaterialize,
			fmt.Errorf("%w: materialization returned non-positive snapshot", deploymentnative.ErrInvalid),
		)
	}
	if err := validateSourceObservations(observations); err != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, err)
	}
	observations = cloneSourceObservations(observations)

	seal, sealErr := environment.SnapshotSealEvidence(ctx, snapshotID)
	if sealErr != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, sealErr)
	}
	closure, closureErr := environment.NativeSnapshotClosureEvidence(ctx, ducklake.NativeSnapshotClosureRequest{
		CatalogID: normalized.CatalogID, SnapshotID: snapshotID, ObjectRoot: canonicalRoot,
		RelationNamespace: normalized.Attempt.Namespace,
	})
	if closureErr != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, closureErr)
	}
	if err := verifyNativePhysicalEvidence(seal, closure, normalized, canonicalMarker, canonicalRoot, snapshotID); err != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, err)
	}

	return NativePhysicalBuildEvidence{
		AttemptID: normalized.Attempt.AttemptID, CatalogID: normalized.CatalogID, ObjectRoot: canonicalRoot,
		SnapshotID: snapshotID, Marker: normalized.Marker,
		CanonicalMarkerJSON: append(json.RawMessage(nil), canonicalMarker...), Seal: cloneSealEvidence(seal), Closure: cloneClosureEvidence(closure),
		SourceObservations: observations,
	}, nil
}

func nativePhysicalBuildFactoryConfigured(factory NativePhysicalBuildEnvironmentFactory) bool {
	if factory == nil {
		return false
	}
	// A nil function adapter is an interface value with a non-nil dynamic
	// type, so account for it before crossing the factory.Open boundary.
	if function, ok := factory.(NativePhysicalBuildEnvironmentFactoryFunc); ok {
		return function != nil
	}
	return true
}

// RunNativePhysicalBuild is an explicit verb alias for callers that prefer a
// command-style name.
func RunNativePhysicalBuild(ctx context.Context, input NativePhysicalBuildInput, factory NativePhysicalBuildEnvironmentFactory) (NativePhysicalBuildEvidence, error) {
	return BuildNativePhysical(ctx, input, factory)
}

func validateNativePhysicalBuildInput(input NativePhysicalBuildInput) (NativePhysicalBuildInput, []byte, string, error) {
	return validateNativePhysicalBuildInputWithPolicy(input, time.Now().UTC(), deploymentnative.AttemptRunning, false)
}

// validateNativePhysicalBuildInputWithPolicy shares all physical-build
// identity checks with both the live writer and recovery paths. An
// indeterminate ledger state is the recovery fence, so recovery does not
// require an otherwise-live lease, but all canonical identities still match.
func validateNativePhysicalBuildInputWithPolicy(input NativePhysicalBuildInput, now time.Time, expectedState deploymentnative.BuildAttemptState, allowExpired bool) (NativePhysicalBuildInput, []byte, string, error) {
	marker, err := input.Marker.Normalize()
	if err != nil {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	canonicalMarker, err := marker.CanonicalJSON()
	if err != nil || len(canonicalMarker) > catalogartifact.MaxCommitMarkerBytes {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: canonical commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	if err := validateAttemptWithPolicy(input.Attempt, now, expectedState, allowExpired); err != nil {
		return NativePhysicalBuildInput{}, nil, "", err
	}
	if marker.AttemptID != input.Attempt.AttemptID || marker.PlanDigest != input.Attempt.PlanDigest || marker.RequestDigest != input.Attempt.RequestDigest || marker.PhysicalPoolID != input.Attempt.PhysicalPoolID || input.Attempt.CatalogID != input.CatalogID || marker.LeaseEpoch != input.Attempt.FencingEpoch {
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
	if request.RelationNamespace != "" && request.RelationNamespace != input.Attempt.Namespace {
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization relation namespace differs from build attempt", deploymentnative.ErrConflict)
	}
	// The attempt namespace is the authority-derived value. Copy it into the
	// value-only materialization request so an omitted field cannot fall back to
	// the shared model schema, while a prepopulated conflicting value is rejected
	// above before any environment is opened.
	request.RelationNamespace = input.Attempt.Namespace
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
		return NativePhysicalBuildInput{}, nil, "", fmt.Errorf("%w: materialization requires non-empty Models, materialized relations, and tables", deploymentnative.ErrInvalid)
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
		if err := validateTextField(name, "materialization Model id", 255); err != nil {
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
	return validateAttemptWithPolicy(attempt, time.Now().UTC(), deploymentnative.AttemptRunning, false)
}

func validateAttemptWithPolicy(attempt deploymentnative.DeliveryBuildAttempt, now time.Time, expectedState deploymentnative.BuildAttemptState, allowExpired bool) error {
	for label, value := range map[string]string{"attempt id": attempt.AttemptID, "plan id": attempt.PlanID, "owner id": attempt.OwnerID, "physical pool id": attempt.PhysicalPoolID, "catalog id": attempt.CatalogID} {
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
	if expectedState != deploymentnative.AttemptRunning && expectedState != deploymentnative.AttemptIndeterminate {
		return fmt.Errorf("%w: expected build attempt state is invalid", deploymentnative.ErrInvalid)
	}
	if attempt.State != expectedState {
		return fmt.Errorf("%w: build attempt must be %s", deploymentnative.ErrConflict, expectedState)
	}
	expectedNamespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{
		CandidateID: attempt.CandidateID, AttemptID: attempt.AttemptID, FencingEpoch: attempt.FencingEpoch,
	})
	if err != nil {
		return fmt.Errorf("%w: derive relation namespace: %v", deploymentnative.ErrInvalid, err)
	}
	if attempt.Namespace != expectedNamespace {
		return fmt.Errorf("%w: build attempt relation namespace differs from canonical identity", deploymentnative.ErrConflict)
	}
	if attempt.LeaseExpiresAt.IsZero() || !allowExpired && !attempt.LeaseExpiresAt.After(now) {
		return fmt.Errorf("%w: build attempt lease is expired", deploymentnative.ErrConflict)
	}
	for label, value := range map[string]string{"request digest": attempt.RequestDigest, "plan digest": attempt.PlanDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: %s: %v", deploymentnative.ErrInvalid, label, err)
		}
	}
	return nil
}

func validateSourceObservations(observations []analyticsmaterialize.SourceObservation) error {
	if len(observations) > maxNativeSourceObservations {
		return fmt.Errorf("%w: source observations exceed maximum count", deploymentnative.ErrInvalid)
	}
	seen := make(map[string]struct{}, len(observations))
	totalColumns := 0
	totalTextBytes := 0
	addText := func(value string) error {
		if len(value) > maxNativeObservationTotalTextBytes-totalTextBytes {
			return fmt.Errorf("%w: source observation text exceeds aggregate bound", deploymentnative.ErrInvalid)
		}
		totalTextBytes += len(value)
		return nil
	}
	for _, observation := range observations {
		if err := validateTextField(observation.ID, "source observation id", maxNativeObservationIDBytes); err != nil {
			return err
		}
		if err := addText(observation.ID); err != nil {
			return err
		}
		if _, duplicate := seen[observation.ID]; duplicate {
			return fmt.Errorf("%w: duplicate source observation id %q", deploymentnative.ErrConflict, observation.ID)
		}
		seen[observation.ID] = struct{}{}
		if observation.Revision != "" {
			if err := validateTextField(observation.Revision, "source observation revision", maxNativeObservationRevisionBytes); err != nil {
				return err
			}
			if err := addText(observation.Revision); err != nil {
				return err
			}
		}
		if observation.ObservationQueries < 0 || observation.ObservationQueries > maxNativeObservationQueries || observation.ObservationRows < 0 || observation.ObservationRows > maxNativeObservationRows || observation.ObservationMillis < 0 || observation.ObservationMillis > maxNativeObservationMillis {
			return fmt.Errorf("%w: source observation counters are outside bounds", deploymentnative.ErrInvalid)
		}
		if len(observation.Schema) > maxNativeObservationSchemaColumns {
			return fmt.Errorf("%w: source observation schema exceeds maximum columns", deploymentnative.ErrInvalid)
		}
		if len(observation.Schema) > maxNativeObservationTotalColumns-totalColumns {
			return fmt.Errorf("%w: source observation schemas exceed aggregate column bound", deploymentnative.ErrInvalid)
		}
		totalColumns += len(observation.Schema)
		for _, column := range observation.Schema {
			if strings.TrimSpace(column.Name) == "" {
				return fmt.Errorf("%w: source observation column name is blank", deploymentnative.ErrInvalid)
			}
			if err := validateBoundedObservationText(column.Name, "source observation column name", maxNativeObservationRevisionBytes); err != nil {
				return err
			}
			if strings.TrimSpace(column.PhysicalType) == "" {
				return fmt.Errorf("%w: source observation column physical type is blank", deploymentnative.ErrInvalid)
			}
			if err := validateBoundedObservationText(column.PhysicalType, "source observation column physical type", maxNativeObservationRevisionBytes); err != nil {
				return err
			}
			if err := addText(column.Name); err != nil {
				return err
			}
			if err := addText(column.PhysicalType); err != nil {
				return err
			}
			if column.Default != "" {
				if err := validateBoundedObservationText(column.Default, "source observation column default", maxNativeObservationRevisionBytes); err != nil {
					return err
				}
				if err := addText(column.Default); err != nil {
					return err
				}
			}
			if column.Comment != "" {
				if err := validateBoundedObservationText(column.Comment, "source observation column comment", maxNativeObservationRevisionBytes); err != nil {
					return err
				}
				if err := addText(column.Comment); err != nil {
					return err
				}
			}
			if column.Ordinal < 0 {
				return fmt.Errorf("%w: source observation column ordinal cannot be negative", deploymentnative.ErrInvalid)
			}
		}
		for label, observedAt := range map[string]time.Time{"revision observed": observation.RevisionObserved, "freshness observed": observation.FreshnessObserved} {
			if !observedAt.IsZero() && (observedAt.Location() != time.UTC || !observedAt.Equal(observedAt.UTC())) {
				return fmt.Errorf("%w: source observation %s timestamp must be UTC", deploymentnative.ErrInvalid, label)
			}
		}
		if err := validateObservationFailure(observation.SchemaFailure, "schema"); err != nil {
			return err
		}
		if err := validateObservationFailure(observation.FreshnessFailure, "freshness"); err != nil {
			return err
		}
	}
	return nil
}

func validateBoundedObservationText(value, label string, max int) error {
	if len(value) > max || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s is invalid", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func validateObservationFailure(value analyticsmaterialize.ObservationFailure, label string) error {
	if value == "" || value == analyticsmaterialize.ObservationUnavailable || value == analyticsmaterialize.ObservationTimeout || value == analyticsmaterialize.ObservationBounds {
		return nil
	}
	return fmt.Errorf("%w: source observation %s failure is unknown", deploymentnative.ErrInvalid, label)
}

func verifyNativePhysicalEvidence(seal ducklake.PostgresSnapshotSealEvidence, closure ducklake.NativeSnapshotClosureEvidence, input NativePhysicalBuildInput, canonicalMarker []byte, canonicalRoot string, snapshotID int64) error {
	expectedNamespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{
		CandidateID: input.Attempt.CandidateID, AttemptID: input.Attempt.AttemptID, FencingEpoch: input.Attempt.FencingEpoch,
	})
	if err != nil {
		return fmt.Errorf("%w: derive relation namespace: %v", deploymentnative.ErrInvalid, err)
	}
	if input.Attempt.Namespace != expectedNamespace {
		return fmt.Errorf("%w: build attempt relation namespace differs from canonical identity", deploymentnative.ErrConflict)
	}
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
		limit := ducklake.MaxCommitMarkerFieldBytes
		if label == "commit marker" {
			limit = ducklake.MaxCommitMarkerBytes
		}
		if err := validateTextField(value, "snapshot seal "+label, limit); err != nil {
			return err
		}
	}
	if closure.CatalogID != input.CatalogID || closure.SnapshotID != snapshotID || closure.ObjectRoot != canonicalRoot || closure.RelationNamespace != input.Attempt.Namespace {
		return fmt.Errorf("%w: native snapshot closure catalog/object/snapshot evidence differs", deploymentnative.ErrConflict)
	}
	if err := ducklake.VerifyNativeSnapshotClosureEvidence(closure); err != nil {
		return fmt.Errorf("%w: native snapshot closure evidence is not canonical: %v", deploymentnative.ErrConflict, err)
	}
	for _, relation := range closure.Relations {
		if relation.Schema != input.Attempt.Namespace {
			return fmt.Errorf("%w: native snapshot closure relation %s.%s is outside candidate relation namespace %q", deploymentnative.ErrConflict, relation.Schema, relation.Table, input.Attempt.Namespace)
		}
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
		RelationNamespace string               `json:"relation_namespace"`
		Relations         []ducklake.BaseTable `json:"relations"`
	}
	type closureManifest struct {
		Objects []ducklake.NativeSnapshotObject `json:"objects"`
	}
	type envelope struct {
		SchemaVersion          int                             `json:"schema_version"`
		CatalogID              string                          `json:"catalog_id"`
		SnapshotID             int64                           `json:"snapshot_id"`
		ObjectRoot             string                          `json:"object_root"`
		RelationNamespace      string                          `json:"relation_namespace"`
		Relations              []ducklake.BaseTable            `json:"relations"`
		Objects                []ducklake.NativeSnapshotObject `json:"objects"`
		RelationManifestDigest string                          `json:"relation_manifest_digest"`
		ClosureDigest          string                          `json:"closure_digest"`
		ObjectRootDigest       string                          `json:"object_root_digest"`
	}
	relationJSON, err := json.Marshal(relationManifest{RelationNamespace: closure.RelationNamespace, Relations: closure.Relations})
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
	expected, err := json.Marshal(envelope{SchemaVersion: ducklake.NativeSnapshotClosureSchemaVersion, CatalogID: closure.CatalogID, SnapshotID: closure.SnapshotID, ObjectRoot: canonicalRoot, RelationNamespace: closure.RelationNamespace, Relations: closure.Relations, Objects: closure.Objects, RelationManifestDigest: closure.RelationManifestDigest, ClosureDigest: closure.ClosureDigest, ObjectRootDigest: closure.ObjectRootDigest})
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

func cloneSourceObservations(observations []analyticsmaterialize.SourceObservation) []analyticsmaterialize.SourceObservation {
	if observations == nil {
		return nil
	}
	result := make([]analyticsmaterialize.SourceObservation, len(observations))
	for i, observation := range observations {
		result[i] = observation
		if observation.Schema != nil {
			result[i].Schema = make([]semanticmodel.ColumnSchema, len(observation.Schema))
			for j, column := range observation.Schema {
				result[i].Schema[j] = column
				if column.Nullable != nil {
					nullable := *column.Nullable
					result[i].Schema[j].Nullable = &nullable
				}
			}
		}
	}
	return result
}
