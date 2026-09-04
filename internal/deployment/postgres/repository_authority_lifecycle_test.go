package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestPostgresDeliveryAuthorityLifecycleAndReplay(t *testing.T) {
	p := deliveryTestDB(t)
	ctx := context.Background()
	ids := map[string]string{
		"plan": "0198f2c0-7c7a-7f00-8a11-000000000001", "candidate": "0198f2c0-7c7a-7f00-8a11-000000000002",
		"attempt": "0198f2c0-7c7a-7f00-8a11-000000000003", "seal": "0198f2c0-7c7a-7f00-8a11-000000000004",
		"generation": "0198f2c0-7c7a-7f00-8a11-000000000005", "publication": "0198f2c0-7c7a-7f00-8a11-000000000006",
		"lease": "0198f2c0-7c7a-7f00-8a11-000000000007",
	}
	lineage := &testActivationLineage{expected: ActivationLineageInput{TargetID: "target_sales_prod", ProjectID: "project_sales", GenerationID: ids["generation"]}}
	r := NewWithOptions(p, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: lineage})
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: "target_sales_prod", ProjectID: "project_sales", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	planInput := richPlanInputFixture(t, ids["plan"], "target_sales_prod", "project_sales")
	planInput.Evidence = []byte(`{"qualification":"none"}`)
	createdPlan, err := r.CreatePlan(ctx, planInput)
	if err != nil {
		got, ge := r.Plan(ctx, ids["plan"])
		t.Logf("plan got=%#v load=%v", got, ge)
		t.Fatalf("plan: %v", err)
	}
	planDigest := createdPlan.PlanDigest
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: ids["candidate"], TargetID: "target_sales_prod", PlanID: ids["plan"], CandidateRevision: 1, ArtifactDigest: testDigest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: ids["attempt"], PlanID: ids["plan"], CandidateID: ids["candidate"], OwnerID: "builder-a", PhysicalPoolID: "pool-sales", CatalogID: "catalog-sales", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: planDigest, Namespace: "candidate/attempt/fence", SessionIdentity: "session-a", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: ids["attempt"], ServingArtifactID: "artifact-sales", ServingArtifactDigest: testDigest('e'), ServingStateID: "generation-test", OwnerID: "builder-a", FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	marker := testCommitMarker(ids["attempt"], "pool-sales", testDigest('f'), planDigest)
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: ids["attempt"], OwnerID: "builder-a", FencingEpoch: 1, SnapshotID: 42, CommitMarker: marker}); err != nil {
		t.Fatal(err)
	}
	sealInput := SnapshotSealInput{SealID: ids["seal"], AttemptID: ids["attempt"], CandidateID: ids["candidate"], PhysicalPoolID: "pool-sales", TenantDomain: "tenant-sales", Region: "us-east", EncryptionDomain: "enc-sales", ObjectNamespace: "objects/sales", CatalogDatabase: "ducklake", CatalogID: "catalog-sales", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000008", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/attempt/fence", RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/sales/42", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/" + testDigest('e'), ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: testDigest('f'), PlanDigest: planDigest, CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-sales", ServingArtifactDigest: testDigest('e'), DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}
	if _, err := r.CreateSnapshotSeal(ctx, sealInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, ids["candidate"], ids["seal"], testDigest('3')); err != nil {
		t.Fatal(err)
	}
	generationInput := GenerationInput{GenerationID: ids["generation"], TargetID: "target_sales_prod", CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], PlanID: ids["plan"], PlanDigest: planDigest, ArtifactRoot: sealInput.ArtifactRoot, ArtifactRootDigest: sealInput.ArtifactRootDigest, ServingArtifactDigest: testDigest('e'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), GenerationRevision: 1}
	if _, err := r.CreateGeneration(ctx, generationInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{RootID: ids["candidate"], TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: ids["generation"], SnapshotSealID: ids["seal"], RootKind: "candidate", State: "live", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: testDigest('4')}); err != nil {
		t.Fatal(err)
	}
	approval, err := newLowLevelApprovalAuthority(r, ApprovalAuthorityOptions{
		Authorize: ApprovalAuthorizerFunc(func(context.Context, ApprovalAuthorizationInput) error { return nil }),
		Operation: approvalNoopEvidenceAppender{}, Event: approvalNoopEvidenceAppender{}, Audit: approvalNoopEvidenceAppender{},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestInput := ApprovalRequestInput{
		RequestID: ids["publication"], PublicationID: ids["publication"], TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: ids["generation"],
		RequestDigest: testDigest('4'), ExpectedTargetRevision: 1, PolicyRevision: 2,
		RequestedBy: ApprovalActor{PrincipalID: "requester", CredentialClass: "human", CredentialID: "requester-session", CredentialExpiresAt: time.Now().UTC().Add(2 * time.Hour)},
		ExpiresAt:   time.Now().UTC().Add(time.Hour), Evidence: ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000000011", EventID: "0198f2c0-7c7a-7f00-8a11-000000000012", AuditID: "0198f2c0-7c7a-7f00-8a11-000000000013"},
	}
	if _, err := approval.Request(ctx, requestInput); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("policy revision mismatch = %v", err)
	}
	validApproval := requestInput
	validApproval.PolicyRevision = 1
	validApproval.Evidence = ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000000014", EventID: "0198f2c0-7c7a-7f00-8a11-000000000015", AuditID: "0198f2c0-7c7a-7f00-8a11-000000000016"}
	if _, err := approval.Request(ctx, validApproval); err != nil {
		t.Fatalf("valid approval request: %v", err)
	}
	if _, err := approval.Approve(ctx, ApprovalDecisionInput{RequestID: validApproval.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000017", ExpectedRevision: 0, Actor: ApprovalActor{PrincipalID: "reviewer", CredentialClass: "human", CredentialID: "reviewer-session", CredentialExpiresAt: time.Now().UTC().Add(2 * time.Hour)}, Evidence: ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000000018", EventID: "0198f2c0-7c7a-7f00-8a11-000000000019", AuditID: "0198f2c0-7c7a-7f00-8a11-000000000020"}}); err != nil {
		t.Fatalf("valid approval decision: %v", err)
	}
	if _, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "other", RequestDigest: testDigest('4')}); !errors.Is(err, ErrConflict) {
		t.Fatalf("publication actor mismatch = %v", err)
	}
	lease, err := r.AcquireLease(ctx, LeaseInput{LeaseID: ids["lease"], TargetID: "target_sales_prod", OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	input := ActivationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], ExpectedTargetRevision: 1, RequestDigest: testDigest('4'), ActorID: "operator", LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}
	beforeLineageFailure, err := r.Target(ctx, "target_sales_prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "missing binding", err: ErrNotFound},
		{name: "mismatched binding", err: ErrConflict},
	} {
		t.Run("lineage "+test.name, func(t *testing.T) {
			lineage.err = test.err
			if _, err := r.Activate(ctx, input); err == nil {
				t.Fatal("activation unexpectedly succeeded without exact lineage binding")
			}
			after, readErr := r.Target(ctx, "target_sales_prod")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if after.TargetRevision != beforeLineageFailure.TargetRevision || after.ActiveGenerationID != beforeLineageFailure.ActiveGenerationID || after.ActivePublicationID != beforeLineageFailure.ActivePublicationID {
				t.Fatalf("target mutated after lineage failure: before=%#v after=%#v", beforeLineageFailure, after)
			}
		})
	}
	lineage.err = nil
	first, err := r.Activate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replay || first.Pointer.ActiveGenerationID != ids["generation"] || first.Publication.ResultTargetRevision != 2 {
		t.Fatalf("unexpected activation result: %#v", first)
	}
	// Committed replays must still prove the immutable target/project/generation
	// lineage before returning durable evidence. A failed proof cannot alter the
	// already-committed target pointer.
	committedReplayBefore, err := r.Target(ctx, "target_sales_prod")
	if err != nil {
		t.Fatal(err)
	}
	lineage.err = ErrConflict
	if _, err := r.Activate(ctx, input); !errors.Is(err, ErrNotQualified) || !errors.Is(err, ErrConflict) {
		t.Fatalf("committed replay lineage failure = %v, want typed lineage error", err)
	}
	committedReplayAfter, err := r.Target(ctx, "target_sales_prod")
	if err != nil {
		t.Fatal(err)
	}
	if committedReplayAfter.TargetRevision != committedReplayBefore.TargetRevision || committedReplayAfter.ActiveGenerationID != committedReplayBefore.ActiveGenerationID || committedReplayAfter.ActivePublicationID != committedReplayBefore.ActivePublicationID {
		t.Fatalf("target mutated after committed replay lineage failure: before=%#v after=%#v", committedReplayBefore, committedReplayAfter)
	}
	lineage.err = nil
	operationalLineageError := errors.New("lineage database unavailable")
	lineage.err = operationalLineageError
	if _, err := r.Activate(ctx, input); !errors.Is(err, operationalLineageError) || errors.Is(err, ErrNotQualified) {
		t.Fatalf("committed replay operational lineage error = %v, want retryable cause without qualification classification", err)
	}
	lineage.err = nil
	activeRootID := generationRootID(ids["publication"])
	if candidateRoot, err := loadRetentionRoot(ctx, p, ids["candidate"]); err != nil || candidateRoot.State != "retiring" || candidateRoot.RetiredAt.IsZero() {
		t.Fatalf("candidate root was not retired by activation: %#v, %v", candidateRoot, err)
	}
	activeGeneration, err := r.ActiveGeneration(ctx, "target_sales_prod")
	if err != nil {
		t.Fatal(err)
	}
	if activeGeneration.GenerationID != ids["generation"] || activeGeneration.TargetID != "target_sales_prod" {
		t.Fatalf("active generation = %#v, want generation %q on target_sales_prod", activeGeneration, ids["generation"])
	}
	if replayedGeneration, err := r.CreateGeneration(ctx, generationInput); err != nil || replayedGeneration.GenerationID != ids["generation"] {
		t.Fatalf("post-activation generation replay = %#v, %v", replayedGeneration, err)
	}
	mismatchedGeneration := generationInput
	mismatchedGeneration.GenerationRevision++
	if _, err := r.CreateGeneration(ctx, mismatchedGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-activation generation mismatch = %v", err)
	}
	replayedPublication, err := r.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: testDigest('4')})
	if err != nil || replayedPublication.PublicationID != ids["publication"] || replayedPublication.ExpectedBaseGenerationID != "" {
		t.Fatalf("post-activation publication replay = %#v, %v", replayedPublication, err)
	}
	if root, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{RootID: activeRootID, TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: ids["generation"], SnapshotSealID: ids["seal"], RootKind: "generation", State: "live"}); err != nil || root.RootID != activeRootID {
		t.Fatalf("retention root replay = %#v, %v", root, err)
	}
	if _, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{RootID: activeRootID, TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: ids["generation"], SnapshotSealID: "", RootKind: "generation", State: "live"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete retention root identity = %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,snapshot_seal_id,status,candidate_revision,artifact_digest,qualification_digest,qualified_at) VALUES('0198f2c0-7c7a-7f00-8a11-000000000010','target_sales_prod',$1::uuid,$2::uuid,'qualified',2,$3,$4,clock_timestamp())`, ids["plan"], ids["seal"], testDigest('e'), testDigest('3')); err == nil {
		t.Fatal("candidate accepted a snapshot seal owned by another candidate")
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

	// Reactivating a retained generation must create a fresh live root rather
	// than attempting to revive its immutable retired root. Using the same
	// generation here isolates the root rotation exercised by a G1->G2->G1
	// rollback without duplicating the full generation fixture.
	reactivationID := "0198f2c0-7c7a-7f00-8a11-000000000021"
	secondGenerationID := "0198f2c0-7c7a-7f00-8a11-000000000022"
	secondPublicationID := "0198f2c0-7c7a-7f00-8a11-000000000023"
	rootTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootTx.Exec(ctx, `
		INSERT INTO delivery.delivery_generation(
			generation_id,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,
			serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision)
		SELECT $1::uuid,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,
			serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision+1
		FROM delivery.delivery_generation WHERE generation_id=$2::uuid`, secondGenerationID, ids["generation"]); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("insert second generation: %v", err)
	}
	if _, err := rootTx.Exec(ctx, `
		INSERT INTO delivery.delivery_publication(
			publication_id,target_id,generation_id,candidate_id,snapshot_seal_id,expected_target_revision,result_target_revision,
			actor_id,state,request_digest,committed_at)
		SELECT $1::uuid,target_id,$2::uuid,candidate_id,snapshot_seal_id,2,3,actor_id,'committed',$3,clock_timestamp()
		FROM delivery.delivery_publication WHERE publication_id=$4::uuid`, secondPublicationID, secondGenerationID, testDigest('5'), ids["publication"]); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("insert second publication: %v", err)
	}
	if _, err := r.CreateRetentionRootTx(ctx, rootTx, DeliveryRetentionRoot{RootID: generationRootID(secondPublicationID), TargetID: "target_sales_prod", CandidateID: ids["candidate"], GenerationID: secondGenerationID, SnapshotSealID: ids["seal"], RootKind: "generation", State: "live"}); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("insert second generation root: %v", err)
	}
	if _, err := rootTx.Exec(ctx, `UPDATE delivery.delivery_active_pointer SET generation_id=$1::uuid,publication_id=$2::uuid WHERE target_id=$3`, secondGenerationID, secondPublicationID, "target_sales_prod"); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("point target at second generation: %v", err)
	}
	if _, err := r.RetireRetentionRootTx(ctx, rootTx, activeRootID); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("retire prior activation root: %v", err)
	}
	if _, err := rootTx.Exec(ctx, `
		INSERT INTO delivery.delivery_publication(
			publication_id,target_id,generation_id,candidate_id,snapshot_seal_id,expected_target_revision,result_target_revision,
			actor_id,state,request_digest,committed_at)
		SELECT $1::uuid,target_id,generation_id,candidate_id,snapshot_seal_id,3,4,actor_id,'committed',$2,clock_timestamp()
		FROM delivery.delivery_publication WHERE publication_id=$3::uuid`, reactivationID, testDigest('6'), ids["publication"]); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("insert reactivation publication: %v", err)
	}
	if _, err := rootTx.Exec(ctx, `UPDATE delivery.delivery_active_pointer SET generation_id=$1::uuid,publication_id=$2::uuid WHERE target_id=$3`, ids["generation"], reactivationID, "target_sales_prod"); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("point target back at retained generation: %v", err)
	}
	if _, err := r.RetireRetentionRootTx(ctx, rootTx, generationRootID(secondPublicationID)); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("retire second generation root: %v", err)
	}
	if err := ensureActivationRoot(ctx, rootTx, DeliveryPublication{PublicationID: reactivationID, TargetID: "target_sales_prod", GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"]}, "target_sales_prod"); err != nil {
		_ = rootTx.Rollback(ctx)
		t.Fatalf("establish reactivation root: %v", err)
	}
	if err := rootTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var oldState, secondState, newState, activeGenerationID string
	if err := p.QueryRow(ctx, `SELECT state FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, activeRootID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT state FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, generationRootID(secondPublicationID)).Scan(&secondState); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT state FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, generationRootID(reactivationID)).Scan(&newState); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT generation_id::text FROM delivery.delivery_active_pointer WHERE target_id='target_sales_prod'`).Scan(&activeGenerationID); err != nil {
		t.Fatal(err)
	}
	if oldState != "retiring" || secondState != "retiring" || newState != "live" || activeGenerationID != ids["generation"] {
		t.Fatalf("generation root rotation = old %q, second %q, new %q, active generation %q", oldState, secondState, newState, activeGenerationID)
	}
	if _, err := r.RetireRetentionRoot(ctx, generationRootID(reactivationID)); err == nil {
		t.Fatal("arbitrary retirement of the active generation root was accepted")
	}
	if activeRoot, err := loadRetentionRoot(ctx, p, generationRootID(reactivationID)); err != nil || activeRoot.State != "live" {
		t.Fatalf("active generation root after rejected retirement = %#v, %v", activeRoot, err)
	}
}
