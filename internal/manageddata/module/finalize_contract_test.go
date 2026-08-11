package module

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddataapi "github.com/flidai/leapview/internal/manageddata/api"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	managedfilesystem "github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobssqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/stretchr/testify/require"
)

func TestFinalizeManagedDataUploadGeneratedExecutionContractEndToEnd(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "finalize-upload.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	jobStore := jobssqlite.NewRepository(store.SQLDB())
	managedRoot := filepath.Join(t.TempDir(), "managed")
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), Jobs: jobStore, Workflow: jobStore, RecordAudit: discardManagedDataAudit,
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-a"}, true },
		Product: ProductConfig{
			Backend: "local", Dir: managedRoot,
			UploadSessionTTL: time.Hour, GCGracePeriod: time.Hour,
			MaxFiles: 10, MaxFileBytes: 1024, MaxRevisionBytes: 4096,
		},
	})
	require.NoError(t, err)

	body := []byte("order_id\n1\n")
	digest := sha256.Sum256(body)
	digestText := hex.EncodeToString(digest[:])
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: int64(len(body)), SHA256: digestText}}}
	created, err := module.uploads.BeginUpload(t.Context(), control.BeginUploadRequest{
		Project: "project-a", Connection: "orders", IdempotencyKey: "create", Manifest: manifest,
	})
	require.NoError(t, err)
	blobs, err := managedfilesystem.New(filepath.Join(managedRoot, "objects"))
	require.NoError(t, err)
	_, err = blobs.Put(t.Context(), storage.Blob{SHA256: digestText, Size: int64(len(body))}, bytes.NewReader(body))
	require.NoError(t, err)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/finalize", nil)
	module.handler.FinalizeManagedDataUploadSession(response, request, "project-a", "orders", created.ID, manageddataapi.GenFinalizeManagedDataUploadSessionHeaders{IdempotencyKey: "finalize"})
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var accepted manageddataapi.ManagedDataUploadSessionResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
	require.Equal(t, manageddataapi.ManagedDataUploadSessionStatusFinalizing, accepted.Status)
	require.Equal(t, module.finalizeExecution.InitialState, string(accepted.Status))

	execution := module.finalizeExecution
	job, err := jobStore.Get(t.Context(), execution.ResourceKind+":"+created.ID+":finalize")
	require.NoError(t, err)
	require.Equal(t, execution.JobKind, job.Kind)
	require.Equal(t, execution.ResourceKind, job.ResourceKind)
	require.Equal(t, created.ID, job.ResourceID)
	events, err := jobStore.ListEvents(t.Context(), execution.ResourceKind, created.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, execution.InitialEvent, events[0].EventType)
	var eventData map[string]any
	require.NoError(t, json.Unmarshal(events[0].Data, &eventData))
	require.Equal(t, execution.InitialState, eventData["status"])

	handlers := module.JobHandlers(jobStore)
	require.Len(t, handlers, 1)
	require.Equal(t, execution.JobKind, handlers[0].Kind())
	require.NoError(t, handlers[0].Handle(t.Context(), job))
	completed, err := module.uploads.RecoverUpload(t.Context(), control.UploadRequest{Project: "project-a", Connection: "orders", UploadID: created.ID})
	require.NoError(t, err)
	require.Equal(t, manageddata.UploadStatusComplete, completed.Status)
	events, err = jobStore.ListEvents(t.Context(), execution.ResourceKind, created.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "upload_session.completed", events[1].EventType)
}

func TestFinalizeManagedDataUploadRejectsJobHandlerRegistrationDrift(t *testing.T) {
	execution, err := loadFinalizeUploadExecutionContract()
	require.NoError(t, err)
	err = validateFinalizeUploadJobHandlers(execution, []jobs.Handler{jobs.HandlerFunc{JobKind: "upload.wrong"}})
	require.ErrorContains(t, err, "does not match generated kind")
}
