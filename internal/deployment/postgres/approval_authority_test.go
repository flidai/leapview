package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// approvalNoopEvidenceAppender keeps authority tests focused on the atomic
// request/decision rows without coupling them to a particular event store.
type approvalNoopEvidenceAppender struct{}

func (approvalNoopEvidenceAppender) AppendApprovalOperation(context.Context, Tx, ApprovalOperation) error {
	return nil
}

func (approvalNoopEvidenceAppender) AppendApprovalEvent(context.Context, Tx, ApprovalEvent) error {
	return nil
}

func (approvalNoopEvidenceAppender) AppendApprovalAudit(context.Context, Tx, ApprovalAudit) error {
	return nil
}

type approvalRecordingAppender struct {
	mu                      sync.Mutex
	fail                    error
	operation, event, audit int
}

func (a *approvalRecordingAppender) append(kind string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch kind {
	case "operation":
		a.operation++
	case "event":
		a.event++
	case "audit":
		a.audit++
	}
	return a.fail
}

func (a *approvalRecordingAppender) AppendApprovalOperation(context.Context, Tx, ApprovalOperation) error {
	return a.append("operation")
}
func (a *approvalRecordingAppender) AppendApprovalEvent(context.Context, Tx, ApprovalEvent) error {
	return a.append("event")
}
func (a *approvalRecordingAppender) AppendApprovalAudit(context.Context, Tx, ApprovalAudit) error {
	return a.append("audit")
}

type approvalFixture struct {
	db         *pgxpool.Pool
	repository *Repository
	authority  *ApprovalAuthority
	ids        map[string]string
	request    ApprovalRequestInput
}

func newApprovalFixture(t *testing.T) approvalFixture {
	t.Helper()
	db := deliveryTestDB(t)
	ctx := t.Context()
	ids := map[string]string{
		"plan": "0198f2c0-7c7a-7f00-8a11-000000002101", "candidate": "0198f2c0-7c7a-7f00-8a11-000000002102",
		"attempt": "0198f2c0-7c7a-7f00-8a11-000000002103", "seal": "0198f2c0-7c7a-7f00-8a11-000000002104",
		"generation": "0198f2c0-7c7a-7f00-8a11-000000002105", "publication": "0198f2c0-7c7a-7f00-8a11-000000002106",
		"lease": "0198f2c0-7c7a-7f00-8a11-000000002107",
	}
	const targetID, projectID = "target_approval", "project_approval"
	lineage := &testActivationLineage{expected: ActivationLineageInput{TargetID: targetID, ProjectID: projectID, GenerationID: ids["generation"], CompiledGraphDigest: testDigest('b')}}
	repository := NewWithOptions(db, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: lineage})
	if _, err := repository.CreateTarget(ctx, TargetInput{TargetID: targetID, ProjectID: projectID, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	planInput := richPlanInputFixture(t, ids["plan"], targetID, projectID)
	planInput.QualificationRequired = true
	planInput.ApprovalRequired = true
	planInput.ApprovalPolicyRevision = 1
	createdPlan, err := repository.CreatePlan(ctx, planInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateCandidate(ctx, CandidateInput{CandidateID: ids["candidate"], TargetID: targetID, PlanID: ids["plan"], CandidateRevision: 1, ArtifactDigest: testDigest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: ids["attempt"], PlanID: ids["plan"], CandidateID: ids["candidate"], OwnerID: "builder-approval", PhysicalPoolID: "pool-approval", CatalogID: "catalog-approval", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: createdPlan.PlanDigest, Namespace: "candidate/approval", SessionIdentity: "session-approval", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: ids["attempt"], ServingArtifactID: "artifact-approval", ServingArtifactDigest: testDigest('e'), ServingStateID: "generation-test", OwnerID: "builder-approval", FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: ids["attempt"], OwnerID: "builder-approval", FencingEpoch: 1, SnapshotID: 42, CommitMarker: testCommitMarker(ids["attempt"], "pool-approval", testDigest('f'), createdPlan.PlanDigest)}); err != nil {
		t.Fatal(err)
	}
	seal := SnapshotSealInput{SealID: ids["seal"], AttemptID: ids["attempt"], CandidateID: ids["candidate"], PhysicalPoolID: "pool-approval", TenantDomain: "tenant-approval", Region: "us-east", EncryptionDomain: "enc-approval", ObjectNamespace: "objects/approval", CatalogDatabase: "ducklake", CatalogID: "catalog-approval", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000002108", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/approval", RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/approval/42", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/" + testDigest('e'), ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: testDigest('f'), PlanDigest: createdPlan.PlanDigest, CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-approval", ServingArtifactDigest: testDigest('e'), DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}
	if _, err := repository.CreateSnapshotSeal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	seedPhysicalRetentionFixture(t, db, seal)
	if _, err := repository.QualifyCandidate(ctx, ids["candidate"], ids["seal"], testDigest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateGeneration(ctx, GenerationInput{GenerationID: ids["generation"], TargetID: targetID, CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], PlanID: ids["plan"], PlanDigest: createdPlan.PlanDigest, ArtifactRoot: seal.ArtifactRoot, ArtifactRootDigest: seal.ArtifactRootDigest, ServingArtifactDigest: testDigest('e'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), GenerationRevision: 1}); err != nil {
		t.Fatal(err)
	}
	publicationDigest := testDigest('4')
	if _, err := repository.CreatePublication(ctx, PublicationInput{PublicationID: ids["publication"], TargetID: targetID, GenerationID: ids["generation"], CandidateID: ids["candidate"], SnapshotSealID: ids["seal"], ExpectedTargetRevision: 1, ActorID: "operator", RequestDigest: publicationDigest}); err != nil {
		t.Fatal(err)
	}
	request := ApprovalRequestInput{
		RequestID: "0198f2c0-7c7a-7f00-8a11-000000002110", PublicationID: ids["publication"], TargetID: targetID, CandidateID: ids["candidate"], GenerationID: ids["generation"], RequestDigest: publicationDigest, ExpectedTargetRevision: 1, PolicyRevision: 1,
		RequestedBy: ApprovalActor{PrincipalID: "requester", CredentialClass: "human", CredentialID: "requester-session", CredentialExpiresAt: time.Now().UTC().Add(3 * time.Hour)}, ExpiresAt: time.Now().UTC().Add(2 * time.Hour), Evidence: approvalEvidence(0),
	}
	request, err = normalizeApprovalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	noop := approvalNoopEvidenceAppender{}
	authority, err := newLowLevelApprovalAuthority(repository, ApprovalAuthorityOptions{Authorize: ApprovalAuthorizerFunc(func(context.Context, ApprovalAuthorizationInput) error { return nil }), Operation: noop, Event: noop, Audit: noop})
	if err != nil {
		t.Fatal(err)
	}
	return approvalFixture{db: db, repository: repository, authority: authority, ids: ids, request: request}
}

func approvalEvidence(n int) ApprovalEvidence {
	return ApprovalEvidence{OperationID: fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-000000002%03d", 120+n*3), EventID: fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-000000002%03d", 121+n*3), AuditID: fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-000000002%03d", 122+n*3), Metadata: []byte(`{"reason":"approval-test"}`)}
}

func approvalActor(principal, credential string) ApprovalActor {
	return ApprovalActor{PrincipalID: principal, CredentialClass: "human", CredentialID: credential, CredentialExpiresAt: time.Now().UTC().Add(3 * time.Hour)}
}

func TestApprovalAuthorityLifecycleAndAtomicity(t *testing.T) {
	f := newApprovalFixture(t)
	ctx := t.Context()
	// Every evidence appender is part of the caller-owned transaction. A
	// failure must leave no approval request behind.
	failing := &approvalRecordingAppender{fail: errors.New("event append failed")}
	failingAuthority, err := newLowLevelApprovalAuthority(f.repository, ApprovalAuthorityOptions{Authorize: ApprovalAuthorizerFunc(func(context.Context, ApprovalAuthorizationInput) error { return nil }), Operation: failing, Event: failing, Audit: failing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failingAuthority.Request(ctx, f.request); !errors.Is(err, failing.fail) {
		t.Fatalf("failed append = %v", err)
	}
	if _, err := f.authority.RequestByID(ctx, f.request.RequestID); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("rolled-back request = %v", err)
	}
	request, err := f.authority.Request(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if request.LatestDecision != nil || request.RequestedBy.CredentialExpiresAt.Nanosecond()%1000 != 0 {
		t.Fatalf("request precision/decision = %#v", request)
	}
	if replay, err := f.authority.Request(ctx, f.request); err != nil || replay.RequestID != request.RequestID {
		t.Fatalf("request replay = %#v, %v", replay, err)
	}
	changedExpiry := f.request
	changedExpiry.ExpiresAt = changedExpiry.ExpiresAt.Add(time.Minute)
	if _, err := f.authority.Request(ctx, changedExpiry); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("changed request expiry = %v", err)
	}
	second := f.request
	second.RequestID = "0198f2c0-7c7a-7f00-8a11-000000002111"
	second.Evidence = approvalEvidence(1)
	if _, err := f.authority.Request(ctx, second); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("second publication request = %v", err)
	}
	decisionInput := ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002130", ExpectedRevision: 0, Actor: approvalActor("reviewer", "reviewer-session"), Evidence: approvalEvidence(2)}
	approved, err := f.authority.Approve(ctx, decisionInput)
	if err != nil || approved.LatestDecision == nil || approved.LatestDecision.Decision != ApprovalActionApprove || approved.LatestDecision.Revision != 1 {
		t.Fatalf("approve = %#v, %v", approved, err)
	}
	if replay, err := f.authority.Approve(ctx, decisionInput); err != nil || replay.LatestDecision == nil || replay.LatestDecision.Revision != 1 {
		t.Fatalf("decision replay = %#v, %v", replay, err)
	}
	changedCredential := decisionInput
	changedCredential.Actor.CredentialExpiresAt = changedCredential.Actor.CredentialExpiresAt.Add(time.Minute)
	if _, err := f.authority.Approve(ctx, changedCredential); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("changed decision credential = %v", err)
	}
	if _, err := f.authority.Deny(ctx, ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002131", ExpectedRevision: 0, Actor: approvalActor("other-reviewer", "other-session"), Evidence: approvalEvidence(3)}); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("stale decision revision = %v", err)
	}
	denied, err := f.authority.Deny(ctx, ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002131", ExpectedRevision: 1, Actor: approvalActor("other-reviewer", "other-session"), Evidence: approvalEvidence(3)})
	if err != nil || denied.LatestDecision == nil || denied.LatestDecision.Decision != ApprovalActionDeny || denied.LatestDecision.Revision != 2 {
		t.Fatalf("deny = %#v, %v", denied, err)
	}
	if _, err := f.authority.Effective(ctx, f.request.RequestID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("denied effective = %v", err)
	}
	approved, err = f.authority.Approve(ctx, ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002132", ExpectedRevision: 2, Actor: approvalActor("reviewer", "reviewer-session-2"), Evidence: approvalEvidence(4)})
	if err != nil || approved.LatestDecision == nil || approved.LatestDecision.Decision != ApprovalActionApprove || approved.LatestDecision.Revision != 3 {
		t.Fatalf("second approve = %#v, %v", approved, err)
	}
	if effective, err := f.authority.Effective(ctx, f.request.RequestID); err != nil || effective.LatestDecision == nil || effective.LatestDecision.Decision != ApprovalActionApprove {
		t.Fatalf("effective approve = %#v, %v", effective, err)
	}
	results := make(chan error, 2)
	var decisions sync.WaitGroup
	for i, decisionID := range []string{"0198f2c0-7c7a-7f00-8a11-000000002133", "0198f2c0-7c7a-7f00-8a11-000000002134"} {
		decisions.Add(1)
		go func(i int, decisionID string) {
			defer decisions.Done()
			_, decideErr := f.authority.Revoke(ctx, ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: decisionID, ExpectedRevision: 3, Actor: approvalActor(fmt.Sprintf("revoker-%d", i), fmt.Sprintf("revoker-session-%d", i)), Evidence: approvalEvidence(5 + i)})
			results <- decideErr
		}(i, decisionID)
	}
	decisions.Wait()
	close(results)
	succeeded, conflicts := 0, 0
	for decideErr := range results {
		switch {
		case decideErr == nil:
			succeeded++
		case errors.Is(decideErr, ErrApprovalConflict):
			conflicts++
		default:
			t.Fatalf("concurrent revoke = %v", decideErr)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatalf("concurrent expected-revision decisions succeeded=%d conflicts=%d", succeeded, conflicts)
	}
	if _, err := f.authority.Effective(ctx, f.request.RequestID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("revoked effective = %v", err)
	}
	if _, err := f.authority.Approve(ctx, ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002136", ExpectedRevision: 4, Actor: approvalActor("requester", "requester-decision"), Evidence: approvalEvidence(7)}); !errors.Is(err, ErrApprovalSeparationOfDuty) {
		t.Fatalf("requester approval = %v", err)
	}
	var typedNil ApprovalAuthorizerFunc
	if _, err := newLowLevelApprovalAuthority(f.repository, ApprovalAuthorityOptions{Authorize: typedNil, Operation: approvalNoopEvidenceAppender{}, Event: approvalNoopEvidenceAppender{}, Audit: approvalNoopEvidenceAppender{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed nil authorizer = %v", err)
	}
	if _, err := NewApprovalAuthority(f.repository, ApprovalAuthorityOptions{Authorize: ApprovalAuthorizerFunc(func(context.Context, ApprovalAuthorizationInput) error { return nil }), Operation: approvalNoopEvidenceAppender{}, Event: approvalNoopEvidenceAppender{}, Audit: approvalNoopEvidenceAppender{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing activation consequence = %v", err)
	}
	// The publication-scoped effective query cannot be redirected to another
	// target even when all other immutable IDs are copied from a valid request.
	finalDecision := ApprovalDecisionInput{RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002135", ExpectedRevision: 4, Actor: approvalActor("reviewer", "reviewer-session-3"), Evidence: approvalEvidence(8)}
	approved, err = f.authority.Approve(ctx, finalDecision)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := depdb.New(f.db).EffectiveApprovalForPublication(ctx, depdb.EffectiveApprovalForPublicationParams{PublicationID: dbUUID(f.ids["publication"]), TargetID: "different-target", GenerationID: dbUUID(f.ids["generation"]), CandidateID: dbUUID(f.ids["candidate"]), RequestDigest: f.request.RequestDigest, ExpectedTargetRevision: 1}); err != nil || ok {
		t.Fatalf("cross-target effective = %v, %v", ok, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE delivery.delivery_approval_request SET target_id='tampered' WHERE request_id=$1::uuid`, f.request.RequestID); err == nil {
		t.Fatal("approval request mutation was accepted")
	}
	if _, err := f.db.Exec(ctx, `UPDATE delivery.delivery_approval_decision SET decision='denied' WHERE decision_id=$1::uuid`, decisionInput.DecisionID); err == nil {
		t.Fatal("approval decision mutation was accepted")
	}
	lease, err := f.repository.AcquireLease(ctx, LeaseInput{LeaseID: f.ids["lease"], TargetID: "target_approval", OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.repository.Activate(ctx, ActivationInput{PublicationID: f.ids["publication"], TargetID: "target_approval", GenerationID: f.ids["generation"], ExpectedTargetRevision: 1, RequestDigest: f.request.RequestDigest, ActorID: "operator", LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}); err != nil || result.Publication.ResultTargetRevision != 2 {
		t.Fatalf("approved activation = %#v, %v", result, err)
	}
	// Direct SQL cannot append a terminal decision: the publication lock and
	// pending-state trigger are the database guard, independent of Go callers.
	if _, err := f.db.Exec(ctx, `INSERT INTO delivery.delivery_approval_decision
		(decision_id, request_id, decision_revision, decision, decided_by,
		 decision_credential_class, decision_credential_id, decision_credential_expires_at,
		 operation_id, event_id, audit_id, evidence)
		VALUES ($1::uuid, $2::uuid, 99, 'approved', 'direct-sql-reviewer',
		 'human', 'direct-sql-session', clock_timestamp() + interval '1 hour',
		 $3::uuid, $4::uuid, $5::uuid, '{}'::jsonb)`,
		"0198f2c0-7c7a-7f00-8a11-000000002137", f.request.RequestID,
		"0198f2c0-7c7a-7f00-8a11-000000002138", "0198f2c0-7c7a-7f00-8a11-000000002139", "0198f2c0-7c7a-7f00-8a11-000000002140"); err == nil {
		t.Fatal("direct SQL terminal decision insertion was accepted")
	}
	if replay, err := f.authority.Request(ctx, f.request); err != nil || replay.RequestID != f.request.RequestID {
		t.Fatalf("terminal request replay = %#v, %v", replay, err)
	}
	if replay, err := f.authority.Approve(ctx, finalDecision); err != nil || replay.LatestDecision == nil || replay.LatestDecision.DecisionID != finalDecision.DecisionID {
		t.Fatalf("terminal decision replay = %#v, %v", replay, err)
	}
	denying, err := newLowLevelApprovalAuthority(f.repository, ApprovalAuthorityOptions{Authorize: ApprovalAuthorizerFunc(func(context.Context, ApprovalAuthorizationInput) error { return errors.New("changed authorization") }), Operation: approvalNoopEvidenceAppender{}, Event: approvalNoopEvidenceAppender{}, Audit: approvalNoopEvidenceAppender{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denying.Approve(ctx, finalDecision); !errors.Is(err, ErrApprovalUnauthorized) {
		t.Fatalf("terminal replay authorization = %v", err)
	}
	if _, err := denying.Request(ctx, f.request); !errors.Is(err, ErrApprovalUnauthorized) {
		t.Fatalf("terminal request replay authorization = %v", err)
	}
}

func TestApprovalRevokeSerializesWithActivation(t *testing.T) {
	f := newApprovalFixture(t)
	ctx := t.Context()
	if _, err := f.authority.Request(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.authority.Approve(ctx, ApprovalDecisionInput{
		RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002170", ExpectedRevision: 0,
		Actor: approvalActor("reviewer", "serialization-reviewer"), Evidence: approvalEvidence(30),
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := f.repository.AcquireLease(ctx, LeaseInput{LeaseID: f.ids["lease"], TargetID: "target_approval", OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type activationResult struct {
		result ActivationResult
		err    error
	}
	activation := make(chan activationResult, 1)
	revocation := make(chan error, 1)
	go func() {
		<-start
		_, revokeErr := f.authority.Revoke(ctx, ApprovalDecisionInput{
			RequestID: f.request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002171", ExpectedRevision: 1,
			Actor: approvalActor("revoker", "serialization-revoker"), Evidence: approvalEvidence(31),
		})
		revocation <- revokeErr
	}()
	go func() {
		<-start
		result, activateErr := f.repository.Activate(ctx, ActivationInput{
			PublicationID: f.ids["publication"], TargetID: "target_approval", GenerationID: f.ids["generation"],
			ExpectedTargetRevision: 1, RequestDigest: f.request.RequestDigest, ActorID: "operator", LeaseID: lease.LeaseID,
			OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
		})
		activation <- activationResult{result: result, err: activateErr}
	}()
	close(start)
	revokeErr := <-revocation
	activate := <-activation
	if revokeErr == nil && activate.err == nil {
		t.Fatal("revoke and activation both committed")
	}
	if revokeErr != nil && activate.err != nil {
		t.Fatalf("revoke and activation both failed: revoke=%v activation=%v", revokeErr, activate.err)
	}
	if activate.err == nil {
		if activate.result.Publication.State != "committed" || !errors.Is(revokeErr, ErrApprovalConflict) {
			t.Fatalf("activation-first serialization result=%#v revoke=%v", activate.result.Publication, revokeErr)
		}
		return
	}
	if !errors.Is(activate.err, ErrNotQualified) || revokeErr != nil {
		t.Fatalf("revoke-first serialization result revoke=%v activation=%v", revokeErr, activate.err)
	}
}

func TestApprovalAuthorityDBClockExpiry(t *testing.T) {
	f := newApprovalFixture(t)
	ctx := t.Context()
	request := f.request
	request.RequestID = "0198f2c0-7c7a-7f00-8a11-000000002150"
	request.Evidence = approvalEvidence(20)
	request.ExpiresAt = normalizeApprovalTime(time.Now().UTC().Add(250 * time.Millisecond))
	if _, err := f.authority.Request(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.authority.Effective(ctx, request.RequestID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("unapproved effective = %v", err)
	}
	// The request expiry is evaluated with the database clock, not the
	// caller's wall clock or a cached Go timestamp.
	time.Sleep(400 * time.Millisecond)
	if _, err := f.authority.Effective(ctx, request.RequestID); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("expired effective = %v", err)
	}
	if _, err := f.authority.Approve(ctx, ApprovalDecisionInput{RequestID: request.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002151", ExpectedRevision: 0, Actor: approvalActor("reviewer", "expiry-reviewer"), Evidence: approvalEvidence(21)}); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("expired decision = %v", err)
	}

	// A decision credential that expires after the decision but before use no
	// longer authorizes activation/effective approval.
	f2 := newApprovalFixture(t)
	shortRequest := f2.request
	shortRequest.RequestID = "0198f2c0-7c7a-7f00-8a11-000000002160"
	shortRequest.Evidence = approvalEvidence(22)
	if _, err := f2.authority.Request(ctx, shortRequest); err != nil {
		t.Fatal(err)
	}
	shortCredential := approvalActor("short-reviewer", "short-reviewer-session")
	shortCredential.CredentialExpiresAt = normalizeApprovalTime(time.Now().UTC().Add(250 * time.Millisecond))
	if _, err := f2.authority.Approve(ctx, ApprovalDecisionInput{RequestID: shortRequest.RequestID, DecisionID: "0198f2c0-7c7a-7f00-8a11-000000002161", ExpectedRevision: 0, Actor: shortCredential, Evidence: approvalEvidence(23)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.authority.Effective(ctx, shortRequest.RequestID); err != nil {
		t.Fatalf("short-lived effective before expiry = %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := f2.authority.Effective(ctx, shortRequest.RequestID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expired decision credential effective = %v", err)
	}
}
