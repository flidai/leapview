package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func TestDeliveryOperatorSnapshotAcceptsSQLiteAdmissionTimestamp(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatalf("create delivery plan: %v", err)
	}

	poolID := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, poolID)
	compatibilityJSON := `{"duckdb_runtime":"v1","ducklake_extension":"v1","catalog_format":"ducklake:v1","storage_implementation":"s3","object_naming_contract":"names:v1"}`
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO physical_pool_admissions (
			pool_id, compatibility_json, evidence_json, evidence_digest,
			compatibility_digest, conformance_version
		) VALUES (?, ?, '{}', ?, ?, 'qualification:v1')`,
		poolID, compatibilityJSON, repoDeliveryDigest('7'), repoDeliveryDigest('8')); err != nil {
		t.Fatalf("insert physical-pool admission: %v", err)
	}
	var admittedAt string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT admitted_at FROM physical_pool_admissions WHERE pool_id = ?`, poolID).Scan(&admittedAt); err != nil {
		t.Fatalf("read admission timestamp: %v", err)
	}
	if !strings.Contains(admittedAt, " ") || strings.Contains(admittedAt, "T") {
		t.Fatalf("admission timestamp = %q, want SQLite CURRENT_TIMESTAMP representation", admittedAt)
	}

	lease := deployment.DeliveryWriterLease{
		ID: "writer-operator-1", AttemptID: "attempt-operator-1", PhysicalPoolID: poolID,
		OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	attempt := deployment.DeliveryBuildAttempt{
		ID: lease.AttemptID, PlanID: plan.ID, PlanDigest: plan.Digest,
		SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest,
		PhysicalPoolID: poolID, WriterLeaseID: lease.ID, CreatedAt: now,
	}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt); err != nil {
		t.Fatalf("create build attempt: %v", err)
	}

	snapshot, err := repo.DeliveryOperatorSnapshot(t.Context(), plan.ProjectID.String(), plan.Environment)
	if err != nil {
		t.Fatalf("read operator snapshot: %v", err)
	}
	if len(snapshot.PhysicalPools) != 1 {
		t.Fatalf("physical-pool admissions = %d, want 1", len(snapshot.PhysicalPools))
	}
	if snapshot.PhysicalPools[0].AdmittedAt.IsZero() || snapshot.PhysicalPools[0].AdmittedAt.Location() != time.UTC {
		t.Fatalf("admission time = %v, want non-zero UTC", snapshot.PhysicalPools[0].AdmittedAt)
	}
}

func TestDeliveryOperatorReadFailureClassifiesTimestampWithoutLosingCause(t *testing.T) {
	_, parseErr := time.Parse(time.RFC3339Nano, "2026-08-28 12:00:00")
	err := deliveryOperatorReadFailure("physical_pool_admission_timestamp", parseErr)
	diagnostic, ok := err.(interface {
		DeliveryReadStage() string
		DeliveryReadCategory() string
	})
	if !ok {
		t.Fatal("operator read failure does not expose bounded diagnostics")
	}
	if diagnostic.DeliveryReadStage() != "physical_pool_admission_timestamp" || diagnostic.DeliveryReadCategory() != "timestamp_parse" {
		t.Fatalf("diagnostic = %s/%s", diagnostic.DeliveryReadStage(), diagnostic.DeliveryReadCategory())
	}
	if !strings.Contains(err.Error(), "cannot parse") {
		t.Fatalf("internal error lost parse cause: %v", err)
	}
}
