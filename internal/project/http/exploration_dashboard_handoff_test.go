package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestDashboardAppendHandlerRequiresGeneratedOperationClaim(t *testing.T) {
	binding := dashboardgen.GenUIActionExecuteDashboardAuthoringCommand()
	if binding.OperationID() == "" {
		t.Fatal("generated dashboard authoring binding has no operation identity")
	}
	handler := &BrowserHandler{DashboardAppendCommand: binding}
	request := httptest.NewRequest(stdhttp.MethodPost, "/explore/add-to-dashboard", strings.NewReader(`{}`))
	request.Header.Set(uicommand.HeaderOperationID, "anotherOperation")
	recorder := httptest.NewRecorder()
	handler.AppendExplorationToDashboard(recorder, request)
	if recorder.Code != stdhttp.StatusBadRequest {
		t.Fatalf("unclaimed append status = %d, want 400", recorder.Code)
	}
}

func TestExplorationDashboardAppendAuditUsesTransactionalIdempotencyIdentity(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodPost, "/explore/add-to-dashboard", nil)
	requestID := "0198f2c0-7c7a-7f00-8a11-000000000099"
	idempotencyKey := "0198f2c0-7c7a-7f00-8a11-000000000098"
	request.Header.Set("X-Request-ID", requestID)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	operationID := dashboardgen.GenUIActionExecuteDashboardAuthoringCommand().OperationID()
	intent, err := buildExplorationDashboardAppendAuditIntent(request, operationID, graph.ResourceID("sales"), "actor", "dashboard-sales")
	if err != nil {
		t.Fatalf("build generated audit intent: %v", err)
	}
	contract, ok := dashboardgen.GetAPIGenCommandRuntimeContract(operationID)
	if !ok || contract.Guarantee != command.GuaranteeTransactional {
		t.Fatalf("generated append operation contract = %#v, %t", contract, ok)
	}
	if intent.EventID != idempotencyKey || intent.RequestID != requestID || intent.CorrelationID != requestID ||
		intent.Operation != operationID || intent.Action != contract.AuditAction || intent.Capability != access.CapabilityResourceEdit {
		t.Fatalf("audit intent identities/action = %#v", intent)
	}
	var envelope struct {
		Payload dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload `json:"payload"`
	}
	if err := json.Unmarshal([]byte(intent.MetadataJSON), &envelope); err != nil {
		t.Fatalf("decode generated audit payload: %v", err)
	}
	payload := envelope.Payload
	if payload.OperationId != operationID || payload.ProjectId != "sales" || payload.DashboardId != "dashboard-sales" || payload.Origin != "ui" || payload.DraftId != "pending-draft" {
		t.Fatalf("generated audit payload = %#v", payload)
	}
}

func TestDecodeExplorationDashboardJSONIsStrictAndBounded(t *testing.T) {
	var value struct {
		OK bool `json:"ok"`
	}
	if err := decodeExplorationDashboardJSON(strings.NewReader(`{"ok":true}`), &value); err != nil || !value.OK {
		t.Fatalf("valid JSON decode = %#v, %v", value, err)
	}
	if err := decodeExplorationDashboardJSON(strings.NewReader(`{"ok":true,"extra":1}`), &value); err == nil {
		t.Fatal("decoder accepted an unknown request field")
	}
	if err := decodeExplorationDashboardJSON(strings.NewReader(strings.Repeat(" ", (2<<20)+1)), &value); err == nil {
		t.Fatal("decoder accepted a body larger than 2 MiB")
	}
}
