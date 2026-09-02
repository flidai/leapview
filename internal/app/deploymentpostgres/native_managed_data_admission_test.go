package deploymentpostgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/jackc/pgx/v5/pgxpool"
)

func nativeManagedDataAdmissionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "native_managed_data_admission")
	pool, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin schema transaction: %v", err)
	}
	if err := manageddatapostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply managed-data schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit schema transaction: %v", err)
	}
	return pool
}

func TestNewNativeManagedDataBindingAdmissionRejectsUnconfiguredRepository(t *testing.T) {
	if admission, err := NewNativeManagedDataBindingAdmission(nil); err == nil || admission != nil {
		t.Fatalf("nil repository constructor = (%v, %v), want nil capability and error", admission, err)
	}
	if admission, err := NewNativeManagedDataBindingAdmission(manageddatapostgres.New(nil)); err == nil || admission != nil {
		t.Fatalf("unconfigured repository constructor = (%v, %v), want nil capability and error", admission, err)
	}
}

func TestNativeManagedDataBindingAdmissionOwnsNoTransactionLifecycle(t *testing.T) {
	pool := nativeManagedDataAdmissionTestPool(t)
	repository := manageddatapostgres.New(pool)
	admission, err := NewNativeManagedDataBindingAdmission(repository)
	if err != nil {
		t.Fatalf("construct admission: %v", err)
	}

	projectID := projectgraph.ResourceID("project_native_binding")
	connectionID := projectgraph.ResourceID("connection_orders")
	collection, err := repository.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: "collection_orders", ProjectID: projectID, ConnectionID: connectionID, Name: "Orders",
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	manifest := manageddata.Manifest{Files: []manageddata.File{{
		Path: "orders.parquet", Size: 1, SHA256: strings.Repeat("a", 64),
	}}}
	session, err := repository.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
		ID: "upload_native_binding", CollectionID: collection.ID, Manifest: manifest, StorageBackend: "local",
		StagingPrefix: "staging/native-binding", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create upload session: %v", err)
	}
	revision, err := repository.CompleteUpload(t.Context(), manageddata.CompleteUploadInput{
		SessionID: session.ID,
		Files:     []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "objects/orders.parquet"}},
	})
	if err != nil {
		t.Fatalf("complete upload: %v", err)
	}

	pinnedIdentity := servingIdentityForNativeBinding(projectID, "prod", "generation_pinned")
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin pinned admission transaction: %v", err)
	}
	if err := admission.AdmitServingStateBindingsTx(t.Context(), tx, pinnedIdentity, []release.ManagedDataPin{{
		ConnectionID: connectionID.String(), RevisionID: revision.Digest,
	}}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admit pinned binding set: %v", err)
	}
	// The marker is visible through the transaction-bound repository before
	// commit, proving that the adapter uses the caller's transaction.
	inside, err := repository.WithTx(tx).ListServingStateBindings(t.Context(), pinnedIdentity)
	if err != nil || len(inside) != 1 || inside[0].RevisionID != revision.ID {
		_ = tx.Rollback(t.Context())
		t.Fatalf("transaction-bound bindings = %#v, error = %v", inside, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit pinned admission transaction: %v", err)
	}
	outside, err := repository.ListServingStateBindings(t.Context(), pinnedIdentity)
	if err != nil || len(outside) != 1 || outside[0].RevisionID != revision.ID {
		t.Fatalf("committed bindings = %#v, error = %v", outside, err)
	}

	emptyIdentity := servingIdentityForNativeBinding(projectID, "prod", "generation_empty")
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin empty admission transaction: %v", err)
	}
	if err := admission.AdmitServingStateBindingsTx(t.Context(), tx, emptyIdentity, nil); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admit empty binding set: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit empty admission transaction: %v", err)
	}
	empty, err := repository.ListServingStateBindings(t.Context(), emptyIdentity)
	if err != nil || len(empty) != 0 {
		t.Fatalf("committed empty bindings = %#v, error = %v", empty, err)
	}

	rollbackIdentity := servingIdentityForNativeBinding(projectID, "prod", "generation_rollback")
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin rollback admission transaction: %v", err)
	}
	if err := admission.AdmitServingStateBindingsTx(t.Context(), tx, rollbackIdentity, []release.ManagedDataPin{{
		ConnectionID: connectionID.String(), RevisionID: revision.Digest,
	}}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admit rollback binding set: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("rollback admission transaction: %v", err)
	}
	if _, err := repository.ListServingStateBindings(t.Context(), rollbackIdentity); !errors.Is(err, manageddata.ErrNotFound) {
		t.Fatalf("rolled-back binding marker error = %v, want manageddata.ErrNotFound", err)
	}
}

func TestNativeManagedDataBindingAdmissionRejectsNonCanonicalPins(t *testing.T) {
	pool := nativeManagedDataAdmissionTestPool(t)
	repository := manageddatapostgres.New(pool)
	admission, err := NewNativeManagedDataBindingAdmission(repository)
	if err != nil {
		t.Fatalf("construct admission: %v", err)
	}
	identity := servingIdentityForNativeBinding("project_native_binding_invalid", "prod", "generation_invalid")
	for _, pins := range [][]release.ManagedDataPin{
		{{ConnectionID: " connection_orders", RevisionID: "sha256:" + strings.Repeat("a", 64)}},
		{{ConnectionID: "connection_orders", RevisionID: "not-a-digest"}},
		{{ConnectionID: "connection_orders", RevisionID: "sha256:" + strings.Repeat("a", 64)}, {ConnectionID: "connection_orders", RevisionID: "sha256:" + strings.Repeat("b", 64)}},
	} {
		tx, beginErr := pool.Begin(t.Context())
		if beginErr != nil {
			t.Fatalf("begin invalid admission transaction: %v", beginErr)
		}
		if callErr := admission.AdmitServingStateBindingsTx(t.Context(), tx, identity, pins); callErr == nil {
			_ = tx.Rollback(t.Context())
			t.Fatalf("admitted invalid pins %#v", pins)
		}
		_ = tx.Rollback(t.Context())
	}
}

func servingIdentityForNativeBinding(projectID projectgraph.ResourceID, environment, generation string) projectgraph.ServingIdentity {
	identity, err := projectgraph.NewServingIdentity(projectID, environment, generation)
	if err != nil {
		panic(err)
	}
	return identity
}
