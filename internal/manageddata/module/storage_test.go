package module

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/flidai/leapview/internal/manageddata/storage"
)

func allowAllConnectionAuthorization(context.Context, string, string, string, access.Capability) (bool, error) {
	return true, nil
}

func TestSetAuthorizeConnectionUpdatesModuleEventAuthorization(t *testing.T) {
	module := &Module{}
	called := false
	module.SetAuthorizeConnection(func(_ context.Context, principalID, projectID, connectionID string, capability access.Capability) (bool, error) {
		called = true
		if principalID != "principal:test" || projectID != "project:test" || connectionID != "connection:test" || capability != access.CapabilityResourceRead {
			t.Fatalf("unexpected authorization tuple %q %q %q %q", principalID, projectID, connectionID, capability)
		}
		return true, nil
	})
	if module.authorizeConnection == nil {
		t.Fatal("module event authorizer was not installed")
	}
	allowed, err := module.authorizeConnection(t.Context(), "principal:test", "project:test", "connection:test", access.CapabilityResourceRead)
	if err != nil || !allowed || !called {
		t.Fatalf("module event authorization allowed=%v called=%v error=%v", allowed, called, err)
	}
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
	_, err := newManagedDataControl(nil, nil, nil, managedDataStorage{}, ProductConfig{})
	if err == nil || !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("error = %v, want control.ErrInvalid", err)
	}
}
