package deploymentpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

func requireNativePhysicalFailure(t *testing.T, err error, phase NativePhysicalBuildPhase, classification NativePhysicalFailureClassification) *NativePhysicalBuildError {
	t.Helper()
	if err == nil {
		t.Fatal("expected native physical build failure")
	}
	failure, ok := NativePhysicalBuildFailureOf(err)
	if !ok {
		t.Fatalf("error %v does not expose NativePhysicalBuildError via errors.As", err)
	}
	if failure.Phase != phase || failure.Classification != classification {
		t.Fatalf("failure = %#v, want phase=%q classification=%q", failure, phase, classification)
	}
	return failure
}

func TestBuildNativePhysicalClassificationBeforeOpenIsDeterministic(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	in.Request.Models = nil
	opens := 0
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		opens++
		return nil, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseValidation, NativePhysicalFailureDeterministic)
	if !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("validation error = %v, want ErrInvalid", err)
	}
	if !NativePhysicalBuildFailureIsDeterministic(err) || NativePhysicalBuildFailureIsIndeterminate(err) {
		t.Fatalf("validation classification helpers disagree for %v", err)
	}
	if opens != 0 {
		t.Fatalf("factory opened %d times after invalid input", opens)
	}

	_, err = BuildNativePhysical(t.Context(), nativePhysicalFixtureInput(t), nil)
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseValidation, NativePhysicalFailureDeterministic)
	if !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("nil factory error = %v, want ErrInvalid", err)
	}

	var nilFunction NativePhysicalBuildEnvironmentFactoryFunc
	_, err = BuildNativePhysical(t.Context(), nativePhysicalFixtureInput(t), nilFunction)
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseValidation, NativePhysicalFailureDeterministic)
	if !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("nil function factory error = %v, want ErrInvalid", err)
	}
}

func TestBuildNativePhysicalOpenFailureIsIndeterminateAndUnwraps(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	want := errors.New("open failed")
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return nil, want
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseOpen, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, want) {
		t.Fatalf("open error = %v, want wrapped sentinel %v", err, want)
	}
}

func TestBuildNativePhysicalNilEnvironmentAfterOpenIsIndeterminate(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return nil, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseOpen, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("nil environment error = %v, want ErrInvalid", err)
	}
}

func TestBuildNativePhysicalMaterializeFailureIsIndeterminateAndCloses(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	want := errors.New("materialize failed")
	env := nativePhysicalEnvironment(t, in)
	env.materialize = func(context.Context, analyticsmaterialization.Request) (int64, error) { return 0, want }
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseMaterialize, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, want) {
		t.Fatalf("materialize error = %v, want wrapped sentinel %v", err, want)
	}
	if env.closes != 1 {
		t.Fatalf("close count = %d, want 1", env.closes)
	}
}

type nativePhysicalSealFailureEnvironment struct {
	*nativePhysicalEnvironmentFake
	sealErr    error
	closureErr error
}

func (e *nativePhysicalSealFailureEnvironment) SnapshotSealEvidence(context.Context, int64) (ducklake.PostgresSnapshotSealEvidence, error) {
	if e.sealErr != nil {
		return ducklake.PostgresSnapshotSealEvidence{}, e.sealErr
	}
	return e.nativePhysicalEnvironmentFake.seal, nil
}

func (e *nativePhysicalSealFailureEnvironment) NativeSnapshotClosureEvidence(context.Context, ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	if e.closureErr != nil {
		return ducklake.NativeSnapshotClosureEvidence{}, e.closureErr
	}
	return e.nativePhysicalEnvironmentFake.closure, nil
}

func TestBuildNativePhysicalEvidenceFailureIsIndeterminateAndUnwraps(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	want := errors.New("seal evidence failed")
	env := &nativePhysicalSealFailureEnvironment{nativePhysicalEnvironmentFake: nativePhysicalEnvironment(t, in), sealErr: want}
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseEvidence, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, want) {
		t.Fatalf("evidence error = %v, want wrapped sentinel %v", err, want)
	}
	if env.closes != 1 {
		t.Fatalf("close count = %d, want 1", env.closes)
	}
}

func TestBuildNativePhysicalRejectsUnboundedObservationColumnText(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	env := nativePhysicalEnvironment(t, in)
	env.observations = []analyticsmaterialize.SourceObservation{{
		ID:     "orders",
		Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Default: strings.Repeat("x", maxNativeObservationRevisionBytes+1)}},
	}}
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseEvidence, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("unbounded observation column error = %v, want ErrInvalid", err)
	}
}

func TestBuildNativePhysicalRejectsNonUTCObservationTimestamp(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	env := nativePhysicalEnvironment(t, in)
	env.observations = []analyticsmaterialize.SourceObservation{{
		ID:               "orders",
		RevisionObserved: time.Date(2026, time.August, 30, 12, 0, 0, 0, time.FixedZone("offset", 3600)),
	}}
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseEvidence, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("non-UTC observation timestamp error = %v, want ErrInvalid", err)
	}
}

func TestBuildNativePhysicalCloseFailureIsIndeterminateAndZeroesEvidence(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	want := errors.New("close failed")
	env := nativePhysicalEnvironment(t, in)
	env.closeErr = want
	evidence, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseClose, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, want) {
		t.Fatalf("close error = %v, want wrapped sentinel %v", err, want)
	}
	if evidence.AttemptID != "" || evidence.CatalogID != "" || evidence.ObjectRoot != "" || evidence.SnapshotID != 0 || evidence.Marker != (catalogartifact.CommitMarker{}) || len(evidence.CanonicalMarkerJSON) != 0 || len(evidence.SourceObservations) != 0 {
		t.Fatalf("evidence after close failure = %#v, want zero value", evidence)
	}
}

func TestBuildNativePhysicalOpenAndCloseFailuresPreserveBothWrappedErrors(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	openErr := errors.New("open failed")
	closeErr := errors.New("close failed")
	env := nativePhysicalEnvironment(t, in)
	env.closeErr = closeErr
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, openErr
	}))
	requireNativePhysicalFailure(t, err, NativePhysicalBuildPhaseOpen, NativePhysicalFailureIndeterminate)
	if !errors.Is(err, openErr) || !errors.Is(err, closeErr) {
		t.Fatalf("joined open/close error = %v, want both sentinels", err)
	}
	var failures []NativePhysicalBuildPhase
	var walk func(error)
	walk = func(current error) {
		if current == nil {
			return
		}
		if failure, ok := current.(*NativePhysicalBuildError); ok {
			failures = append(failures, failure.Phase)
			walk(failure.Unwrap())
			return
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range unwrapped.Unwrap() {
				walk(child)
			}
		case interface{ Unwrap() error }:
			walk(unwrapped.Unwrap())
		}
	}
	walk(err)
	if len(failures) != 2 || failures[0] != NativePhysicalBuildPhaseOpen || failures[1] != NativePhysicalBuildPhaseClose {
		t.Fatalf("joined error phases = %v, want [open close] classifications", failures)
	}
}
