package deploymentpostgres

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

type callbackPhysicalEnvironment struct {
	*nativePhysicalEnvironmentFake
	events               *[]string
	writerCalls          int
	returnedObservations []analyticsmaterialize.SourceObservation
}

func (e *callbackPhysicalEnvironment) MaterializeWithObservationWriter(ctx context.Context, request analyticsmaterialization.Request, writer analyticsmaterialization.ObservationWriter) (int64, []analyticsmaterialize.SourceObservation, error) {
	*e.events = append(*e.events, "materialize")
	snapshotID, observations, err := e.nativePhysicalEnvironmentFake.MaterializeWithObservations(ctx, request)
	if err != nil {
		return 0, nil, err
	}
	writerCalls := e.writerCalls
	if writerCalls <= 0 {
		writerCalls = 1
	}
	for range writerCalls {
		*e.events = append(*e.events, "observation_writer")
		if err := writer(ctx, observations); err != nil {
			return 0, nil, err
		}
	}
	*e.events = append(*e.events, "commit")
	if e.returnedObservations != nil {
		return snapshotID, e.returnedObservations, nil
	}
	return snapshotID, observations, nil
}

type callbackObservationWriter struct {
	err      error
	captures []ducklakepostgres.SourceObservationCapture
}

func (w *callbackObservationWriter) RecordSourceObservationCapture(_ context.Context, capture ducklakepostgres.SourceObservationCapture) (ducklakepostgres.SourceObservationCapture, error) {
	if w.err != nil {
		return ducklakepostgres.SourceObservationCapture{}, w.err
	}
	w.captures = append(w.captures, capture)
	return capture, nil
}

func TestBuildNativePhysicalObservationWriterRunsBeforeCommit(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	events := []string{}
	env := nativePhysicalEnvironment(t, in)
	callbackEnv := &callbackPhysicalEnvironment{nativePhysicalEnvironmentFake: env, events: &events}
	writer := &callbackObservationWriter{}
	in.ObservationWriter = writer
	in.CaptureClock = func() time.Time {
		return time.Date(2026, time.August, 31, 4, 3, 4, 5_000, time.FixedZone("CEST", 2*60*60))
	}
	if _, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return callbackEnv, nil
	})); err != nil {
		t.Fatal(err)
	}
	if got, want := events, []string{"materialize", "observation_writer", "commit"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("callback ordering=%v, want %v", got, want)
	}
	if len(writer.captures) != 1 || writer.captures[0].AttemptID != in.Attempt.AttemptID {
		t.Fatalf("captures=%#v", writer.captures)
	}
	if writer.captures[0].CapturedAt.Location() != time.UTC || !writer.captures[0].CapturedAt.Equal(time.Date(2026, time.August, 31, 2, 3, 4, 5_000, time.UTC)) {
		t.Fatalf("capture time=%v, want normalized UTC instant", writer.captures[0].CapturedAt)
	}
}

func TestBuildNativePhysicalObservationCaptureIsExactAcrossCommitCallbackRetry(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	events := []string{}
	env := nativePhysicalEnvironment(t, in)
	callbackEnv := &callbackPhysicalEnvironment{nativePhysicalEnvironmentFake: env, events: &events, writerCalls: 2}
	writer := &callbackObservationWriter{}
	in.ObservationWriter = writer
	clockCalls := 0
	in.CaptureClock = func() time.Time {
		clockCalls++
		return time.Date(2026, time.August, 31, 2, 3, 4+clockCalls, 0, time.UTC)
	}
	if _, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return callbackEnv, nil
	})); err != nil {
		t.Fatal(err)
	}
	if clockCalls != 1 {
		t.Fatalf("capture clock calls=%d, want one sample across callback retries", clockCalls)
	}
	if len(writer.captures) != 2 || !writer.captures[0].CapturedAt.Equal(writer.captures[1].CapturedAt) || writer.captures[0].ContentDigest != writer.captures[1].ContentDigest {
		t.Fatalf("retry captures differ: %#v", writer.captures)
	}
	if got, want := events, []string{"materialize", "observation_writer", "observation_writer", "commit"}; len(got) != len(want) {
		t.Fatalf("retry events=%v, want %v", got, want)
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("retry events=%v, want %v", got, want)
			}
		}
	}
}

func TestBuildNativePhysicalRejectsReturnedObservationsDifferentFromPersistedCapture(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	events := []string{}
	env := nativePhysicalEnvironment(t, in)
	returned := cloneSourceObservations(env.observations)
	returned[0].Revision = "different-after-callback"
	callbackEnv := &callbackPhysicalEnvironment{nativePhysicalEnvironmentFake: env, events: &events, returnedObservations: returned}
	in.ObservationWriter = &callbackObservationWriter{}
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return callbackEnv, nil
	}))
	if err == nil || !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("different returned observations error=%v, want conflict", err)
	}
}

func TestBuildNativePhysicalObservationWriterFailurePreventsCommit(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	events := []string{}
	env := nativePhysicalEnvironment(t, in)
	callbackEnv := &callbackPhysicalEnvironment{nativePhysicalEnvironmentFake: env, events: &events}
	in.ObservationWriter = &callbackObservationWriter{err: errors.New("observation writer failed")}
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return callbackEnv, nil
	}))
	if err == nil || !errors.Is(err, in.ObservationWriter.(*callbackObservationWriter).err) {
		t.Fatalf("writer failure=%v", err)
	}
	if len(events) != 2 || events[0] != "materialize" || events[1] != "observation_writer" {
		t.Fatalf("callback ordering after writer failure=%v", events)
	}
}
