package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authoringOwnershipOwner  = "018f4f2e-0000-7000-0000-000000000601"
	authoringOwnershipTarget = "018f4f2e-0000-7000-0000-000000000602"
)

func insertOwnershipPrincipal(t *testing.T, db *pgxpool.Pool, principalID string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type) VALUES ($1::uuid,'user') ON CONFLICT (id) DO NOTHING`, principalID); err != nil {
		t.Fatal(err)
	}
}

func insertOwnedDashboard(t *testing.T, db *pgxpool.Pool, projectID, dashboardID, eventID string) {
	t.Helper()
	insertAuthoringEvidence(t, db, eventID, projectID, dashboardID)
	if _, err := db.Exec(t.Context(), `
		INSERT INTO dashboard.authoring_dashboards
			(project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status,last_event_id)
		VALUES ($1,$2,$3::uuid,$4,'Owned dashboard','sales','private','draft',$5::uuid)
	`, projectID, dashboardID, authoringOwnershipOwner, strings.TrimPrefix(dashboardID, "dashboard:"), eventID); err != nil {
		t.Fatal(err)
	}
	const contentHash = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	revisionID := "018f4f2e-0000-7000-8000-000000009111"
	draftID := "018f4f2e-0000-7000-8000-000000009112"
	if _, err := db.Exec(t.Context(), `
		INSERT INTO dashboard.authoring_revisions
			(project_id,dashboard_id,revision_id,revision_number,document_json,content_hash,provenance_json,created_at)
		VALUES ($1,$2,$3::uuid,1,'{}'::jsonb,$4,'{}'::jsonb,clock_timestamp())
	`, projectID, dashboardID, revisionID, contentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO dashboard.authoring_drafts
			(project_id,dashboard_id,draft_id,revision_id,revision_number,content_hash,provenance_json)
		VALUES ($1,$2,$3::uuid,$4::uuid,1,$5,'{}'::jsonb)
	`, projectID, dashboardID, draftID, revisionID, contentHash); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPostgreSQL18OwnershipTransferIsIdempotentAndRetainsChildren(t *testing.T) {
	db := authoringDB(t)
	insertOwnershipPrincipal(t, db, authoringOwnershipTarget)
	insertOwnedDashboard(t, db, "project:ownership-transfer", "dashboard:ownership-transfer", "018f4f2e-0000-7000-8000-000000009101")
	repo, err := New(testDBTX{db}, &authoringAudit{}, authoringEvents{}, &authoringFence{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := repo.TransferOwnedObjects(t.Context(), authoringOwnershipOwner, authoringOwnershipTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Kind != "dashboard" {
		t.Fatalf("first transfer report = %#v", first)
	}
	second, err := repo.TransferOwnedObjects(t.Context(), authoringOwnershipOwner, authoringOwnershipTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 0 {
		t.Fatalf("retry transfer report = %#v, want no-op", second)
	}

	var owner string
	if err := db.QueryRow(t.Context(), `SELECT owner_principal_id::text FROM dashboard.authoring_dashboards WHERE project_id=$1 AND dashboard_id=$2`, "project:ownership-transfer", "dashboard:ownership-transfer").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != authoringOwnershipTarget {
		t.Fatalf("dashboard owner = %q, want %q", owner, authoringOwnershipTarget)
	}
	var revisions, drafts int
	if err := db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM dashboard.authoring_revisions WHERE project_id=$1 AND dashboard_id=$2), (SELECT count(*) FROM dashboard.authoring_drafts WHERE project_id=$1 AND dashboard_id=$2)`, "project:ownership-transfer", "dashboard:ownership-transfer").Scan(&revisions, &drafts); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 || drafts != 1 {
		t.Fatalf("retained dashboard children = revisions %d, drafts %d", revisions, drafts)
	}
	if report, err := repo.ListOwnedObjects(t.Context(), authoringOwnershipOwner); err != nil {
		t.Fatal(err)
	} else if len(report.Objects) != 0 {
		t.Fatalf("source ownership after transfer = %#v", report)
	}
	if report, err := repo.ListOwnedObjects(t.Context(), authoringOwnershipTarget); err != nil {
		t.Fatal(err)
	} else if len(report.Objects) != 1 {
		t.Fatalf("target ownership after transfer = %#v", report)
	}
}

func TestCreateDashboardSerializesWithPrincipalOffboardingPostgreSQL18(t *testing.T) {
	f := newAuthoringFixture(t)
	ctx := t.Context()
	deletionTx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deletionTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('leapview.platform-role-authority', 0))`); err != nil {
		_ = deletionTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := deletionTx.Exec(ctx, `SELECT id FROM access.principal WHERE id=$1::uuid FOR UPDATE`, authoringOwnershipOwner); err != nil {
		_ = deletionTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := deletionTx.Exec(ctx, `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp() WHERE id=$1::uuid`, authoringOwnershipOwner); err != nil {
		_ = deletionTx.Rollback(ctx)
		t.Fatal(err)
	}

	blockedCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, createErr := f.repo.Create(blockedCtx, authoring.CreateInput{ProjectID: f.project, Lifecycle: f.lifecycle, Revision: f.revision})
	cancel()
	if !errors.Is(createErr, context.DeadlineExceeded) {
		_ = deletionTx.Rollback(ctx)
		t.Fatalf("dashboard create during uncommitted offboarding = %v, want context deadline", createErr)
	}
	if err := deletionTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Create(ctx, authoring.CreateInput{ProjectID: f.project, Lifecycle: f.lifecycle, Revision: f.revision}); err == nil {
		t.Fatal("dashboard creation succeeded after principal offboarding")
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM dashboard.authoring_dashboards WHERE owner_principal_id=$1::uuid`, authoringOwnershipOwner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("dashboards created for offboarded principal = %d, want 0", count)
	}
}

func TestRepositoryPostgreSQL18OwnershipTombstoneIsIdempotentAndRetainsChildren(t *testing.T) {
	db := authoringDB(t)
	insertOwnedDashboard(t, db, "project:ownership-tombstone", "dashboard:ownership-tombstone", "018f4f2e-0000-7000-8000-000000009201")
	repo, err := New(testDBTX{db}, &authoringAudit{}, authoringEvents{}, &authoringFence{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := repo.TombstoneOwnedObjects(t.Context(), authoringOwnershipOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Lifecycle != "archived" {
		t.Fatalf("first tombstone report = %#v", first)
	}
	second, err := repo.TombstoneOwnedObjects(t.Context(), authoringOwnershipOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 0 {
		t.Fatalf("retry tombstone report = %#v, want no-op", second)
	}

	var status string
	if err := db.QueryRow(t.Context(), `SELECT status FROM dashboard.authoring_dashboards WHERE project_id=$1 AND dashboard_id=$2`, "project:ownership-tombstone", "dashboard:ownership-tombstone").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Fatalf("dashboard status = %q, want archived", status)
	}
	var revisions, drafts int
	if err := db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM dashboard.authoring_revisions WHERE project_id=$1 AND dashboard_id=$2), (SELECT count(*) FROM dashboard.authoring_drafts WHERE project_id=$1 AND dashboard_id=$2)`, "project:ownership-tombstone", "dashboard:ownership-tombstone").Scan(&revisions, &drafts); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 || drafts != 1 {
		t.Fatalf("retained dashboard children = revisions %d, drafts %d", revisions, drafts)
	}
	if report, err := repo.ListOwnedObjects(t.Context(), authoringOwnershipOwner); err != nil {
		t.Fatal(err)
	} else if len(report.Objects) != 0 {
		t.Fatalf("ownership after tombstone = %#v", report)
	}
}
