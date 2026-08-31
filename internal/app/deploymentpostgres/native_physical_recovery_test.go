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
		MarkerResolverFactory: &recoveryMarkerResolverFactory{resolver: resolver}, ObservationReader: reader, SnapshotFactory: factory,
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
