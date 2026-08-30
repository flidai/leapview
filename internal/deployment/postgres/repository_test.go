package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func testCommitMarker(attempt, pool, request, plan string) []byte {
	marker := ducklake.CommitMarker{
		SchemaVersion: ducklake.CommitMarkerSchemaVersion,
		DeliveryID:    "delivery-test", GenerationID: "generation-test", AttemptID: attempt,
		LeaseEpoch: 1, RequestDigest: request, PlanDigest: plan,
		Project: "project-test", Environment: "prod", PhysicalPoolID: pool,
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		panic(err)
	}
	return []byte(canonical)
}

// testActivationAudit is an explicit injected adapter. Wrapping the app
// composition implementation keeps these PostgreSQL tests focused on
// deployment transaction behavior while still exercising the canonical Access
// append/read contract.
type testActivationAudit struct {
	audit *accesspostgres.AuditRepository
}

func (a testActivationAudit) AppendActivationAudit(ctx context.Context, tx Tx, input ActivationAuditInput) (AuditEvent, error) {
	intent, err := testAuditIntent(input)
	if err != nil {
		return AuditEvent{}, err
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		return AuditEvent{}, err
	}
	if err := validateTestAudit(stored, intent); err != nil {
		return AuditEvent{}, err
	}
	return mapTestAudit(stored), nil
}

func (a testActivationAudit) GetActivationAudit(ctx context.Context, tx Tx, input ActivationAuditInput) (AuditEvent, error) {
	intent, err := testAuditIntent(input)
	if err != nil {
		return AuditEvent{}, err
	}
	stored, err := a.audit.GetAuditEvent(ctx, tx, input.EventID)
	if err != nil {
		return AuditEvent{}, err
	}
	if err := validateTestAudit(stored, intent); err != nil {
		return AuditEvent{}, err
	}
	return mapTestAudit(stored), nil
}

func testAuditIntent(input ActivationAuditInput) (access.AuditIntent, error) {
	return access.AuditIntent{EventID: input.EventID, DomainEventID: input.DomainEventID, ScopeID: input.ScopeID, ActorID: input.ActorID, Source: "deployment", Operation: "activate", Action: input.Action, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, Outcome: "success", CorrelationID: input.CorrelationID, RequestDigest: input.RequestDigest, AggregateKey: input.AggregateKey, AggregateSequence: input.AggregateSequence, MetadataJSON: string(input.Metadata)}, nil
}

func validateTestAudit(stored accesspostgres.Event, expected access.AuditIntent) error {
	canonical, err := expected.Canonicalize()
	if err != nil {
		return err
	}
	digest, err := canonical.PayloadDigest()
	if err != nil {
		return err
	}
	if stored.AuditID != canonical.EventID || stored.DomainEventID != canonical.DomainEventID || stored.ScopeID != canonical.ScopeID || stored.ActorID != canonical.ActorID || stored.PrincipalID != canonical.PrincipalID || stored.Source != canonical.Source || stored.Operation != canonical.Operation || stored.Action != canonical.Action || stored.ResourceKind != canonical.ResourceKind || stored.ResourceID != canonical.ResourceID || stored.Capability != canonical.Capability || stored.Outcome != canonical.Outcome || stored.RequestID != canonical.RequestID || stored.CorrelationID != canonical.CorrelationID || stored.RequestDigest != canonical.RequestDigest || stored.AggregateKey != canonical.AggregateKey || stored.AggregateSequence != canonical.AggregateSequence || !sameTestJSON(stored.MetadataJSON, canonical.MetadataJSON) || stored.IntentDigest != digest {
		return fmt.Errorf("%w: got=%#v want=%#v digest=%s", ErrConflict, stored, canonical, digest)
	}
	return nil
}

func sameTestJSON(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return false
	}
	leftCanonical, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(b)
	return err == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func mapTestAudit(stored accesspostgres.Event) AuditEvent {
	return AuditEvent{AuditID: stored.AuditID, EventID: stored.DomainEventID, ScopeID: stored.ScopeID, ActorID: stored.ActorID, Action: stored.Action, ResourceKind: stored.ResourceKind, ResourceID: stored.ResourceID, Outcome: "accepted", RequestDigest: stored.RequestDigest, Metadata: []byte(stored.MetadataJSON), OccurredAt: stored.OccurredAt}
}

func deliveryTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "delivery_authority_test")
	dsn := db.AdminURL()
	p, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), accesspostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPostgresDeliveryAuthorityLifecycleAndReplay(t *testing.T) {
	p := deliveryTestDB(t)
	r := NewWithActivationAudit(p, testActivationAudit{audit: accesspostgres.New()})
	ctx := context.Background()
	ids := map[string]string{
		"plan": "0198f2c0-7c7a-7f00-8a11-000000000001", "candidate": "0198f2c0-7c7a-7f00-8a11-000000000002",
		"attempt": "0198f2c0-7c7a-7f00-8a11-000000000003", "seal": "0198f2c0-7c7a-7f00-8a11-000000000004",
		"generation": "0198f2c0-7c7a-7f00-8a11-000000000005", "publication": "0198f2c0-7c7a-7f00-8a11-000000000006",
		"lease": "0198f2c0-7c7a-7f00-8a11-000000000007",
	}
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: "target_sales_prod", ProjectID: "project_sales", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePlan(ctx, PlanInput{PlanID: ids["plan"], TargetID: "target_sales_prod", PlanRevision: 1, PlanDigest: testDigest('a'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), ArtifactDigest: testDigest('e'), Evidence: []byte(`{"qualification":"none"}`)}); err != nil {
		got, ge := r.Plan(ctx, ids["plan"])
		t.Logf("plan got=%#v load=%v", got, ge)
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: ids["candidate"], TargetID: "target_sales_prod", PlanID: ids["plan"], CandidateRevision: 1, ArtifactDigest: testDigest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: ids["attempt"], PlanID: ids["plan"], CandidateID: ids["candidate"], OwnerID: "builder-a", PhysicalPoolID: "pool-sales", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: testDigest('a'), Namespace: "candidate/attempt/fence", SessionIdentity: "session-a", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	marker := testCommitMarker(ids["attempt"], "pool-sales", testDigest('f'), testDigest('a'))
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: ids["attempt"], OwnerID: "builder-a", FencingEpoch: 1, SnapshotID: 42, CommitMarker: marker}); err != nil {
		t.Fatal(err)
	}
	sealInput := SnapshotSealInput{SealID: ids["seal"], AttemptID: ids["attempt"], CandidateID: ids["candidate"], PhysicalPoolID: "pool-sales", TenantDomain: "tenant-sales", Region: "us-east", EncryptionDomain: "enc-sales", ObjectNamespace: "objects/sales", CatalogDatabase: "ducklake", CatalogID: "catalog-sales", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000008", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/attempt/fence", RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/sales/42", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/" + testDigest('e'), ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: testDigest('f'), PlanDigest: testDigest('a'), CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-sales", ServingArtifactDigest: testDigest('e'), DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}
	if _, err := r.CreateSnapshotSeal(ctx, sealInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, ids["candidate"], ids["seal"], testDigest('3')); err != nil {
		t.Fatal(err)
	}
	approvalID := "0198f2c0-7c7a-7f00-8a11-000000000009"
	approval, err := r.ApproveCandidate(ctx, DeliveryApproval{ApprovalID: approvalID, CandidateID: ids["candidate"], Decision: "approved", Evidence: json.RawMessage(`{"review":"ok"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := r.ApproveCandidate(ctx, DeliveryApproval{ApprovalID: approvalID, CandidateID: ids["candidate"], Decision: "approved", Evidence: json.RawMessage(`{"review":"ok"}`)}); err != nil || replay.ApprovalID != approval.ApprovalID {
		t.Fatalf("approval replay = %#v, %v", replay, err)
	}
	if _, err := r.ApproveCandidate(ctx, DeliveryApproval{ApprovalID: approvalID, CandidateID: ids["candidate"], Decision: "denied", Evidence: json.RawMessage(`{"review":"ok"}`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval identity mismatch = %v", err)
	}
	if _, err := r.CreateGeneration(ctx, GenerationInput{GenerationID: ids["generation"], TargetID: "target_sales_prod", CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], PlanID: ids["plan"], PlanDigest: testDigest('a'), ArtifactRoot: sealInput.ArtifactRoot, ArtifactRootDigest: sealInput.ArtifactRootDigest, ServingArtifactDigest: testDigest('e'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), GenerationRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: testDigest('4')}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "other", RequestDigest: testDigest('4')}); !errors.Is(err, ErrConflict) {
		t.Fatalf("publication actor mismatch = %v", err)
	}
	lease, err := r.AcquireLease(ctx, LeaseInput{LeaseID: ids["lease"], TargetID: "target_sales_prod", OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	input := ActivationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], ExpectedTargetRevision: 1, RequestDigest: testDigest('4'), ActorID: "operator", LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}
	first, err := r.Activate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replay || first.Pointer.ActiveGenerationID != ids["generation"] || first.Publication.ResultTargetRevision != 2 {
		t.Fatalf("unexpected activation result: %#v", first)
	}
	replayedPublication, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: testDigest('4')})
	if err != nil || replayedPublication.PublicationID != ids["publication"] || replayedPublication.ExpectedBaseGenerationID != "" {
		t.Fatalf("post-activation publication replay = %#v, %v", replayedPublication, err)
	}
	if root, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{RootID: ids["generation"], TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: ids["generation"], SnapshotSealID: ids["seal"], RootKind: "generation", State: "live"}); err != nil || root.RootID != ids["generation"] {
		t.Fatalf("retention root replay = %#v, %v", root, err)
	}
	if _, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{RootID: ids["generation"], TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: ids["generation"], SnapshotSealID: "", RootKind: "generation", State: "live"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("retention root identity mismatch = %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,snapshot_seal_id,status,candidate_revision,artifact_digest,qualification_digest,qualified_at) VALUES('0198f2c0-7c7a-7f00-8a11-000000000010','target_sales_prod',$1::uuid,$2::uuid,'qualified',2,$3,$4,clock_timestamp())`, ids["plan"], ids["seal"], testDigest('e'), testDigest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_publication(publication_id,target_id,generation_id,candidate_id,snapshot_seal_id,expected_target_revision,actor_id,state,request_digest) VALUES('0198f2c0-7c7a-7f00-8a11-000000000013','target_sales_prod',$1::uuid,'0198f2c0-7c7a-7f00-8a11-000000000010',$2::uuid,2,'operator','pending',$3)`, ids["generation"], ids["seal"], testDigest('8')); err == nil {
		t.Fatal("publication accepted mismatched generation candidate")
	}
	if _, err := p.Exec(ctx, `ALTER TABLE event.event_log DISABLE TRIGGER event_log_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE event.event_log SET payload='{}'::jsonb WHERE event_id=$1::uuid`, ids["publication"]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE event.event_log ENABLE TRIGGER event_log_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Activate(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered activation replay = %v", err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE event.event_log DISABLE TRIGGER event_log_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE event.event_log SET payload=$2::jsonb WHERE event_id=$1::uuid`, ids["publication"], first.Event.Payload); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE event.event_log ENABLE TRIGGER event_log_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event DISABLE TRIGGER audit_event_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE audit.audit_event SET capability='resource:view' WHERE audit_id=$1::uuid`, ids["publication"]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event ENABLE TRIGGER audit_event_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Activate(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered activation audit replay = %v", err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event DISABLE TRIGGER audit_event_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE audit.audit_event SET capability='' WHERE audit_id=$1::uuid`, ids["publication"]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event ENABLE TRIGGER audit_event_immutable`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, column, replacement, restore string
	}{
		{name: "scope id", column: "scope_id", replacement: "tampered_scope", restore: "target_sales_prod"},
		{name: "domain event id", column: "event_id", replacement: "0198f2c0-7c7a-7f00-8a11-000000000099", restore: ids["publication"]},
		{name: "actor id", column: "actor_id", replacement: "tampered_actor", restore: "operator"},
		{name: "request digest", column: "request_digest", replacement: testDigest('9'), restore: testDigest('4')},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event DISABLE TRIGGER audit_event_immutable`); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE audit.audit_event SET ` + test.column + `=$2 WHERE audit_id=$1::uuid`
			if _, err := p.Exec(ctx, query, ids["publication"], test.replacement); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event ENABLE TRIGGER audit_event_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Activate(ctx, input); !errors.Is(err, ErrConflict) {
				t.Fatalf("tampered %s replay = %v", test.name, err)
			}
			if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event DISABLE TRIGGER audit_event_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Exec(ctx, query, ids["publication"], test.restore); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Exec(ctx, `ALTER TABLE audit.audit_event ENABLE TRIGGER audit_event_immutable`); err != nil {
				t.Fatal(err)
			}
		})
	}
	replay, err := r.Activate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replay || replay.Event.EventID != first.Event.EventID || replay.Audit.AuditID != first.Audit.AuditID {
		t.Fatalf("replay evidence changed: %#v", replay)
	}
	if _, err := r.Activate(ctx, ActivationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], ExpectedTargetRevision: 1, RequestDigest: testDigest('4'), ActorID: "other", LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}); !errors.Is(err, ErrConflict) {
		t.Fatalf("actor mismatch = %v", err)
	}
}

func TestDeploymentSchemaContainsNoEventOrAuditDDL(t *testing.T) {
	schema := strings.ToLower(SchemaSQL())
	for _, forbidden := range []string{"create schema if not exists event", "create schema if not exists audit", "create table if not exists event.", "create table if not exists audit.", "event.event_log", "audit.audit_event"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("deployment schema contains forbidden shared DDL %q", forbidden)
		}
	}
}

func TestActivationFailsClosedWithoutAuditPort(t *testing.T) {
	r := New(nil)
	if _, err := r.Activate(t.Context(), ActivationInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("activation without audit port = %v, want ErrInvalid", err)
	}
}

func TestPostgresLeaseCASRaceAndStaleFence(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	if _, err := r.CreateTarget(t.Context(), TargetInput{TargetID: "target_lease_race", ProjectID: "project", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	leases := make(chan DeliveryLease, 2)
	errs := make(chan error, 2)
	for i, id := range []string{"0198f2c0-7c7a-7f00-8a11-000000000011", "0198f2c0-7c7a-7f00-8a11-000000000012"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			l, err := r.AcquireLease(t.Context(), LeaseInput{LeaseID: id, TargetID: "target_lease_race", OwnerID: "worker-" + string(rune('a'+i)), ExpiresAt: time.Now().UTC().Add(time.Hour)})
			if err != nil {
				errs <- err
				return
			}
			leases <- l
		}(i, id)
	}
	wg.Wait()
	close(leases)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got := make([]DeliveryLease, 0, 2)
	for l := range leases {
		got = append(got, l)
	}
	if len(got) != 2 || got[0].FencingEpoch == got[1].FencingEpoch {
		t.Fatalf("lease epochs = %#v", got)
	}
	var old DeliveryLease
	if got[0].FencingEpoch < got[1].FencingEpoch {
		old = got[0]
	} else {
		old = got[1]
	}
	if err := r.ReleaseLease(t.Context(), LeaseFence{LeaseID: old.LeaseID, TargetID: old.TargetID, OwnerID: old.OwnerID, FencingEpoch: old.FencingEpoch}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale release = %v", err)
	}
	var active int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_lease WHERE target_id='target_lease_race' AND state='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active lease count = %d", active)
	}
}

func TestPostgresEventVersionRetainsBigintWidth(t *testing.T) {
	p := deliveryTestDB(t)
	const scope = "target_event_version_width"
	if _, err := p.Exec(t.Context(), `INSERT INTO event.event_aggregate(scope_id,aggregate_type,aggregate_id,next_version) VALUES($1,'delivery_target',$1,2147483648)`, scope); err != nil {
		t.Fatal(err)
	}
	event, err := eventspostgres.New().AppendEvent(t.Context(), p, eventspostgres.EventInput{EventID: "0198f2c0-7c7a-7f00-8a11-000000000040", ScopeID: scope, AggregateType: "delivery_target", AggregateID: scope, EventType: "version_width", SchemaVersion: 1, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if event.AggregateVersion != 2147483648 {
		t.Fatalf("event version = %d, want bigint boundary value", event.AggregateVersion)
	}
}

func TestPostgresCanonicalTargetProofSerializesActivation(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	ctx := context.Background()
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: "target_canonical_lock", ProjectID: "project_canonical_lock", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	proofTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.TargetForShareTx(ctx, proofTx, "target_canonical_lock"); err != nil {
		_ = proofTx.Rollback(ctx)
		t.Fatal(err)
	}
	activationTx, err := p.Begin(ctx)
	if err != nil {
		_ = proofTx.Rollback(ctx)
		t.Fatal(err)
	}
	activationDone := make(chan error, 1)
	go func() {
		var id string
		activationDone <- activationTx.QueryRow(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE`, "target_canonical_lock").Scan(&id)
	}()
	select {
	case err := <-activationDone:
		_ = activationTx.Rollback(ctx)
		_ = proofTx.Rollback(ctx)
		t.Fatalf("activation lock overtook canonical proof, err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := proofTx.Commit(ctx); err != nil {
		_ = activationTx.Rollback(ctx)
		t.Fatal(err)
	}
	select {
	case err := <-activationDone:
		if err != nil {
			_ = activationTx.Rollback(ctx)
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = activationTx.Rollback(ctx)
		t.Fatal("activation lock did not proceed after canonical proof committed")
	}
	if err := activationTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresDeliveryHistoryIsImmutable(t *testing.T) {
	p := deliveryTestDB(t)
	if _, err := p.Exec(t.Context(), `INSERT INTO delivery.delivery_target(target_id,project_id,environment) VALUES('immutable_target','immutable_project','prod')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO delivery.delivery_target_fence(target_id) VALUES('immutable_target')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE delivery.delivery_target SET project_id='changed' WHERE target_id='immutable_target'`); err == nil {
		t.Fatal("target identity update was accepted")
	}
}

func TestPostgresBuildAttemptCommitAbortRace(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	target := "target_attempt_race"
	plan := "0198f2c0-7c7a-7f00-8a11-000000000021"
	candidate := "0198f2c0-7c7a-7f00-8a11-000000000022"
	attempt := "0198f2c0-7c7a-7f00-8a11-000000000023"
	if _, err := r.CreateTarget(t.Context(), TargetInput{TargetID: target, ProjectID: "project_race", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePlan(t.Context(), PlanInput{PlanID: plan, TargetID: target, PlanRevision: 1, PlanDigest: testDigest('a'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), ArtifactDigest: testDigest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateCandidate(t.Context(), CandidateInput{CandidateID: candidate, TargetID: target, PlanID: plan, CandidateRevision: 1, ArtifactDigest: testDigest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(t.Context(), BuildAttemptInput{AttemptID: attempt, PlanID: plan, CandidateID: candidate, OwnerID: "builder", PhysicalPoolID: "pool-race", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: testDigest('a'), Namespace: "candidate/race", SessionIdentity: "session-race", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	marker := json.RawMessage(testCommitMarker(attempt, "pool-race", testDigest('f'), testDigest('a')))
	results := make(chan error, 2)
	go func() {
		_, err := r.CommitBuildAttempt(t.Context(), CommitAttemptInput{AttemptID: attempt, OwnerID: "builder", FencingEpoch: 1, SnapshotID: 7, CommitMarker: marker})
		results <- err
	}()
	go func() {
		_, err := r.AbortBuildAttempt(t.Context(), TerminateAttemptInput{AttemptID: attempt, OwnerID: "builder", FencingEpoch: 1, Evidence: json.RawMessage(`{"reason":"race"}`)})
		results <- err
	}()
	var successes, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("race error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("race outcomes successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestPostgresCommitBuildAttemptRejectsIncompleteDuckLakeMarker(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	ctx := t.Context()
	const target = "target_marker_schema"
	plan := "0198f2c0-7c7a-7f00-0000-000000000031"
	attempt := "0198f2c0-7c7a-7f00-0000-000000000032"
	request, planDigest := testDigest('f'), testDigest('a')
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: target, ProjectID: "project_marker", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePlan(ctx, PlanInput{PlanID: plan, TargetID: target, PlanRevision: 1, PlanDigest: planDigest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), ArtifactDigest: testDigest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: attempt, PlanID: plan, OwnerID: "builder", PhysicalPoolID: "pool-marker", FencingEpoch: 1, RequestDigest: request, PlanDigest: planDigest, Namespace: "candidate/marker", SessionIdentity: "session-marker", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	valid := testCommitMarker(attempt, "pool-marker", request, planDigest)
	cases := []struct {
		name   string
		marker []byte
	}{
		{name: "unknown field", marker: []byte(strings.TrimSuffix(string(valid), "}") + `,"unreviewed":"x"}`)},
		{name: "missing required field", marker: []byte(strings.Replace(string(valid), `"delivery_id":"delivery-test",`, "", 1))},
		{name: "wrong schema version", marker: []byte(strings.Replace(string(valid), `"schema_version":1`, `"schema_version":2`, 1))},
		{name: "invalid normalized field", marker: []byte(strings.Replace(string(valid), `"environment":"prod"`, `"environment":" prod"`, 1))},
		{name: "invalid digest", marker: []byte(strings.Replace(string(valid), `"plan_digest":"`+planDigest+`"`, `"plan_digest":"sha256:not-a-digest"`, 1))},
		{name: "identity mismatch", marker: []byte(strings.Replace(string(valid), `"lease_epoch":1`, `"lease_epoch":2`, 1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attempt, OwnerID: "builder", FencingEpoch: 1, SnapshotID: 42, CommitMarker: tc.marker}); err == nil {
				t.Fatal("invalid commit marker was accepted")
			}
		})
	}
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attempt, OwnerID: "builder", FencingEpoch: 1, SnapshotID: 42, CommitMarker: valid}); err != nil {
		t.Fatalf("valid full marker rejected after invalid attempts: %v", err)
	}
}

func TestPostgresAuthorityDatabaseGuards(t *testing.T) {
	p := deliveryTestDB(t)
	ctx := t.Context()
	var owner, publicUsage bool
	if err := p.QueryRow(ctx, `SELECT has_schema_privilege(current_user,'delivery','USAGE'), has_schema_privilege('public','delivery','USAGE')`).Scan(&owner, &publicUsage); err != nil {
		t.Fatal(err)
	}
	if !owner || publicUsage {
		t.Fatalf("schema privileges owner=%v public=%v", owner, publicUsage)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_target(target_id,project_id,environment) VALUES(repeat('x',256),'project_guard','prod')`); err == nil {
		t.Fatal("overlong target id was accepted")
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_target(target_id,project_id,environment) VALUES('guard_target','project_guard','prod')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_target_fence(target_id) VALUES('guard_target')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_target_fence SET next_fencing_epoch=0 WHERE target_id='guard_target'`); err == nil {
		t.Fatal("fencing counter moved backwards")
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_target SET target_revision=0 WHERE target_id='guard_target'`); err == nil {
		t.Fatal("target revision moved backwards")
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_lease(lease_id,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at) VALUES('0198f2c0-7c7a-7f00-8a11-000000000030','guard_target','owner',1,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),clock_timestamp())`); err == nil {
		t.Fatal("active lease with release timestamp was accepted")
	}
	if _, err := p.Exec(ctx, `INSERT INTO event.event_log(event_id,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,payload) VALUES('0198f2c0-7c7a-7f00-8a11-000000000035',repeat('x',256),'delivery_target','guard_target',1,'guard',1,'{}')`); err == nil {
		t.Fatal("overlong event scope was accepted")
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_retention_root(root_id,target_id,root_kind,state) VALUES('0198f2c0-7c7a-7f00-8a11-000000000036','guard_target','query','live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_retention_root SET state='expired',expired_at=clock_timestamp() WHERE root_id='0198f2c0-7c7a-7f00-8a11-000000000036'`); err == nil {
		t.Fatal("live retention root skipped retiring state")
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_retention_root SET state='retiring',retired_at=clock_timestamp() WHERE root_id='0198f2c0-7c7a-7f00-8a11-000000000036'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_retention_root SET state='live' WHERE root_id='0198f2c0-7c7a-7f00-8a11-000000000036'`); err == nil {
		t.Fatal("retiring retention root reverted to live")
	}
	plan := "0198f2c0-7c7a-7f00-8a11-000000000031"
	candidate := "0198f2c0-7c7a-7f00-8a11-000000000032"
	if _, err := New(p).CreateTarget(ctx, TargetInput{TargetID: "guard_target_other", ProjectID: "project_guard_other", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest) VALUES($1::uuid,'guard_target',1,$2,$2,$2,$2,$2)`, plan, testDigest('a')); err != nil {
		t.Fatal(err)
	}
	if _, err := New(p).CreateCandidate(ctx, CandidateInput{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000038", TargetID: "guard_target_other", PlanID: plan, CandidateRevision: 1, ArtifactDigest: testDigest('a')}); !errors.Is(err, ErrConflict) {
		t.Fatalf("candidate cross-target plan = %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,candidate_revision,artifact_digest) VALUES('0198f2c0-7c7a-7f00-8a11-000000000039','guard_target_other',$1::uuid,1,$2)`, plan, testDigest('a')); err == nil {
		t.Fatal("direct SQL candidate cross-target plan was accepted")
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,candidate_revision,artifact_digest) VALUES($1::uuid,'guard_target',$2::uuid,1,$3)`, candidate, plan, testDigest('a')); err != nil {
		t.Fatal(err)
	}
	r := New(p)
	if _, err := r.ApproveCandidate(ctx, DeliveryApproval{ApprovalID: "0198f2c0-7c7a-7f00-8a11-000000000034", CandidateID: candidate, Decision: "approved", Evidence: json.RawMessage(`{"source":"guard"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_approval SET decision='denied' WHERE approval_id='0198f2c0-7c7a-7f00-8a11-000000000034'`); err == nil {
		t.Fatal("approval mutation was accepted")
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_build_attempt(attempt_id,plan_id,candidate_id,owner_id,physical_pool_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity) VALUES('0198f2c0-7c7a-7f00-8a11-000000000033',$1::uuid,$2::uuid,'builder','guard-pool',1,$3,$3,'committed','guard',clock_timestamp()+interval '1 hour','session')`, plan, candidate, testDigest('a')); err == nil {
		t.Fatal("terminal build attempt without evidence was accepted")
	}
}
