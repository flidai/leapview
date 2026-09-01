package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata"
	managedmaintenance "github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGeneratedOpaqueIDUsesUUIDv7(t *testing.T) {
	id, err := uuidID("collection")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "collection_"
	if !strings.HasPrefix(id, prefix) {
		t.Fatalf("generated id %q does not use %q prefix", id, prefix)
	}
	raw := strings.TrimPrefix(id, prefix)
	if len(raw) != 32 {
		t.Fatalf("generated UUID suffix length = %d, want 32", len(raw))
	}
	canonical := raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	parsed, err := uuid.Parse(canonical)
	if err != nil || parsed.Version() != 7 {
		t.Fatalf("generated UUID suffix = %q (%v), want version 7", canonical, err)
	}
}

func TestMaintenanceConfiguredRejectsTypedNilDatabase(t *testing.T) {
	var pool *pgxpool.Pool
	if NewMaintenance(pool).Configured() {
		t.Fatal("maintenance accepted typed-nil PostgreSQL pool")
	}
}

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
	for _, forbidden := range []string{"database/sql", "managed_data_collections", "CURRENT_TIMESTAMP", "physical_pool_id", "catalog_id", "snapshot_id"} {
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
	if _, err := p.Exec(t.Context(), `SELECT managed_data.prune_upload_sessions(clock_timestamp(), 100)`); err == nil {
		t.Fatal("runtime role unexpectedly has prune capability")
	}
	maintenanceFacade := NewMaintenance(maintenance)
	if n, err := maintenanceFacade.PruneUploadSessions(t.Context(), time.Now().Add(time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("maintenance prune before cleanup marker removed evidence: n=%d err=%v", n, err)
	}
	providerIDs, err = r.ListS3MultipartProviderIDsByDigest(t.Context(), manifest.Files[0].SHA256)
	if err != nil || !containsString(providerIDs, "provider_orders") {
		t.Fatalf("provider IDs after unmarked prune: %v %#v", err, providerIDs)
	}
	if err := r.MarkUploadCleanupComplete(t.Context(), s.ID); err == nil {
		t.Fatal("runtime role unexpectedly has cleanup evidence capability")
	}
	if err := NewMaintenance(maintenance).MarkUploadCleanupComplete(t.Context(), s.ID); err != nil {
		t.Fatalf("bounded cleanup marker: %v", err)
	}
	if err := NewMaintenance(maintenance).MarkUploadCleanupComplete(t.Context(), s.ID); err != nil {
		t.Fatalf("idempotent cleanup marker retry: %v", err)
	}
	if n, err := maintenanceFacade.PruneUploadSessions(t.Context(), time.Now().Add(time.Hour), 100); err != nil || n == 0 {
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
	// Retention roots require an existing revision in the declared project;
	// DuckLake snapshot roots belong to the separate DuckLake authority.
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,revision_id,state,evidence) VALUES ('root_missing','project_demo','prod','revision_missing','live','{"source":"test"}')`); err == nil {
		t.Fatal("retention root accepted missing revision")
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,revision_id,state,evidence) VALUES ('root_wrong_project','project_other','prod','revision_orders','live','{"source":"test"}')`); err == nil {
		t.Fatal("retention root accepted cross-project revision")
	}
	root, err := r.RecordRetentionRoot(t.Context(), RetentionRoot{RootID: "root_revision", ProjectID: "project_demo", Environment: "prod", RevisionID: rev.ID.String(), State: "live", Evidence: json.RawMessage(`{"source":"test"}`)})
	if err != nil || root.RevisionID != rev.ID.String() {
		t.Fatalf("valid retention root: %v %#v", err, root)
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

func TestPostgresReachabilityStableSnapshotAndRollback(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	r := New(p)
	source, err := NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: "collection_reachability", ProjectID: "project_reachability", ConnectionID: "connection_reachability", Name: "Reachability",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
		ID: "upload_reachability", CollectionID: c.ID,
		Manifest:       manageddata.Manifest{Files: []manageddata.File{{Path: "data.parquet", Size: 3, SHA256: digest}}},
		StorageBackend: "s3", StagingPrefix: "uploads/upload_reachability", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	initial, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.SHA256s) != 1 || initial.SHA256s[0] != digest {
		t.Fatalf("reachability snapshot = %#v", initial)
	}
	rollbackErr := errors.New("callback failed")
	if err := source.WithStableSnapshot(t.Context(), initial.Generation, func(got managedmaintenance.ReachabilitySnapshot) error {
		if got.Generation != initial.Generation || len(got.SHA256s) != 1 || got.SHA256s[0] != digest {
			return errors.New("stable snapshot changed")
		}
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("stable snapshot callback error = %v", err)
	}
	if after, err := source.Snapshot(t.Context()); err != nil || after.Generation != initial.Generation {
		t.Fatalf("snapshot after rollback = %#v, %v", after, err)
	}
}

func TestPostgresReachabilityStableSnapshotFencesLifecycleWrite(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	p.Config().MaxConns = 4
	r := New(p)
	source, err := NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: "collection_reachability_fence", ProjectID: "project_reachability_fence", ConnectionID: "connection_reachability_fence", Name: "Reachability Fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
		ID: "upload_reachability_fence", CollectionID: c.ID,
		Manifest:       manageddata.Manifest{Files: []manageddata.File{{Path: "data.parquet", Size: 3, SHA256: "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"}}},
		StorageBackend: "s3", StagingPrefix: "uploads/upload_reachability_fence", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	initial, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	updateDone := make(chan error, 1)
	if err := source.WithStableSnapshot(t.Context(), initial.Generation, func(managedmaintenance.ReachabilitySnapshot) error {
		close(entered)
		go func() {
			updateDone <- r.UpdateUploadProgress(t.Context(), "upload_reachability_fence", manageddata.UploadProgress{UploadedFileCount: 1, UploadedSizeBytes: 3})
		}()
		select {
		case err := <-updateDone:
			t.Fatalf("lifecycle write completed while stable snapshot held: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("fenced lifecycle write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle write remained blocked after stable snapshot commit")
	}
	select {
	case <-entered:
	default:
		t.Fatal("stable snapshot callback did not run")
	}
}

type transitionWorkflowProbe struct {
	fail  bool
	calls int
}

func (p *transitionWorkflowProbe) RecordWorkflow(ctx context.Context, tx Tx, _ jobspkg.WorkflowIntent) error {
	p.calls++
	if _, err := tx.Exec(ctx, `INSERT INTO managed_data.reconciliation_evidence(project_id,environment,object_key,observed_state,action,evidence) VALUES ('project_transition','prod','workflow-probe','observed','workflow','{"kind":"workflow"}')`); err != nil {
		return err
	}
	if p.fail {
		return errors.New("workflow probe failure")
	}
	return nil
}

type transitionAuditProbe struct {
	fail  bool
	calls int
}

func (p *transitionAuditProbe) RecordAuditIntent(ctx context.Context, tx Tx, _ access.AuditIntent) error {
	p.calls++
	if _, err := tx.Exec(ctx, `INSERT INTO managed_data.reconciliation_evidence(project_id,environment,object_key,observed_state,action,evidence) VALUES ('project_transition','prod','audit-probe','observed','audit','{"kind":"audit"}')`); err != nil {
		return err
	}
	if p.fail {
		return errors.New("audit probe failure")
	}
	return nil
}

func TestPostgresUploadTransitionAtomicReplayAndRollback(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	workflow := &transitionWorkflowProbe{}
	audit := &transitionAuditProbe{}
	r := NewWithOptions(p, Options{Workflow: workflow, Audit: audit})
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: "collection_transition", ProjectID: "project_transition", ConnectionID: "connection_transition", Name: "Transition",
	})
	if err != nil {
		t.Fatal(err)
	}
	newUpload := func(id string) manageddata.UploadSession {
		t.Helper()
		session, createErr := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
			ID: manageddata.UploadID(id), CollectionID: c.ID,
			Manifest:       manageddata.Manifest{Files: []manageddata.File{{Path: "data.parquet", Size: 1, SHA256: "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"}}},
			StorageBackend: "s3", StagingPrefix: "uploads/" + id, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return session
	}
	intent := manageddata.UploadTransition{
		Workflow:    jobspkg.WorkflowIntent{Event: jobspkg.EventInput{Key: "upload:transition:begin", ResourceKind: "upload", ResourceID: "upload_transition", EventType: "upload_session.finalizing", Data: []byte(`{"status":"finalizing"}`)}},
		AuditIntent: &access.AuditIntent{EventID: "audit-transition", Source: "managed-data", Operation: "finalize", Action: "upload.finalize", ResourceKind: "upload", ResourceID: "upload_transition", Outcome: "accepted", MetadataJSON: `{"kind":"transition"}`},
	}
	session := newUpload("upload_transition")
	finalizing, err := r.BeginUploadFinalizationTransition(t.Context(), session.ID, intent)
	if err != nil || finalizing.Status != manageddata.UploadStatusCommitting {
		t.Fatalf("atomic transition = %#v, %v", finalizing, err)
	}
	if workflow.calls != 1 || audit.calls != 1 {
		t.Fatalf("side-effect calls workflow=%d audit=%d", workflow.calls, audit.calls)
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM managed_data.reconciliation_evidence WHERE project_id='project_transition'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("committed side effects count=%d err=%v", count, err)
	}
	// A replay of the committing transition is allowed and delegates
	// idempotency to the workflow and audit authorities.
	if replay, replayErr := r.BeginUploadFinalizationTransition(t.Context(), session.ID, intent); replayErr != nil || replay.Status != manageddata.UploadStatusCommitting {
		t.Fatalf("transition replay = %#v, %v", replay, replayErr)
	}
	if workflow.calls != 2 || audit.calls != 2 {
		t.Fatalf("replay side-effect calls workflow=%d audit=%d", workflow.calls, audit.calls)
	}

	failed := newUpload("upload_transition_failed")
	workflow.fail = true
	if _, err := r.BeginUploadFinalizationTransition(t.Context(), failed.ID, manageddata.UploadTransition{Workflow: jobspkg.WorkflowIntent{Event: jobspkg.EventInput{Key: "upload:transition:rollback", ResourceKind: "upload", ResourceID: failed.ID.String(), EventType: "upload_session.finalizing", Data: []byte(`{"status":"finalizing"}`)}}}); err == nil {
		t.Fatal("workflow failure unexpectedly committed upload transition")
	}
	stored, err := r.UploadSessionByID(t.Context(), failed.ID)
	if err != nil || stored.Status != manageddata.UploadStatusOpen {
		t.Fatalf("rollback upload status=%s err=%v", stored.Status, err)
	}
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM managed_data.reconciliation_evidence WHERE object_key='workflow-probe' AND project_id='project_transition'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rolled-back side effect count=%d err=%v", count, err)
	}
}

func TestPostgresAuditIntentMutationsAtomicReplayAndRollback(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	audit := &transitionAuditProbe{}
	r := NewWithOptions(p, Options{Audit: audit})
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: "collection_audit_mutations", ProjectID: "project_transition", ConnectionID: "connection_audit_mutations", Name: "Audit Mutations",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "data.parquet", Size: 1, SHA256: "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"}}}
	auditIntent := func(id string) *access.AuditIntent {
		return &access.AuditIntent{EventID: id, Source: "managed-data", Operation: "test", Action: "managed-data.test", ResourceKind: "upload", ResourceID: id, Outcome: "accepted", MetadataJSON: `{"kind":"test"}`}
	}
	input := manageddata.CreateUploadSessionInput{ID: "upload_audit_atomic", CollectionID: c.ID, Manifest: manifest, StorageBackend: "s3", StagingPrefix: "uploads/audit", ExpiresAt: time.Now().UTC().Add(time.Hour), AuditIntent: auditIntent("audit-upload")}
	if _, err := r.CreateUploadSession(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateUploadSession(t.Context(), input); err != nil {
		t.Fatalf("upload replay: %v", err)
	}
	if audit.calls != 2 {
		t.Fatalf("upload audit calls=%d", audit.calls)
	}
	audit.fail = true
	failed := input
	failed.ID = "upload_audit_rollback"
	failed.AuditIntent = auditIntent("audit-upload-rollback")
	if _, err := r.CreateUploadSession(t.Context(), failed); err == nil {
		t.Fatal("audit failure unexpectedly committed upload session")
	}
	var present bool
	if err := p.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM managed_data.upload_session WHERE upload_id='upload_audit_rollback')`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("rolled-back upload session remained")
	}
	audit.fail = false
	// Multipart create and initialize carry audit intents as well.
	mpInput := manageddata.CreateS3MultipartUploadInput{ID: "multipart_audit_atomic", UploadSessionID: input.ID, LogicalPath: "data.parquet", SHA256: manifest.Files[0].SHA256, SizeBytes: 1, IdempotencyIdentity: "idem-audit", AuditIntent: auditIntent("audit-multipart-create")}
	if _, err := r.CreateS3MultipartUpload(t.Context(), mpInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateS3MultipartUpload(t.Context(), mpInput); err != nil {
		t.Fatalf("multipart create replay: %v", err)
	}
	if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: mpInput.ID, ObjectKey: "objects/data.parquet", ProviderUploadID: "provider-audit", AuditIntent: auditIntent("audit-multipart-init")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReserveS3MultipartPart(t.Context(), manageddata.S3MultipartPart{MultipartUploadID: mpInput.ID, PartNumber: 1, SizeBytes: 1, SHA256: manifest.Files[0].SHA256}); err != nil {
		t.Fatal(err)
	}
	completionInput := manageddata.BeginS3MultipartCompletionInput{ID: mpInput.ID, IdempotencyIdentity: "complete-audit", RequestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AuditIntent: auditIntent("audit-multipart-complete")}
	completion, err := r.BeginS3MultipartCompletion(t.Context(), completionInput)
	if err != nil || !completion.Execute {
		t.Fatalf("multipart completion audit: %#v %v", completion, err)
	}
	if replay, err := r.BeginS3MultipartCompletion(t.Context(), completionInput); err != nil || replay.Execute {
		t.Fatalf("multipart completion replay: %#v %v", replay, err)
	}
	if _, err := r.FinishS3MultipartCompletion(t.Context(), mpInput.ID); err != nil {
		t.Fatal(err)
	}
	mpAbort := mpInput
	mpAbort.ID = "multipart_audit_abort"
	mpAbort.IdempotencyIdentity = "idem-audit-abort"
	if _, err := r.CreateS3MultipartUpload(t.Context(), mpAbort); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: mpAbort.ID, ObjectKey: "objects/abort.parquet", ProviderUploadID: "provider-abort"}); err != nil {
		t.Fatal(err)
	}
	abortInput := manageddata.BeginS3MultipartAbortInput{ID: mpAbort.ID, IdempotencyIdentity: "abort-audit", AuditIntent: auditIntent("audit-multipart-abort")}
	if abort, err := r.BeginS3MultipartAbort(t.Context(), abortInput); err != nil || !abort.Execute {
		t.Fatalf("multipart abort audit: %#v %v", abort, err)
	}
	if replay, err := r.BeginS3MultipartAbort(t.Context(), abortInput); err != nil || replay.Execute {
		t.Fatalf("multipart abort replay: %#v %v", replay, err)
	}
	if _, err := r.FinishS3MultipartAbort(t.Context(), mpAbort.ID); err != nil {
		t.Fatal(err)
	}
	mpCompletionRollback := mpInput
	mpCompletionRollback.ID = "multipart_audit_completion_rollback"
	mpCompletionRollback.IdempotencyIdentity = "idem-audit-completion-rollback"
	if _, err := r.CreateS3MultipartUpload(t.Context(), mpCompletionRollback); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: mpCompletionRollback.ID, ObjectKey: "objects/completion-rollback.parquet", ProviderUploadID: "provider-completion-rollback"}); err != nil {
		t.Fatal(err)
	}
	mpAbortRollback := mpInput
	mpAbortRollback.ID = "multipart_audit_abort_rollback"
	mpAbortRollback.IdempotencyIdentity = "idem-audit-abort-rollback"
	if _, err := r.CreateS3MultipartUpload(t.Context(), mpAbortRollback); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: mpAbortRollback.ID, ObjectKey: "objects/abort-rollback.parquet", ProviderUploadID: "provider-abort-rollback"}); err != nil {
		t.Fatal(err)
	}
	audit.fail = true
	if _, err := r.BeginS3MultipartCompletion(t.Context(), manageddata.BeginS3MultipartCompletionInput{ID: mpCompletionRollback.ID, IdempotencyIdentity: "complete-rollback", RequestHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", AuditIntent: auditIntent("audit-complete-rollback")}); err == nil {
		t.Fatal("audit failure unexpectedly committed completion transition")
	}
	if status, err := r.S3MultipartUploadByID(t.Context(), mpCompletionRollback.ID); err != nil || status.Status != manageddata.S3MultipartStatusOpen {
		t.Fatalf("completion rollback status=%s err=%v", status.Status, err)
	}
	if _, err := r.BeginS3MultipartAbort(t.Context(), manageddata.BeginS3MultipartAbortInput{ID: mpAbortRollback.ID, IdempotencyIdentity: "abort-rollback", AuditIntent: auditIntent("audit-abort-rollback")}); err == nil {
		t.Fatal("audit failure unexpectedly committed abort transition")
	}
	if status, err := r.S3MultipartUploadByID(t.Context(), mpAbortRollback.ID); err != nil || status.Status != manageddata.S3MultipartStatusOpen {
		t.Fatalf("abort rollback status=%s err=%v", status.Status, err)
	}
	failedMP := mpInput
	failedMP.ID = "multipart_audit_rollback"
	failedMP.IdempotencyIdentity = "idem-audit-rollback"
	failedMP.AuditIntent = auditIntent("audit-multipart-rollback")
	if _, err := r.CreateS3MultipartUpload(t.Context(), failedMP); err == nil {
		t.Fatal("audit failure unexpectedly committed multipart upload")
	}
	if err := p.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM managed_data.multipart_upload WHERE multipart_id='multipart_audit_rollback')`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("rolled-back multipart upload remained")
	}
}

// Keep pgx imported in this package's tests to make the intended native
// transaction surface explicit and catch accidental database/sql adapters.
var _ pgx.Tx
