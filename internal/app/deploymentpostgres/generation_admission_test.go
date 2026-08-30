package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
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
	artifactDigest := admissionDigest('e')
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project_admission", Kind: projectgraph.KindProject, Name: "project"}, {ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"}}, []projectgraph.Edge{{From: "project_admission", To: "dashboard", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	planRecord := nativePlanFixture(t, deploymentnative.PlanInput{
		PlanID: planID, TargetID: "target-admission", PlanRevision: 1,
		CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: admissionDigest('c'),
		SecurityDomainFingerprint: admissionDigest('d'), ArtifactDigest: artifactDigest,
		QualificationDigest: admissionDigest('3'),
	}, "project_admission")
	planDigest := planRecord.PlanDigest
	relationNamespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{CandidateID: candidateID, AttemptID: attemptID, FencingEpoch: 1})
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
		Commit:              CommitEvidence{DeliveryID: "delivery-admission", AttemptID: attemptID, OwnerID: "builder-admission", FencingEpoch: 1, SnapshotID: 42, CommitMarker: json.RawMessage(markerJSON)},
		Seal:                SnapshotSealEvidence{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: pool, TenantDomain: "tenant", Region: "us-east", EncryptionDomain: "enc", ObjectNamespace: "objects/admission", CatalogDatabase: "ducklake", CatalogID: "catalog-admission", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000108", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: relationNamespace, RelationManifestDigest: admissionDigest('1'), ClosureDigest: admissionDigest('8'), ObjectRoot: "objects/admission/42", ObjectRootDigest: admissionDigest('6'), ArtifactRoot: "artifacts/admission", ArtifactRootDigest: admissionDigest('7'), CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: admissionDigest('c'), SecurityDomainFingerprint: admissionDigest('d'), RequestDigest: admissionDigest('f'), PlanDigest: planDigest, CompatibilityDigest: admissionDigest('2'), ServingArtifactID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: json.RawMessage(`{"checks":["schema"]}`)},
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
		{name: "commit marker delivery", mutate: func(in *GenerationAdmissionInput) {
			in.Commit.DeliveryID = "delivery-other"
		}},
		{name: "locator", mutate: func(in *GenerationAdmissionInput) { in.Bundle.ArtifactLocator = "objects/not-the-digest.tar.gz" }},
		{name: "artifact digest", mutate: func(in *GenerationAdmissionInput) { in.Bundle.Artifact.Digest = admissionDigest('4') }},
		{name: "relation namespace", mutate: func(in *GenerationAdmissionInput) { in.Seal.RelationNamespace = "_not_the_canonical_namespace" }},
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

type nilDuckLakeAuthority struct{}

func (*nilDuckLakeAuthority) Configured() bool { return true }
func (*nilDuckLakeAuthority) CommitAttemptTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.CommitAttemptInput) (ducklakepostgres.AttemptEvidence, error) {
	return ducklakepostgres.AttemptEvidence{}, nil
}
func (*nilDuckLakeAuthority) BindGenerationTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.GenerationBinding) (ducklakepostgres.GenerationBinding, error) {
	return ducklakepostgres.GenerationBinding{}, nil
}

type tamperingDuckLakeAuthority struct {
	inner        DuckLakeAuthority
	tamperCommit bool
	tamperBind   bool
}

func (a *tamperingDuckLakeAuthority) Configured() bool {
	return a != nil && a.inner != nil && a.inner.Configured()
}

func (a *tamperingDuckLakeAuthority) CommitAttemptTx(ctx context.Context, tx ducklakepostgres.Tx, in ducklakepostgres.CommitAttemptInput) (ducklakepostgres.AttemptEvidence, error) {
	got, err := a.inner.CommitAttemptTx(ctx, tx, in)
	if err == nil && a.tamperCommit {
		got.OwnerID = "tampered-owner"
	}
	return got, err
}

func (a *tamperingDuckLakeAuthority) BindGenerationTx(ctx context.Context, tx ducklakepostgres.Tx, in ducklakepostgres.GenerationBinding) (ducklakepostgres.GenerationBinding, error) {
	got, err := a.inner.BindGenerationTx(ctx, tx, in)
	if err == nil && a.tamperBind {
		got.GenerationID = "tampered-generation"
	}
	return got, err
}

func TestNewGenerationAdmissionRejectsNilAuthorities(t *testing.T) {
	db := deploymentPostgresDBStub{}
	configuredDelivery := deploymentnative.New(db)
	configuredServing := servingnative.New(db)
	configuredDuckLake := ducklakepostgres.New(db)
	cases := []struct {
		name     string
		delivery *deploymentnative.Repository
		serving  *servingnative.Repository
		ducklake DuckLakeAuthority
	}{
		{name: "nil delivery", serving: configuredServing, ducklake: configuredDuckLake},
		{name: "nil serving", delivery: configuredDelivery, ducklake: configuredDuckLake},
		{name: "typed nil ducklake", delivery: configuredDelivery, serving: configuredServing, ducklake: (*nilDuckLakeAuthority)(nil)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got, err := NewGenerationAdmission(test.delivery, test.serving, test.ducklake); err == nil || got != nil {
				t.Fatalf("NewGenerationAdmission() = (%v, %v), want nil capability and error", got, err)
			}
		})
	}
}

func TestGenerationAdmissionRejectsTamperedDuckLakeEvidence(t *testing.T) {
	input := validGenerationAdmissionInput(t)
	now := time.Now().UTC()
	attempt := ducklakepostgres.AttemptEvidence{
		AttemptID: input.Commit.AttemptID, RequestDigest: input.Seal.RequestDigest, PlanDigest: input.Generation.PlanDigest,
		PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogID: input.Seal.CatalogID, OwnerID: input.Commit.OwnerID,
		FencingEpoch: input.Commit.FencingEpoch, State: ducklakepostgres.AttemptCommitted, SnapshotID: input.Commit.SnapshotID,
		LeaseExpiresAt: now.Add(time.Hour), SessionIdentity: "duckdb-session-test", CommitMarker: string(input.Commit.CommitMarker), CreatedAt: now, UpdatedAt: now, TerminalAt: now,
	}
	if err := verifyDuckLakeAttempt(attempt, input); err != nil {
		t.Fatalf("verify valid DuckLake attempt: %v", err)
	}
	tampered := attempt
	tampered.SnapshotID++
	if err := verifyDuckLakeAttempt(tampered, input); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("verify tampered DuckLake attempt error = %v, want conflict", err)
	}

	binding := ducklakepostgres.GenerationBinding{
		DeliveryID: input.Commit.DeliveryID, GenerationID: input.Generation.GenerationID, AttemptID: input.Commit.AttemptID,
		PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogID: input.Seal.CatalogID, SnapshotID: input.Commit.SnapshotID,
		RelationManifestDigest: input.Seal.RelationManifestDigest, CompatibilityDigest: input.Seal.CompatibilityDigest,
		ServingArtifactDigest: input.Seal.ServingArtifactDigest, RequestDigest: input.Seal.RequestDigest, PlanDigest: input.Seal.PlanDigest,
		FencingEpoch: input.Commit.FencingEpoch, BoundAt: now,
	}
	if err := verifyDuckLakeBinding(binding, input); err != nil {
		t.Fatalf("verify valid DuckLake binding: %v", err)
	}
	binding.GenerationID = "tampered-generation"
	if err := verifyDuckLakeBinding(binding, input); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("verify tampered DuckLake binding error = %v, want conflict", err)
	}
}

func TestGenerationAdmissionJSONEvidencePreservesIntegerPrecision(t *testing.T) {
	if sameJSON([]byte(`{"value":9007199254740992}`), []byte(`{"value":9007199254740993}`)) {
		t.Fatal("sameJSON equated distinct large integer evidence")
	}
	if !sameJSON([]byte(`{"value":9007199254740993}`), []byte(`{"value":9007199254740993}`)) {
		t.Fatal("sameJSON rejected equal large integer evidence")
	}
	if sameJSON([]byte(`{"value":1,"value":1}`), []byte(`{"value":1}`)) {
		t.Fatal("sameJSON accepted duplicate-key evidence")
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
	if err := ducklakepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func seedGenerationAdmission(t *testing.T, repo *deploymentnative.Repository, ducklake *ducklakepostgres.Repository, input GenerationAdmissionInput) {
	t.Helper()
	ctx := t.Context()
	leaseExpiresAt := timeNowPlusHour().Truncate(time.Microsecond)
	const sessionIdentity = "duckdb-session-admission"
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
		plan := nativePlanFixture(t, deploymentnative.PlanInput{PlanID: input.Generation.PlanID, TargetID: input.Generation.TargetID, PlanRevision: 1, CompiledGraphDigest: input.Generation.CompiledGraphDigest, CompiledConfigDigest: input.Generation.CompiledConfigDigest, SecurityDomainFingerprint: input.Generation.SecurityDomainFingerprint, ArtifactDigest: input.Generation.ServingArtifactDigest, QualificationDigest: input.QualificationDigest}, input.Bundle.ProjectID.String())
		if plan.PlanDigest != input.Generation.PlanDigest {
			t.Fatalf("native plan fixture digest = %s, generation expects %s", plan.PlanDigest, input.Generation.PlanDigest)
		}
		if _, err := repo.CreatePlan(ctx, plan); err != nil {
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
	if _, _, err := repo.AcquireLeaseAndBeginBuildAttemptTx(ctx, tx, deploymentnative.LeaseInput{LeaseID: input.Fence.LeaseID, TargetID: input.Fence.TargetID, OwnerID: input.Fence.OwnerID, ExpiresAt: leaseExpiresAt}, deploymentnative.BuildAttemptInput{AttemptID: input.Commit.AttemptID, PlanID: input.Generation.PlanID, CandidateID: input.Generation.CandidateID, OwnerID: input.Commit.OwnerID, PhysicalPoolID: input.Seal.PhysicalPoolID, RequestDigest: input.Seal.RequestDigest, PlanDigest: input.Generation.PlanDigest, Namespace: input.Seal.RelationNamespace, SessionIdentity: sessionIdentity, LeaseExpiresAt: leaseExpiresAt}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ducklake.RegisterCatalog(ctx, ducklakepostgres.CatalogIdentity{
		PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogDatabase: input.Seal.CatalogDatabase,
		CatalogID: input.Seal.CatalogID, CatalogUUID: input.Seal.CatalogUUID, MetadataSchema: "main",
		CompatibilityDigest: input.Seal.CompatibilityDigest, CatalogSchemaVersion: input.Seal.CatalogSchemaVersion,
	}); err != nil && !errors.Is(err, ducklakepostgres.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := ducklake.BeginAttempt(ctx, ducklakepostgres.BeginAttemptInput{
		AttemptID: input.Commit.AttemptID, RequestDigest: input.Seal.RequestDigest, PlanDigest: input.Generation.PlanDigest,
		PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogID: input.Seal.CatalogID, OwnerID: input.Commit.OwnerID,
		FencingEpoch: input.Commit.FencingEpoch, SessionIdentity: sessionIdentity, LeaseExpiresAt: leaseExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func timeNowPlusHour() time.Time { return time.Now().UTC().Add(time.Hour) }

func TestGenerationAdmissionPostgresAtomicSuccessReplayAndRollback(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	serving := servingnative.New(p)
	ducklake := ducklakepostgres.New(p)
	if _, err := NewGenerationAdmission(delivery, serving, nil); err == nil {
		t.Fatal("generation admission accepted a nil DuckLake authority")
	}
	var unconfigured *ducklakepostgres.Repository
	if _, err := NewGenerationAdmission(delivery, serving, unconfigured); err == nil {
		t.Fatal("generation admission accepted an unconfigured DuckLake authority")
	}
	capability, err := NewGenerationAdmission(delivery, serving, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, ducklake, input)
	tamperedCapability, err := NewGenerationAdmission(delivery, serving, &tamperingDuckLakeAuthority{inner: ducklake, tamperCommit: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamperedCapability.CompleteBuildAndAdmit(t.Context(), input); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("tampered DuckLake authority error = %v, want conflict", err)
	}
	if attempt, err := ducklake.LoadAttempt(t.Context(), input.Commit.AttemptID); err != nil {
		t.Fatalf("load rolled-back DuckLake attempt: %v", err)
	} else if attempt.State != ducklakepostgres.AttemptRunning {
		t.Fatalf("tampered authority changed DuckLake attempt state to %q", attempt.State)
	}
	first, err := capability.CompleteBuildAndAdmit(t.Context(), input)
	if err != nil {
		t.Fatalf("complete and admit: %v", err)
	}
	if first.Generation.GenerationID != input.Generation.GenerationID || first.Generation.GenerationRevision != 1 || first.Bundle.ArtifactLocator != input.Bundle.ArtifactLocator {
		t.Fatalf("admission result = %#v", first)
	}
	attemptEvidence, err := ducklake.LoadAttempt(t.Context(), input.Commit.AttemptID)
	if err != nil {
		t.Fatalf("load DuckLake attempt evidence: %v", err)
	}
	gotMarker, markerErr := catalogartifact.DecodeCommitMarker([]byte(attemptEvidence.CommitMarker))
	wantMarker, wantMarkerErr := catalogartifact.DecodeCommitMarker(input.Commit.CommitMarker)
	if markerErr != nil || wantMarkerErr != nil || gotMarker != wantMarker || attemptEvidence.State != ducklakepostgres.AttemptCommitted || attemptEvidence.SnapshotID != input.Commit.SnapshotID {
		t.Fatalf("DuckLake attempt evidence = %#v", attemptEvidence)
	}
	binding, err := ducklake.LoadBinding(t.Context(), input.Commit.DeliveryID, input.Generation.GenerationID)
	if err != nil {
		t.Fatalf("load DuckLake generation binding: %v", err)
	}
	if binding.AttemptID != input.Commit.AttemptID || binding.PhysicalPoolID != input.Seal.PhysicalPoolID || binding.CatalogID != input.Seal.CatalogID || binding.SnapshotID != input.Commit.SnapshotID {
		t.Fatalf("DuckLake generation binding = %#v", binding)
	}
	root, err := ducklake.LoadSnapshotRoot(t.Context(), input.Commit.AttemptID)
	if err != nil {
		t.Fatalf("load DuckLake generation retention root: %v", err)
	}
	if root.Kind != ducklakepostgres.RootGeneration || root.State != "live" || root.PhysicalPoolID != input.Seal.PhysicalPoolID || root.CatalogID != input.Seal.CatalogID || root.SnapshotID != input.Commit.SnapshotID {
		t.Fatalf("DuckLake retention root = %#v", root)
	}
	retention, err := ducklake.LoadSnapshotRetention(t.Context(), ducklakepostgres.SnapshotRef{PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogID: input.Seal.CatalogID, SnapshotID: input.Commit.SnapshotID})
	if err != nil {
		t.Fatalf("load DuckLake retention state: %v", err)
	}
	if retention.State != ducklakepostgres.RetentionLive {
		t.Fatalf("DuckLake retention state = %q", retention.State)
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
	seedGenerationAdmission(t, delivery, ducklake, rollbackInput)
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
	ducklakeAttempt, err := ducklake.LoadAttempt(t.Context(), rollbackInput.Commit.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if ducklakeAttempt.State != ducklakepostgres.AttemptRunning {
		t.Fatalf("DuckLake rollback left attempt state %q", ducklakeAttempt.State)
	}
	var bindings, seals, generations, bundles int
	if err := p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery.delivery_build_artifact_binding WHERE attempt_id=$1::uuid), (SELECT count(*) FROM delivery.delivery_snapshot_seal WHERE seal_id=$2::uuid), (SELECT count(*) FROM delivery.delivery_generation WHERE generation_id=$3::uuid), (SELECT count(*) FROM serving_state.bundle WHERE generation_id=$3::uuid)`, rollbackInput.Commit.AttemptID, rollbackInput.Seal.SealID, rollbackInput.Generation.GenerationID).Scan(&bindings, &seals, &generations, &bundles); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || seals != 0 || generations != 0 || bundles != 0 {
		t.Fatalf("rollback retained partial admission: bindings=%d seals=%d generations=%d bundles=%d", bindings, seals, generations, bundles)
	}
	if _, err := ducklake.LoadBinding(t.Context(), rollbackInput.Commit.DeliveryID, rollbackInput.Generation.GenerationID); !errors.Is(err, ducklakepostgres.ErrNotFound) {
		t.Fatalf("rollback retained DuckLake generation binding, err=%v", err)
	}
	if _, err := ducklake.LoadSnapshotRoot(t.Context(), rollbackInput.Commit.AttemptID); !errors.Is(err, ducklakepostgres.ErrNotFound) {
		t.Fatalf("rollback retained DuckLake generation root, err=%v", err)
	}
	if _, err := ducklake.LoadSnapshotRetention(t.Context(), ducklakepostgres.SnapshotRef{PhysicalPoolID: rollbackInput.Seal.PhysicalPoolID, CatalogID: rollbackInput.Seal.CatalogID, SnapshotID: rollbackInput.Commit.SnapshotID}); !errors.Is(err, ducklakepostgres.ErrNotFound) {
		t.Fatalf("rollback retained DuckLake snapshot retention, err=%v", err)
	}
}

func TestGenerationAdmissionTxComposesAdjacentMutation(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	serving := servingnative.New(p)
	ducklake := ducklakepostgres.New(p)
	admission, err := NewGenerationAdmission(delivery, serving, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, ducklake, input)

	tx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := admission.CompleteBuildAndAdmitTx(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("complete and admit in caller transaction: %v", err)
	}
	if result.Generation.GenerationID != input.Generation.GenerationID {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admission result = %#v", result)
	}
	adjacent := deploymentnative.TargetInput{TargetID: "target-generation-admission-adjacent", ProjectID: "project_admission", Environment: "staging"}
	if _, err := delivery.CreateTargetTx(t.Context(), tx, adjacent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("adjacent mutation after caller-owned admission: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit composed transaction: %v", err)
	}
	if _, err := delivery.Target(t.Context(), adjacent.TargetID); err != nil {
		t.Fatalf("adjacent mutation was not committed with admission: %v", err)
	}
}
