package http_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	apigenapi "github.com/flidai/leapview/internal/manageddata/api"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	"github.com/flidai/leapview/internal/manageddata/control"
	managedhttp "github.com/flidai/leapview/internal/manageddata/http"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
)

func TestManagedDataCommandsAuditExactlyOnceAndPreserveBestEffortResult(t *testing.T) {
	operations := []struct {
		operationID string
		body        string
		success     int
		invoke      func(*managedhttp.Handler, http.ResponseWriter, *http.Request)
		mutations   func(*fakeUploads, *fakeMultipart) int
	}{
		{
			operationID: manageddatagen.GenOperationCreateManagedDataUploadSession,
			body:        `{"manifest":{"files":[{"path":"orders.csv","size":3,"sha256":"` + digestA + `"}]}}`, success: http.StatusCreated,
			invoke: func(handler *managedhttp.Handler, w http.ResponseWriter, r *http.Request) {
				handler.CreateManagedDataUploadSession(w, r, "project-a", "orders", apigenapi.GenCreateManagedDataUploadSessionHeaders{IdempotencyKey: "create-key"})
			},
			mutations: func(uploads *fakeUploads, _ *fakeMultipart) int {
				if uploads.begin.Project != "" {
					return 1
				}
				return 0
			},
		},
		{
			operationID: manageddatagen.GenOperationCancelManagedDataUploadSession, success: http.StatusOK,
			invoke: func(handler *managedhttp.Handler, w http.ResponseWriter, r *http.Request) {
				handler.CancelManagedDataUploadSession(w, r, "project-a", "orders", "upload-a", apigenapi.GenCancelManagedDataUploadSessionHeaders{IdempotencyKey: "cancel-key"})
			},
			mutations: func(uploads *fakeUploads, _ *fakeMultipart) int { return uploads.abortCalls },
		},
		{
			operationID: manageddatagen.GenOperationFinalizeManagedDataUploadSession, success: http.StatusAccepted,
			invoke: func(handler *managedhttp.Handler, w http.ResponseWriter, r *http.Request) {
				handler.FinalizeManagedDataUploadSession(w, r, "project-a", "orders", "upload-a", apigenapi.GenFinalizeManagedDataUploadSessionHeaders{IdempotencyKey: "finalize-key"})
			},
			mutations: func(uploads *fakeUploads, _ *fakeMultipart) int { return uploads.finalizeCalls },
		},
		{
			operationID: manageddatagen.GenOperationCreateManagedDataS3MultipartUpload,
			body:        `{"path":"orders.csv"}`, success: http.StatusCreated,
			invoke: func(handler *managedhttp.Handler, w http.ResponseWriter, r *http.Request) {
				handler.CreateManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", apigenapi.GenCreateManagedDataS3MultipartUploadHeaders{IdempotencyKey: "multipart-create-key"})
			},
			mutations: func(_ *fakeUploads, multipart *fakeMultipart) int {
				if multipart.create.Project != "" {
					return 1
				}
				return 0
			},
		},
		{
			operationID: manageddatagen.GenOperationCompleteManagedDataS3MultipartUpload,
			body:        `{"parts":[{"partNumber":1,"etag":"etag-a"}]}`, success: http.StatusOK,
			invoke: func(handler *managedhttp.Handler, w http.ResponseWriter, r *http.Request) {
				handler.CompleteManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", "multipart-a", apigenapi.GenCompleteManagedDataS3MultipartUploadHeaders{IdempotencyKey: "multipart-complete-key"})
			},
			mutations: func(_ *fakeUploads, multipart *fakeMultipart) int {
				if multipart.complete.Project != "" {
					return 1
				}
				return 0
			},
		},
		{
			operationID: manageddatagen.GenOperationAbortManagedDataS3MultipartUpload, success: http.StatusOK,
			invoke: func(handler *managedhttp.Handler, w http.ResponseWriter, r *http.Request) {
				handler.AbortManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", "multipart-a", apigenapi.GenAbortManagedDataS3MultipartUploadHeaders{IdempotencyKey: "multipart-abort-key"})
			},
			mutations: func(_ *fakeUploads, multipart *fakeMultipart) int {
				if multipart.abort.Project != "" {
					return 1
				}
				return 0
			},
		},
	}

	for _, operation := range operations {
		for _, auditFailure := range []bool{false, true} {
			name := operation.operationID + "/success"
			if auditFailure {
				name = operation.operationID + "/audit-failure"
			}
			t.Run(name, func(t *testing.T) {
				var logs bytes.Buffer
				uploadResult := uploadFixture()
				uploadResult.Files[0].Transport = control.TransportDescription{
					Protocol:    control.ProtocolS3Multipart,
					S3Multipart: &control.S3MultipartDescription{CreateEndpoint: "/multipart", MinimumPartSize: 1, MaximumPartSize: 1024, MaximumParts: 100},
				}
				uploads := &fakeUploads{result: uploadResult}
				multipart := &fakeMultipart{upload: s3multipart.UploadResult{
					ID: "multipart-a", UploadSessionID: "upload-a",
					File:   manageddata.File{Path: "orders.csv", Size: 3, SHA256: digestA},
					Status: s3multipart.StatusOpen, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T01:00:00Z",
				}}
				options := handlerOptions(metadataFixture(), uploads, multipart)
				attempts := 0
				var persisted []managedhttp.CommandAuditInput
				options.RecordCommandAudit = func(_ context.Context, input managedhttp.CommandAuditInput) error {
					attempts++
					if auditFailure {
						return errors.New("audit store unavailable")
					}
					persisted = append(persisted, input)
					return nil
				}
				options.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
				handler := managedhttp.NewHandler(options)
				recorder := call(t, operation.body, func(w http.ResponseWriter, r *http.Request) {
					r.Header.Set("X-Request-ID", "request-a")
					r.Header.Set("X-Correlation-ID", "correlation-a")
					r.Header.Set("X-LeapView-Invocation-Surface", "cli")
					operation.invoke(handler, w, r)
				})
				wantStatus := operation.success
				wantPersisted := 1
				if auditFailure {
					wantPersisted = 0
				}
				if recorder.Code != wantStatus || attempts != 1 || len(persisted) != wantPersisted || operation.mutations(uploads, multipart) != 1 {
					t.Fatalf("status=%d attempts=%d persisted=%d mutations=%d body=%s", recorder.Code, attempts, len(persisted), operation.mutations(uploads, multipart), recorder.Body.String())
				}
				if !auditFailure {
					input := persisted[0]
					if input.OperationID != operation.operationID || input.PrincipalID != "principal-a" || input.ProjectID != "project-a" ||
						input.ConnectionID != "orders" || input.RequestID != "request-a" || input.CorrelationID != "correlation-a" || input.Surface != "cli" {
						t.Fatalf("audit input = %#v", input)
					}
				} else if output := logs.String(); !strings.Contains(output, "best-effort managed-data command audit failed") ||
					!strings.Contains(output, operation.operationID) || !strings.Contains(output, "principal-a") {
					t.Fatalf("audit failure log = %s", output)
				}
			})
		}
	}
}

func TestManagedDataCommandRejectsMissingAuditSinkBeforeMutation(t *testing.T) {
	uploads := &fakeUploads{result: uploadFixture()}
	options := handlerOptions(metadataFixture(), uploads, nil)
	options.RecordCommandAudit = nil
	handler := managedhttp.NewHandler(options)
	recorder := call(t, `{"manifest":{"files":[{"path":"orders.csv","size":3,"sha256":"`+digestA+`"}]}}`, func(w http.ResponseWriter, r *http.Request) {
		handler.CreateManagedDataUploadSession(w, r, "project-a", "orders", apigenapi.GenCreateManagedDataUploadSessionHeaders{IdempotencyKey: "create-key"})
	})
	if recorder.Code != http.StatusServiceUnavailable || uploads.begin.Project != "" {
		t.Fatalf("status=%d mutation=%#v body=%s", recorder.Code, uploads.begin, recorder.Body.String())
	}
}
