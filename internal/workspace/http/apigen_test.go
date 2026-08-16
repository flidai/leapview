package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetiredWorkspaceAPIDispatcherDoesNotRegisterOperations(t *testing.T) {
	if DispatchAPIGenOperation("listWorkspaces", slog.Default(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("retired workspace operation was dispatched")
	}
}
