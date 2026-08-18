package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
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

func TestApprovalRepositoryAppendsRequestedGrantedAndRejectedEvents(t *testing.T) {
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
