package apigenruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigenaudit "github.com/Yacobolo/toolbelt/apigen/runtime/audit"
	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
)

type testAuthorizer struct{}

func (testAuthorizer) Protect(_ string, next http.Handler) (http.Handler, bool) {
	return next, true
}

func noCommandLookup(string) (apigencommand.Contract, bool) {
	return apigencommand.Contract{}, false
}

func buildTestHandler(dispatch Dispatch, commands ...apigencommand.Lookup) (*Handler, error) {
	lookup := apigencommand.Lookup(noCommandLookup)
	if len(commands) != 0 {
		lookup = commands[0]
	}
	return Build(testAuthorizer{}, dispatch, lookup)
}

func commandLookup(operationID string) (apigencommand.Contract, bool) {
	if operationID != "createWidget" {
		return apigencommand.Contract{}, false
	}
	return apigencommand.Contract{
		OperationID: operationID, Owner: "Widgets", AuditAction: "widget.created", Guarantee: apigencommand.GuaranteeBestEffort,
		AuditPayload: &apigenaudit.Contract{
			Schema: "WidgetCreatedAuditPayload", SchemaVersion: 1, Retention: apigenaudit.RetentionSecurity,
			Fields: []apigenaudit.FieldContract{{Name: "widgetId", Sensitivity: apigenaudit.SensitivityInternal}},
		},
	}, true
}

func TestGeneratedCommandBoundaryRejectsSuccessfulBypass(t *testing.T) {
	handler, err := buildTestHandler(func(_ string, w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusCreated)
		return true
	}, commandLookup)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.HandleAPIGen("createWidget", recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "COMMAND_CONTRACT_NOT_EXECUTED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGeneratedCommandBoundaryAcceptsExecutorCompletion(t *testing.T) {
	executor, err := apigencommand.NewExecutor(commandLookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := buildTestHandler(func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		if err := executor.Execute(r.Context(), operationID, apigencommand.Execution{BestEffortAudit: func(context.Context, apigencommand.Contract) error { return nil }}); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		return true
	}, commandLookup)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.HandleAPIGen("createWidget", recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerUsesInjectedGeneratedPartitionDispatch(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(operationID string, w http.ResponseWriter, _ *http.Request) bool {
		called = true
		if operationID != "getAgentConversation" {
			t.Fatalf("operation ID = %q", operationID)
		}
		w.WriteHeader(http.StatusAccepted)
		return true
	})
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.HandleAPIGen("getAgentConversation", recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("partition dispatch was not called")
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	if len(recorder.Header().Values("X-Request-ID")) != 1 || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("request ID headers = %#v", recorder.Header().Values("X-Request-ID"))
	}
}

func TestBuildRejectsMissingDispatch(t *testing.T) {
	if _, err := buildTestHandler(nil); err == nil {
		t.Fatal("Build accepted a nil dispatch function")
	}
}

func TestBuildRejectsMissingCommandContractLookup(t *testing.T) {
	if _, err := Build(testAuthorizer{}, func(string, http.ResponseWriter, *http.Request) bool { return true }, nil); err == nil {
		t.Fatal("Build accepted a nil command contract lookup")
	}
}

func TestGeneratedTransportRejectsEmptyPageTokenBeforeService(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool {
		called = true
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?pageToken=%20", nil)
	handler.HandleAPIGen("listProjects", rec, req)
	if called || rec.Code != http.StatusBadRequest {
		t.Fatalf("boundary status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

func TestGeneratedSemanticTransportRejectsLimitsAndArraysBeforeService(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "limit", body: `{"limit":1001}`},
		{name: "body pageToken", body: `{"pageToken":"  "}`},
		{name: "dimensions", body: `{"dimensions":[` + strings.TrimSuffix(strings.Repeat("1,", 50), ",") + `,1]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool { called = true; return true })
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/w/semantic-models/m/query", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleAPIGen("querySemanticModel", rec, req)
			if called || rec.Code != http.StatusBadRequest {
				t.Fatalf("boundary status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
			}
		})
	}
}

func TestGeneratedTransportRejectsUnsupportedContentTypeBeforeService(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool { called = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/w/semantic-models/m/query", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.HandleAPIGen("querySemanticModel", rec, req)
	if called || rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("boundary status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
	var problem map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil || problem["code"] != "UNSUPPORTED_MEDIA_TYPE" {
		t.Fatalf("problem=%s err=%v", rec.Body.String(), err)
	}
}

func TestGeneratedTransportRequiresContentTypeForNonEmptyBody(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool { called = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(`{"name":"automation"}`))
	rec := httptest.NewRecorder()
	handler.HandleAPIGen("createCurrentAPIToken", rec, req)
	if called || rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("boundary status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

func TestGeneratedTransportAllowsEmptyBodyWithoutContentType(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool { called = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.HandleAPIGen("cancelAgentRun", rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if !called {
		t.Fatalf("empty request was rejected: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGeneratedTransportAcceptsJSONContentTypeParameters(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool { called = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.HandleAPIGen("createCurrentAPIToken", rec, req)
	if !called {
		t.Fatalf("JSON request was rejected: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGeneratedSemanticTransportBoundsBodyBeforeService(t *testing.T) {
	called := false
	handler, err := buildTestHandler(func(string, http.ResponseWriter, *http.Request) bool { called = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/w/semantic-models/m/query", io.MultiReader(strings.NewReader(`{"padding":"`), strings.NewReader(strings.Repeat("x", int(maxGeneratedJSONBodyBytes))), strings.NewReader(`"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleAPIGen("querySemanticModel", rec, req)
	if called || rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("boundary status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}
