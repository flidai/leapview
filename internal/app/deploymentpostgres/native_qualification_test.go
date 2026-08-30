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
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/gates"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/release"
)

func qualificationDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type qualificationEnvironmentFake struct {
	compat  NativeRuntimeCompatibilityEvidence
	closure ducklake.NativeSnapshotClosureEvidence
	query   func(context.Context, semanticquery.Plan) (semanticquery.Rows, error)
	closed  int
}

func (e *qualificationEnvironmentFake) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	if e.query == nil {
		return nil, errors.New("query not configured")
	}
	return e.query(ctx, plan)
}
func (e *qualificationEnvironmentFake) RuntimeCompatibility(context.Context) (NativeRuntimeCompatibilityEvidence, error) {
	return e.compat, nil
}
func (e *qualificationEnvironmentFake) NativeSnapshotClosureEvidence(context.Context, ducklake.NativeSnapshotClosureRequest) (ducklake.NativeSnapshotClosureEvidence, error) {
	return e.closure, nil
}
func (e *qualificationEnvironmentFake) Close() error { e.closed++; return nil }

type qualificationFactoryFake struct {
	env *qualificationEnvironmentFake
	got NativeQualificationOpenRequest
}

func (f *qualificationFactoryFake) Open(_ context.Context, request NativeQualificationOpenRequest) (NativeQualificationEnvironment, error) {
	f.got = request
	return f.env, nil
}

func qualificationBuildEvidence(t *testing.T) (NativePhysicalBuildEvidence, string) {
	t.Helper()
	marker := catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery",
		GenerationID: "generation", AttemptID: "attempt", LeaseEpoch: 7,
		RequestDigest: qualificationDigest("request"), PlanDigest: qualificationDigest("plan"),
		Project: "project", Environment: "prod", PhysicalPoolID: "pool",
	}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	namespace := "_candidate_attempt"
	relations := []ducklake.BaseTable{{Schema: namespace, Table: "orders"}}
	relationJSON, _ := json.Marshal(struct {
		RelationNamespace string               `json:"relation_namespace"`
		Relations         []ducklake.BaseTable `json:"relations"`
	}{namespace, relations})
	closureJSON := []byte(`{"objects":[]}`)
	relationDigest, closureDigest, rootDigest := qualificationDigest(string(relationJSON)), qualificationDigest(string(closureJSON)), qualificationDigest("/objects")
	canonicalClosure, _ := json.Marshal(struct {
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
	}{ducklake.NativeSnapshotClosureSchemaVersion, "catalog", 42, "/objects", namespace, relations, []ducklake.NativeSnapshotObject{}, relationDigest, closureDigest, rootDigest})
	return NativePhysicalBuildEvidence{
		AttemptID: "attempt", CatalogID: "catalog", ObjectRoot: "/objects", SnapshotID: 42,
		Marker: marker, CanonicalMarkerJSON: []byte(markerJSON),
		Seal:    ducklake.PostgresSnapshotSealEvidence{CatalogType: "postgres", MetadataSchema: ducklake.MetadataSchemaForPool("pool"), DataPath: "/objects", ExtensionVersion: "1", CatalogVersion: "1", SnapshotID: 42, CommitMarker: markerJSON},
		Closure: ducklake.NativeSnapshotClosureEvidence{CatalogID: "catalog", SnapshotID: 42, ObjectRoot: "/objects", RelationNamespace: namespace, Relations: relations, Objects: []ducklake.NativeSnapshotObject{}, RelationManifestJSON: relationJSON, ClosureJSON: closureJSON, CanonicalJSON: canonicalClosure, RelationManifestDigest: relationDigest, ClosureDigest: closureDigest, ObjectRootDigest: rootDigest},
	}, namespace
}

func qualificationRequest(t *testing.T) (NativeQualificationRequest, string) {
	t.Helper()
	build, namespace := qualificationBuildEvidence(t)
	compat := ducklakepostgres.RuntimeCompatibility{RuntimeTuple: ducklakepostgres.RuntimeTuple{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:1"}, CompatibilityDigest: qualificationDigest("compat"), CatalogSchemaVersion: "schema-v1"}
	minimum, maximum := int64(1), int64(1)
	return NativeQualificationRequest{
		Build: build, CandidateID: "candidate", SourceDigest: qualificationDigest("source"), BindingGeneration: qualificationDigest("binding"), RuntimeVersion: "runtime", Compatibility: compat,
		Models: []gates.ModelInput{{ID: "orders", Model: semanticmodel.Table{ModelName: "orders", Checks: []semanticmodel.ModelCheck{{Type: "row_count", Minimum: &minimum, Maximum: &maximum, Severity: "error"}}}}}, Bounds: gates.Bounds{MaxRows: 10, MaxQueries: 5, MaxMillis: 1000}, Now: time.Now().UTC(),
	}, namespace
}

func TestQualifyNativeSnapshotRunsGatesAgainstExactNamespace(t *testing.T) {
	request, namespace := qualificationRequest(t)
	compat := NativeRuntimeCompatibilityEvidence{SnapshotID: 42, CatalogType: "postgres", DataPath: "/objects", MetadataSchema: ducklake.MetadataSchemaForPool("pool"), DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "1", CompatibilityDigest: request.Compatibility.CompatibilityDigest, CatalogSchemaVersion: request.Compatibility.CatalogSchemaVersion}
	env := &qualificationEnvironmentFake{compat: compat, closure: request.Build.Closure, query: func(_ context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
		if !strings.Contains(plan.SQL, `"`+namespace+`"."orders"`) {
			t.Fatalf("gate query did not bind candidate namespace: %s", plan.SQL)
		}
		return semanticquery.Rows{{"count": int64(1)}}, nil
	}}
	factory := &qualificationFactoryFake{env: env}
	result, err := QualifyNativeSnapshot(t.Context(), request, factory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gates.Outcome != release.GateSuccess || result.Digest == "" || env.closed != 1 {
		t.Fatalf("result=%#v closed=%d", result, env.closed)
	}
	if factory.got.SnapshotID != 42 || factory.got.RelationNamespace != namespace || factory.got.PhysicalPoolID != "pool" {
		t.Fatalf("open request=%#v", factory.got)
	}
	encoded, digestValue, err := result.Canonical()
	if err != nil || digestValue != result.Digest || len(encoded) == 0 {
		t.Fatalf("canonical evidence bytes=%d digest=%q err=%v", len(encoded), digestValue, err)
	}
}

func TestQualifyNativeSnapshotRejectsTamperedPhysicalEvidenceBeforeOpen(t *testing.T) {
	request, _ := qualificationRequest(t)
	request.Build.Seal.CommitMarker = "{}"
	opened := false
	_, err := QualifyNativeSnapshot(t.Context(), request, NativeQualificationEnvironmentFactoryFunc(func(context.Context, NativeQualificationOpenRequest) (NativeQualificationEnvironment, error) {
		opened = true
		return nil, nil
	}))
	if err == nil || !errors.Is(err, ErrNativeQualificationInvalid) || opened {
		t.Fatalf("err=%v opened=%v, want fail-closed validation", err, opened)
	}
}

func TestDuckLakeNativeQualificationFactoryRejectsFileCatalogConfiguration(t *testing.T) {
	request, namespace := qualificationRequest(t)
	factory := DuckLakeNativeQualificationEnvironmentFactory{Config: ducklake.Config{CatalogPath: "/tmp/catalog.duckdb"}}
	if _, err := factory.Open(t.Context(), NativeQualificationOpenRequest{PhysicalPoolID: "pool", CatalogID: request.Build.CatalogID, SnapshotID: request.Build.SnapshotID, ObjectRoot: request.Build.ObjectRoot, RelationNamespace: namespace, Compatibility: request.Compatibility}); err == nil || !errors.Is(err, ErrNativeQualificationRuntime) {
		t.Fatalf("err=%v, want missing native admission", err)
	}
}
