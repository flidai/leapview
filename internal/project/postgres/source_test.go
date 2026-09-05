package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type countingSourceTx struct {
	pgx.Tx
	queries int
}

func (tx *countingSourceTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	tx.queries++
	return tx.Tx.Exec(ctx, sql, arguments...)
}

func (tx *countingSourceTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.queries++
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *countingSourceTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.queries++
	return tx.Tx.QueryRow(ctx, sql, args...)
}

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

func TestSourcePlanAndSnapshotNearLimitUseBoundedQueries(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	const entryCount = maxSourceSnapshotFiles
	planEntries := make([]SourceSyncPlanEntryInput, entryCount)
	snapshotEntries := make([]SourceSnapshotEntryInput, entryCount)
	for i := range planEntries {
		path := fmt.Sprintf("files/%05d.yaml", i)
		digest := sha256Identity([]byte(path))
		planEntries[i] = SourceSyncPlanEntryInput{Path: path, Digest: digest, SizeBytes: int64(i%17 + 1), Ordinal: i}
		snapshotEntries[i] = SourceSnapshotEntryInput{Path: path, Digest: digest, SizeBytes: planEntries[i].SizeBytes, Ordinal: i}
	}
	projectID := "project:source-near-limit"
	domain := "runtime"
	ownerID := "near-limit-owner"
	source := sourceDigest(projectID, planEntries[0].Path, snapshotEntries)
	planInput := SyncPlanInput{
		PlanID: uuid.New(), OperationID: uuid.New(), ProjectID: projectID,
		StorageSecurityDomain: domain, OwnerID: ownerID, CandidateKey: "near-limit",
		SourceDigest: source, ProjectFile: planEntries[0].Path,
		RequestDigest: sourceTestDigest("a"), ExpiresAt: time.Now().Add(5 * time.Minute),
		Entries: planEntries,
	}

	baseTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	planTx := &countingSourceTx{Tx: baseTx}
	plan, err := r.CreateSyncPlanTx(t.Context(), planTx, planInput)
	if err != nil {
		_ = planTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if planTx.queries > 8 {
		_ = planTx.Rollback(t.Context())
		t.Fatalf("near-limit plan used %d queries, want at most 8", planTx.queries)
	}
	if err := planTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != entryCount || plan.Entries[0].Path != planEntries[0].Path || plan.Entries[entryCount-1].Path != planEntries[entryCount-1].Path {
		t.Fatalf("near-limit plan ordering changed: entries=%d first=%q last=%q", len(plan.Entries), plan.Entries[0].Path, plan.Entries[len(plan.Entries)-1].Path)
	}

	entryBatch, err := sourceEntryBatch(planEntries)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO project.source_blob
			(project_id, storage_security_domain, digest, size_bytes, object_key, content_type, metadata_digest)
		SELECT $1, $2, entry.digest, entry.size_bytes,
		       'sources/' || substring(entry.digest from 8), 'text/plain', $3
		FROM jsonb_to_recordset($4::jsonb)
			AS entry(path text, digest text, size_bytes bigint, ordinal integer)
		WHERE entry.ordinal < $5`,
		projectID, domain, sourceTestDigest("b"), entryBatch, entryCount-1); err != nil {
		t.Fatal(err)
	}
	// Prove that one missing blob aborts before any snapshot state is written.
	attestationPayload := []byte(`{"sourceDigest":"` + source + `"}`)
	commit := CommitSnapshotInput{
		PlanID: plan.PlanID, OwnerID: ownerID, SnapshotID: uuid.New(), ProjectID: projectID,
		StorageSecurityDomain: domain, SourceDigest: source, ProjectFile: plan.ProjectFile,
		ProjectDigest: sourceTestDigest("c"), ProjectArtifactObjectKey: "artifacts/project.json",
		ProjectArtifactDigest: sourceTestDigest("d"), ProjectArtifactSizeBytes: 10,
		ManifestObjectKey: "manifests/source.json", ManifestObjectDigest: sourceTestDigest("e"),
		ManifestObjectSizeBytes: 20, CompilerVersion: "compiler:v1", SchemaVersion: 1,
		Entries: snapshotEntries, Attestation: SourceAttestationInput{
			AttestationID: uuid.New(), SourceDigest: source,
			AttestationDigest: sha256Identity(attestationPayload), Payload: attestationPayload,
		},
	}
	missingTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitSnapshotTx(t.Context(), missingTx, commit); !errors.Is(err, ErrSourceConflict) {
		_ = missingTx.Rollback(t.Context())
		t.Fatalf("near-limit missing blob commit err=%v, want ErrSourceConflict", err)
	}
	if err := missingTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var snapshots int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.source_snapshot WHERE snapshot_id=$1`, commit.SnapshotID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("missing-blob snapshot rows=%d err=%v, want atomic rollback", snapshots, err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO project.source_blob
			(project_id, storage_security_domain, digest, size_bytes, object_key, content_type, metadata_digest)
		VALUES ($1,$2,$3,$4,$5,'text/plain',$6)`, projectID, domain,
		planEntries[entryCount-1].Digest, planEntries[entryCount-1].SizeBytes,
		"sources/"+strings.TrimPrefix(planEntries[entryCount-1].Digest, "sha256:"), sourceTestDigest("b")); err != nil {
		t.Fatal(err)
	}

	baseTx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	commitTx := &countingSourceTx{Tx: baseTx}
	snapshot, err := r.CommitSnapshotTx(t.Context(), commitTx, commit)
	if err != nil {
		_ = commitTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if commitTx.queries > 16 {
		_ = commitTx.Rollback(t.Context())
		t.Fatalf("near-limit snapshot used %d queries, want at most 16", commitTx.queries)
	}
	if snapshot.SnapshotID != commit.SnapshotID {
		_ = commitTx.Rollback(t.Context())
		t.Fatalf("snapshot id=%s, want %s", snapshot.SnapshotID, commit.SnapshotID)
	}
	if err := commitTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, err := r.SnapshotEntries(t.Context(), snapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != entryCount || stored[0].Path != snapshotEntries[0].Path || stored[entryCount-1].Path != snapshotEntries[entryCount-1].Path {
		t.Fatalf("near-limit snapshot ordering changed: entries=%d first=%q last=%q", len(stored), stored[0].Path, stored[len(stored)-1].Path)
	}
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

func TestSourceSyncPlanAllowsDistinctPlansForSameRequest(t *testing.T) {
	db := sourceTestDB(t)
	r := New(db)
	entries := []SourceSyncPlanEntryInput{{Path: "leapview.yaml", Digest: sourceTestDigest("a"), SizeBytes: 1}}
	input := SyncPlanInput{
		PlanID: uuid.New(), OperationID: uuid.New(), ProjectID: "project:source-plans",
		StorageSecurityDomain: "runtime", OwnerID: "owner-1", CandidateKey: "default",
		SourceDigest: sourceDigest("project:source-plans", "leapview.yaml", snapshotEntries(entries)),
		ProjectFile:  "leapview.yaml", RequestDigest: sourceTestDigest("b"),
		ExpiresAt: time.Now().Add(2 * time.Minute), Entries: entries,
	}
	create := func(in SyncPlanInput) SyncPlan {
		t.Helper()
		tx, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := r.CreateSyncPlanTx(t.Context(), tx, in)
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		return plan
	}

	first := create(input)
	secondInput := input
	secondInput.PlanID = uuid.New()
	secondInput.OperationID = uuid.New()
	second := create(secondInput)
	if second.PlanID == first.PlanID || second.OperationID == first.OperationID {
		t.Fatalf("distinct source plans reused identity: first=%#v second=%#v", first, second)
	}
	if second.SourceDigest != first.SourceDigest || second.RequestDigest != first.RequestDigest || len(second.Entries) != len(first.Entries) {
		t.Fatalf("same source request changed across independent plans: first=%#v second=%#v", first, second)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.source_sync_plan WHERE project_id=$1 AND request_digest=$2`, input.ProjectID, input.RequestDigest).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("same-request source plans=%d, want 2", count)
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
	var runtimeUpdate, runtimeDelete, maintenanceSelect, runtimeSnapshotStateUpdate, runtimeSnapshotSealedAtUpdate bool
	if err := admin.QueryRow(t.Context(), `SELECT has_table_privilege('leapview_control_runtime','project.source_blob','UPDATE'), has_table_privilege('leapview_control_runtime','project.source_blob','DELETE'), has_table_privilege('leapview_control_maintenance','project.source_blob','SELECT'), has_column_privilege('leapview_control_runtime','project.source_snapshot','state','UPDATE'), has_column_privilege('leapview_control_runtime','project.source_snapshot','sealed_at','UPDATE')`).Scan(&runtimeUpdate, &runtimeDelete, &maintenanceSelect, &runtimeSnapshotStateUpdate, &runtimeSnapshotSealedAtUpdate); err != nil {
		t.Fatal(err)
	}
	if runtimeUpdate || runtimeDelete || maintenanceSelect || !runtimeSnapshotStateUpdate || !runtimeSnapshotSealedAtUpdate {
		t.Fatalf("source privileges runtime update=%v delete=%v snapshot state update=%v sealed-at update=%v maintenance select=%v", runtimeUpdate, runtimeDelete, runtimeSnapshotStateUpdate, runtimeSnapshotSealedAtUpdate, maintenanceSelect)
	}
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	var currentUser string
	if err := runtimeDB.QueryRow(t.Context(), "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	if currentUser != runtimeRole.Name {
		t.Fatalf("source sync-plan test connected as %q, want %q", currentUser, runtimeRole.Name)
	}
	runtimeRepo := New(runtimeDB)
	entries := []SourceSyncPlanEntryInput{{Path: "leapview.yaml", Digest: sourceTestDigest("a"), SizeBytes: 1}}
	runtimePlanInput := SyncPlanInput{
		PlanID: uuid.New(), OperationID: uuid.New(), ProjectID: "project:runtime",
		StorageSecurityDomain: "runtime", OwnerID: "principal:runtime", CandidateKey: "default",
		SourceDigest: sourceDigest("project:runtime", "leapview.yaml", snapshotEntries(entries)),
		ProjectFile:  "leapview.yaml", RequestDigest: sourceTestDigest("b"),
		ExpiresAt: time.Now().Add(2 * time.Minute), Entries: entries,
	}
	runtimeTx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runtimeRepo.CreateSyncPlanTx(t.Context(), runtimeTx, runtimePlanInput)
	if err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime source sync-plan creation: %v", err)
	}
	if err := runtimeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeTx, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	locked, err := runtimeRepo.SyncPlanForUpdateTx(t.Context(), runtimeTx, plan.PlanID)
	if err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime source sync-plan lock: %v", err)
	}
	if locked.PlanID != plan.PlanID || locked.OwnerID != runtimePlanInput.OwnerID {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime locked sync-plan = %#v", locked)
	}
	if _, err := runtimeTx.Exec(t.Context(), "SELECT 1"); err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime source sync-plan transaction after lock: %v", err)
	}
	if err := runtimeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeTx, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRepo.InsertSourceBlobTx(t.Context(), runtimeTx, SourceBlobInput{
		ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain,
		Digest: entries[0].Digest, SizeBytes: entries[0].SizeBytes,
		ObjectKey:   "sources/" + strings.TrimPrefix(entries[0].Digest, "sha256:"),
		ContentType: "text/plain", MetadataDigest: sourceTestDigest("d"),
		PlanID: plan.PlanID, OwnerID: plan.OwnerID,
	}); err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime source blob insertion: %v", err)
	}
	if err := runtimeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	attestationPayload := []byte(`{"sourceDigest":"` + plan.SourceDigest + `"}`)
	runtimeTx, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimeRepo.CommitSnapshotTx(t.Context(), runtimeTx, CommitSnapshotInput{
		PlanID: plan.PlanID, OwnerID: plan.OwnerID, SnapshotID: uuid.New(),
		ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain,
		SourceDigest: plan.SourceDigest, ProjectFile: plan.ProjectFile,
		ProjectDigest: sourceTestDigest("e"), ProjectArtifactObjectKey: "artifacts/project.json",
		ProjectArtifactDigest: sourceTestDigest("f"), ProjectArtifactSizeBytes: 10,
		ManifestObjectKey: "manifests/source.json", ManifestObjectDigest: sourceTestDigest("1"),
		ManifestObjectSizeBytes: 20, CompilerVersion: "compiler:v1", SchemaVersion: 1,
		Entries:     []SourceSnapshotEntryInput{{Path: entries[0].Path, Digest: entries[0].Digest, SizeBytes: entries[0].SizeBytes}},
		Attestation: SourceAttestationInput{AttestationID: uuid.New(), SourceDigest: plan.SourceDigest, AttestationDigest: sha256Identity(attestationPayload), Payload: attestationPayload},
	})
	if err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime source snapshot commit: %v", err)
	}
	if err := runtimeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE project.source_blob SET object_key='mutated'`); err == nil {
		t.Fatal("runtime UPDATE on immutable source blob unexpectedly succeeded")
	}
}
