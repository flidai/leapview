package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/flidai/leapview/internal/manageddata"
	apigenapi "github.com/flidai/leapview/internal/manageddata/api"
	"github.com/flidai/leapview/internal/manageddata/control"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
)

type PageParams = apigenapi.PageParams
type IdempotencyHeaders = apigenapi.IdempotencyHeaders
type EventHeaders = apigenapi.GenListManagedDataUploadSessionEventsHeaders

func (m *Module) beginFinalize(ctx context.Context, request control.UploadRequest) (control.UploadResult, error) {
	payload, err := json.Marshal(FinalizeUploadJob{Project: request.Project, Connection: request.Connection, UploadSession: request.UploadID})
	if err != nil {
		return control.UploadResult{}, err
	}
	event, err := json.Marshal(map[string]any{"uploadSessionId": request.UploadID, "status": m.finalizeExecution.InitialState})
	if err != nil {
		return control.UploadResult{}, err
	}
	request.Workflow = jobs.WorkflowIntent{
		Event: jobs.EventInput{
			Key: m.finalizeExecution.InitialEvent, ResourceKind: m.finalizeExecution.ResourceKind, ResourceID: request.UploadID,
			EventType: m.finalizeExecution.InitialEvent, Data: event,
		},
		Job: jobs.EnqueueInput{
			ID: m.finalizeExecution.ResourceKind + ":" + request.UploadID + ":finalize", Kind: m.finalizeExecution.JobKind,
			WorkloadClass: "control", WorkspaceID: "_node",
			ResourceKind: m.finalizeExecution.ResourceKind, ResourceID: request.UploadID, Payload: payload,
		},
	}
	if m == nil || m.uploads == nil {
		return control.UploadResult{}, errors.New("managed-data finalization is unavailable")
	}
	return m.uploads.BeginFinalizeUpload(ctx, request)
}

func (m *Module) abortUpload(ctx context.Context, request control.UploadRequest) (control.UploadResult, error) {
	if m == nil || m.workflow == nil {
		result, err := m.uploads.AbortUpload(ctx, request)
		if err == nil && result.Status == manageddata.UploadStatusAborted {
			// Non-production adapters may not provide the atomic SQLite workflow
			// capability. Keep their event history correct, while production uses
			// the transactional path below.
			err = m.recordUploadCancelled(ctx, result)
		}
		return result, err
	}
	request.Workflow = jobs.WorkflowIntent{Event: jobs.EventInput{
		Key: "upload:" + request.UploadID + ":cancelled", ResourceKind: "upload", ResourceID: request.UploadID,
		EventType: "upload_session.cancelled",
		Data:      []byte(`{"uploadSessionId":"` + request.UploadID + `","status":"cancelled"}`),
	}}
	return m.uploads.AbortUpload(ctx, request)
}

func (m *Module) recordUploadCreated(ctx context.Context, result control.UploadResult) error {
	return m.appendEvent(ctx, result.ID, "upload_session.created", map[string]any{
		"uploadSessionId": result.ID, "projectId": result.Collection.Project,
		"connectionId": result.Collection.Connection, "status": result.Status,
	})
}

func (m *Module) recordUploadCancelled(ctx context.Context, result control.UploadResult) error {
	if m == nil || m.jobs == nil {
		return errors.New("managed-data event store is unavailable")
	}
	m.eventMu.Lock()
	defer m.eventMu.Unlock()
	// Cancellation is replayed by idempotent clients. Check the durable stream
	// before appending so a replay emits exactly one terminal event.
	events, err := m.jobs.ListEvents(ctx, m.finalizeExecution.ResourceKind, result.ID, 0, 250)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.EventType == "upload_session.cancelled" {
			return nil
		}
	}
	return m.appendEvent(ctx, result.ID, "upload_session.cancelled", map[string]any{
		"uploadSessionId": result.ID, "status": "cancelled",
	})
}

func (m *Module) appendEvent(ctx context.Context, uploadID, eventType string, data any) error {
	if m == nil || m.jobs == nil {
		return errors.New("managed-data event store is unavailable")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = m.jobs.AppendEvent(ctx, m.finalizeExecution.ResourceKind, uploadID, eventType, encoded)
	return err
}

func (m *Module) ListUploadSessionEvents(w http.ResponseWriter, r *http.Request, projectID, connectionID, sessionID string, params apigenapi.GenListManagedDataUploadSessionEventsParams, _ apigenapi.GenListManagedDataUploadSessionEventsHeaders) {
	if m == nil || m.uploads == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "UPLOAD_SERVICE_UNAVAILABLE", "Managed-data uploads are unavailable", nil)
		return
	}
	if _, err := m.uploads.RecoverUpload(r.Context(), control.UploadRequest{Project: projectID, Connection: connectionID, UploadID: sessionID}); err != nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "UPLOAD_SESSION_NOT_FOUND", "Upload session not found", nil)
		return
	}
	if m.jobs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_EVENT_STORE_UNAVAILABLE", "Upload events are unavailable", nil)
		return
	}
	jobhttp.WriteEventPage(w, r, m.jobs, m.finalizeExecution.ResourceKind, sessionID, params.Limit, params.PageToken, m.finalizeExecution.ResourceKind+":"+projectID+":"+connectionID+":"+sessionID)
}
