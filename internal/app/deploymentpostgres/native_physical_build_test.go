package deploymentpostgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func nativePhysicalDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

type nativePhysicalEnvironmentFake struct {
	materialize  func(context.Context, analyticsmaterialization.Request) (int64, error)
	seal         ducklake.PostgresSnapshotSealEvidence
	closure      ducklake.NativeSnapshotClosureEvidence
	closeErr     error
	materializes int
	seals        int
	closures     int
	closes       int
	request      analyticsmaterialization.Request
	closureReq   ducklake.NativeSnapshotClosureRequest
}

func (f *nativePhysicalEnvironmentFake) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
	f.materializes++
	f.request = request
	if f.materialize != nil {
		return f.materialize(ctx, request)
	}
	return 42, nil
}
func (f *nativePhysicalEnvironmentFake) SnapshotSealEvidence(context.Context, int64) (ducklake.PostgresSnapshotSealEvidence, error) {
	f.seals++
	return f.seal, nil
}
func (f *nativePhysicalEnvironmentFake) NativeSnapshotClosureEvidence(_ context.Context, request ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	f.closures++
	f.closureReq = request
	return f.closure, nil
}
func (f *nativePhysicalEnvironmentFake) Close() error {
	f.closes++
	return f.closeErr
}

func validNativePhysicalBuildInput(t *testing.T) NativePhysicalBuildInput {
	t.Helper()
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000103"
	planID := "0198f2c0-7c7a-7f00-8a11-000000000101"
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000000102"
	requestDigest, planDigest := nativePhysicalDigest('f'), nativePhysicalDigest('a')
	namespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{CandidateID: candidateID, AttemptID: attemptID, FencingEpoch: 3})
	if err != nil {
		t.Fatal(err)
	}
	marker := catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-native",
		GenerationID: "generation-native", AttemptID: attemptID, LeaseEpoch: 3,
		RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project-native",
		Environment: "prod", PhysicalPoolID: "pool-native",
	}
	return NativePhysicalBuildInput{
		Attempt: deploymentnative.DeliveryBuildAttempt{
			AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder-native", PhysicalPoolID: "pool-native",
			FencingEpoch: 3, RequestDigest: requestDigest, PlanDigest: planDigest,
			Namespace: namespace, State: deploymentnative.AttemptRunning, LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
		},
		Marker:    marker,
		CatalogID: "catalog-native", ObjectRoot: "/tmp/native-objects",
	}
}

func nativePhysicalFixtureInput(t *testing.T) NativePhysicalBuildInput {
	t.Helper()
	in := validNativePhysicalBuildInput(t)
	in.Request = analyticsmaterialization.Request{
		Models:      map[string]*semanticmodel.Model{"orders": {}},
		ModelTables: map[string]semanticmodel.Table{"orders": {}},
		Identity:    projectgraph.ServingIdentity{ProjectID: "project-native", Environment: "prod", GenerationID: "generation-native"},
		CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102",
		Environment: "prod", TargetType: "deployment", TargetID: "target-native", Tables: []string{"orders"},
	}
	return in
}

func nativePhysicalEnvironment(t *testing.T, in NativePhysicalBuildInput) *nativePhysicalEnvironmentFake {
	t.Helper()
	canonical, err := in.Marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	root := "/tmp/native-objects"
	snapshot := int64(42)
	relationJSON := []byte(`{"relations":[]}`)
	closureJSON := []byte(`{"objects":[]}`)
	relationDigest := nativePhysicalValueDigest(relationJSON)
	closureDigest := nativePhysicalValueDigest(closureJSON)
	rootDigest := nativePhysicalValueDigest([]byte(root))
	canonicalClosure, err := json.Marshal(struct {
		SchemaVersion          int                             `json:"schema_version"`
		CatalogID              string                          `json:"catalog_id"`
		SnapshotID             int64                           `json:"snapshot_id"`
		ObjectRoot             string                          `json:"object_root"`
		Relations              []ducklake.BaseTable            `json:"relations"`
		Objects                []ducklake.NativeSnapshotObject `json:"objects"`
		RelationManifestDigest string                          `json:"relation_manifest_digest"`
		ClosureDigest          string                          `json:"closure_digest"`
		ObjectRootDigest       string                          `json:"object_root_digest"`
	}{1, in.CatalogID, snapshot, root, []ducklake.BaseTable{}, []ducklake.NativeSnapshotObject{}, relationDigest, closureDigest, rootDigest})
	if err != nil {
		t.Fatal(err)
	}
	f := &nativePhysicalEnvironmentFake{
		seal:    ducklake.PostgresSnapshotSealEvidence{CatalogType: "postgres", MetadataSchema: ducklake.MetadataSchemaForPool(in.Attempt.PhysicalPoolID), DataPath: root, ExtensionVersion: "1", CatalogVersion: "1", SnapshotID: snapshot, CommitMarker: canonical},
		closure: ducklake.NativeSnapshotClosureEvidence{CatalogID: in.CatalogID, SnapshotID: snapshot, ObjectRoot: root, Relations: []ducklake.BaseTable{}, Objects: []ducklake.NativeSnapshotObject{}, RelationManifestJSON: relationJSON, ClosureJSON: closureJSON, CanonicalJSON: canonicalClosure, RelationManifestDigest: relationDigest, ClosureDigest: closureDigest, ObjectRootDigest: rootDigest},
	}
	return f
}

func nativePhysicalValueDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestBuildNativePhysicalSuccess(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	env := nativePhysicalEnvironment(t, in)
	opens := 0
	got, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(_ context.Context, marker catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		opens++
		if marker != in.Marker {
			t.Fatalf("factory marker = %#v, want %#v", marker, in.Marker)
		}
		return env, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := in.Marker.CanonicalJSON()
	if opens != 1 || env.materializes != 1 || env.seals != 1 || env.closures != 1 || env.closes != 1 {
		t.Fatalf("calls open=%d materialize=%d seal=%d closure=%d close=%d", opens, env.materializes, env.seals, env.closures, env.closes)
	}
	if got.SnapshotID != 42 || got.CatalogID != in.CatalogID || got.ObjectRoot != "/tmp/native-objects" || string(got.CanonicalMarkerJSON) != canonical {
		t.Fatalf("evidence = %#v", got)
	}
	if got.Closure.Relations == nil || got.Closure.Objects == nil {
		t.Fatal("successful evidence collapsed canonical empty arrays to nil")
	}
	if env.closureReq.CatalogID != in.CatalogID || env.closureReq.SnapshotID != 42 || env.closureReq.ObjectRoot != "/tmp/native-objects" {
		t.Fatalf("closure request = %#v", env.closureReq)
	}
}

func TestBuildNativePhysicalValidatesBeforeOpen(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	in.Request.Models = nil
	opens := 0
	if _, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		opens++
		return nil, nil
	})); err == nil {
		t.Fatal("validation unexpectedly succeeded")
	}
	if opens != 0 {
		t.Fatalf("factory opened %d times after invalid input", opens)
	}
}

func TestBuildNativePhysicalRejectsRelationNamespaceDrift(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	in.Attempt.Namespace = "_not-the-canonical-namespace"
	if _, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		t.Fatal("factory opened despite relation namespace drift")
		return nil, nil
	})); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("namespace drift error = %v, want conflict", err)
	}
}

func TestBuildNativePhysicalRejectsEvidenceMismatchAndCloses(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	env := nativePhysicalEnvironment(t, in)
	env.seal.SnapshotID = 99
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	if err == nil || !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("mismatch error = %v", err)
	}
	if env.closes != 1 {
		t.Fatalf("close count = %d, want 1", env.closes)
	}
}

func TestBuildNativePhysicalRejectsForgedClosureDigest(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	env := nativePhysicalEnvironment(t, in)
	env.closure.ClosureDigest = nativePhysicalDigest('e')
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	if err == nil || !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("forged closure digest error = %v", err)
	}
	if env.closes != 1 {
		t.Fatalf("close count = %d, want 1", env.closes)
	}
}

func TestBuildNativePhysicalMaterializationFailureClosesAndPreservesError(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	want := errors.New("materialize failed")
	env := nativePhysicalEnvironment(t, in)
	env.materialize = func(context.Context, analyticsmaterialization.Request) (int64, error) { return 0, want }
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if env.closes != 1 {
		t.Fatalf("close count = %d, want 1", env.closes)
	}
}

func TestBuildNativePhysicalCloseErrorIsReturned(t *testing.T) {
	in := nativePhysicalFixtureInput(t)
	env := nativePhysicalEnvironment(t, in)
	want := errors.New("close failed")
	env.closeErr = want
	_, err := BuildNativePhysical(t.Context(), in, NativePhysicalBuildEnvironmentFactoryFunc(func(context.Context, catalogartifact.CommitMarker) (NativePhysicalBuildEnvironment, error) {
		return env, nil
	}))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
