package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/queryaudit"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type recordingReader struct {
	filter queryaudit.Filter
	rows   []queryaudit.Event
}

func (*recordingReader) GetQueryEvent(context.Context, string) (queryaudit.Event, error) {
	return queryaudit.Event{}, errors.New("not implemented")
}

func (r *recordingReader) ListQueryEvents(_ context.Context, filter queryaudit.Filter) ([]queryaudit.Event, error) {
	r.filter = filter
	return r.rows, nil
}

func (*recordingReader) ListQueryEventFilterOptions(context.Context, string, string, int) ([]queryaudit.FilterOption, error) {
	return nil, errors.New("not implemented")
}

func TestListQueryEventsUsesActiveInstanceIdentityAndHidesProject(t *testing.T) {
	reader := &recordingReader{rows: []queryaudit.Event{{
		ID: "event-1", EventInput: queryaudit.EventInput{
			ProjectID: "project:active", Surface: "api", Operation: "query", QueryKind: "semantic",
			ModelID: "semantic:sales", ObjectType: "dashboard", ObjectID: "dashboard:sales",
			RequestID: "request-1", CorrelationID: "correlation-1", Status: "succeeded", QueryJSON: `{}`,
		}, CreatedAt: "2026-09-03T00:00:00Z",
	}}}
	handler := Handler{
		Reader: func() (queryaudit.Reader, error) { return reader, nil },
		ProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:active", nil
		},
	}
	recorder := httptest.NewRecorder()
	handler.ListQueryEvents(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/query-events?limit=10", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if reader.filter.ProjectID != "project:active" {
		t.Fatalf("filter project = %q, want active instance project", reader.filter.ProjectID)
	}
	if strings.Contains(recorder.Body.String(), `"projectId"`) {
		t.Fatalf("instance-bound query event leaked project identity: %s", recorder.Body.String())
	}
}

func TestListQueryEventsFailsClosedWithoutActiveInstanceIdentity(t *testing.T) {
	handler := Handler{Reader: func() (queryaudit.Reader, error) { return &recordingReader{}, nil }}
	recorder := httptest.NewRecorder()
	handler.ListQueryEvents(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/query-events", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
