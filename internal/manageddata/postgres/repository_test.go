package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openManagedDataTestPool(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *postgrestest.Database, *postgrestest.Harness) {
	t.Helper()
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "maintenance-secret", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup", Password: "backup-secret", Login: true})
	db := h.NewDatabase(t, "")
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
	p, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Ping(t.Context()); err != nil {
		p.Close()
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	maintenance, err := pgxpool.New(t.Context(), db.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Ping(t.Context()); err != nil {
		maintenance.Close()
		t.Fatal(err)
	}
	t.Cleanup(maintenance.Close)
	return p, maintenance, db, h
}

func TestSchemaSQLIsCleanAndBounded(t *testing.T) {
	s := SchemaSQL()
	for _, forbidden := range []string{"database/sql", "managed_data_collections", "CURRENT_TIMESTAMP"} {
		if containsSQL(s, forbidden) {
			t.Fatalf("schema contains forbidden legacy/unbounded token %q", forbidden)
		}
	}
	if len(s) < 2000 {
		t.Fatalf("schema unexpectedly small")
	}
}

func containsSQL(s, token string) bool {
	for i := 0; i+len(token) <= len(s); i++ {
		if s[i:i+len(token)] == token {
			return true
		}
	}
	return false
}

func TestPostgresUploadRevisionAndBindings(t *testing.T) {
	p, maintenance, _, _ := openManagedDataTestPool(t)
	r := New(p)
	projectID := projectgraph.ResourceID("project_demo")
	connID := projectgraph.ResourceID("connection_orders")
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.collection(collection_id,project_id,connection_id,name,status,archived_at,request_digest) VALUES ('collection_forged','project_demo','connection_forged','Forged','archived',clock_timestamp(),'sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err == nil {
		t.Fatal("runtime inserted a pre-archived collection")
	}
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{ID: "collection_orders", ProjectID: projectID, ConnectionID: connID, Name: "Orders"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.collection SET created_by='tampered' WHERE collection_id='collection_orders'`); err == nil {
		t.Fatal("runtime rewrote collection authored identity")
	}
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.parquet", Size: 12, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}}
	s, err := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{ID: "upload_orders", CollectionID: c.ID, Manifest: manifest, StorageBackend: "s3", StagingPrefix: "staging/orders", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	mp, err := r.CreateS3MultipartUpload(t.Context(), manageddata.CreateS3MultipartUploadInput{ID: "multipart_orders", UploadSessionID: s.ID, LogicalPath: "orders.parquet", SHA256: manifest.Files[0].SHA256, SizeBytes: manifest.Files[0].Size, IdempotencyIdentity: "idem_orders"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: mp.ID, ObjectKey: "objects/orders.parquet", ProviderUploadID: "provider_orders"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReserveS3MultipartPart(t.Context(), manageddata.S3MultipartPart{MultipartUploadID: mp.ID, PartNumber: 1, SizeBytes: manifest.Files[0].Size, SHA256: manifest.Files[0].SHA256}); err != nil {
		t.Fatal(err)
	}
	completion, err := r.BeginS3MultipartCompletion(t.Context(), manageddata.BeginS3MultipartCompletionInput{ID: mp.ID, IdempotencyIdentity: "complete_orders", RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || !completion.Execute {
		t.Fatalf("multipart completion begin: %v %#v", err, completion)
	}
	if _, err := r.FinishS3MultipartCompletion(t.Context(), mp.ID); err != nil {
		t.Fatal(err)
	}
	// Multipart completion requires non-empty parts and an exact byte sum for
	// provider-created objects.  This direct SQL attempt has no parts.
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.multipart_upload(multipart_id,upload_id,logical_path,sha256,size_bytes,idempotency_identity) VALUES ('multipart_empty','upload_orders','empty.parquet','0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',10,'idem_empty')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET object_key='objects/empty.parquet',provider_upload_id='provider_empty',status='open' WHERE multipart_id='multipart_empty'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET status='completing' WHERE multipart_id='multipart_empty'`); err == nil {
		t.Fatal("multipart aggregate mismatch was accepted")
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET status='aborting',abort_identity='abort_empty' WHERE multipart_id='multipart_empty'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET status='aborted',aborted_at=clock_timestamp() WHERE multipart_id='multipart_empty'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.multipart_upload(multipart_id,upload_id,logical_path,sha256,size_bytes,idempotency_identity) VALUES ('multipart_identity','upload_orders','identity.parquet','0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',10,'idem_identity')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET object_key='objects/identity.parquet',provider_upload_id='provider_identity',status='open' WHERE multipart_id='multipart_identity'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.multipart_part(multipart_id,part_number,size_bytes,sha256) VALUES ('multipart_identity',1,10,'0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET status='completing' WHERE multipart_id='multipart_identity'`); err == nil {
		t.Fatal("multipart completion accepted empty identity evidence")
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET status='aborting',abort_identity='abort_identity' WHERE multipart_id='multipart_identity'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.multipart_upload SET status='aborted',aborted_at=clock_timestamp() WHERE multipart_id='multipart_identity'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginUploadFinalization(t.Context(), s.ID, jobspkg.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	rev, err := r.CompleteUpload(t.Context(), manageddata.CompleteUploadInput{SessionID: s.ID, RevisionID: "revision_orders", Files: []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "objects/orders.parquet"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Status != manageddata.RevisionStatusReady || rev.Digest != manifest.RevisionID() {
		t.Fatalf("unexpected revision: %#v", rev)
	}
	// Terminal upload evidence cannot be rewritten by a runtime connection,
	// including progress or the revision binding itself.
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.upload_session SET uploaded_file_count=0 WHERE upload_id='upload_orders'`); err == nil {
		t.Fatal("runtime rewrote terminal upload progress")
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.upload_session SET revision_id='revision_other' WHERE upload_id='upload_orders'`); err == nil {
		t.Fatal("runtime rewrote terminal upload revision")
	}
	altered := manageddata.CompleteUploadInput{SessionID: s.ID, RevisionID: rev.ID, Files: []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "objects/tampered.parquet"}}}
	if _, err := r.CompleteUpload(t.Context(), altered); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("altered completion replay error=%v", err)
	}
	providerIDs, err := r.ListS3MultipartProviderIDsByDigest(t.Context(), manifest.Files[0].SHA256)
	if err != nil || !containsString(providerIDs, "provider_orders") {
		t.Fatalf("provider IDs before cleanup: %v %#v", err, providerIDs)
	}
	if _, err := r.PruneUploadSessions(t.Context(), time.Now().Add(time.Hour), 100); err == nil {
		t.Fatal("runtime role unexpectedly has prune capability")
	}
	if n, err := New(maintenance).PruneUploadSessions(t.Context(), time.Now().Add(time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("maintenance prune before cleanup marker removed evidence: n=%d err=%v", n, err)
	}
	providerIDs, err = r.ListS3MultipartProviderIDsByDigest(t.Context(), manifest.Files[0].SHA256)
	if err != nil || !containsString(providerIDs, "provider_orders") {
		t.Fatalf("provider IDs after unmarked prune: %v %#v", err, providerIDs)
	}
	if err := r.MarkUploadCleanupComplete(t.Context(), s.ID); err == nil {
		t.Fatal("runtime role unexpectedly has cleanup evidence capability")
	}
	if err := New(maintenance).MarkUploadCleanupComplete(t.Context(), s.ID); err != nil {
		t.Fatalf("bounded cleanup marker: %v", err)
	}
	if n, err := New(maintenance).PruneUploadSessions(t.Context(), time.Now().Add(time.Hour), 100); err != nil || n == 0 {
		t.Fatalf("maintenance prune after cleanup marker: n=%d err=%v", n, err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.upload_session SET cleanup_completed_at=clock_timestamp() WHERE upload_id='upload_orders'`); err == nil {
		t.Fatal("runtime fabricated cleanup evidence")
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.revision SET error='tampered' WHERE revision_id='revision_orders'`); err == nil {
		t.Fatal("ready revision unexpectedly mutable")
	}
	// A revision cannot be admitted directly as ready; files must be attached
	// while pending and the lifecycle trigger performs the aggregate check.
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.revision(revision_id,collection_id,sequence,digest,status,manifest,file_count,size_bytes) VALUES ('revision_forged','collection_orders',99,'sha256:1111111111111111111111111111111111111111111111111111111111111111','ready','{"files":[]}',0,0)`); err == nil {
		t.Fatal("runtime inserted a ready revision directly")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.revision(revision_id,collection_id,sequence,digest,status,manifest,file_count,size_bytes) VALUES ('revision_mismatch','collection_orders',100,'sha256:2222222222222222222222222222222222222222222222222222222222222222','pending','{"files":[{"path":"orders.parquet","size":12,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}',1,12)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.revision_file(revision_id,logical_path,size_bytes,sha256,storage_key) VALUES ('revision_mismatch','tampered.parquet',12,'0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef','objects/tampered.parquet')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.revision SET status='ready',ready_at=clock_timestamp() WHERE revision_id='revision_mismatch'`); err == nil {
		t.Fatal("revision manifest/file identity mismatch was accepted")
	}
	// Cleanup evidence is a dedicated maintenance capability, not a runtime
	// function.  The maintenance role call above is the only successful path.
	if _, err := p.Exec(t.Context(), `SELECT managed_data.mark_upload_cleanup('upload_orders')`); err == nil {
		t.Fatal("runtime invoked cleanup evidence function")
	}
	// Retention roots require either an existing revision in the declared
	// project or one complete DuckLake snapshot tuple; partial, combined and
	// cross-project identities are rejected by checks/triggers.
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,revision_id,state,evidence) VALUES ('root_missing','project_demo','prod','revision_missing','live','{"source":"test"}')`); err == nil {
		t.Fatal("retention root accepted missing revision")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,physical_pool_id,snapshot_id,state,evidence) VALUES ('root_partial','project_demo','prod','pool_1',1,'live','{"source":"test"}')`); err == nil {
		t.Fatal("retention root accepted partial snapshot tuple")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,revision_id,physical_pool_id,catalog_id,snapshot_id,state,evidence) VALUES ('root_combined','project_demo','prod','revision_orders','pool_1','catalog_1',1,'live','{"source":"test"}')`); err == nil {
		t.Fatal("retention root accepted combined revision and snapshot identity")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,revision_id,state,evidence) VALUES ('root_wrong_project','project_other','prod','revision_orders','live','{"source":"test"}')`); err == nil {
		t.Fatal("retention root accepted cross-project revision")
	}
	root, err := r.RecordRetentionRoot(t.Context(), RetentionRoot{RootID: "root_revision", ProjectID: "project_demo", Environment: "prod", RevisionID: rev.ID.String(), State: "live", Evidence: json.RawMessage(`{"source":"test"}`)})
	if err != nil || root.RevisionID != rev.ID.String() {
		t.Fatalf("valid retention root: %v %#v", err, root)
	}
	if _, err := r.RecordRetentionRoot(t.Context(), RetentionRoot{RootID: "root_snapshot", ProjectID: "project_demo", Environment: "prod", PhysicalPoolID: "pool_1", CatalogID: "catalog_1", SnapshotID: ptrInt64(1), State: "live", Evidence: json.RawMessage(`{"source":"test"}`)}); err == nil {
		t.Fatal("managed-data authority accepted a DuckLake snapshot retention root")
	}
	if root, err = r.TransitionRetentionRoot(t.Context(), "root_revision", "retiring"); err != nil || root.State != "retiring" {
		t.Fatalf("retention live->retiring: %v %#v", err, root)
	}
	if root, err = r.TransitionRetentionRoot(t.Context(), "root_revision", "expired"); err != nil || root.State != "expired" {
		t.Fatalf("retention retiring->expired: %v %#v", err, root)
	}
	if root, err = r.TransitionRetentionRoot(t.Context(), "root_revision", "expired"); err != nil || root.State != "expired" {
		t.Fatalf("retention exact terminal replay: %v %#v", err, root)
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.retention_root SET revision_id='revision_other' WHERE root_id='root_revision'`); err == nil {
		t.Fatal("runtime rewrote retention root identity")
	}
	identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: "prod", GenerationID: "generation_1"}
	if err := r.InstallServingStateBindings(t.Context(), identity, []manageddata.ServingStateBinding{{CollectionID: c.ID, RevisionID: rev.ID}}); err != nil {
		t.Fatal(err)
	}
	bindings, err := r.ListServingStateBindings(t.Context(), identity)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings: %v %#v", err, bindings)
	}
	if err := r.InstallServingStateBindings(t.Context(), identity, []manageddata.ServingStateBinding{}); !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("binding replay mismatch error=%v", err)
	}
	ptr := manageddata.EnvironmentPointer{CollectionID: c.ID, Environment: manageddata.Environment("prod"), RevisionID: rev.ID, RevisionDigest: rev.Digest, DeploymentID: "deployment_1", Generation: 1}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.InstallEnvironmentPointerTx(t.Context(), tx, ptr); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := r.InstallEnvironmentPointerTx(t.Context(), tx, ptr); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("exact pointer replay: %v", err)
	}
	changed := ptr
	changed.DeploymentID = "deployment_other"
	if err := r.InstallEnvironmentPointerTx(t.Context(), tx, changed); !errors.Is(err, manageddata.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("same-generation pointer conflict=%v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func ptrInt64(v int64) *int64 { return &v }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestPostgresLeaseFencingRaceAndRuntimeDeleteDenied(t *testing.T) {
	p, maintenance, _, _ := openManagedDataTestPool(t)
	r := New(p)
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.lease(lease_key,owner_id,fencing_epoch,state,expires_at) VALUES ('forged','worker',2,'held',clock_timestamp()+interval '1 minute')`); err == nil {
		t.Fatal("runtime inserted a non-canonical lease epoch")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.multipart_digest_lease(sha256,owner_id,fencing_epoch,state,lease_until) VALUES ('0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef','worker',1,'released',clock_timestamp())`); err == nil {
		t.Fatal("runtime inserted a released digest lease")
	}
	var wg sync.WaitGroup
	results := make(chan Lease, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"worker_a", "worker_b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			l, err := r.AcquireLease(context.Background(), "dataset:orders", owner, time.Minute)
			if err != nil {
				errs <- err
			} else {
				results <- l
			}
		}(owner)
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 1 {
		t.Fatalf("expected exactly one lease winner, got %d (errors=%d)", len(results), len(errs))
	}
	lease := <-results
	if lease.FencingEpoch != 1 {
		t.Fatalf("first lease epoch=%d", lease.FencingEpoch)
	}
	if _, err := p.Exec(t.Context(), `DELETE FROM managed_data.revision`); err == nil {
		t.Fatal("runtime role unexpectedly has direct DELETE privilege")
	}
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.reconciliation_evidence SET action='tampered'`); err == nil {
		t.Fatal("runtime role unexpectedly has reconciliation UPDATE privilege")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.binding_set(project_id,environment,generation_id,binding_digest,binding_count) VALUES ('project_demo','prod','forged','sha256:0000000000000000000000000000000000000000000000000000000000000000',0)`); err == nil {
		t.Fatal("runtime role unexpectedly has direct binding-set INSERT privilege")
	}
	if _, err := r.RenewLease(t.Context(), lease.Key, "stale-owner", lease.FencingEpoch, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale renew error=%v", err)
	}
	if err := r.ReleaseLease(t.Context(), lease.Key, lease.Owner, lease.FencingEpoch); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	takeover, err := r.AcquireLease(t.Context(), lease.Key, "worker_takeover", time.Minute)
	if err != nil || takeover.FencingEpoch <= lease.FencingEpoch {
		t.Fatalf("lease takeover epoch=%d err=%v", takeover.FencingEpoch, err)
	}
	if _, err := p.Exec(t.Context(), `SELECT managed_data.prune_upload_sessions(clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime role unexpectedly has prune capability")
	}
	if _, err := maintenance.Exec(t.Context(), `SELECT managed_data.prune_upload_sessions(clock_timestamp(), 1)`); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresReadonlyAndBackupGrantMatrix(t *testing.T) {
	_, _, db, _ := openManagedDataTestPool(t)
	readonly := postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true}
	backup := postgrestest.Role{Name: "leapview_control_backup", Password: "backup-secret", Login: true}
	ro, err := pgxpool.New(t.Context(), db.URL(readonly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ro.Close)
	if err := ro.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.Exec(t.Context(), `SELECT count(*) FROM managed_data.collection`); err != nil {
		t.Fatalf("readonly SELECT denied: %v", err)
	}
	if _, err := ro.Exec(t.Context(), `INSERT INTO managed_data.collection(collection_id,project_id,connection_id,name,request_digest) VALUES ('ro_forbidden','project_demo','conn_ro','ReadOnly','sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err == nil {
		t.Fatal("readonly role unexpectedly has INSERT privilege")
	}
	bk, err := pgxpool.New(t.Context(), db.URL(backup))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bk.Close)
	if err := bk.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := bk.Exec(t.Context(), `SELECT count(*) FROM managed_data.reconciliation_evidence`); err != nil {
		t.Fatalf("backup SELECT denied: %v", err)
	}
	if _, err := bk.Exec(t.Context(), `DELETE FROM managed_data.collection`); err == nil {
		t.Fatal("backup role unexpectedly has DELETE privilege")
	}
}

// Keep pgx imported in this package's tests to make the intended native
// transaction surface explicit and catch accidental database/sql adapters.
var _ pgx.Tx
