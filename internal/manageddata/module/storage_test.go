package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata"
	apigenapi "github.com/flidai/leapview/internal/manageddata/api"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/flidai/leapview/internal/manageddata/storage"
	managedfilesystem "github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	managedtus "github.com/flidai/leapview/internal/manageddata/storage/tus"
	"github.com/flidai/leapview/internal/platform"
	jobssqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestBuildKeepsPersistencePrivateAndExposesNamedServices(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	states, err := servingstatemodule.Build(t.Context(), servingstatemodule.Config{Database: store.SQLDB()})
	if err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), ServingStates: states, RecordAudit: discardManagedDataAudit,
		AuthorizeConnection: allowAllConnectionAuthorization,
		Product: ProductConfig{
			Backend:          "local",
			Dir:              filepath.Join(t.TempDir(), "managed"),
			UploadSessionTTL: time.Hour,
			GCGracePeriod:    time.Hour,
			MaxFiles:         10,
			MaxFileBytes:     1024,
			MaxRevisionBytes: 4096,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if module.BindingValidation() == nil || module.RuntimeResolution() == nil || module.DeploymentMetadata() == nil {
		t.Fatal("managed-data module did not expose its named cross-capability services")
	}
}

func TestModuleHTTPConcurrentCancelEmitsOneEventAndCleansTusState(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs := jobssqlite.NewRepository(store.SQLDB())
	managedRoot := filepath.Join(t.TempDir(), "managed")
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), Jobs: jobs, Workflow: jobs, RecordAudit: discardManagedDataAudit,
		CurrentPrincipal:    func(*http.Request) (Principal, bool) { return Principal{ID: "principal-a"}, true },
		AuthorizeConnection: allowAllConnectionAuthorization,
		Product:             ProductConfig{Backend: "local", Dir: managedRoot, UploadSessionTTL: time.Hour, GCGracePeriod: time.Hour, MaxFiles: 10, MaxFileBytes: 1024, MaxRevisionBytes: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("data")
	digest := sha256.Sum256(body)
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}}}
	// Exercise cancellation before any bytes have been uploaded. BeginUpload
	// still creates offset-zero TUS staging that must be removed atomically.
	before, err := module.uploads.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "cancel-before", Manifest: manifest,
	})
	if err != nil || len(before.MissingBlobs) != 1 || before.MissingBlobs[0].Transport.Tus == nil {
		t.Fatalf("begin pre-upload cancellation = %#v, %v", before, err)
	}
	preReq := httptest.NewRequest(http.MethodPost, "/", nil)
	preResp := httptest.NewRecorder()
	module.handler.CancelManagedDataUploadSession(preResp, preReq, "project-a", "orders", before.ID, apigenapi.GenCancelManagedDataUploadSessionHeaders{IdempotencyKey: "cancel-before"})
	if preResp.Code != http.StatusOK {
		t.Fatalf("pre-upload cancellation status=%d body=%s", preResp.Code, preResp.Body.String())
	}
	preID := before.MissingBlobs[0].Transport.Tus.UploadID
	for _, suffix := range []string{"", ".info"} {
		if _, err := os.Stat(filepath.Join(managedRoot, "uploads", preID+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pre-upload staging %q remains: %v", suffix, err)
		}
	}
	result, err := module.uploads.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "cancel-http",
		Manifest: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MissingBlobs) != 1 || result.MissingBlobs[0].Transport.Tus == nil {
		t.Fatalf("missing blob transport = %#v", result.MissingBlobs)
	}
	// Write a real partial TUS body so cancellation covers both data and the
	// sidecar metadata file produced by tusd, rather than only an empty intent.
	blobs, err := managedfilesystem.New(filepath.Join(managedRoot, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := managedtus.New(filepath.Join(managedRoot, "uploads"), blobs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.WriteChunk(t.Context(), result.MissingBlobs[0].Transport.Tus.UploadID, 0, bytes.NewReader(body[:2])); err != nil {
		t.Fatalf("write partial tus body: %v", err)
	}
	stagedID := result.MissingBlobs[0].Transport.Tus.UploadID
	for _, suffix := range []string{"", ".info"} {
		if _, err := os.Stat(filepath.Join(managedRoot, "uploads", stagedID+suffix)); err != nil {
			t.Fatalf("staging %q missing before cancel: %v", suffix, err)
		}
	}
	var group sync.WaitGroup
	responses := make(chan int, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			recorder := httptest.NewRecorder()
			module.handler.CancelManagedDataUploadSession(recorder, req, "project-a", "orders", result.ID, apigenapi.GenCancelManagedDataUploadSessionHeaders{IdempotencyKey: "cancel-http"})
			responses <- recorder.Code
		}()
	}
	group.Wait()
	close(responses)
	for code := range responses {
		if code != http.StatusOK {
			t.Fatalf("cancel response status=%d", code)
		}
	}
	events, err := jobs.ListEvents(t.Context(), "upload", result.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "upload_session.cancelled" {
		t.Fatalf("cancel events=%#v err=%v", events, err)
	}
	entries, err := os.ReadDir(filepath.Join(managedRoot, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("TUS staging remains after cancellation: %v", entries)
	}
	for _, suffix := range []string{"", ".info"} {
		if _, err := os.Stat(filepath.Join(managedRoot, "uploads", stagedID+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging %q remains after cancellation: %v", suffix, err)
		}
	}
}

func allowAllConnectionAuthorization(context.Context, string, string, string, access.Capability) (bool, error) {
	return true, nil
}

func TestNewManagedDataStorageLocal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	services, err := newManagedDataStorage(context.Background(), ProductConfig{
		Backend:      "local",
		Dir:          root,
		MaxFileBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if services.blobs == nil || services.transport == nil || services.materializer == nil || services.tus == nil || services.s3 != nil {
		t.Fatalf("services = %#v", services)
	}
	if services.runtimeCache != nil {
		t.Fatal("local backend unexpectedly allocated a copying runtime cache")
	}
	collector, err := newManagedDataRuntimeCollector(services, ProductConfig{GCGracePeriod: time.Hour})
	if err != nil || collector != nil {
		t.Fatalf("local runtime collector = %#v, %v; want nil", collector, err)
	}
	if services.transport.Backend() != "local" {
		t.Fatalf("backend = %q", services.transport.Backend())
	}
	for _, relative := range []string{"objects", "uploads"} {
		info, statErr := os.Stat(filepath.Join(root, relative))
		if statErr != nil {
			t.Fatalf("stat %s: %v", relative, statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions = %o, want private", relative, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local runtime cache stat error = %v, want not exist", err)
	}
}

func TestCapacityProtectedTusRejectsChunkWithoutReserve(t *testing.T) {
	checker, err := maintenance.NewCapacityChecker(t.TempDir(), math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := capacityProtectedTus(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), checker)
	request := httptest.NewRequest(http.MethodPatch, "/tus/upload", strings.NewReader("x"))
	request.ContentLength = 1
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInsufficientStorage || called {
		t.Fatalf("status = %d, called = %v", recorder.Code, called)
	}
}

func TestTusMethodsAreClosedByDefault(t *testing.T) {
	handler := TusProtocolHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodConnect, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(method, "/upload-protocols/tus/tus_abc", nil))
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

func TestTusMethodsForwardsResumableOperations(t *testing.T) {
	var methods []string
	handler := TusProtocolHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, method := range []string{http.MethodOptions, http.MethodHead, http.MethodPatch, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, "/upload-protocols/tus/tus_abc", nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want %d", method, recorder.Code, http.StatusNoContent)
		}
	}
	if want := []string{http.MethodOptions, http.MethodHead, http.MethodPatch, http.MethodDelete}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("forwarded methods = %v, want %v", methods, want)
	}
}

func TestNewManagedDataStorageRejectsUnknownBackend(t *testing.T) {
	_, err := newManagedDataStorage(context.Background(), ProductConfig{
		Backend: "shared-filesystem",
		Dir:     t.TempDir(),
	})
	if err == nil || !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("error = %v, want storage.ErrInvalid", err)
	}
}

func TestNewManagedDataControlRequiresStorage(t *testing.T) {
	_, err := newManagedDataControl(nil, managedDataStorage{}, ProductConfig{})
	if err == nil || !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("error = %v, want control.ErrInvalid", err)
	}
}
