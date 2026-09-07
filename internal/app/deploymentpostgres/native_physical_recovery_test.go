package deploymentpostgres

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

type recoveryMarkerResolver struct {
	resolution ducklake.PhysicalMarkerResolution
	err        error
	closeErr   error
	closed     int
	resolved   int
}

func (r *recoveryMarkerResolver) ResolveCommittedMarker(context.Context, catalogartifact.CommitMarker) (ducklake.PhysicalMarkerResolution, error) {
	r.resolved++
	return r.resolution, r.err
}

func (r *recoveryMarkerResolver) Close() error {
	r.closed++
	return r.closeErr
}

type recoveryMarkerResolverFactory struct {
	resolver *recoveryMarkerResolver
	err      error
	opened   int
}

func (f *recoveryMarkerResolverFactory) OpenReadOnly(context.Context) (NativePhysicalMarkerResolver, error) {
	f.opened++
	return f.resolver, f.err
}

type recoveryObservationReader struct {
	capture ducklakepostgres.SourceObservationCapture
	err     error
	reads   int
}

type recoveryMarkerQuarantineWriter struct {
	input  ducklakepostgres.MarkerQuarantineInput
	err    error
	writes int
}

func (w *recoveryMarkerQuarantineWriter) QuarantineMarker(_ context.Context, input ducklakepostgres.MarkerQuarantineInput) (ducklakepostgres.MarkerQuarantine, error) {
	w.writes++
	w.input = input
	return ducklakepostgres.MarkerQuarantine{PhysicalPoolID: input.PhysicalPoolID, CatalogID: input.CatalogID, AttemptID: input.AttemptID, Reason: input.Reason}, w.err
}

func (r *recoveryObservationReader) LoadSourceObservationCapture(context.Context, string) (ducklakepostgres.SourceObservationCapture, error) {
	r.reads++
	return r.capture, r.err
}

type recoverySnapshotInspector struct {
	seal       ducklake.PostgresSnapshotSealEvidence
	closure    ducklake.NativeSnapshotClosureEvidence
	sealErr    error
	closureErr error
	closeErr   error
	closed     int
	sealID     int64
	closureIn  ducklake.NativeSnapshotClosureRequest
}

func (i *recoverySnapshotInspector) SnapshotSealEvidence(_ context.Context, snapshotID int64) (ducklake.PostgresSnapshotSealEvidence, error) {
	i.sealID = snapshotID
	return i.seal, i.sealErr
}

func (i *recoverySnapshotInspector) NativeSnapshotClosureEvidence(_ context.Context, request ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	i.closureIn = request
	return i.closure, i.closureErr
}

func (i *recoverySnapshotInspector) Close() error {
	i.closed++
	return i.closeErr
}

type recoverySnapshotFactory struct {
	inspector *recoverySnapshotInspector
	err       error
	request   NativeQualificationOpenRequest
	opened    int
}

type recoveryQualificationWithoutSeal struct {
	env *qualificationEnvironmentFake
}

func (e *recoveryQualificationWithoutSeal) Query(ctx context.Context, plan semanticQueryPlan) (semanticRows, error) {
	return e.env.Query(ctx, plan)
}

func (e *recoveryQualificationWithoutSeal) RuntimeCompatibility(ctx context.Context) (NativeRuntimeCompatibilityEvidence, error) {
	return e.env.RuntimeCompatibility(ctx)
}

func (e *recoveryQualificationWithoutSeal) NativeSnapshotClosureEvidence(ctx context.Context, request ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	return e.env.NativeSnapshotClosureEvidence(ctx, request)
}

func (e *recoveryQualificationWithoutSeal) Close() error { return e.env.Close() }

func (f *recoverySnapshotFactory) Open(_ context.Context, request NativeQualificationOpenRequest) (NativePhysicalSnapshotInspector, error) {
	f.opened++
	f.request = request
	return f.inspector, f.err
}

func recoveryFixture(t *testing.T) (NativePhysicalRecoveryInput, *recoveryMarkerResolver, *recoveryObservationReader, *recoverySnapshotFactory) {
	t.Helper()
	build := nativePhysicalFixtureInput(t)
	build.Attempt.State = deploymentnative.AttemptIndeterminate
	build.Attempt.LeaseExpiresAt = time.Now().UTC().Add(-time.Hour)
	environment := nativePhysicalEnvironment(t, build)
	capture, err := ducklakepostgres.NewSourceObservationCapture(build.Attempt.AttemptID, build.Marker, environment.observations, time.Date(2026, time.August, 31, 2, 3, 4, 5_000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	capture.CreatedAt = capture.CapturedAt
	resolver := &recoveryMarkerResolver{resolution: ducklake.PhysicalMarkerResolution{SnapshotID: 42, Found: true}}
	reader := &recoveryObservationReader{capture: capture}
	inspector := &recoverySnapshotInspector{seal: environment.seal, closure: environment.closure}
	factory := &recoverySnapshotFactory{inspector: inspector}
	return NativePhysicalRecoveryInput{
		Attempt: build.Attempt, Marker: build.Marker, Request: build.Request,
		CatalogID: build.CatalogID, ObjectRoot: build.ObjectRoot,
		Compatibility:         ducklakepostgres.RuntimeCompatibility{RuntimeTuple: ducklakepostgres.RuntimeTuple{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1"}, CompatibilityDigest: nativePhysicalDigest('c'), CatalogSchemaVersion: "schema-v1"},
		MarkerResolverFactory: &recoveryMarkerResolverFactory{resolver: resolver}, MarkerQuarantine: &recoveryMarkerQuarantineWriter{}, ObservationReader: reader, SnapshotFactory: factory,
	}, resolver, reader, factory
}

func TestRecoverNativePhysicalBuildFoundWithoutMaterialization(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	got, err := RecoverNativePhysicalBuild(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptID != input.Attempt.AttemptID || got.SnapshotID != 42 || got.CatalogID != input.CatalogID || got.ObjectRoot != input.ObjectRoot {
		t.Fatalf("recovered evidence = %#v", got)
	}
	if len(got.SourceObservations) != 1 || got.SourceObservations[0].ID != "orders" {
		t.Fatalf("recovered observations = %#v", got.SourceObservations)
	}
	if resolver.resolved != 1 || resolver.closed != 1 || reader.reads != 1 || factory.opened != 1 || factory.inspector.closed != 1 {
		t.Fatalf("calls resolver=%d/%d reader=%d inspector=%d/%d", resolver.resolved, resolver.closed, reader.reads, factory.opened, factory.inspector.closed)
	}
	if factory.request.SnapshotID != 42 || factory.request.CommitMarker.AttemptID != input.Attempt.AttemptID || factory.request.CommitMarker.DeliveryID != input.Marker.DeliveryID {
		t.Fatalf("pinned open request = %#v", factory.request)
	}
	if factory.inspector.sealID != 42 || factory.inspector.closureIn.SnapshotID != 42 || factory.inspector.closureIn.CatalogID != input.CatalogID || factory.inspector.closureIn.ObjectRoot != input.ObjectRoot || factory.inspector.closureIn.RelationNamespace != input.Attempt.Namespace {
		t.Fatalf("pinned inspection seal=%d closure=%#v", factory.inspector.sealID, factory.inspector.closureIn)
	}
	got.SourceObservations[0].ID = "mutated"
	got.SourceObservations[0].Schema[0].Name = "mutated"
	stored, err := reader.capture.Observations()
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].ID == "mutated" || stored[0].Schema[0].Name == "mutated" {
		t.Fatal("recovered observations alias persisted capture")
	}
}

func TestRecoverNativePhysicalBuildMarkerAbsentSkipsCaptureAndSnapshot(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	resolver.resolution = ducklake.PhysicalMarkerResolution{}
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("absent marker error = %v, want unresolved", err)
	}
	if reader.reads != 0 || factory.opened != 0 || resolver.closed != 1 {
		t.Fatalf("absent marker calls reader=%d factory=%d resolver close=%d", reader.reads, factory.opened, resolver.closed)
	}
}

func TestRecoverNativePhysicalBuildRejectsContradictoryMarkerResolution(t *testing.T) {
	for name, resolution := range map[string]ducklake.PhysicalMarkerResolution{
		"absent with snapshot":   {SnapshotID: 42},
		"absent with negative":   {SnapshotID: -1},
		"found without snapshot": {Found: true},
	} {
		t.Run(name, func(t *testing.T) {
			input, resolver, reader, factory := recoveryFixture(t)
			resolver.resolution = resolution
			if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, deploymentnative.ErrConflict) {
				t.Fatalf("contradictory resolution error = %v, want conflict", err)
			}
			if reader.reads != 0 || factory.opened != 0 || resolver.closed != 1 {
				t.Fatalf("contradictory resolution calls reader=%d factory=%d resolver close=%d", reader.reads, factory.opened, resolver.closed)
			}
		})
	}
}

func TestRecoverNativePhysicalBuildResolverErrorsCloseAndSkipCapture(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	want := errors.New("ambiguous marker")
	resolver.err = want
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, want) || !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("resolver error = %v, want %v", err, want)
	}
	if reader.reads != 0 || factory.opened != 0 || resolver.closed != 1 {
		t.Fatalf("resolver error calls reader=%d factory=%d resolver close=%d", reader.reads, factory.opened, resolver.closed)
	}
}

func TestRecoverNativePhysicalBuildPersistsMarkerAnomalyBeforeFailingClosed(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	markerDigest := nativePhysicalDigest('9')
	resolver.resolution = ducklake.PhysicalMarkerResolution{Anomaly: ducklake.PhysicalMarkerAnomalyDigestMismatch}
	resolver.resolution.ObservedMarkerDigests[0] = markerDigest
	resolver.resolution.ObservedSnapshotIDs[0] = 91
	resolver.err = ducklake.ErrCommittedSnapshotDigestMismatch
	writer := &recoveryMarkerQuarantineWriter{}
	input.MarkerQuarantine = writer

	_, err := RecoverNativePhysicalBuild(t.Context(), input)
	if !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) || !errors.Is(err, ducklakepostgres.ErrMarkerQuarantined) || !errors.Is(err, ducklake.ErrCommittedSnapshotDigestMismatch) {
		t.Fatalf("marker anomaly error = %v", err)
	}
	if writer.writes != 1 || writer.input.AttemptID != input.Attempt.AttemptID || writer.input.CatalogID != input.CatalogID || writer.input.Reason != ducklakepostgres.MarkerQuarantineDigestMismatch || writer.input.ObservedMarkerDigest != markerDigest || len(writer.input.ObservedSnapshotIDs) != 1 || writer.input.ObservedSnapshotIDs[0] != 91 {
		t.Fatalf("marker quarantine write = %d %#v", writer.writes, writer.input)
	}
	if reader.reads != 0 || factory.opened != 0 || resolver.closed != 1 {
		t.Fatalf("anomaly reached downstream recovery reader=%d factory=%d close=%d", reader.reads, factory.opened, resolver.closed)
	}
}

func TestRecoverNativePhysicalBuildMarkerAnomalyPersistenceFailureFailsClosed(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	markerDigest := nativePhysicalDigest('8')
	resolver.resolution = ducklake.PhysicalMarkerResolution{Anomaly: ducklake.PhysicalMarkerAnomalyDuplicate}
	resolver.resolution.ObservedMarkerDigests[0] = markerDigest
	resolver.resolution.ObservedSnapshotIDs[0] = 92
	resolver.err = ducklake.ErrCommittedSnapshotAmbiguous
	writerErr := errors.New("control database unavailable")
	writer := &recoveryMarkerQuarantineWriter{err: writerErr}
	input.MarkerQuarantine = writer

	_, err := RecoverNativePhysicalBuild(t.Context(), input)
	if !errors.Is(err, writerErr) || !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) || !errors.Is(err, ducklakepostgres.ErrMarkerQuarantined) {
		t.Fatalf("marker quarantine persistence failure = %v", err)
	}
	if writer.writes != 1 || reader.reads != 0 || factory.opened != 0 || resolver.closed != 1 {
		t.Fatalf("persistence failure calls writes=%d reader=%d factory=%d close=%d", writer.writes, reader.reads, factory.opened, resolver.closed)
	}
}

func TestRecoverNativePhysicalBuildRejectsAnomalyWithPositiveSnapshot(t *testing.T) {
	for name, resolution := range map[string]ducklake.PhysicalMarkerResolution{
		"found":    {Found: true, SnapshotID: 93, Anomaly: ducklake.PhysicalMarkerAnomalyIdentityMismatch},
		"snapshot": {SnapshotID: 94, Anomaly: ducklake.PhysicalMarkerAnomalyDigestMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			input, resolver, reader, factory := recoveryFixture(t)
			resolver.resolution = resolution
			resolver.err = ducklake.ErrCommittedSnapshotIdentityMismatch
			writer := &recoveryMarkerQuarantineWriter{}
			input.MarkerQuarantine = writer

			_, err := RecoverNativePhysicalBuild(t.Context(), input)
			if !errors.Is(err, deploymentnative.ErrConflict) || !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
				t.Fatalf("contradictory anomaly resolution = %v", err)
			}
			if writer.writes != 0 || reader.reads != 0 || factory.opened != 0 || resolver.closed != 1 {
				t.Fatalf("contradictory anomaly calls writes=%d reader=%d factory=%d close=%d", writer.writes, reader.reads, factory.opened, resolver.closed)
			}
		})
	}
}

func TestPersistNativeMarkerQuarantineMapsReasonsAndCanonicalEvidence(t *testing.T) {
	for _, tc := range []struct {
		anomaly ducklake.PhysicalMarkerAnomaly
		reason  ducklakepostgres.MarkerQuarantineReason
	}{
		{ducklake.PhysicalMarkerAnomalyDuplicate, ducklakepostgres.MarkerQuarantineDuplicate},
		{ducklake.PhysicalMarkerAnomalyDigestMismatch, ducklakepostgres.MarkerQuarantineDigestMismatch},
		{ducklake.PhysicalMarkerAnomalyIdentityMismatch, ducklakepostgres.MarkerQuarantineIdentityMismatch},
	} {
		t.Run(string(tc.anomaly), func(t *testing.T) {
			input, _, _, _ := recoveryFixture(t)
			writer := &recoveryMarkerQuarantineWriter{}
			input.MarkerQuarantine = writer
			resolution := ducklake.PhysicalMarkerResolution{Anomaly: tc.anomaly}
			resolution.ObservedMarkerDigests[0] = nativePhysicalDigest('7')
			resolution.ObservedSnapshotIDs[0] = 95
			if err := persistNativeMarkerQuarantine(t.Context(), input, resolution); err != nil {
				t.Fatal(err)
			}
			if writer.writes != 1 || writer.input.Reason != tc.reason || writer.input.ObservedMarkerDigest != nativePhysicalDigest('7') || len(writer.input.ObservedSnapshotIDs) != 1 || writer.input.ObservedSnapshotIDs[0] != 95 {
				t.Fatalf("quarantine input = %#v", writer.input)
			}
			if string(writer.input.Evidence) != `{"schema_version":1,"anomaly":"`+string(tc.anomaly)+`","physical_pool_id":"`+input.Attempt.PhysicalPoolID+`","catalog_id":"`+input.CatalogID+`","attempt_id":"`+input.Attempt.AttemptID+`","request_digest":"`+input.Attempt.RequestDigest+`","plan_digest":"`+input.Attempt.PlanDigest+`","observed_marker_digests":["`+nativePhysicalDigest('7')+`"],"observed_snapshot_ids":[95]}` {
				t.Fatalf("non-canonical marker quarantine evidence = %s", writer.input.Evidence)
			}
		})
	}
}

func TestRecoverNativePhysicalBuildResolverOpenErrorIsUnresolvedAndClosesPartialOpen(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	openErr := errors.New("resolver open failed")
	input.MarkerResolverFactory = &recoveryMarkerResolverFactory{resolver: resolver, err: openErr}
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, openErr) || !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("resolver open error = %v, want root and unresolved", err)
	}
	if resolver.closed != 1 || reader.reads != 0 || factory.opened != 0 {
		t.Fatalf("resolver open failure calls close=%d reader=%d factory=%d", resolver.closed, reader.reads, factory.opened)
	}
}

func TestRecoverNativePhysicalBuildRequiresIndeterminateAttempt(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	input.Attempt.State = deploymentnative.AttemptRunning
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("running attempt error = %v, want conflict", err)
	}
	if resolver.resolved != 0 || reader.reads != 0 || factory.opened != 0 {
		t.Fatalf("running attempt reached recovery authorities resolver=%d reader=%d factory=%d", resolver.resolved, reader.reads, factory.opened)
	}
}

func TestRecoverNativePhysicalBuildRejectsTypedNilAuthorities(t *testing.T) {
	for name, mutate := range map[string]func(*NativePhysicalRecoveryInput){
		"marker resolver": func(input *NativePhysicalRecoveryInput) {
			var factory *recoveryMarkerResolverFactory
			input.MarkerResolverFactory = factory
		},
		"marker quarantine": func(input *NativePhysicalRecoveryInput) {
			var writer *recoveryMarkerQuarantineWriter
			input.MarkerQuarantine = writer
		},
		"observation reader": func(input *NativePhysicalRecoveryInput) {
			var reader *recoveryObservationReader
			input.ObservationReader = reader
		},
		"snapshot inspector": func(input *NativePhysicalRecoveryInput) {
			var factory *recoverySnapshotFactory
			input.SnapshotFactory = factory
		},
	} {
		t.Run(name, func(t *testing.T) {
			input, _, _, _ := recoveryFixture(t)
			mutate(&input)
			if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("typed nil authority error = %v, want invalid", err)
			}
		})
	}
}

func TestRecoverNativePhysicalBuildRejectsCaptureMarkerMismatch(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	reader.capture.CommitMarker = []byte(`{"schema_version":1,"attempt_id":"wrong"}`)
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("capture marker mismatch = %v, want unresolved", err)
	}
	if resolver.closed != 1 || factory.opened != 0 {
		t.Fatalf("capture mismatch calls resolver close=%d factory=%d", resolver.closed, factory.opened)
	}
}

func TestRecoverNativePhysicalBuildPropagatesCloseErrors(t *testing.T) {
	input, resolver, reader, factory := recoveryFixture(t)
	resolver.closeErr = errors.New("resolver close failed")
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, resolver.closeErr) || !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("resolver close error = %v", err)
	}
	if reader.reads != 0 || factory.opened != 0 {
		t.Fatalf("resolver close failure continued reader=%d factory=%d", reader.reads, factory.opened)
	}
}

func TestRecoverNativePhysicalBuildInspectorCloseFailureClearsEvidence(t *testing.T) {
	input, _, _, factory := recoveryFixture(t)
	closeErr := errors.New("inspector close failed")
	factory.inspector.closeErr = closeErr
	got, err := RecoverNativePhysicalBuild(t.Context(), input)
	if !errors.Is(err, closeErr) || !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("inspector close error = %v", err)
	}
	if got.AttemptID != "" || got.SourceObservations != nil {
		t.Fatalf("evidence survived inspector close failure: %#v", got)
	}
}

func TestRecoverNativePhysicalBuildRejectsRuntimeCompatibilityMismatch(t *testing.T) {
	input, _, _, factory := recoveryFixture(t)
	factory.inspector.seal.ExtensionVersion = "2"
	if _, err := RecoverNativePhysicalBuild(t.Context(), input); !errors.Is(err, ErrNativePhysicalRecoveryUnresolved) {
		t.Fatalf("runtime mismatch error = %v, want unresolved", err)
	}
}

func TestDuckLakePhysicalMarkerResolverFactoryClosesPartialOpen(t *testing.T) {
	resolver := &recoveryMarkerResolver{}
	openErr := errors.New("partial open")
	factory := DuckLakePhysicalMarkerResolverFactory{ResolverFactory: ducklake.PhysicalMarkerResolverFactoryFunc(func(context.Context) (ducklake.PhysicalMarkerResolver, error) {
		return resolver, openErr
	})}
	if _, err := factory.OpenReadOnly(t.Context()); !errors.Is(err, openErr) {
		t.Fatalf("partial open error = %v, want %v", err, openErr)
	}
	if resolver.closed != 1 {
		t.Fatalf("partial resolver closes = %d, want 1", resolver.closed)
	}
}

func TestNativeQualificationSnapshotInspectorFactoryRequiresSealCapability(t *testing.T) {
	env := &qualificationEnvironmentFake{}
	factory := NativeQualificationSnapshotInspectorFactory{QualificationFactory: NativeQualificationEnvironmentFactoryFunc(func(context.Context, NativeQualificationOpenRequest) (NativeQualificationEnvironment, error) {
		return &recoveryQualificationWithoutSeal{env: env}, nil
	})}
	if _, err := factory.Open(t.Context(), NativeQualificationOpenRequest{}); !errors.Is(err, ErrNativeQualificationRuntime) {
		t.Fatalf("missing seal capability error = %v, want runtime", err)
	}
	if env.closed != 1 {
		t.Fatalf("qualification environment closes = %d, want 1", env.closed)
	}
}
