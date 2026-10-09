package migrations

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	managedmaintenance "github.com/flidai/leapview/internal/manageddata/maintenance"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
)

func TestManagedMultipartGCUpgradePreservesIntentAndInvalidatesOldAuthority(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	if _, err := provider.UpTo(t.Context(), 58); err != nil {
		t.Fatal(err)
	}
	r := managedpostgres.New(pool)
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{ID: "collection_gc_upgrade", ProjectID: "project_gc_upgrade", ConnectionID: "connection_gc_upgrade", Name: "GC upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{ID: "upload_gc_upgrade", CollectionID: c.ID, Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", Size: 0, SHA256: strings.Repeat("b", 64)}}}, StorageBackend: "s3", StagingPrefix: "uploads/gc-upgrade", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.CreateS3MultipartUpload(t.Context(), manageddata.CreateS3MultipartUploadInput{ID: "multipart_gc_upgrade", UploadSessionID: u.ID, LogicalPath: "data.csv", SizeBytes: 0, SHA256: strings.Repeat("b", 64), IdempotencyIdentity: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AbortUploadSession(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	readRow := func() string {
		t.Helper()
		var row string
		if err := pool.QueryRow(t.Context(), `SELECT row_to_json(m)::text FROM managed_data.multipart_upload m WHERE multipart_id=$1`, m.ID.String()).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	beforeRow := readRow()
	source, err := managedpostgres.NewReachabilitySource(pool)
	if err != nil {
		t.Fatal(err)
	}
	before, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 59); err != nil {
		t.Fatal(err)
	}
	if afterRow := readRow(); afterRow != beforeRow {
		t.Fatalf("forward safety migration changed durable intent: before=%s after=%s", beforeRow, afterRow)
	}
	called := false
	err = source.WithStableSnapshot(t.Context(), before.Generation, func(managedmaintenance.ReachabilitySnapshot) error { called = true; return nil })
	if !errors.Is(err, managedmaintenance.ErrReachabilityChanged) || called {
		t.Fatalf("migration retained cached preupgrade GC authority: callback=%t error=%v", called, err)
	}
	after, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != before.Generation+1 || len(after.SHA256s) != 1 || after.SHA256s[0] != m.SHA256 {
		t.Fatalf("upgraded reachability=%#v, before=%#v", after, before)
	}
	var runtimeExecute, publicExecute, rootUpdate bool
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT
 has_function_privilege('leapview_control_runtime','managed_data.lock_stable_reachability()','EXECUTE'),
 has_function_privilege('leapview_control_readonly','managed_data.lock_stable_reachability()','EXECUTE'),
 has_table_privilege('leapview_control_runtime','managed_data.retention_root','UPDATE'),
 pg_get_userbyid(proowner)
 FROM pg_proc WHERE oid='managed_data.lock_stable_reachability()'::regprocedure`).Scan(&runtimeExecute, &publicExecute, &rootUpdate, &owner); err != nil {
		t.Fatal(err)
	}
	if !runtimeExecute || publicExecute || rootUpdate || owner != "leapview_control_owner" {
		t.Fatalf("helper grants/owner unsafe: runtime=%t readonly=%t rootUpdate=%t owner=%s", runtimeExecute, publicExecute, rootUpdate, owner)
	}
	if _, err := provider.Down(t.Context()); err == nil {
		t.Fatal("multipart GC safety allowed destructive downgrade")
	}
	if _, err := provider.UpTo(t.Context(), 59); err != nil {
		t.Fatal(err)
	}
	if afterRow := readRow(); afterRow != beforeRow {
		t.Fatal("migration replay changed durable intent")
	}
}
