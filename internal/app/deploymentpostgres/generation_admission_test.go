package deploymentpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func admissionDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func validGenerationAdmissionInput(t *testing.T) GenerationAdmissionInput {
	t.Helper()
	genID := "0198f2c0-7c7a-7f00-8a11-000000000105"
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000103"
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000000102"
	sealID := "0198f2c0-7c7a-7f00-8a11-000000000104"
	planID := "0198f2c0-7c7a-7f00-8a11-000000000101"
	leaseID := "0198f2c0-7c7a-7f00-8a11-000000000107"
	pool := "pool-admission"
	planDigest, artifactDigest := admissionDigest('a'), admissionDigest('e')
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project_admission", Kind: projectgraph.KindProject, Name: "project"}, {ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"}}, []projectgraph.Edge{{From: "project_admission", To: "dashboard", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	marker := catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-admission", GenerationID: genID, AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: admissionDigest('f'), PlanDigest: planDigest, Project: "project_admission", Environment: "prod", PhysicalPoolID: pool}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1}`
	return GenerationAdmissionInput{
		Commit:              CommitEvidence{AttemptID: attemptID, OwnerID: "builder-admission", FencingEpoch: 1, SnapshotID: 42, CommitMarker: json.RawMessage(markerJSON)},
		Seal:                SnapshotSealEvidence{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: pool, TenantDomain: "tenant", Region: "us-east", EncryptionDomain: "enc", ObjectNamespace: "objects/admission", CatalogDatabase: "ducklake", CatalogID: "catalog-admission", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000108", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/admission", RelationManifestDigest: admissionDigest('1'), ClosureDigest: admissionDigest('8'), ObjectRoot: "objects/admission/42", ObjectRootDigest: admissionDigest('6'), ArtifactRoot: "artifacts/admission", ArtifactRootDigest: admissionDigest('7'), CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: admissionDigest('c'), SecurityDomainFingerprint: admissionDigest('d'), RequestDigest: admissionDigest('f'), PlanDigest: planDigest, CompatibilityDigest: admissionDigest('2'), ServingArtifactID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: json.RawMessage(`{"checks":["schema"]}`)},
		QualificationDigest: admissionDigest('3'),
		Fence:               LeaseFenceEvidence{LeaseID: leaseID, TargetID: "target-admission", OwnerID: "builder-admission", FencingEpoch: 1},
		Generation:          GenerationEvidence{GenerationID: genID, TargetID: "target-admission", CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: "artifacts/admission", ArtifactRootDigest: admissionDigest('7'), ServingArtifactDigest: artifactDigest, CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: admissionDigest('c'), SecurityDomainFingerprint: admissionDigest('d')},
		Bundle:              BundleEvidenceInput{GenerationID: genID, ProjectID: "project_admission", Environment: "prod", Artifact: servingstate.Artifact{ID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingStateID: servingstate.ID(genID), Digest: artifactDigest, Format: projectbundle.BundleFormat, ManifestJSON: manifest, SizeBytes: 1}, ArtifactLocator: "serving-artifacts/" + strings.TrimPrefix(artifactDigest, "sha256:") + ".tar.gz", StorageSecurityDomain: "runtime", ArtifactContentType: projectbundle.BundleContentType, ArtifactMetadataDigest: admissionDigest('9'), ProjectDigest: admissionDigest('b'), AccessPolicyJSON: `{}`, DashboardPublicationsJSON: `{}`, DashboardAppearancesJSON: `{}`, CreatedBy: "builder-admission"},
		Graph:               graph,
	}
}

func TestNormalizeGenerationAdmissionAcceptsExactEvidence(t *testing.T) {
	input := validGenerationAdmissionInput(t)
	got, err := normalizeInput(input)
	if err != nil {
		t.Fatalf("normalize valid input: %v", err)
	}
	if string(got.Commit.CommitMarker) != string(input.Commit.CommitMarker) {
		t.Fatal("normalization changed canonical commit marker")
	}
}

func TestNormalizeGenerationAdmissionRejectsCrossFieldMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GenerationAdmissionInput)
	}{
		{name: "generation id", mutate: func(in *GenerationAdmissionInput) {
			in.Generation.GenerationID = "0198f2c0-7c7a-7f00-8a11-000000000106"
		}},
		{name: "artifact serving state", mutate: func(in *GenerationAdmissionInput) { in.Bundle.Artifact.ServingStateID = "other" }},
		{name: "commit marker generation", mutate: func(in *GenerationAdmissionInput) {
			marker := catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-admission", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000000106", AttemptID: in.Commit.AttemptID, LeaseEpoch: 1, RequestDigest: admissionDigest('f'), PlanDigest: in.Generation.PlanDigest, Project: "project_admission", Environment: "prod", PhysicalPoolID: "pool-admission"}
			raw, _ := marker.CanonicalJSON()
			in.Commit.CommitMarker = json.RawMessage(raw)
		}},
		{name: "locator", mutate: func(in *GenerationAdmissionInput) { in.Bundle.ArtifactLocator = "objects/not-the-digest.tar.gz" }},
		{name: "artifact digest", mutate: func(in *GenerationAdmissionInput) { in.Bundle.Artifact.Digest = admissionDigest('4') }},
		{name: "filesystem path", mutate: func(in *GenerationAdmissionInput) { in.Bundle.Artifact.Path = "/tmp/artifact" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validGenerationAdmissionInput(t)
			test.mutate(&input)
			if _, err := normalizeInput(input); err == nil {
				t.Fatal("normalize unexpectedly accepted mismatched evidence")
			} else if !errors.Is(err, deploymentnative.ErrConflict) && !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("normalize error = %v, want native conflict/invalid", err)
			}
		})
	}
}

func generationAdmissionDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "generation_admission_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := deploymentnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := servingnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func seedGenerationAdmission(t *testing.T, repo *deploymentnative.Repository, input GenerationAdmissionInput) {
	t.Helper()
	ctx := t.Context()
	targetCreated := false
	if _, err := repo.Target(ctx, input.Generation.TargetID); errors.Is(err, deploymentnative.ErrNotFound) {
		if _, err := repo.CreateTarget(ctx, deploymentnative.TargetInput{TargetID: input.Generation.TargetID, ProjectID: input.Bundle.ProjectID.String(), Environment: string(input.Bundle.Environment)}); err != nil {
			t.Fatal(err)
		}
		targetCreated = true
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Plan(ctx, input.Generation.PlanID); errors.Is(err, deploymentnative.ErrNotFound) {
		if _, err := repo.CreatePlan(ctx, deploymentnative.PlanInput{PlanID: input.Generation.PlanID, TargetID: input.Generation.TargetID, PlanRevision: 1, PlanDigest: input.Generation.PlanDigest, CompiledGraphDigest: input.Generation.CompiledGraphDigest, CompiledConfigDigest: input.Generation.CompiledConfigDigest, SecurityDomainFingerprint: input.Generation.SecurityDomainFingerprint, ArtifactDigest: input.Generation.ServingArtifactDigest}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	candidateRevision := int64(2)
	if targetCreated {
		candidateRevision = 1
	}
	if _, err := repo.CreateCandidate(ctx, deploymentnative.CandidateInput{CandidateID: input.Generation.CandidateID, TargetID: input.Generation.TargetID, PlanID: input.Generation.PlanID, CandidateRevision: candidateRevision, ArtifactDigest: input.Generation.ServingArtifactDigest}); err != nil {
		t.Fatal(err)
	}
	tx, err := repo.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AcquireLeaseAndBeginBuildAttemptTx(ctx, tx, deploymentnative.LeaseInput{LeaseID: input.Fence.LeaseID, TargetID: input.Fence.TargetID, OwnerID: input.Fence.OwnerID, ExpiresAt: timeNowPlusHour()}, deploymentnative.BuildAttemptInput{AttemptID: input.Commit.AttemptID, PlanID: input.Generation.PlanID, CandidateID: input.Generation.CandidateID, OwnerID: input.Commit.OwnerID, PhysicalPoolID: input.Seal.PhysicalPoolID, RequestDigest: input.Seal.RequestDigest, PlanDigest: input.Generation.PlanDigest, Namespace: input.Seal.RelationNamespace, SessionIdentity: "session-admission"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func timeNowPlusHour() time.Time { return time.Now().UTC().Add(time.Hour) }

func TestGenerationAdmissionPostgresAtomicSuccessReplayAndRollback(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	serving := servingnative.New(p)
	capability, err := NewGenerationAdmission(delivery, serving)
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)
	first, err := capability.CompleteBuildAndAdmit(t.Context(), input)
	if err != nil {
		t.Fatalf("complete and admit: %v", err)
	}
	if first.Generation.GenerationID != input.Generation.GenerationID || first.Generation.GenerationRevision != 1 || first.Bundle.ArtifactLocator != input.Bundle.ArtifactLocator {
		t.Fatalf("admission result = %#v", first)
	}
	replayed, err := capability.CompleteBuildAndAdmit(t.Context(), input)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replayed.Generation != first.Generation || replayed.Bundle != first.Bundle {
		t.Fatalf("replay changed immutable result: first=%#v replay=%#v", first, replayed)
	}

	rollbackInput := validGenerationAdmissionInput(t)
	rollbackInput.Generation.GenerationID = "0198f2c0-7c7a-7f00-8a11-000000000205"
	rollbackInput.Bundle.GenerationID = rollbackInput.Generation.GenerationID
	rollbackInput.Bundle.Artifact.ServingStateID = servingstate.ID(rollbackInput.Generation.GenerationID)
	rollbackInput.Commit.AttemptID = "0198f2c0-7c7a-7f00-8a11-000000000203"
	rollbackInput.Seal.AttemptID = rollbackInput.Commit.AttemptID
	rollbackInput.Seal.SealID = "0198f2c0-7c7a-7f00-8a11-000000000204"
	rollbackInput.Generation.SnapshotSealID = rollbackInput.Seal.SealID
	rollbackInput.Generation.CandidateID = "0198f2c0-7c7a-7f00-8a11-000000000202"
	rollbackInput.Seal.CandidateID = rollbackInput.Generation.CandidateID
	rollbackInput.Fence.LeaseID = "0198f2c0-7c7a-7f00-8a11-000000000207"
	rollbackInput.Seal.CatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000000208"
	rollbackInput.Commit.SnapshotID = 43
	rollbackInput.Seal.DuckLakeSnapshotID = 43
	rollbackDigest := admissionDigest('4')
	rollbackInput.Bundle.Artifact.Digest = rollbackDigest
	rollbackInput.Bundle.Artifact.ID = "artifact-" + strings.TrimPrefix(rollbackDigest, "sha256:")
	rollbackInput.Bundle.ArtifactLocator = "serving-artifacts/" + strings.TrimPrefix(rollbackDigest, "sha256:") + ".tar.gz"
	rollbackInput.Seal.ServingArtifactID, rollbackInput.Seal.ServingArtifactDigest = rollbackInput.Bundle.Artifact.ID, rollbackDigest
	rollbackInput.Generation.ServingArtifactDigest = rollbackDigest
	marker := catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-admission", GenerationID: rollbackInput.Generation.GenerationID, AttemptID: rollbackInput.Commit.AttemptID, LeaseEpoch: 1, RequestDigest: rollbackInput.Seal.RequestDigest, PlanDigest: rollbackInput.Generation.PlanDigest, Project: rollbackInput.Bundle.ProjectID.String(), Environment: string(rollbackInput.Bundle.Environment), PhysicalPoolID: rollbackInput.Seal.PhysicalPoolID}
	raw, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rollbackInput.Commit.CommitMarker = json.RawMessage(raw)
	seedGenerationAdmission(t, delivery, rollbackInput)
	if _, err := p.Exec(t.Context(), `CREATE OR REPLACE FUNCTION serving_state.reject_test_bundle() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected serving admission conflict'; END; $$; CREATE TRIGGER reject_test_bundle BEFORE INSERT ON serving_state.bundle FOR EACH ROW EXECUTE FUNCTION serving_state.reject_test_bundle()`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = p.Exec(t.Context(), `DROP TRIGGER IF EXISTS reject_test_bundle ON serving_state.bundle; DROP FUNCTION IF EXISTS serving_state.reject_test_bundle()`)
	}()
	if _, err := capability.CompleteBuildAndAdmit(t.Context(), rollbackInput); err == nil {
		t.Fatal("injected serving admission conflict unexpectedly succeeded")
	}
	var attemptState string
	if err := p.QueryRow(t.Context(), `SELECT state FROM delivery.delivery_build_attempt WHERE attempt_id=$1::uuid`, rollbackInput.Commit.AttemptID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != string(deploymentnative.AttemptRunning) {
		t.Fatalf("rollback left attempt state %q", attemptState)
	}
	var bindings, seals, generations, bundles int
	if err := p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery.delivery_build_artifact_binding WHERE attempt_id=$1::uuid), (SELECT count(*) FROM delivery.delivery_snapshot_seal WHERE seal_id=$2::uuid), (SELECT count(*) FROM delivery.delivery_generation WHERE generation_id=$3::uuid), (SELECT count(*) FROM serving_state.bundle WHERE generation_id=$3::uuid)`, rollbackInput.Commit.AttemptID, rollbackInput.Seal.SealID, rollbackInput.Generation.GenerationID).Scan(&bindings, &seals, &generations, &bundles); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || seals != 0 || generations != 0 || bundles != 0 {
		t.Fatalf("rollback retained partial admission: bindings=%d seals=%d generations=%d bundles=%d", bindings, seals, generations, bundles)
	}
}
