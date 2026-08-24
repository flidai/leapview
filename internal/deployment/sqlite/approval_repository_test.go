package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/stretchr/testify/require"
)

func TestApprovalRepositoryPersistsImmutableScopeAndOptimisticTransitions(t *testing.T) {
	ctx, db, repository := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	approval := deployment.Approval{
		ID: "approval_1", ProjectID: "finance", DeploymentID: "deployment_1",
		Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1",
		Status:      deployment.ApprovalPending,
		RequestedBy: "publisher", RequestCredentialClass: deployment.CredentialClassWorkload,
		RequestCredentialID: "workload_1", RequestedAt: now,
		ExpiresAt: now.Add(time.Hour), Revision: 1,
	}
	created, err := repository.CreateApproval(ctx, approval)
	if err != nil || created != approval {
		t.Fatalf("CreateApproval() = %#v, %v", created, err)
	}
	restarted := NewRepositoryWithHooks(db, ActivationHooks{})
	loaded, err := restarted.ApprovalByDeployment(ctx, approval.DeploymentID)
	if err != nil || loaded != approval {
		t.Fatalf("ApprovalByDeployment() = %#v, %v", loaded, err)
	}
	if _, err := restarted.CreateApproval(ctx, approval); !errors.Is(err, deployment.ErrApprovalConflict) {
		t.Fatalf("duplicate CreateApproval() error = %v", err)
	}

	approved := approval
	approved.Status = deployment.ApprovalApproved
	approved.ApprovedBy = "reviewer"
	approved.ApprovalCredentialClass = deployment.CredentialClassHuman
	approved.ApprovalCredentialID = "session_review"
	approved.ApprovalCredentialExpiresAt = now.Add(time.Hour)
	approved.ApprovedAt = now.Add(time.Minute)
	approved.Revision++
	saved, err := restarted.SaveApproval(ctx, approved, approval.Revision)
	if err != nil || saved != approved {
		t.Fatalf("SaveApproval() = %#v, %v", saved, err)
	}
	if _, err := restarted.SaveApproval(ctx, approved, approval.Revision); !errors.Is(err, deployment.ErrApprovalConflict) {
		t.Fatalf("stale SaveApproval() error = %v", err)
	}
}

func TestApprovalAuditIntentTracksAtomicRequestAndDecision(t *testing.T) {
	ctx, db, _ := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	repository := NewRepositoryWithHooks(db, ActivationHooks{Audit: accesssqlite.NewRepository(db)})
	pending := deployment.Approval{ID: "approval-audit", ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1", Status: deployment.ApprovalPending, RequestedBy: "publisher", RequestCredentialClass: deployment.CredentialClassWorkload, RequestCredentialID: "workload_1", RequestedAt: now, ExpiresAt: now.Add(time.Hour), Revision: 1}
	requestMetadata, err := deploymentgen.EncodeGenRequestDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalRequestedAuditPayload{DeploymentId: pending.DeploymentID, ApprovalId: ""})
	if err != nil {
		t.Fatal(err)
	}
	requestIntent := access.AuditIntent{EventID: "approval-request-audit", Source: "deployment", Operation: "requestDeploymentApproval", Action: "deployment.approval_requested", ResourceKind: "project", ResourceID: "finance", Capability: access.CapabilityResourcePublish, Outcome: "success", AggregateKey: "deployment:deployment_1:approval", AggregateSequence: 0, MetadataJSON: requestMetadata}
	if _, err := repository.CreateApproval(deployment.WithAuditIntent(ctx, requestIntent), pending); err != nil {
		t.Fatal(err)
	}
	var metadata string
	if err := db.QueryRowContext(ctx, `SELECT metadata_json FROM audit_outbox WHERE event_id = ?`, requestIntent.EventID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(metadata), &envelope); err != nil {
		t.Fatal(err)
	}
	payload, ok := envelope["payload"].(map[string]any)
	if !ok || payload["approvalId"] != pending.ID {
		t.Fatalf("request audit payload = %#v, want approval id %q", payload, pending.ID)
	}
	if _, exists := envelope["approvalId"]; exists {
		t.Fatalf("request audit envelope contains undeclared top-level approvalId: %s", metadata)
	}
	approved := pending
	approved.Status = deployment.ApprovalApproved
	approved.ApprovedBy = "reviewer"
	approved.ApprovalCredentialClass = deployment.CredentialClassHuman
	approved.ApprovalCredentialID = "session-review"
	approved.ApprovalCredentialExpiresAt = now.Add(time.Hour)
	approved.ApprovedAt = now.Add(time.Minute)
	approved.Revision = 2
	decisionIntent := access.AuditIntent{EventID: "approval-decision-audit", Source: "deployment", Operation: "approveDeployment", Action: "deployment.approved", ResourceKind: "project", ResourceID: "finance", Capability: access.CapabilityProjectAdmin, Outcome: "success", AggregateKey: "deployment:deployment_1:approval", AggregateSequence: 0, MetadataJSON: `{"approvalId":"approval-audit","approvalRevision":2}`}
	if _, err := repository.SaveApproval(deployment.WithAuditIntent(ctx, decisionIntent), approved, 1); err != nil {
		t.Fatal(err)
	}
	var aggregateKey string
	var sequence int64
	if err := db.QueryRowContext(ctx, `SELECT aggregate_key, aggregate_sequence FROM audit_outbox WHERE event_id = ?`, decisionIntent.EventID).Scan(&aggregateKey, &sequence); err != nil {
		t.Fatal(err)
	}
	if aggregateKey != "deployment:deployment_1:approval:approval-audit" {
		t.Fatalf("decision aggregate key = %q, want approval-specific key", aggregateKey)
	}
	if sequence != 2 {
		t.Fatalf("decision aggregate sequence = %d, want 2", sequence)
	}
}

func TestApprovalReplacementAuditIntentUsesUniqueAggregateIdentity(t *testing.T) {
	ctx, db, _ := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	audit := accesssqlite.NewRepository(db)
	repository := NewRepositoryWithHooks(db, ActivationHooks{Audit: audit})
	first := deployment.Approval{
		ID: "approval-first", ProjectID: "finance", DeploymentID: "deployment_1",
		Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1",
		Status: deployment.ApprovalPending, RequestedBy: "publisher",
		RequestCredentialClass: deployment.CredentialClassWorkload, RequestCredentialID: "workload_1",
		RequestedAt: now, ExpiresAt: now.Add(time.Minute), Revision: 1,
	}
	requestIntent := access.AuditIntent{
		EventID: "approval-replacement-first", Source: "deployment", Operation: "requestDeploymentApproval",
		Action: "deployment.approval_requested", ResourceKind: "project", ResourceID: "finance",
		Capability: access.CapabilityResourcePublish, Outcome: "success",
		AggregateKey: "deployment:deployment_1:approval", AggregateSequence: 0,
		MetadataJSON: `{"approvalId":""}`,
	}
	if _, err := repository.CreateApproval(deployment.WithAuditIntent(ctx, requestIntent), first); err != nil {
		t.Fatal(err)
	}

	expired := first
	expired.Status = deployment.ApprovalExpired
	expired.Revision = 2
	if _, err := repository.SaveApproval(ctx, expired, first.Revision); err != nil {
		t.Fatal(err)
	}

	replacement := first
	replacement.ID = "approval-replacement"
	replacement.RequestCredentialID = "workload_2"
	replacement.RequestedAt = now.Add(2 * time.Minute)
	replacement.ExpiresAt = now.Add(time.Hour)
	replacement.Revision = 1
	replacement.Status = deployment.ApprovalPending
	replacementIntent := requestIntent
	replacementIntent.EventID = "approval-replacement-second"
	if _, err := repository.CreateApproval(deployment.WithAuditIntent(ctx, replacementIntent), replacement); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		eventID string
		wantKey string
	}{
		{requestIntent.EventID, "deployment:deployment_1:approval:approval-first"},
		{replacementIntent.EventID, "deployment:deployment_1:approval:approval-replacement"},
	} {
		var aggregateKey string
		var sequence int64
		if err := db.QueryRowContext(ctx, `SELECT aggregate_key, aggregate_sequence FROM audit_outbox WHERE event_id = ?`, test.eventID).Scan(&aggregateKey, &sequence); err != nil {
			t.Fatal(err)
		}
		if aggregateKey != test.wantKey || sequence != 1 {
			t.Fatalf("%s audit aggregate = %s/%d, want %s/1", test.eventID, aggregateKey, sequence, test.wantKey)
		}
	}
}

func TestApprovalCreateRollsBackWhenAuditIntentFails(t *testing.T) {
	ctx, db, _ := testRepository(t)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	injected := errors.New("injected audit failure")
	repository := NewRepositoryWithHooks(db, ActivationHooks{Audit: access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error { return injected })})
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	pending := deployment.Approval{ID: "approval-audit-rollback", ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1", Status: deployment.ApprovalPending, RequestedBy: "publisher", RequestCredentialClass: deployment.CredentialClassWorkload, RequestCredentialID: "workload_1", RequestedAt: now, ExpiresAt: now.Add(time.Hour), Revision: 1}
	intent := access.AuditIntent{EventID: "approval-create-rollback", Source: "deployment", Operation: "requestDeploymentApproval", Action: "deployment.approval_requested", Outcome: "success", AggregateKey: "deployment:deployment_1:approval", AggregateSequence: 1, MetadataJSON: `{}`}
	if _, err := repository.CreateApproval(deployment.WithAuditIntent(ctx, intent), pending); !errors.Is(err, injected) {
		t.Fatalf("CreateApproval error = %v, want injected audit failure", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_approvals WHERE id = ?`, pending.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back approval count = %d, want 0", count)
	}
}

func TestApprovalRepositoryAppendsRequestedGrantedRejectedAndRevokedEvents(t *testing.T) {
	ctx, db, repository := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO delivery_target_revisions (target_id,project_id,environment,target_revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`, "approval-target", "finance", "prod", 0, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	pending := deployment.Approval{ID: "approval-events", ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1", Status: deployment.ApprovalPending, RequestedBy: "publisher", RequestCredentialClass: deployment.CredentialClassWorkload, RequestCredentialID: "workload_1", RequestedAt: now, ExpiresAt: now.Add(time.Hour), Revision: 1}
	if _, err := repository.CreateApproval(ctx, pending); err != nil {
		t.Fatal(err)
	}
	requested := deployment.DeliveryEventID("approval-target", pending.RequestDigest, "approval_requested", "approval", pending.ID)
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_events WHERE id=? AND actor_id=?`, requested, pending.RequestedBy).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("approval_requested count=%d", count)
	}
	approved := pending
	approved.Status = deployment.ApprovalApproved
	approved.ApprovedBy = "reviewer"
	approved.ApprovalCredentialClass = deployment.CredentialClassHuman
	approved.ApprovalCredentialID = "session-review"
	approved.ApprovalCredentialExpiresAt = now.Add(time.Hour)
	approved.ApprovedAt = now.Add(time.Minute)
	approved.Revision++
	if _, err := repository.SaveApproval(ctx, approved, pending.Revision); err != nil {
		t.Fatal(err)
	}
	granted := deployment.DeliveryEventID("approval-target", pending.RequestDigest, "approval_granted", "approval", pending.ID)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_events WHERE id=? AND actor_id=?`, granted, approved.ApprovedBy).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("approval_granted count=%d", count)
	}
	revoked := approved
	revoked.Status = deployment.ApprovalRevoked
	revoked.RevokedBy = "reviewer"
	revoked.RevokedAt = now.Add(2 * time.Minute)
	revoked.Revision++
	if _, err := repository.SaveApproval(ctx, revoked, approved.Revision); err != nil {
		t.Fatal(err)
	}
	revokedEvent := deployment.DeliveryEventID("approval-target", pending.RequestDigest, "approval_revoked", "approval", pending.ID)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_events WHERE id=? AND actor_id=?`, revokedEvent, revoked.RevokedBy).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("approval_revoked count=%d", count)
	}

	// A second approval identity exercises the rejection producer without
	// changing the first immutable decision event.
	if _, err := db.ExecContext(ctx, `INSERT INTO project_deployments (id,project_id,environment,generation_id,artifact_digest,request_digest,status,created_by) VALUES ('deployment_2','finance','prod','generation_approval',?,?, 'pending','publisher')`, deploymentDigest("a"), deploymentDigest("c")); err != nil {
		t.Fatal(err)
	}
	denied := pending
	denied.ID = "approval-denied-events"
	denied.DeploymentID = "deployment_2"
	denied.RequestDigest = deploymentDigest("c")
	denied.RequestCredentialID = "workload-2"
	if _, err := repository.CreateApproval(ctx, denied); err != nil {
		t.Fatal(err)
	}
	denied.Status = deployment.ApprovalDenied
	denied.ApprovedBy = "reviewer"
	denied.ApprovalCredentialClass = deployment.CredentialClassHuman
	denied.ApprovalCredentialID = "session-review"
	denied.ApprovalCredentialExpiresAt = now.Add(time.Hour)
	denied.ApprovedAt = now.Add(2 * time.Minute)
	denied.Revision++
	if _, err := repository.SaveApproval(ctx, denied, 1); err != nil {
		t.Fatal(err)
	}
	rejected := deployment.DeliveryEventID("approval-target", denied.RequestDigest, "approval_rejected", "approval", denied.ID)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_events WHERE id=? AND actor_id=?`, rejected, denied.ApprovedBy).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("approval_rejected count=%d", count)
	}
}

func TestApprovalRepositoryFailsClosedOnTamperedEvidence(t *testing.T) {
	ctx, db, repository := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	approval := deployment.Approval{
		ID: "approval_1", ProjectID: "finance", DeploymentID: "deployment_1",
		Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1",
		Status:      deployment.ApprovalPending,
		RequestedBy: "publisher", RequestCredentialClass: deployment.CredentialClassWorkload,
		RequestCredentialID: "workload_1", RequestedAt: now,
		ExpiresAt: now.Add(time.Hour), Revision: 1,
	}
	if _, err := repository.CreateApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE deployment_approvals
		SET status = 'approved',
		    approved_by = requested_by,
		    approval_credential_class = 'human',
		    approval_credential_id = 'session_self',
		    approved_at = ?
		WHERE id = ?`, now.Add(time.Minute).Format(time.RFC3339Nano), approval.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApprovalByDeployment(ctx, approval.DeploymentID); !errors.Is(err, deployment.ErrApprovalInvalid) {
		t.Fatalf("ApprovalByDeployment() error = %v, want ErrApprovalInvalid", err)
	}
}

func TestApprovalRepositoryRetainsExpiredHistoryAndAcceptsReplacement(t *testing.T) {
	ctx, db, repository := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	first := deployment.Approval{
		ID: "approval_1", ProjectID: "finance", DeploymentID: "deployment_1",
		Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1",
		Status:      deployment.ApprovalPending,
		RequestedBy: "publisher", RequestCredentialClass: deployment.CredentialClassWorkload,
		RequestCredentialID: "workload_1", RequestedAt: now,
		ExpiresAt: now.Add(time.Minute), Revision: 1,
	}
	if _, err := repository.CreateApproval(ctx, first); err != nil {
		t.Fatal(err)
	}
	expired := first
	expired.Status = deployment.ApprovalExpired
	expired.Revision++
	if _, err := repository.SaveApproval(ctx, expired, first.Revision); err != nil {
		t.Fatal(err)
	}
	replacement := first
	replacement.ID = "approval_2"
	replacement.RequestCredentialID = "workload_2"
	replacement.RequestedAt = now.Add(2 * time.Minute)
	replacement.ExpiresAt = now.Add(time.Hour)
	if _, err := repository.CreateApproval(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	current, err := repository.ApprovalByDeployment(ctx, first.DeploymentID)
	require.NoError(t, err)
	if current.ID != replacement.ID {
		t.Fatalf("current approval = %q, want %q", current.ID, replacement.ID)
	}
	var history int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM deployment_approvals WHERE deployment_id = ?`,
		first.DeploymentID,
	).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("approval history count = %d, want 2", history)
	}
}

func TestApprovalRepositoryPersistsDeniedDecision(t *testing.T) {
	ctx, db, repository := testRepository(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	insertApprovalPrincipalsAndDeployment(t, ctx, db)
	pending := deployment.Approval{
		ID: "approval_denied", ProjectID: "finance", DeploymentID: "deployment_1",
		Environment: "prod", RequestDigest: deploymentDigest("b"), ReleaseID: "release_1",
		Status: deployment.ApprovalPending, RequestedBy: "publisher",
		RequestCredentialClass: deployment.CredentialClassWorkload,
		RequestCredentialID:    "workload_1", RequestedAt: now,
		ExpiresAt: now.Add(time.Hour), Revision: 1,
	}
	if _, err := repository.CreateApproval(ctx, pending); err != nil {
		t.Fatal(err)
	}
	denied := pending
	denied.Status = deployment.ApprovalDenied
	denied.ApprovedBy = "reviewer"
	denied.ApprovalCredentialClass = deployment.CredentialClassHuman
	denied.ApprovalCredentialID = "session_review"
	denied.ApprovalCredentialExpiresAt = now.Add(time.Hour)
	denied.ApprovedAt = now.Add(time.Minute)
	denied.Revision++
	if _, err := repository.SaveApproval(ctx, denied, pending.Revision); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewRepositoryWithHooks(db, ActivationHooks{}).
		ApprovalByDeployment(ctx, pending.DeploymentID)
	require.NoError(t, err)
	if loaded != denied {
		t.Fatalf("loaded denial = %#v, want %#v", loaded, denied)
	}
}

func insertApprovalPrincipalsAndDeployment(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	for _, id := range []string{"publisher", "reviewer"} {
		if _, err := db.ExecContext(
			ctx,
			`INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`,
			id,
			id+"@example.test",
			id,
		); err != nil {
			t.Fatal(err)
		}
	}
	digest := deploymentDigest("a")
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO serving_states (id, project_id, environment, status, source, digest)
		 VALUES ('generation_approval', 'finance', 'prod', 'validated', 'publish', ?)`,
		digest,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO project_deployments (
		   id, project_id, environment, generation_id, artifact_digest, request_digest, status, created_by
		 ) VALUES ('deployment_1', 'finance', 'prod', 'generation_approval', ?, ?, 'pending', 'publisher')`,
		digest,
		deploymentDigest("b"),
	); err != nil {
		t.Fatal(err)
	}
}
