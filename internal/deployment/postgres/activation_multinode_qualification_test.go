package postgres

import (
	"errors"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgreSQL18MultiNodeActivationServingStateQualification exercises the
// activation authority with two independent PostgreSQL pools. A commit
// acknowledgement is lost after the first node's transaction commits; both
// nodes then retry the exact identity concurrently and must replay the one
// durable publication. A stale lease cannot mutate a later publication, and a
// second serving-state reader converges by rereading the target's durable
// active pointer rather than receiving an event notification.
func TestPostgreSQL18MultiNodeActivationServingStateQualification(t *testing.T) {
	ctx := t.Context()
	poolA := deliveryTestDB(t)
	poolB, err := pgxpool.New(ctx, poolA.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolB.Close)

	lineageA := &testActivationLineage{}
	lineageB := &testActivationLineage{}
	nodeA := NewWithOptions(poolA, Options{
		ActivationAudit: testActivationAudit{audit: accesspostgres.New()},
		Lineage:         lineageA,
	})
	nodeB := NewWithOptions(poolB, Options{
		ActivationAudit: testActivationAudit{audit: accesspostgres.New()},
		Lineage:         lineageB,
	})

	input, ids := prepareLostAckActivation(t, nodeA)
	lineageA.expected = ActivationLineageInput{TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: ids.generation}
	lineageB.expected = lineageA.expected
	second := prepareSecondActivationGeneration(t, nodeA, ids.generation)

	// The first node has committed authority but loses only the client-side
	// acknowledgement. The database state is therefore already replayable.
	nodeA.db = &activateLostAckDB{Pool: poolA}
	if _, err := nodeA.Activate(ctx, input); !errors.Is(err, errActivateCommitLostAck) {
		t.Fatalf("first activation error = %v, want lost acknowledgement", err)
	}

	// Independent nodes concurrently retry the exact activation identity. The
	// publication lock serializes the reads, while the committed evidence makes
	// both responses replays and keeps the target revision at exactly two.
	type activationCall struct {
		result ActivationResult
		err    error
	}
	replays := make(chan activationCall, 2)
	go func() {
		result, callErr := nodeA.Activate(ctx, input)
		replays <- activationCall{result: result, err: callErr}
	}()
	go func() {
		result, callErr := nodeB.Activate(ctx, input)
		replays <- activationCall{result: result, err: callErr}
	}()
	for i := 0; i < 2; i++ {
		call := <-replays
		if call.err != nil {
			t.Fatalf("concurrent exact replay: %v", call.err)
		}
		if !call.result.Replay || call.result.Pointer.TargetRevision != 2 || call.result.Pointer.ActiveGenerationID != ids.generation {
			t.Fatalf("concurrent exact replay result = %#v, want committed generation at revision 2", call.result)
		}
	}

	publication, err := nodeB.Publication(ctx, ids.publication)
	if err != nil {
		t.Fatal(err)
	}
	target, err := nodeB.Target(ctx, ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if publication.State != "committed" || publication.ResultTargetRevision != 2 || target.TargetRevision != 2 || target.ActiveGenerationID != ids.generation || target.ActivePublicationID != ids.publication {
		t.Fatalf("first activation state = publication %#v target %#v", publication, target)
	}
	var eventRows, auditRows int
	if err := poolB.QueryRow(ctx, `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, ids.publication).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if err := poolB.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, ids.publication).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if eventRows != 1 || auditRows != 1 {
		t.Fatalf("first activation evidence rows = event %d audit %d, want one each", eventRows, auditRows)
	}

	// This reader is a separate repository over a separate pool. It has no
	// event subscription; each call rereads the exact delivery pointer.
	reader := servingstatepostgres.New(poolB)
	scope, present, err := reader.ActiveScopeForTarget(ctx, ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if !present || scope.ProjectID.String() != "project_lost_ack" || string(scope.Environment) != "prod" {
		t.Fatalf("first serving scope = %#v present=%v", scope, present)
	}

	secondPublicationID := "0198f2c0-7c7a-7f00-8a11-000000001016"
	secondLeaseID := "0198f2c0-7c7a-7f00-8a11-000000001017"
	secondCorrelationID := "0198f2c0-7c7a-7f00-8a11-000000001018"
	secondRequestDigest := testDigest('9')
	if _, err := nodeB.CreatePublication(ctx, PublicationInput{
		PublicationID:          secondPublicationID,
		TargetID:               ids.target,
		GenerationID:           second.generationID,
		CandidateID:            second.candidateID,
		SnapshotSealID:         second.sealID,
		ExpectedTargetRevision: 2,
		ActorID:                "operator-2",
		RequestDigest:          secondRequestDigest,
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := nodeB.AcquireLease(ctx, LeaseInput{
		LeaseID:   secondLeaseID,
		TargetID:  ids.target,
		OwnerID:   "operator-2",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondInput := ActivationInput{
		PublicationID:          secondPublicationID,
		TargetID:               ids.target,
		GenerationID:           second.generationID,
		ExpectedTargetRevision: 2,
		RequestDigest:          secondRequestDigest,
		ActorID:                "operator-2",
		CorrelationID:          secondCorrelationID,
		LeaseID:                lease.LeaseID,
		OwnerID:                lease.OwnerID,
		FencingEpoch:           lease.FencingEpoch,
	}
	lineageA.expected.GenerationID = second.generationID
	lineageB.expected.GenerationID = second.generationID

	// Acquiring the second lease expired the first activation lease. Reusing
	// that old fence against the still-pending publication is rejected before
	// any pointer/CAS mutation can occur.
	stale := secondInput
	stale.LeaseID, stale.OwnerID, stale.FencingEpoch = input.LeaseID, input.OwnerID, input.FencingEpoch
	if _, err := nodeB.Activate(ctx, stale); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale competing activation error = %v, want ErrStaleFence", err)
	}
	pending, err := nodeB.Publication(ctx, secondPublicationID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending" || pending.ResultTargetRevision != 0 {
		t.Fatalf("stale activation changed publication = %#v", pending)
	}
	target, err = nodeB.Target(ctx, ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 2 || target.ActiveGenerationID != ids.generation {
		t.Fatalf("stale activation changed target = %#v", target)
	}

	activated, err := nodeA.Activate(ctx, secondInput)
	if err != nil {
		t.Fatalf("second activation: %v", err)
	}
	if activated.Replay || activated.Pointer.TargetRevision != 3 || activated.Pointer.ActiveGenerationID != second.generationID {
		t.Fatalf("second activation = %#v, want fresh revision 3", activated)
	}

	// The same independent serving reader converges only by rereading durable
	// state. No event/listener/cache path participates in this assertion.
	scope, present, err = reader.ActiveScopeForTarget(ctx, ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if !present || scope.ProjectID.String() != "project_lost_ack" || string(scope.Environment) != "prod" {
		t.Fatalf("second serving scope = %#v present=%v", scope, present)
	}
	active, err := nodeB.ActiveGeneration(ctx, ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if active.GenerationID != second.generationID || active.GenerationRevision != 2 {
		t.Fatalf("runtime-side active generation = %#v, want second generation", active)
	}
	finalPublication, err := nodeB.Publication(ctx, secondPublicationID)
	if err != nil {
		t.Fatal(err)
	}
	if finalPublication.State != "committed" || finalPublication.ResultTargetRevision != 3 {
		t.Fatalf("second publication = %#v", finalPublication)
	}
	target, err = nodeB.Target(ctx, ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 3 || target.ActiveGenerationID != second.generationID || target.ActivePublicationID != secondPublicationID {
		t.Fatalf("second activation target = %#v", target)
	}
}

type secondActivationGeneration struct {
	generationID string
	candidateID  string
	sealID       string
}

// prepareSecondActivationGeneration admits the next immutable generation
// while the target still has no active pointer. Its publication is deferred
// until after generation one activates so CreatePublication's base-generation
// CAS proof is exercised by the real repository.
func prepareSecondActivationGeneration(t *testing.T, r *Repository, firstGenerationID string) secondActivationGeneration {
	t.Helper()
	ctx := t.Context()
	const (
		candidateID  = "0198f2c0-7c7a-7f00-8a11-000000001012"
		attemptID    = "0198f2c0-7c7a-7f00-8a11-000000001013"
		sealID       = "0198f2c0-7c7a-7f00-8a11-000000001014"
		generationID = "0198f2c0-7c7a-7f00-8a11-000000001015"
	)
	first, err := r.Generation(ctx, firstGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	firstSeal, err := r.SnapshotSeal(ctx, first.SnapshotSealID)
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt, err := r.BuildAttempt(ctx, firstSeal.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: candidateID, TargetID: first.TargetID, PlanID: first.PlanID, CandidateRevision: 2, ArtifactDigest: first.ServingArtifactDigest}); err != nil {
		t.Fatal(err)
	}
	const owner = "builder-lost-ack-2"
	const pool = "pool-lost-ack-2"
	const catalog = "catalog-lost-ack-2"
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{
		AttemptID: attemptID, PlanID: first.PlanID, CandidateID: candidateID,
		OwnerID: owner, PhysicalPoolID: pool, CatalogID: catalog, FencingEpoch: 1,
		RequestDigest: firstAttempt.RequestDigest, PlanDigest: firstAttempt.PlanDigest,
		Namespace: "candidate/lost-ack-2", SessionIdentity: "session-lost-ack-2",
		LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	const artifactID = "artifact-lost-ack-2"
	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: artifactID, ServingArtifactDigest: first.ServingArtifactDigest, ServingStateID: "generation-test", OwnerID: owner, FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	marker := testCommitMarker(attemptID, pool, firstAttempt.RequestDigest, firstAttempt.PlanDigest)
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attemptID, OwnerID: owner, FencingEpoch: 1, SnapshotID: firstSeal.DuckLakeSnapshotID + 1, CommitMarker: marker}); err != nil {
		t.Fatal(err)
	}
	secondSeal := firstSeal
	secondSeal.SealID = sealID
	secondSeal.AttemptID = attemptID
	secondSeal.CandidateID = candidateID
	secondSeal.PhysicalPoolID = pool
	secondSeal.CatalogID = catalog
	secondSeal.CatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000001019"
	secondSeal.DuckLakeSnapshotID = firstSeal.DuckLakeSnapshotID + 1
	secondSeal.ObjectNamespace = "objects/lost-ack-2"
	secondSeal.ObjectRoot = "objects/lost-ack-2/43"
	secondSeal.RelationNamespace = "candidate/lost-ack-2"
	secondSeal.ServingArtifactID = artifactID
	secondSeal.QualificationEvidence = []byte(`{"checks":["schema","multinode"]}`)
	if _, err := r.CreateSnapshotSeal(ctx, secondSeal); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, candidateID, sealID, testDigest('5')); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateGeneration(ctx, GenerationInput{
		GenerationID: generationID, TargetID: first.TargetID, CandidateID: candidateID, SnapshotSealID: sealID,
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ArtifactRoot: first.ArtifactRoot,
		ArtifactRootDigest: first.ArtifactRootDigest, ServingArtifactDigest: first.ServingArtifactDigest,
		CompiledGraphDigest: first.CompiledGraphDigest, CompiledConfigDigest: first.CompiledConfigDigest,
		SecurityDomainFingerprint: first.SecurityDomainFingerprint, GenerationRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	return secondActivationGeneration{generationID: generationID, candidateID: candidateID, sealID: sealID}
}
