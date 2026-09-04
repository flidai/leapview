package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errActivateCommitLostAck = errors.New("injected activation commit acknowledgement loss")

// TestPostgresActivateReplaysAfterCommitLostAcknowledgement proves that a
// successful PostgreSQL commit followed by an unavailable acknowledgement is
// recovered by the exact activation identity. The retry must only read the
// committed publication/evidence and must not allocate another revision or
// append duplicate event/audit rows.
func TestPostgresActivateReplaysAfterCommitLostAcknowledgement(t *testing.T) {
	p := deliveryTestDB(t)
	lineage := &testActivationLineage{}
	r := NewWithOptions(p, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: lineage})
	input, ids := prepareLostAckActivation(t, r)
	lineage.expected = ActivationLineageInput{TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: ids.generation, CompiledGraphDigest: testDigest('b')}
	if _, err := r.Activate(t.Context(), input); !errors.Is(err, ErrConflict) {
		t.Fatalf("activation without physical retention error = %v, want conflict", err)
	}
	seedPhysicalRetentionFixture(t, p, ids.seal)

	// Setup uses the real pool so its commits are ordinary. Swap only the
	// activation database handle after setup; the wrapper preserves every
	// pgx operation and changes the first Commit result after PostgreSQL has
	// already committed the transaction.
	r.db = &activateLostAckDB{Pool: p}
	if _, err := r.Activate(t.Context(), input); !errors.Is(err, errActivateCommitLostAck) {
		t.Fatalf("first activation error = %v, want lost acknowledgement", err)
	}

	replay, err := r.Activate(t.Context(), input)
	if err != nil {
		t.Fatalf("activation replay: %v", err)
	}
	if !replay.Replay {
		t.Fatalf("activation replay = %#v, want Replay=true", replay)
	}
	if replay.Publication.PublicationID != ids.publication || replay.Publication.State != "committed" || replay.Publication.ResultTargetRevision != 2 {
		t.Fatalf("replayed publication = %#v", replay.Publication)
	}
	if replay.Pointer.TargetRevision != 2 || replay.Pointer.ActiveGenerationID != ids.generation || replay.Pointer.ActivePublicationID != ids.publication {
		t.Fatalf("replayed target pointer = %#v", replay.Pointer)
	}
	if replay.Event.EventID != ids.publication || replay.Event.ScopeID != ids.target || replay.Event.AggregateID != ids.target || replay.Event.EventType != "activation_committed" {
		t.Fatalf("replayed event = %#v", replay.Event)
	}
	if replay.Audit.AuditID != ids.publication || replay.Audit.EventID != ids.publication || replay.Audit.ResourceID != ids.generation || replay.Audit.Outcome != "accepted" {
		t.Fatalf("replayed audit = %#v", replay.Audit)
	}

	publication, err := r.Publication(t.Context(), ids.publication)
	if err != nil {
		t.Fatal(err)
	}
	target, err := r.Target(t.Context(), ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if publication != replay.Publication || target.TargetRevision != replay.Pointer.TargetRevision || target.ActiveGenerationID != replay.Pointer.ActiveGenerationID || target.ActivePublicationID != replay.Pointer.ActivePublicationID {
		t.Fatalf("committed state changed on replay: publication=%#v target=%#v replay=%#v", publication, target, replay)
	}
	var eventRows, auditRows int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, ids.publication).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, ids.publication).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if eventRows != 1 || auditRows != 1 {
		t.Fatalf("activation evidence rows = event %d audit %d, want one each", eventRows, auditRows)
	}
}

type lostAckActivationIDs struct {
	target, generation, publication string
	seal                            SnapshotSealInput
}

func seedPhysicalRetentionFixture(t testing.TB, db DBTX, seal SnapshotSealInput) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		INSERT INTO ducklake.catalog_identity(
			physical_pool_id, catalog_database, catalog_id, catalog_uuid, metadata_schema
		) VALUES ($1, $2, $3, $4::uuid, 'lake')`,
		seal.PhysicalPoolID, seal.CatalogDatabase, seal.CatalogID, seal.CatalogUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO ducklake.snapshot_retention(
			physical_pool_id, catalog_id, snapshot_id, state
		) VALUES ($1, $2, $3, 'live')`,
		seal.PhysicalPoolID, seal.CatalogID, seal.DuckLakeSnapshotID); err != nil {
		t.Fatal(err)
	}
}

func prepareLostAckActivation(t *testing.T, r *Repository) (ActivationInput, lostAckActivationIDs) {
	t.Helper()
	ctx := t.Context()
	ids := lostAckActivationIDs{
		target:      "target_lost_ack",
		generation:  "0198f2c0-7c7a-7f00-8a11-000000001005",
		publication: "0198f2c0-7c7a-7f00-8a11-000000001006",
	}
	planID := "0198f2c0-7c7a-7f00-8a11-000000001001"
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000001002"
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000001003"
	sealID := "0198f2c0-7c7a-7f00-8a11-000000001004"
	leaseID := "0198f2c0-7c7a-7f00-8a11-000000001007"
	projectID := "project_lost_ack"
	planDigest := testDigest('a')
	artifactDigest := testDigest('e')

	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: ids.target, ProjectID: projectID, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	// The shared rich fixture requires approval. Make this isolated test a
	// pure activation proof by rebuilding its canonical plan document with
	// approval disabled, leaving all other deployment identity fields intact.
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
	planDigest = plan.PlanDigest
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: candidateID, TargetID: ids.target, PlanID: planID, CandidateRevision: 1, ArtifactDigest: artifactDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder-lost-ack", PhysicalPoolID: "pool-lost-ack", CatalogID: "catalog-lost-ack", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: planDigest, Namespace: "candidate/lost-ack", SessionIdentity: "session-lost-ack", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: "artifact-lost-ack", ServingArtifactDigest: artifactDigest, ServingStateID: "generation-test", OwnerID: "builder-lost-ack", FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	marker := testCommitMarker(attemptID, "pool-lost-ack", testDigest('f'), planDigest)
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder-lost-ack", FencingEpoch: 1, SnapshotID: 42, CommitMarker: marker}); err != nil {
		t.Fatal(err)
	}
	sealInput := SnapshotSealInput{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: "pool-lost-ack", TenantDomain: "tenant-lost-ack", Region: "us-east", EncryptionDomain: "enc-lost-ack", ObjectNamespace: "objects/lost-ack", CatalogDatabase: "ducklake", CatalogID: "catalog-lost-ack", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000001008", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/lost-ack", RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/lost-ack/42", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/" + artifactDigest, ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: testDigest('f'), PlanDigest: planDigest, CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-lost-ack", ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}
	ids.seal = sealInput
	if _, err := r.CreateSnapshotSeal(ctx, sealInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, candidateID, sealID, testDigest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateGeneration(ctx, GenerationInput{GenerationID: ids.generation, TargetID: ids.target, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: sealInput.ArtifactRoot, ArtifactRootDigest: sealInput.ArtifactRootDigest, ServingArtifactDigest: artifactDigest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), GenerationRevision: 1}); err != nil {
		t.Fatal(err)
	}
	requestDigest := testDigest('4')
	if _, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids.publication, TargetID: ids.target, GenerationID: ids.generation, CandidateID: candidateID, SnapshotSealID: sealID, ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: requestDigest}); err != nil {
		t.Fatal(err)
	}
	lease, err := r.AcquireLease(ctx, LeaseInput{LeaseID: leaseID, TargetID: ids.target, OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return ActivationInput{PublicationID: ids.publication, TargetID: ids.target, GenerationID: ids.generation, ExpectedTargetRevision: 1, RequestDigest: requestDigest, ActorID: "operator", CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000001009", LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}, ids
}

type activateLostAckDB struct {
	*pgxpool.Pool
	mu      sync.Mutex
	lostAck bool
}

func (db *activateLostAckDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &activateLostAckTx{Tx: tx, db: db}, nil
}

type activateLostAckTx struct {
	pgx.Tx
	db *activateLostAckDB
}

func (tx *activateLostAckTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	tx.db.mu.Lock()
	injected := !tx.db.lostAck
	if injected {
		tx.db.lostAck = true
	}
	tx.db.mu.Unlock()
	if injected {
		return errActivateCommitLostAck
	}
	return nil
}

var _ DBTX = (*activateLostAckDB)(nil)
var _ pgx.Tx = (*activateLostAckTx)(nil)
