package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/go-chi/chi/v5"
)

const (
	activationOperation = "90703271-1f07-46d4-a037-ba0c5059a430"
	activationVersion   = "315d3660-b76f-4574-bf8a-a9c4a65750a6"
	activationReceipt   = "07c01113-9057-45b6-93c5-b92cbbcd276e"
)

func TestCredentialActivationTransportUsesServerScopeAndReportsDurablePreparation(t *testing.T) {
	service := &activationTransportFake{state: "prepared"}
	response := callActivationTransport(t, service, "startCredentialActivation", `{"operationId":"`+activationOperation+`","receiptId":"`+activationReceipt+`","expectedBindingRevision":41}`)
	if response.Code != http.StatusAccepted || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	wantResource := credential.Resource{ScopeKind: "connection", ProjectID: "project_one", TargetID: "lvinst_0123456789abcdefghijklmnopqrstuv", Environment: "production", ResourceID: "warehouse"}
	if service.actor != "principal_test" || service.resource != wantResource || service.input != (credential.ActivationRequest{
		OperationID: activationOperation, VersionID: activationVersion, ReceiptID: activationReceipt, ExpectedBindingRevision: 41,
	}) {
		t.Fatalf("caller input replaced server scope or exact intent: %#v", service)
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["operationId"] != activationOperation || result["versionId"] != activationVersion || result["state"] != "prepared" || result["runtimeReady"] != false {
		t.Fatalf("preparation implies activation: %#v", result)
	}
	for _, key := range []string{"receiptId", "fields", "password", "configurationDigest", "ownerId"} {
		if _, exists := result[key]; exists {
			t.Fatalf("private field returned: %s", key)
		}
	}
}

func TestCredentialActivationTransportRejectsMalformedIntentBeforeService(t *testing.T) {
	valid := `{"operationId":"` + activationOperation + `","receiptId":"` + activationReceipt + `","expectedBindingRevision":41}`
	for _, body := range []string{
		`null`, `{}`, strings.Replace(valid, `"expectedBindingRevision":41`, `"expectedBindingRevision":0`, 1),
		strings.Replace(valid, `"operationId":`, `"OperationId":`, 1),
		strings.Replace(valid, `41}`, `41,"expectedBindingRevision":42}`, 1),
		strings.Replace(valid, `41}`, `41,"ownerId":"secret-sentinel"}`, 1),
		strings.Replace(valid, activationOperation, strings.ToUpper(activationOperation), 1),
		strings.Replace(valid, activationReceipt, "00000000-0000-0000-0000-000000000000", 1),
		valid + ` {}`, strings.Repeat(" ", 2049),
	} {
		t.Run(body[:min(len(body), 60)], func(t *testing.T) {
			service := &activationTransportFake{}
			response := callActivationTransport(t, service, "startCredentialActivation", body)
			if (response.Code != http.StatusBadRequest && response.Code != http.StatusRequestEntityTooLarge) || service.calls != 0 || strings.Contains(response.Body.String(), "secret-sentinel") {
				t.Fatalf("invalid body reached service or was echoed: %d calls=%d %s", response.Code, service.calls, response.Body.String())
			}
		})
	}
}

func TestCredentialActivationTransportStatusRetryAndAbortUseExactOperation(t *testing.T) {
	for _, test := range []struct {
		operation, body, state string
		status                 int
	}{
		{"getCredentialActivation", "", "committed", http.StatusOK},
		{"retryCredentialActivation", `{}`, "committed", http.StatusAccepted},
		{"retryCredentialActivation", `{"receiptId":"` + activationReceipt + `"}`, "completed", http.StatusOK},
		{"abortCredentialActivation", `{}`, "aborted", http.StatusOK},
	} {
		t.Run(test.operation+test.state, func(t *testing.T) {
			service := &activationTransportFake{state: test.state}
			response := callActivationTransport(t, service, test.operation, test.body)
			if response.Code != test.status || service.operationID != activationOperation || service.calls != 1 {
				t.Fatalf("wrong operation dispatch: status=%d service=%#v body=%s", response.Code, service, response.Body.String())
			}
			var result map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["runtimeReady"] != (test.state == "completed") {
				t.Fatalf("wrong readiness: %#v", result)
			}
			if test.body != "{}" && test.operation == "retryCredentialActivation" && service.receiptID != activationReceipt {
				t.Fatal("retry lost exact receipt")
			}
		})
	}
}

func TestCredentialActivationTransportFailsClosedAndSanitizesServiceErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{credential.ErrForbidden, http.StatusForbidden}, {credential.ErrNotFound, http.StatusNotFound},
		{credential.ErrConflict, http.StatusConflict}, {credential.ErrInvalid, http.StatusBadRequest},
		{errors.New("password=secret-sentinel"), http.StatusServiceUnavailable},
	} {
		service := &activationTransportFake{err: test.err}
		response := callActivationTransport(t, service, "retryCredentialActivation", `{}`)
		if response.Code != test.status || strings.Contains(response.Body.String(), "secret-sentinel") {
			t.Fatalf("unsafe failure: %d %s", response.Code, response.Body.String())
		}
	}
	for _, service := range []credential.ActivationService{nil, (*activationTransportFake)(nil)} {
		response := callActivationTransport(t, service, "getCredentialActivation", "")
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing service status=%d", response.Code)
		}
	}
}

func TestCredentialActivationTransportRejectsUnauthenticatedAndAmbiguousCommands(t *testing.T) {
	for _, test := range []struct {
		name, operation, body string
		status                int
		configure             func(*CredentialDraftAPIGenConfig, *http.Request)
	}{
		{"unauthenticated", "getCredentialActivation", "", http.StatusUnauthorized, func(c *CredentialDraftAPIGenConfig, _ *http.Request) { c.CurrentPrincipal = nil }},
		{"transport idempotency", "retryCredentialActivation", `{}`, http.StatusBadRequest, func(_ *CredentialDraftAPIGenConfig, r *http.Request) {
			r.Header.Set("Idempotency-Key", "second-journal")
		}},
		{"unknown retry field", "retryCredentialActivation", `{"ownerId":"secret-sentinel"}`, http.StatusBadRequest, nil},
		{"null retry receipt", "retryCredentialActivation", `{"receiptId":null}`, http.StatusBadRequest, nil},
		{"abort body ownership", "abortCredentialActivation", `{"ownerId":"secret-sentinel"}`, http.StatusBadRequest, nil},
		{"abort null", "abortCredentialActivation", `null`, http.StatusBadRequest, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &activationTransportFake{}
			response := callActivationTransport(t, service, test.operation, test.body, test.configure)
			if response.Code != test.status || service.calls != 0 || strings.Contains(response.Body.String(), "secret-sentinel") {
				t.Fatalf("unsafe command reached service: status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
			}
		})
	}
}

func TestCredentialActivationGeneratedContractsBindExactConnectionAndDurableAudit(t *testing.T) {
	contracts := credentialgen.GetAPIGenOperationContracts()
	for operation, action := range map[string]string{
		"startCredentialActivation": "credential.activation.requested",
		"retryCredentialActivation": "credential.activation.retried",
		"abortCredentialActivation": "credential.activation.aborted",
	} {
		contract, ok := contracts[operation]
		if !ok || contract.Authz.Action != "connection.manage" || contract.Authz.Resolver != "connection" || contract.Command == nil {
			t.Fatalf("activation has no exact connection mutation contract: %s %#v", operation, contract)
		}
		command := contract.Command
		if command.Idempotency != "forbidden" || command.Audit.SuccessAction != action || command.Audit.Guarantee != "transactional" || command.Audit.Payload == nil || command.Audit.Payload.Schema != "CredentialActivationAuditPayload" {
			t.Fatalf("activation command lost audit/retry policy: %s %#v", operation, command)
		}
	}
	if contract := contracts["getCredentialActivation"]; contract.Authz.Action != "connection.read" || contract.Authz.Resolver != "connection" {
		t.Fatalf("activation status lost exact read authority: %#v", contract)
	}
}

func callActivationTransport(t *testing.T, service credential.ActivationService, operation, body string, configure ...func(*CredentialDraftAPIGenConfig, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := withCredentialDraftRouteParams(httptest.NewRequest(http.MethodPost, "/activation", strings.NewReader(body)), "project_one", "lvinst_0123456789abcdefghijklmnopqrstuv", "warehouse", activationVersion)
	chi.RouteContext(request.Context()).URLParams.Add("operation", activationOperation)
	response := httptest.NewRecorder()
	config := CredentialDraftAPIGenConfig{Service: new(credential.Service), Activation: service, Environment: "production", CurrentPrincipal: func(*http.Request) (string, bool) { return "principal_test", true }}
	for _, apply := range configure {
		if apply != nil {
			apply(&config, request)
		}
	}
	if !DispatchAPIGenOperation(config, operation, nil, response, request) {
		t.Fatalf("generated route does not dispatch %s", operation)
	}
	return response
}

type activationTransportFake struct {
	state, actor, operationID, receiptID string
	resource                             credential.Resource
	input                                credential.ActivationRequest
	calls                                int
	err                                  error
}

func (s *activationTransportFake) result(actor string, resource credential.Resource, operation string) (credential.ActivationStatus, error) {
	s.calls++
	s.actor = actor
	s.resource = resource
	s.operationID = operation
	return credential.ActivationStatus{OperationID: operation, VersionID: activationVersion, State: s.state, BindingRevision: 41, RuntimeReady: s.state == "completed", CreatedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 8, 12, 1, 0, 0, time.UTC)}, s.err
}
func (s *activationTransportFake) StartActivation(_ context.Context, actor string, resource credential.Resource, input credential.ActivationRequest) (credential.ActivationStatus, error) {
	s.input = input
	return s.result(actor, resource, input.OperationID)
}
func (s *activationTransportFake) GetActivation(_ context.Context, actor string, resource credential.Resource, operation string) (credential.ActivationStatus, error) {
	return s.result(actor, resource, operation)
}
func (s *activationTransportFake) RetryActivation(_ context.Context, actor string, resource credential.Resource, operation, receipt string) (credential.ActivationStatus, error) {
	s.receiptID = receipt
	return s.result(actor, resource, operation)
}
func (s *activationTransportFake) AbortActivation(_ context.Context, actor string, resource credential.Resource, operation string) (credential.ActivationStatus, error) {
	return s.result(actor, resource, operation)
}
