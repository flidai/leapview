package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/manageddata"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	manageddatasqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	managedstorage "github.com/flidai/leapview/internal/manageddata/storage"
	managedfilesystem "github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	managedtus "github.com/flidai/leapview/internal/manageddata/storage/tus"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestManagedDataTusRouteRejectsClientCreatedUploads(t *testing.T) {
	called := false
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{

		ManagedDataTus: manageddatamodule.TusProtocolHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		})),
	}))

	request := httptest.NewRequest(http.MethodPost, "/upload-protocols/tus", nil)
	request.Header.Set("Authorization", "Bearer dev")
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if called {
		t.Fatal("tus backend received a client-created upload request")
	}
}

func TestManagedDataTusRouteForwardsResumableOperations(t *testing.T) {
	store := testStore(t)
	principal := testPlatformPrincipal(t, context.Background(), store, "publisher@example.com", "Publisher")
	token, _ := testScopedAPIToken(t, context.Background(), store, access.APITokenInput{
		PrincipalID:  principal.ID,
		Name:         "managed-data-publisher",
		Capabilities: []access.Capability{access.CapabilityResourceEdit},
	})
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	statePersistence, err := servingstatemodule.NewSQLitePersistence(store.SQLDB())
	if err != nil {
		t.Fatalf("build serving-state persistence: %v", err)
	}
	states, err := servingstatemodule.Build(context.Background(), servingstatemodule.Config{Persistence: &statePersistence})
	if err != nil {
		t.Fatalf("build serving states: %v", err)
	}
	host, err := ensureTestRuntimeHost(context.Background(), store, states, testProjectID, "dev")
	if err != nil {
		t.Fatalf("build runtime host: %v", err)
	}
	base := testStoreOptions(store, assemblyConfig{Auth: auth, ProjectID: testProjectID, ServingStateRepo: states, RuntimeHost: host, DefaultEnvironment: "dev"})
	root := t.TempDir()
	managedDataPersistence, err := manageddatamodule.NewSQLitePersistence(manageddatamodule.SQLitePersistenceConfig{
		Database:            store.SQLDB(),
		AuditIntentRecorder: accesssqlite.NewRepository(store.SQLDB()),
	})
	if err != nil {
		t.Fatalf("build managed-data persistence: %v", err)
	}
	managedData, err := manageddatamodule.Build(context.Background(), manageddatamodule.Config{
		Persistence: &managedDataPersistence, ServingStates: base.ServingStateRepo, Environment: base.DefaultEnvironment,
		Product:             manageddatamodule.ProductConfig{Backend: "local", Dir: root, UploadSessionTTL: time.Hour, GCGracePeriod: time.Hour, MaxFiles: 10, MaxFileBytes: 1024, MaxRevisionBytes: 4096},
		AuditIntentRecorder: accesssqlite.NewRepository(store.SQLDB()),
	})
	if err != nil {
		t.Fatalf("build managed data module: %v", err)
	}
	repo := manageddatasqlite.NewRepository(store.SQLDB())
	collection, err := repo.CreateCollection(context.Background(), manageddata.CreateCollectionInput{ID: "collection_test", ProjectID: testProjectID, ConnectionID: "connection:test", Name: "Test"})
	if err != nil {
		t.Fatalf("create managed data collection: %v", err)
	}
	const uploadSession = manageddata.UploadID("upload_test")
	digest := strings.Repeat("a", 64)
	if _, err := repo.CreateUploadSession(context.Background(), manageddata.CreateUploadSessionInput{ID: uploadSession, CollectionID: collection.ID, Manifest: manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 1, SHA256: digest}}}, StorageBackend: "local", StagingPrefix: "uploads/" + uploadSession.String(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create upload session: %v", err)
	}
	transportDigest := sha256.Sum256([]byte(uploadSession.String() + "\x00" + digest))
	transportID := "tus_" + hex.EncodeToString(transportDigest[:])
	blobs, err := managedfilesystem.New(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatalf("build tus blob store: %v", err)
	}
	engine, err := managedtus.New(filepath.Join(root, "uploads"), blobs)
	if err != nil {
		t.Fatalf("build tus engine: %v", err)
	}
	if _, err := engine.Create(context.Background(), managedstorage.CreateUpload{ID: transportID, Size: 1, Metadata: map[string]string{"sha256": digest, "size": "1", "session_id": uploadSession.String()}}); err != nil {
		t.Fatalf("seed tus staging: %v", err)
	}
	var method, path string
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth: auth, AccessModule: base.AccessModule, ServingStateRepo: base.ServingStateRepo, RuntimeHost: base.RuntimeHost,
		ManagedDataModule: managedData, DefaultEnvironment: base.DefaultEnvironment, ProjectID: testProjectID,

		ManagedDataTus: manageddatamodule.TusProtocolHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		})),
	}))

	request := httptest.NewRequest(http.MethodPatch, "/upload-protocols/tus/"+transportID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent || method != http.MethodPatch || path != "/upload-protocols/tus/"+transportID {
		t.Fatalf("status = %d, method = %q, path = %q, body = %s", recorder.Code, method, path, recorder.Body.String())
	}
}
