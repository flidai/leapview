package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestCommittedGenerationTxUsesCallerTransaction(t *testing.T) {
	db := deliveryTestDB(t)
	r := NewWithOptions(db, Options{ActivationAdmission: allowTestActivation, ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: &testActivationLineage{}})
	input, ids := prepareCommittedGenerationFixture(t, r, testDigest('c'))
	seedPhysicalRetentionFixture(t, db, ids.seal)
	identity := projectgraph.ServingIdentity{ProjectID: "project_lost_ack", Environment: "prod", GenerationID: ids.generation}
	if _, err := r.CommittedGenerationTx(t.Context(), nil, ids.target, identity); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing transaction = %v", err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := r.ActivateTx(t.Context(), tx, input); err != nil {
		t.Fatal(err)
	}
	proof, err := r.CommittedGenerationTx(t.Context(), tx, ids.target, identity)
	if err != nil || proof.PublicationID != ids.publication || proof.Identity != identity || proof.BindingFingerprint != testDigest('c') {
		t.Fatalf("proof inside activation transaction = %#v, %v", proof, err)
	}
	if _, err := r.CommittedGeneration(t.Context(), ids.target, identity); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uncommitted publication visible outside transaction: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommittedGeneration(t.Context(), ids.target, identity); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back publication has commit proof: %v", err)
	}
}

func TestCommittedGenerationRequiresDurablePublicationAndPreservesHistory(t *testing.T) {
	p := deliveryTestDB(t)
	lineage := &testActivationLineage{}
	r := NewWithOptions(p, Options{ActivationAdmission: allowTestActivation, ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: lineage})
	bindingFingerprint := testDigest('c')
	input, ids := prepareCommittedGenerationFixture(t, r, bindingFingerprint)
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID("project_lost_ack"), "prod", ids.generation)
	if err != nil {
		t.Fatal(err)
	}
	lineage.expected = ActivationLineageInput{TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: ids.generation, CompiledGraphDigest: testDigest('b')}

	if _, err := r.CommittedGeneration(t.Context(), ids.target, identity); !errors.Is(err, ErrNotFound) {
		t.Fatalf("qualified candidate with pending publication = %v, want ErrNotFound", err)
	}
	seedPhysicalRetentionFixture(t, p, ids.seal)

	// The database commits activation but the client loses its acknowledgement.
	// The committed-generation proof must be recoverable from PostgreSQL alone.
	r.db = &activateLostAckDB{Pool: p}
	if _, err := r.Activate(t.Context(), input); !errors.Is(err, errActivateCommitLostAck) {
		t.Fatalf("activation with lost acknowledgement = %v, want injected acknowledgement error", err)
	}
	proof, err := r.CommittedGeneration(t.Context(), ids.target, identity)
	if err != nil {
		t.Fatalf("committed-generation proof after lost acknowledgement: %v", err)
	}
	if proof.TargetID != ids.target || proof.Identity != identity || proof.PublicationID != ids.publication || proof.CandidateID != "0198f2c0-7c7a-7f00-8a11-000000001002" || proof.CandidateRevision != 1 || proof.SnapshotSealID != ids.seal.SealID || proof.ServingArtifactDigest != ids.seal.ServingArtifactDigest || proof.BindingFingerprint != bindingFingerprint {
		t.Fatalf("committed-generation proof = %#v", proof)
	}
	replay, err := r.Activate(t.Context(), input)
	if err != nil || !replay.Replay {
		t.Fatalf("activation replay = %#v, %v; want committed replay", replay, err)
	}

	// A second committed publication of the same generation proves the lookup
	// chooses the newest coherent commit deterministically.
	const (
		republishID = "0198f2c0-7c7a-7f00-8a11-000000001020"
		leaseID     = "0198f2c0-7c7a-7f00-8a11-000000001021"
		correlation = "0198f2c0-7c7a-7f00-8a11-000000001022"
	)
	requestDigest := testDigest('5')
	if _, err := r.CreatePublication(t.Context(), PublicationInput{
		PublicationID: republishID, TargetID: ids.target, GenerationID: ids.generation,
		CandidateID: proof.CandidateID, SnapshotSealID: proof.SnapshotSealID,
		ExpectedTargetRevision: 2, ActorID: "operator", RequestDigest: requestDigest,
	}); err != nil {
		t.Fatalf("create same-generation publication: %v", err)
	}
	lease, err := r.AcquireLease(t.Context(), LeaseInput{LeaseID: leaseID, TargetID: ids.target, OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Activate(t.Context(), ActivationInput{
		PublicationID: republishID, TargetID: ids.target, GenerationID: ids.generation,
		ExpectedTargetRevision: 2, RequestDigest: requestDigest, ActorID: "operator", CorrelationID: correlation,
		LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	}); err != nil {
		t.Fatalf("activate same-generation publication: %v", err)
	}
	proof, err = r.CommittedGeneration(t.Context(), ids.target, identity)
	if err != nil || proof.PublicationID != republishID {
		t.Fatalf("latest same-generation proof = %#v, %v; want publication %s", proof, err, republishID)
	}

	// Activate a successor, then prove the retained old generation still has
	// durable commit evidence while it is no longer selected by the pointer.
	next := prepareSecondActivationGeneration(t, r, ids.generation)
	const (
		nextPublicationID = "0198f2c0-7c7a-7f00-8a11-000000001030"
		nextLeaseID       = "0198f2c0-7c7a-7f00-8a11-000000001031"
		nextCorrelation   = "0198f2c0-7c7a-7f00-8a11-000000001032"
	)
	nextRequestDigest := testDigest('6')
	if _, err := r.CreatePublication(t.Context(), PublicationInput{
		PublicationID: nextPublicationID, TargetID: ids.target, GenerationID: next.generationID,
		CandidateID: next.candidateID, SnapshotSealID: next.sealID,
		ExpectedTargetRevision: 3, ActorID: "operator", RequestDigest: nextRequestDigest,
	}); err != nil {
		t.Fatalf("create successor publication: %v", err)
	}
	nextLease, err := r.AcquireLease(t.Context(), LeaseInput{LeaseID: nextLeaseID, TargetID: ids.target, OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	lineage.expected = ActivationLineageInput{TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: next.generationID, CompiledGraphDigest: testDigest('b')}
	if _, err := r.Activate(t.Context(), ActivationInput{
		PublicationID: nextPublicationID, TargetID: ids.target, GenerationID: next.generationID,
		ExpectedTargetRevision: 3, RequestDigest: nextRequestDigest, ActorID: "operator", CorrelationID: nextCorrelation,
		LeaseID: nextLease.LeaseID, OwnerID: nextLease.OwnerID, FencingEpoch: nextLease.FencingEpoch,
	}); err != nil {
		t.Fatalf("activate successor generation: %v", err)
	}
	active, err := r.Target(t.Context(), ids.target)
	if err != nil || active.ActiveGenerationID != next.generationID {
		t.Fatalf("active target after successor = %#v, %v", active, err)
	}
	proof, err = r.CommittedGeneration(t.Context(), ids.target, identity)
	if err != nil || proof.PublicationID != republishID {
		t.Fatalf("historical committed-generation proof = %#v, %v; want retained publication %s", proof, err, republishID)
	}

	if _, err := r.CreateTarget(t.Context(), TargetInput{TargetID: "target_other_project", ProjectID: "different_project", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	otherProject, _ := projectgraph.NewServingIdentity(projectgraph.ResourceID("different_project"), "prod", ids.generation)
	otherEnvironment, _ := projectgraph.NewServingIdentity(identity.ProjectID, "stage", ids.generation)
	missingGeneration, _ := projectgraph.NewServingIdentity(identity.ProjectID, identity.Environment, "0198f2c0-7c7a-7f00-8a11-000000001099")
	for name, args := range map[string]struct {
		target   string
		identity projectgraph.ServingIdentity
	}{
		"other target":      {target: "target_other_project", identity: identity},
		"other project":     {target: ids.target, identity: otherProject},
		"other environment": {target: ids.target, identity: otherEnvironment},
		"other generation":  {target: ids.target, identity: missingGeneration},
	} {
		if _, err := r.CommittedGeneration(t.Context(), args.target, args.identity); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s proof error = %v, want ErrNotFound", name, err)
		}
	}
	uppercaseIdentity := identity
	uppercaseIdentity.GenerationID = strings.ToUpper(identity.GenerationID)
	if _, err := r.CommittedGeneration(t.Context(), ids.target, uppercaseIdentity); !errors.Is(err, ErrInvalid) {
		t.Errorf("noncanonical generation id error = %v, want ErrInvalid", err)
	}
	if _, err := r.CommittedGeneration(t.Context(), " "+ids.target, identity); !errors.Is(err, ErrInvalid) {
		t.Errorf("noncanonical target id error = %v, want ErrInvalid", err)
	}
	if _, err := (*Repository)(nil).CommittedGeneration(t.Context(), ids.target, identity); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil repository error = %v, want ErrInvalid", err)
	}
}

func prepareCommittedGenerationFixture(t *testing.T, r *Repository, bindingFingerprint string) (ActivationInput, lostAckActivationIDs) {
	t.Helper()
	ctx := t.Context()
	ids := lostAckActivationIDs{
		target: "target_lost_ack", generation: "0198f2c0-7c7a-7f00-8a11-000000001005",
		publication: "0198f2c0-7c7a-7f00-8a11-000000001006",
	}
	const (
		planID      = "0198f2c0-7c7a-7f00-8a11-000000001001"
		candidateID = "0198f2c0-7c7a-7f00-8a11-000000001002"
		attemptID   = "0198f2c0-7c7a-7f00-8a11-000000001003"
		sealID      = "0198f2c0-7c7a-7f00-8a11-000000001004"
		leaseID     = "0198f2c0-7c7a-7f00-8a11-000000001007"
		projectID   = "project_lost_ack"
	)
	artifactDigest := testDigest('e')
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: ids.target, ProjectID: projectID, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	rich, _ := richPlanDocumentFixture(t, planID, ids.target, projectID)
	rich.Governance.RequiresApproval = false
	rich, err := deploymentdomain.NewDeliveryPlan(rich)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(rich)
	if err != nil {
		t.Fatal(err)
	}
	planInput := planDocumentProjectionFixture(t, rich, document)
	planInput.Evidence = []byte(`{"qualification":"none"}`)
	plan, err := r.CreatePlan(ctx, planInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: candidateID, TargetID: ids.target, PlanID: planID, CandidateRevision: 1, ArtifactDigest: artifactDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder-lost-ack", PhysicalPoolID: "pool-lost-ack", CatalogID: "catalog-lost-ack", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: plan.PlanDigest, Namespace: "candidate/lost-ack", SessionIdentity: "session-lost-ack", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: "artifact-lost-ack", ServingArtifactDigest: artifactDigest, ServingStateID: "generation-test", OwnerID: "builder-lost-ack", FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder-lost-ack", FencingEpoch: 1, SnapshotID: 42, CommitMarker: testCommitMarker(attemptID, "pool-lost-ack", testDigest('f'), plan.PlanDigest)}); err != nil {
		t.Fatal(err)
	}
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: candidateID, SourceDigest: artifactDigest, BindingGeneration: bindingFingerprint,
		RuntimeVersion: "runtime-v1", DuckDBVersion: "1", Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 1, MaxMillis: 100},
		Outcome: release.GateSuccess, EvaluatedAt: time.Now().UTC(),
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	qualificationEvidence, err := json.Marshal(struct {
		Gates release.GateEvidence `json:"gates"`
	}{Gates: gate})
	if err != nil {
		t.Fatal(err)
	}
	sealInput := SnapshotSealInput{
		SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: "pool-lost-ack",
		TenantDomain: "tenant-lost-ack", Region: "us-east", EncryptionDomain: "enc-lost-ack", ObjectNamespace: "objects/lost-ack",
		CatalogDatabase: "ducklake", CatalogID: "catalog-lost-ack", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000001008",
		CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/lost-ack", RelationManifestDigest: testDigest('1'),
		ClosureDigest: testDigest('8'), ObjectRoot: "objects/lost-ack/42", ObjectRootDigest: testDigest('6'),
		ArtifactRoot: "artifacts/" + artifactDigest, ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'),
		CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), AuthorizationPolicyRevision: 1,
		AuthorizationPolicyDigest: testDigest('a'), RequestDigest: testDigest('f'), PlanDigest: plan.PlanDigest,
		CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-lost-ack", ServingArtifactDigest: artifactDigest,
		DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1",
		CatalogSchemaVersion: "1", QualificationEvidence: qualificationEvidence,
	}
	ids.seal = sealInput
	if _, err := r.CreateSnapshotSeal(ctx, sealInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, candidateID, sealID, testDigest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateGeneration(ctx, GenerationInput{
		GenerationID: ids.generation, TargetID: ids.target, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID,
		PlanDigest: plan.PlanDigest, ArtifactRoot: sealInput.ArtifactRoot, ArtifactRootDigest: sealInput.ArtifactRootDigest,
		ServingArtifactDigest: artifactDigest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'),
		SecurityDomainFingerprint: testDigest('d'), GenerationRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	requestDigest := testDigest('4')
	if _, err := r.CreatePublication(ctx, PublicationInput{
		PublicationID: ids.publication, TargetID: ids.target, GenerationID: ids.generation, CandidateID: candidateID,
		SnapshotSealID: sealID, ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: requestDigest,
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := r.AcquireLease(ctx, LeaseInput{LeaseID: leaseID, TargetID: ids.target, OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return ActivationInput{
		PublicationID: ids.publication, TargetID: ids.target, GenerationID: ids.generation,
		ExpectedTargetRevision: 1, RequestDigest: requestDigest, ActorID: "operator",
		CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000001009", LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	}, ids
}
