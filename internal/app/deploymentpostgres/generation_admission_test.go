package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func admissionDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

type testManagedDataBindingAdmission struct {
	calls int
	pins  []release.ManagedDataPin
	err   error
}

type testCandidateProvenanceAdmission struct {
	calls    int
	retained []release.Provenance
	err      error
}

func (a *testCandidateProvenanceAdmission) Configured() bool { return a != nil }

func (a *testCandidateProvenanceAdmission) RetainCandidateProvenanceTx(_ context.Context, _ releasepostgres.Tx, _ projectgraph.ResourceID, provenance release.Provenance) (release.Provenance, error) {
	if a == nil {
		return release.Provenance{}, errors.New("nil test provenance authority")
	}
	a.calls++
	if a.err != nil {
		return release.Provenance{}, a.err
	}
	a.retained = append(a.retained, provenance)
	return provenance, nil
}

func (a *testManagedDataBindingAdmission) AdmitServingStateBindingsTx(_ context.Context, _ deploymentnative.Tx, _ projectgraph.ServingIdentity, pins []release.ManagedDataPin) error {
	a.calls++
	a.pins = make([]release.ManagedDataPin, len(pins))
	copy(a.pins, pins)
	return a.err
}

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
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: candidateID, SourceDigest: admissionDigest('a'),
		BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime", DuckDBVersion: "1",
		Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}, Outcome: release.GateSuccess,
		EvaluatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return GenerationAdmissionInput{
		Commit:              CommitEvidence{DeliveryID: "delivery-admission", AttemptID: attemptID, OwnerID: "builder-admission", FencingEpoch: 1, SnapshotID: 42, CommitMarker: json.RawMessage(markerJSON)},
		Seal:                SnapshotSealEvidence{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: pool, TenantDomain: "tenant", Region: "us-east", EncryptionDomain: "enc", ObjectNamespace: "objects/admission", CatalogDatabase: "ducklake", CatalogID: "catalog-admission", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000108", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: relationNamespace, RelationManifestDigest: admissionDigest('1'), ClosureDigest: admissionDigest('8'), ObjectRoot: "objects/admission/42", ObjectRootDigest: admissionDigest('6'), ArtifactRoot: "artifacts/admission", ArtifactRootDigest: admissionDigest('7'), CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: admissionDigest('c'), SecurityDomainFingerprint: admissionDigest('d'), RequestDigest: admissionDigest('f'), PlanDigest: planDigest, CompatibilityDigest: admissionDigest('2'), ServingArtifactID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: json.RawMessage(`{"checks":["schema"]}`)},
		QualificationDigest: admissionDigest('3'),
		CandidateExpiresAt:  time.Date(2099, 1, 1, 13, 0, 0, 0, time.UTC),
		Fence:               LeaseFenceEvidence{LeaseID: leaseID, TargetID: "target-admission", OwnerID: "builder-admission", FencingEpoch: 1},
		Generation:          GenerationEvidence{GenerationID: genID, TargetID: "target-admission", CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: "artifacts/admission", ArtifactRootDigest: admissionDigest('7'), ServingArtifactDigest: artifactDigest, CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: admissionDigest('c'), SecurityDomainFingerprint: admissionDigest('d')},
		Bundle:              BundleEvidenceInput{GenerationID: genID, ProjectID: "project_admission", Environment: "prod", Artifact: servingstate.Artifact{ID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingStateID: servingstate.ID(genID), Digest: artifactDigest, Format: projectbundle.BundleFormat, ManifestJSON: manifest, SizeBytes: 1}, ArtifactLocator: "serving-artifacts/" + strings.TrimPrefix(artifactDigest, "sha256:") + ".tar.gz", StorageSecurityDomain: "runtime", ArtifactContentType: projectbundle.BundleContentType, ArtifactMetadataDigest: admissionDigest('9'), ProjectDigest: admissionDigest('b'), AccessPolicyJSON: `{}`, DashboardPublicationsJSON: `{}`, DashboardAppearancesJSON: `{}`, CreatedBy: "builder-admission"},
		Graph:               graph,
		ManagedDataPins:     []release.ManagedDataPin{},
		Provenance: release.ProvenanceInput{
			Artifact:  release.ProjectArtifactProvenance{SourceDigest: admissionDigest('a'), ProjectDigest: admissionDigest('b'), ContentDigest: artifactDigest, CompilerVersion: "compiler", SchemaVersion: 1},
			Candidate: release.CandidateProvenance{ID: candidateID, OwnerID: "builder-admission"},
			Plan: release.GenerationPlanProvenance{
				Identity: projectgraph.ServingIdentity{ProjectID: "project_admission", Environment: "prod", GenerationID: genID}, TargetID: "target-admission", RuntimeVersion: "runtime", PolicyDigest: admissionDigest('d'), DataRevision: "sources:admission", DataMode: release.GenerationDataRefreshSources,
				AuthoredConnections: []release.AuthoredConnectionEvidence{{ConnectionID: "connection_admission", ConnectorKind: "postgres"}}, GateEvidence: &gate,
			},
		},
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

func TestNormalizeGenerationAdmissionRejectsInvalidCandidateExpiry(t *testing.T) {
	for name, expiry := range map[string]time.Time{
		"missing": {},
		"non-UTC": time.Date(2099, 1, 1, 13, 0, 0, 0, time.FixedZone("candidate-expiry", 3600)),
	} {
		t.Run(name, func(t *testing.T) {
			input := validGenerationAdmissionInput(t)
			input.CandidateExpiresAt = expiry
			if _, err := normalizeInput(input); err == nil || !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("normalize candidate expiry error = %v, want native invalid", err)
			}
		})
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
		{name: "missing managed-data pins", mutate: func(in *GenerationAdmissionInput) { in.ManagedDataPins = nil }},
		{name: "invalid managed-data revision", mutate: func(in *GenerationAdmissionInput) {
			in.ManagedDataPins = []release.ManagedDataPin{{ConnectionID: "orders", RevisionID: "revision-1"}}
		}},
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

func TestNormalizeGenerationAdmissionCanonicalizesManagedDataPins(t *testing.T) {
	input := validGenerationAdmissionInput(t)
	input.ManagedDataPins = []release.ManagedDataPin{
		{ConnectionID: "orders", RevisionID: admissionDigest('b')},
		{ConnectionID: "customers", RevisionID: admissionDigest('a')},
	}
	got, err := normalizeInput(input)
	if err != nil {
		t.Fatalf("normalize managed-data pins: %v", err)
	}
	if len(got.ManagedDataPins) != 2 || got.ManagedDataPins[0].ConnectionID != "customers" || got.ManagedDataPins[1].ConnectionID != "orders" {
		t.Fatalf("managed-data pins order = %#v", got.ManagedDataPins)
	}
	got.ManagedDataPins[0].ConnectionID = "mutated"
	if input.ManagedDataPins[0].ConnectionID != "orders" {
		t.Fatal("normalization did not clone managed-data pins")
	}
}

func TestNewGenerationAdmissionRejectsNilAuthorities(t *testing.T) {
	db := deploymentPostgresDBStub{}
	configuredDelivery := deploymentnative.New(db)
	configuredServing := servingnative.New(db)
	configuredProvenance := &testCandidateProvenanceAdmission{}
	cases := []struct {
		name        string
		delivery    *deploymentnative.Repository
		serving     *servingnative.Repository
		managedData NativeManagedDataBindingAdmission
		provenance  NativeCandidateProvenanceAdmission
	}{
		{name: "nil delivery", serving: configuredServing, managedData: &testManagedDataBindingAdmission{}, provenance: configuredProvenance},
		{name: "nil serving", delivery: configuredDelivery, managedData: &testManagedDataBindingAdmission{}, provenance: configuredProvenance},
		{name: "nil managed data", delivery: configuredDelivery, serving: configuredServing, provenance: configuredProvenance},
		{name: "typed nil managed data", delivery: configuredDelivery, serving: configuredServing, managedData: (*testManagedDataBindingAdmission)(nil), provenance: configuredProvenance},
		{name: "nil provenance", delivery: configuredDelivery, serving: configuredServing, managedData: &testManagedDataBindingAdmission{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got, err := NewGenerationAdmission(test.delivery, test.serving, candidatePhysicalAdmissionStub{}, test.managedData, test.provenance); err == nil || got != nil {
				t.Fatalf("NewGenerationAdmission() = (%v, %v), want nil capability and error", got, err)
			}
		})
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
	if err := releasepostgres.ApplySchema(t.Context(), tx); err != nil {
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
	if _, _, err := repo.AcquireLeaseAndBeginBuildAttemptTx(ctx, tx, deploymentnative.LeaseInput{LeaseID: input.Fence.LeaseID, TargetID: input.Fence.TargetID, OwnerID: input.Fence.OwnerID, ExpiresAt: leaseExpiresAt}, deploymentnative.BuildAttemptInput{AttemptID: input.Commit.AttemptID, PlanID: input.Generation.PlanID, CandidateID: input.Generation.CandidateID, OwnerID: input.Commit.OwnerID, PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogID: input.Seal.CatalogID, RequestDigest: input.Seal.RequestDigest, PlanDigest: input.Generation.PlanDigest, Namespace: input.Seal.RelationNamespace, SessionIdentity: sessionIdentity, LeaseExpiresAt: leaseExpiresAt}); err != nil {
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
	managedData := &testManagedDataBindingAdmission{}
	provenance := releasepostgres.New(p)
	capability, err := NewGenerationAdmission(delivery, serving, candidatePhysicalAdmissionStub{}, managedData, provenance)
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)
	first, err := capability.CompleteBuildAndAdmit(t.Context(), input)
	if err != nil {
		t.Fatalf("complete and admit: %v", err)
	}
	if first.Generation.GenerationID != input.Generation.GenerationID || first.Generation.GenerationRevision != 1 || first.CandidateRevision != 1 || first.Bundle.ArtifactLocator != input.Bundle.ArtifactLocator {
		t.Fatalf("admission result = %#v", first)
	}
	retained, err := provenance.CandidateProvenance(t.Context(), input.Bundle.ProjectID, input.Generation.CandidateID, first.CandidateRevision)
	if err != nil {
		t.Fatalf("load committed candidate provenance: %v", err)
	}
	if retained.Candidate.ID != input.Generation.CandidateID || retained.Candidate.Revision != first.CandidateRevision || retained.Plan.Identity.GenerationID != input.Generation.GenerationID {
		t.Fatalf("committed candidate provenance = %#v", retained)
	}
	servingProvenance, err := provenance.ProvenanceForServingState(t.Context(), retained.Plan.Identity)
	if err != nil {
		t.Fatalf("load committed provenance by serving identity: %v", err)
	}
	if servingProvenance.Digest != retained.Digest {
		t.Fatalf("serving provenance digest = %q, want %q", servingProvenance.Digest, retained.Digest)
	}
	if managedData.calls < 1 || managedData.pins == nil || len(managedData.pins) != 0 {
		t.Fatalf("managed-data binding admission calls=%d pins=%#v, want nonnil empty pins", managedData.calls, managedData.pins)
	}
	var rootTarget, rootCandidate, rootGeneration, rootSeal, rootKind, rootState string
	var rootExpiry time.Time
	if err := p.QueryRow(t.Context(), `SELECT target_id,candidate_id::text,generation_id::text,snapshot_seal_id::text,root_kind,state,expires_at FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, input.Generation.CandidateID).Scan(&rootTarget, &rootCandidate, &rootGeneration, &rootSeal, &rootKind, &rootState, &rootExpiry); err != nil {
		t.Fatalf("load candidate delivery retention root: %v", err)
	}
	if rootTarget != input.Generation.TargetID || rootCandidate != input.Generation.CandidateID || rootGeneration != input.Generation.GenerationID || rootSeal != input.Seal.SealID || rootKind != "candidate" || rootState != "live" || !rootExpiry.Equal(input.CandidateExpiresAt) {
		t.Fatalf("candidate delivery retention root = %q %q %q %q %q %q expires=%v, want exact candidate tuple and expiry %v", rootTarget, rootCandidate, rootGeneration, rootSeal, rootKind, rootState, rootExpiry, input.CandidateExpiresAt)
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
	var retentionRoots int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, rollbackInput.Generation.CandidateID).Scan(&retentionRoots); err != nil {
		t.Fatal(err)
	}
	if retentionRoots != 0 {
		t.Fatalf("rollback retained candidate delivery retention root: %d", retentionRoots)
	}
	if _, err := provenance.CandidateProvenance(t.Context(), rollbackInput.Bundle.ProjectID, rollbackInput.Generation.CandidateID, 1); !errors.Is(err, releasepostgres.ErrNotFound) {
		t.Fatalf("rollback retained candidate provenance, err=%v", err)
	}
}

func TestGenerationAdmissionRejectsCandidateExpiryDrift(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	serving := servingnative.New(p)
	admission, err := NewGenerationAdmission(delivery, serving, candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, &testCandidateProvenanceAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)
	input.CandidateExpiresAt = input.CandidateExpiresAt.Add(time.Minute)
	if _, err := admission.CompleteBuildAndAdmit(t.Context(), input); err == nil || !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("candidate expiry drift error = %v, want native conflict", err)
	}
	var rootCount int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, input.Generation.CandidateID).Scan(&rootCount); err != nil {
		t.Fatal(err)
	}
	if rootCount != 0 {
		t.Fatalf("expiry-drift admission created candidate retention root: %d", rootCount)
	}
}

func TestGenerationAdmissionTxComposesAdjacentMutation(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	serving := servingnative.New(p)
	admission, err := NewGenerationAdmission(delivery, serving, candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, &testCandidateProvenanceAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)

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
