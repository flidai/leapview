package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sourceTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "project_source_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
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

func sourceTestDigest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }

func sourceObjectRefsPlan(t *testing.T, db *pgxpool.Pool, r *Repository, expiresAt time.Time) (SyncPlan, []SourceSyncPlanEntryInput) {
	t.Helper()
	entries := []SourceSyncPlanEntryInput{
		// Deliberately provide these out of order; plan admission canonicalizes
		// ordinal order by logical source path.
		{Path: "z-model.yaml", Digest: sourceTestDigest("a"), SizeBytes: 3},
		{Path: "a-source.yaml", Digest: sourceTestDigest("b"), SizeBytes: 4},
	}
	canonical := append([]SourceSyncPlanEntryInput(nil), entries...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Path < canonical[j].Path })
	source := sourceDigest("project:object-refs", "leapview.yaml", snapshotEntries(canonical))
	input := SyncPlanInput{PlanID: uuid.New(), OperationID: uuid.New(), ProjectID: "project:object-refs", StorageSecurityDomain: "runtime", OwnerID: "object-owner", CandidateKey: "candidate-" + uuid.NewString(), SourceDigest: source, ProjectFile: "leapview.yaml", RequestDigest: sourceTestDigest("c"), ExpiresAt: expiresAt, Entries: entries}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.CreateSyncPlanTx(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return plan, entries
}

func admitSourceObjectRefBlob(t *testing.T, db *pgxpool.Pool, r *Repository, plan SyncPlan, entry SourceSyncPlanEntry) {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.InsertSourceBlobTx(t.Context(), tx, SourceBlobInput{ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain, Digest: entry.Digest, SizeBytes: entry.SizeBytes, ObjectKey: "sources/" + strings.TrimPrefix(entry.Digest, "sha256:"), ContentType: "text/plain", MetadataDigest: sourceTestDigest("d"), PlanID: plan.PlanID, OwnerID: plan.OwnerID}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSourceObjectRefsTx(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	plan, _ := sourceObjectRefsPlan(t, db, r, time.Now().Add(2*time.Minute))
	for _, entry := range plan.Entries {
		admitSourceObjectRefBlob(t, db, r, plan, entry)
	}

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := r.PlanSourceObjectRefsTx(t.Context(), tx, plan.PlanID, plan.OwnerID)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("plan object refs: %v", err)
	}
	if len(refs) != len(plan.Entries) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("plan object refs len=%d, want %d", len(refs), len(plan.Entries))
	}
	for i, ref := range refs {
		entry := plan.Entries[i]
		wantKey := "sources/" + strings.TrimPrefix(entry.Digest, "sha256:")
		if ref.PlanID != plan.PlanID || ref.ProjectID != plan.ProjectID || ref.StorageSecurityDomain != plan.StorageSecurityDomain || ref.Path != entry.Path || ref.Digest != entry.Digest || ref.SizeBytes != entry.SizeBytes || ref.Ordinal != i || ref.ObjectKey != wantKey || ref.ContentType != "text/plain" || ref.MetadataDigest != sourceTestDigest("d") {
			_ = tx.Rollback(t.Context())
			t.Fatalf("plan object ref[%d]=%#v", i, ref)
		}
	}
	// The repository must leave the caller-owned transaction usable and open.
	if _, err := tx.Exec(t.Context(), "SELECT 1"); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("caller transaction after plan object refs: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}

	if _, err := r.PlanSourceObjectRefsTx(t.Context(), nil, uuid.New(), plan.OwnerID); !errors.Is(err, ErrSourceInvalid) {
		t.Fatalf("non-transaction source object refs err=%v, want ErrSourceInvalid", err)
	}
	wrongTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PlanSourceObjectRefsTx(t.Context(), wrongTx, plan.PlanID, "other-owner"); !errors.Is(err, ErrSourceWrongOwner) {
		_ = wrongTx.Rollback(t.Context())
		t.Fatalf("wrong owner source object refs err=%v, want ErrSourceWrongOwner", err)
	}
	_ = wrongTx.Rollback(t.Context())

	missingPlanTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PlanSourceObjectRefsTx(t.Context(), missingPlanTx, uuid.New(), plan.OwnerID); !errors.Is(err, ErrSourceNotFound) {
		_ = missingPlanTx.Rollback(t.Context())
		t.Fatalf("missing plan source object refs err=%v, want ErrSourceNotFound", err)
	}
	_ = missingPlanTx.Rollback(t.Context())
}

func TestPlanSourceObjectRefsTxMissingBlob(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	plan, _ := sourceObjectRefsPlan(t, db, r, time.Now().Add(2*time.Minute))
	admitSourceObjectRefBlob(t, db, r, plan, plan.Entries[0])
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PlanSourceObjectRefsTx(t.Context(), tx, plan.PlanID, plan.OwnerID)
	if !errors.Is(err, ErrSourceConflict) || !strings.Contains(err.Error(), plan.Entries[1].Digest) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("missing blob source object refs err=%v, want digest conflict", err)
	}
	_ = tx.Rollback(t.Context())
}

func TestPlanSourceObjectRefsTxExpiredAndNonOpen(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	expired, _ := sourceObjectRefsPlan(t, db, r, time.Now().Add(100*time.Millisecond))
	time.Sleep(250 * time.Millisecond)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PlanSourceObjectRefsTx(t.Context(), tx, expired.PlanID, expired.OwnerID); !errors.Is(err, ErrSourceExpired) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("expired source object refs err=%v, want ErrSourceExpired", err)
	}
	_ = tx.Rollback(t.Context())

	committed, _ := sourceObjectRefsPlan(t, db, r, time.Now().Add(2*time.Minute))
	if _, err := db.Exec(t.Context(), `UPDATE project.source_sync_plan SET state='committed' WHERE plan_id=$1`, committed.PlanID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PlanSourceObjectRefsTx(t.Context(), tx, committed.PlanID, committed.OwnerID); !errors.Is(err, ErrSourceConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("non-open source object refs err=%v, want ErrSourceConflict", err)
	}
	_ = tx.Rollback(t.Context())
}

func TestSourcePlanBlobSnapshotLifecycle(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	digestA, digestB := sourceTestDigest("a"), sourceTestDigest("b")
	entries := []SourceSyncPlanEntryInput{{Path: "leapview.yaml", Digest: digestA, SizeBytes: 3}, {Path: "models/orders.yaml", Digest: digestB, SizeBytes: 4}}
	source := sourceDigest("project:sales", "leapview.yaml", snapshotEntries(entries))
	planInput := SyncPlanInput{PlanID: uuid.New(), OperationID: uuid.New(), ProjectID: "project:sales", StorageSecurityDomain: "runtime", OwnerID: "owner-1", CandidateKey: "default", SourceDigest: source, ProjectFile: "leapview.yaml", RequestDigest: sourceTestDigest("c"), ExpiresAt: time.Now().Add(2 * time.Minute), Entries: entries}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.CreateSyncPlanTx(t.Context(), tx, planInput)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	missing, err := r.ListMissingPlanSourceBlobDigestsTx(t.Context(), tx, plan.PlanID, plan.OwnerID)
	if err != nil || len(missing) != 2 {
		_ = tx.Rollback(t.Context())
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, item := range entries {
		tx, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.InsertSourceBlobTx(t.Context(), tx, SourceBlobInput{ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain, Digest: item.Digest, SizeBytes: item.SizeBytes, ObjectKey: "sources/" + strings.TrimPrefix(item.Digest, "sha256:"), ContentType: "text/plain", MetadataDigest: sourceTestDigest("d"), PlanID: plan.PlanID, OwnerID: plan.OwnerID}); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.SnapshotSourceObjectRefs(t.Context(), plan.ProjectID, plan.StorageSecurityDomain, source); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("building snapshot object refs err=%v, want ErrSourceNotFound", err)
	}
	attestationPayload := []byte(`{"sourceDigest":"` + source + `"}`)
	attestationDigest := sha256Identity(attestationPayload)
	commit := CommitSnapshotInput{PlanID: plan.PlanID, OwnerID: plan.OwnerID, SnapshotID: uuid.New(), ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain, SourceDigest: source, ProjectFile: plan.ProjectFile, ProjectDigest: sourceTestDigest("e"), ProjectArtifactObjectKey: "artifacts/project.json", ProjectArtifactDigest: sourceTestDigest("f"), ProjectArtifactSizeBytes: 10, ManifestObjectKey: "manifests/source.json", ManifestObjectDigest: sourceTestDigest("1"), ManifestObjectSizeBytes: 20, CompilerVersion: "compiler:v1", SchemaVersion: 1, Entries: []SourceSnapshotEntryInput{{Path: entries[0].Path, Digest: entries[0].Digest, SizeBytes: entries[0].SizeBytes}, {Path: entries[1].Path, Digest: entries[1].Digest, SizeBytes: entries[1].SizeBytes}}, Attestation: SourceAttestationInput{AttestationID: uuid.New(), SourceDigest: source, AttestationDigest: attestationDigest, Payload: attestationPayload}}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.CommitSnapshotTx(t.Context(), tx, commit)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := r.CommitSnapshotTx(t.Context(), tx, commit)
	if err != nil || replayed.SnapshotID != snapshot.SnapshotID {
		_ = tx.Rollback(t.Context())
		t.Fatalf("snapshot replay=%#v err=%v", replayed, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	conflict := commit
	conflict.SnapshotID = uuid.New()
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitSnapshotTx(t.Context(), tx, conflict); !errors.Is(err, ErrSourceConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting source snapshot replay err=%v", err)
	}
	_ = tx.Rollback(t.Context())
	got, err := r.Snapshot(t.Context(), plan.ProjectID, plan.StorageSecurityDomain, source)
	if err != nil || got.SnapshotID != snapshot.SnapshotID {
		t.Fatalf("snapshot=%#v err=%v", got, err)
	}
	if _, err := r.SnapshotAttestation(t.Context(), snapshot.SnapshotID, attestationDigest); err != nil {
		t.Fatal(err)
	}
	refs, err := r.SnapshotSourceObjectRefs(t.Context(), plan.ProjectID, plan.StorageSecurityDomain, source)
	if err != nil {
		t.Fatalf("snapshot object refs: %v", err)
	}
	if len(refs) != len(entries) {
		t.Fatalf("snapshot object refs len=%d, want %d", len(refs), len(entries))
	}
	for i, ref := range refs {
		entry := entries[i]
		wantKey := "sources/" + strings.TrimPrefix(entry.Digest, "sha256:")
		if ref.SnapshotID != snapshot.SnapshotID || ref.ProjectID != plan.ProjectID || ref.StorageSecurityDomain != plan.StorageSecurityDomain || ref.Path != entry.Path || ref.Digest != entry.Digest || ref.SizeBytes != entry.SizeBytes || ref.Ordinal != i || ref.ObjectKey != wantKey || ref.ContentType != "text/plain" || ref.MetadataDigest != sourceTestDigest("d") {
			t.Fatalf("snapshot object ref[%d]=%#v", i, ref)
		}
		blob, blobErr := r.SourceBlob(t.Context(), plan.ProjectID, plan.StorageSecurityDomain, entry.Digest)
		if blobErr != nil {
			t.Fatalf("source blob[%d]: %v", i, blobErr)
		}
		if blob.Digest != ref.Digest || blob.SizeBytes != ref.SizeBytes || blob.ObjectKey != ref.ObjectKey || blob.ContentType != ref.ContentType || blob.MetadataDigest != ref.MetadataDigest {
			t.Fatalf("source blob[%d]=%#v disagrees with ref=%#v", i, blob, ref)
		}
	}
	if _, err := r.SnapshotSourceObjectRefs(t.Context(), plan.ProjectID, "other-domain", source); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("wrong source domain object refs err=%v, want ErrSourceNotFound", err)
	}
	if _, err := r.SnapshotSourceObjectRefs(t.Context(), plan.ProjectID, plan.StorageSecurityDomain, sourceTestDigest("9")); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("wrong source digest object refs err=%v, want ErrSourceNotFound", err)
	}
	if _, err := r.SourceBlob(t.Context(), plan.ProjectID, "other-domain", entries[0].Digest); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("wrong source domain blob err=%v, want ErrSourceNotFound", err)
	}
	if _, err := r.SourceBlob(t.Context(), plan.ProjectID, plan.StorageSecurityDomain, sourceTestDigest("9")); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("wrong source digest blob err=%v, want ErrSourceNotFound", err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE project.source_blob SET object_key='mutated' WHERE project_id=$1`, plan.ProjectID); err == nil {
		t.Fatal("source blob UPDATE bypassed immutable trigger")
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM project.source_snapshot WHERE snapshot_id=$1`, snapshot.SnapshotID); err == nil {
		t.Fatal("source snapshot DELETE bypassed immutable trigger")
	}
	if _, err := db.Exec(t.Context(), `UPDATE project.source_attestation SET revision='mutated' WHERE snapshot_id=$1`, snapshot.SnapshotID); err == nil {
		t.Fatal("source attestation UPDATE bypassed immutable trigger")
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO project.source_sync_plan_entry(plan_id,path,digest,size_bytes,ordinal) VALUES($1,'late.yaml',$2,1,99)`, plan.PlanID, digestA); err == nil {
		t.Fatal("committed source plan accepted a late entry")
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO project.source_snapshot_entry(snapshot_id,project_id,storage_security_domain,path,digest,size_bytes,ordinal) VALUES($1,$2,$3,'late.yaml',$4,1,99)`, snapshot.SnapshotID, plan.ProjectID, plan.StorageSecurityDomain, digestA); err == nil {
		t.Fatal("sealed source snapshot accepted a late entry")
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if missing, err := r.ListMissingPlanSourceBlobDigestsTx(t.Context(), tx, plan.PlanID, plan.OwnerID); !errors.Is(err, ErrSourceExpired) && (err != nil || len(missing) != 0) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("post-commit missing=%v err=%v", missing, err)
	}
	_ = tx.Rollback(t.Context())
}

func TestSourcePlanRollbackAndValidation(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	if _, err := r.CreateSyncPlanTx(context.Background(), nil, SyncPlanInput{}); !errors.Is(err, ErrSourceInvalid) {
		t.Fatalf("nil tx err=%v", err)
	}
	if _, err := r.Snapshot(context.Background(), "project:x", "runtime", sourceTestDigest("a")); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing snapshot err=%v", err)
	}
}

func TestSourceSchemaRoleBoundaryAndImmutableRows(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "source-runtime", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "source-maintenance", Login: true})
	db := h.NewDatabase(t, "project_source_privilege_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var runtimeUpdate, runtimeDelete, maintenanceSelect bool
	if err := admin.QueryRow(t.Context(), `SELECT has_table_privilege('leapview_control_runtime','project.source_blob','UPDATE'), has_table_privilege('leapview_control_runtime','project.source_blob','DELETE'), has_table_privilege('leapview_control_maintenance','project.source_blob','SELECT')`).Scan(&runtimeUpdate, &runtimeDelete, &maintenanceSelect); err != nil {
		t.Fatal(err)
	}
	if runtimeUpdate || runtimeDelete || maintenanceSelect {
		t.Fatalf("source privileges runtime update=%v delete=%v maintenance select=%v", runtimeUpdate, runtimeDelete, maintenanceSelect)
	}
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE project.source_blob SET object_key='mutated'`); err == nil {
		t.Fatal("runtime UPDATE on immutable source blob unexpectedly succeeded")
	}
}
