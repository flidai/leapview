package runtimefactory

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSealedRootResolverCandidatePreview(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "runtimefactory_sealed_root")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := deploymentpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := physicalpoolpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	targetID := "target-preview"
	projectID := "project-preview"
	environment := "prod"
	generationID := "11111111-1111-4111-8111-111111111111"
	planID := "22222222-2222-4222-8222-222222222222"
	candidateID := "33333333-3333-4333-8333-333333333333"
	attemptID := "44444444-4444-4444-8444-444444444444"
	sealID := "55555555-5555-4555-8555-555555555555"
	requestDigest := testPostgresResolverDigest('1')
	planDigest := testPostgresResolverDigest('2')
	graphDigest := testPostgresResolverDigest('3')
	configDigest := testPostgresResolverDigest('4')
	securityDigest := testPostgresResolverDigest('5')
	artifactDigest := testPostgresResolverDigest('6')
	artifactRootDigest := testPostgresResolverDigest('7')
	closureDigest := testPostgresResolverDigest('8')
	relationDigest := testPostgresResolverDigest('9')
	objectDigest := testPostgresResolverDigest('a')
	qualificationDigest := testPostgresResolverDigest('b')

	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0",
		CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: filepath.Join(t.TempDir(), "warehouse"), StorageNamespace: "warehouse",
		Region: "us-east-1", Tenant: "tenant-preview", IsolationBoundary: targetID,
		RetentionAuthority: targetID, RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 300, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 60},
		Compatibility: compatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: "runtimefactory-test/v1",
		Checks: []physicalpool.EvidenceCheck{{ID: "resolver", Passed: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pools := physicalpoolpostgres.New(db)
	pool, _, err = pools.CreateAndAdmit(t.Context(), pool, evidence)
	if err != nil {
		t.Fatal(err)
	}
	compatibilityDigest, err := compatibility.Digest()
	if err != nil {
		t.Fatal(err)
	}

	delivery := deploymentpostgres.New(db)
	if _, err := delivery.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: projectID, Environment: environment}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreatePlan(t.Context(), deploymentpostgres.PlanInput{
		PlanID: planID, TargetID: targetID, PlanRevision: 1, PlanDigest: planDigest,
		CompiledGraphDigest: graphDigest, CompiledConfigDigest: configDigest,
		SecurityDomainFingerprint: securityDigest, ArtifactDigest: artifactDigest,
		Evidence: json.RawMessage(`{"source":"resolver-test"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateCandidate(t.Context(), deploymentpostgres.CandidateInput{
		CandidateID: candidateID, TargetID: targetID, PlanID: planID, CandidateRevision: 1, ArtifactDigest: artifactDigest,
	}); err != nil {
		t.Fatal(err)
	}
	attempt, err := delivery.BeginBuildAttempt(t.Context(), deploymentpostgres.BuildAttemptInput{
		AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "resolver-test-owner",
		PhysicalPoolID: pool.ID.String(), FencingEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest,
		Namespace: "relation_namespace", SessionIdentity: "resolver-session", LeaseExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	markerJSON, err := (ducklake.CommitMarker{
		SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-preview",
		GenerationID: generationID, AttemptID: attemptID, LeaseEpoch: attempt.FencingEpoch,
		RequestDigest: requestDigest, PlanDigest: planDigest, Project: projectID, Environment: environment,
		PhysicalPoolID: pool.ID.String(),
	}).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CommitBuildAttempt(t.Context(), deploymentpostgres.CommitAttemptInput{
		AttemptID: attemptID, OwnerID: attempt.OwnerID, FencingEpoch: attempt.FencingEpoch, SnapshotID: 42,
		CommitMarker: json.RawMessage(markerJSON),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateSnapshotSeal(t.Context(), deploymentpostgres.SnapshotSealInput{
		SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: pool.ID.String(),
		TenantDomain: "tenant-preview", Region: "us-east-1", EncryptionDomain: "kms-preview", ObjectNamespace: "objects-preview",
		CatalogDatabase: "catalog_preview", CatalogID: "catalog-id", CatalogUUID: "66666666-6666-4666-8666-666666666666", CatalogVersion: 3, DuckLakeSnapshotID: 42,
		RelationNamespace: "relation_namespace", RelationManifestDigest: relationDigest, ClosureDigest: closureDigest,
		ObjectRoot: "objects/root", ObjectRootDigest: objectDigest, ArtifactRoot: "artifacts/root", ArtifactRootDigest: artifactRootDigest,
		CompiledGraphDigest: graphDigest, CompiledConfigDigest: configDigest, SecurityDomainFingerprint: securityDigest,
		RequestDigest: requestDigest, PlanDigest: planDigest, CompatibilityDigest: compatibilityDigest,
		ServingArtifactID: "artifact-preview", ServingArtifactDigest: artifactDigest,
		DuckDBVersion: "duckdb:1.5.4", RuntimeVersion: "runtime-preview", DuckLakeExtensionVersion: "ducklake:0.3.0",
		DuckLakeSpecVersion: "ducklake-catalog:v1", CatalogSchemaVersion: "schema-v1", QualificationEvidence: json.RawMessage(`{"qualified":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.QualifyCandidate(t.Context(), candidateID, sealID, qualificationDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateGeneration(t.Context(), deploymentpostgres.GenerationInput{
		GenerationID: generationID, TargetID: targetID, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID,
		PlanDigest: planDigest, ArtifactRoot: "artifacts/root", ArtifactRootDigest: artifactRootDigest, ServingArtifactDigest: artifactDigest,
		CompiledGraphDigest: graphDigest, CompiledConfigDigest: configDigest, SecurityDomainFingerprint: securityDigest, GenerationRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	publication, err := delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{
		PublicationID: "88888888-8888-4888-8888-888888888888", TargetID: targetID, GenerationID: generationID,
		CandidateID: candidateID, SnapshotSealID: sealID, ExpectedTargetRevision: 1, ActorID: "resolver-test", RequestDigest: requestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	activationTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activationTx.Exec(t.Context(), `UPDATE delivery.delivery_target SET target_revision=2, updated_at=clock_timestamp() WHERE target_id=$1`, targetID); err != nil {
		_ = activationTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := activationTx.Exec(t.Context(), `INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id) VALUES($1,$2::uuid,$3::uuid)`, targetID, generationID, publication.PublicationID); err != nil {
		_ = activationTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := activationTx.Exec(t.Context(), `UPDATE delivery.delivery_publication SET state='committed', result_target_revision=2, committed_at=clock_timestamp() WHERE publication_id=$1::uuid`, publication.PublicationID); err != nil {
		_ = activationTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := activationTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	resolver := NewPostgresSealedRootResolver(targetID, delivery, pools)
	input := runtimehost.RuntimeInput{
		State:     servingstate.State{ID: servingstate.ID(generationID), ProjectID: projectgraph.ResourceID(projectID), Environment: servingstate.Environment(environment), DuckLakeSnapshotID: 42},
		Artifact:  servingstate.Artifact{ID: "artifact-preview", Digest: artifactDigest},
		Candidate: &runtimehost.CandidateRuntimeContext{CandidateID: candidateID},
	}
	root, err := resolver(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if root.DeliveryID != "delivery-preview" || root.GenerationID != generationID || root.ServingStateID != generationID || root.CandidateID != candidateID || root.AttemptID != attemptID || root.SealID != sealID || root.ServingArtifactID != "artifact-preview" || root.ServingArtifactDigest != artifactDigest || root.ClosureDigest != closureDigest || root.RuntimeVersion != "runtime-preview" || root.CatalogSnapshotID != 42 || root.PoolContract == nil {
		t.Fatalf("unexpected resolved root: %#v", root)
	}
	t.Run("artifact digest mismatch fails closed", func(t *testing.T) {
		bad := input
		bad.Artifact.Digest = testPostgresResolverDigest('c')
		if _, err := resolver(t.Context(), bad); !errors.Is(err, ErrSealedRootMismatch) {
			t.Fatalf("error=%v, want sealed-root mismatch", err)
		}
	})
	t.Run("marker generation mismatch fails closed", func(t *testing.T) {
		bad := input
		bad.State.ID = servingstate.ID("77777777-7777-4777-8777-777777777777")
		if _, err := resolver(t.Context(), bad); !errors.Is(err, ErrSealedRootMismatch) {
			t.Fatalf("error=%v, want sealed-root mismatch", err)
		}
	})
	t.Run("candidate runtime mismatch fails closed", func(t *testing.T) {
		bad := input
		bad.Candidate = &runtimehost.CandidateRuntimeContext{CandidateID: candidateID, RuntimeVersion: "runtime-other"}
		if _, err := resolver(t.Context(), bad); !errors.Is(err, ErrSealedRootMismatch) {
			t.Fatalf("error=%v, want sealed-root mismatch", err)
		}
	})
	t.Run("active publication resolves the same sealed root", func(t *testing.T) {
		activeInput := input
		activeInput.Candidate = nil
		root, err := resolver(t.Context(), activeInput)
		if err != nil {
			t.Fatal(err)
		}
		if root.GenerationID != generationID || root.DeliveryID != "delivery-preview" {
			t.Fatalf("root identity=%s/%s", root.GenerationID, root.DeliveryID)
		}
	})
}

func testPostgresResolverDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
