package module

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/stretchr/testify/require"
)

const firstSourceTestPreparation = "0198f2c0-7c7a-7f00-8a11-000000000401"
const firstSourceTestVersion = "0198f2c0-7c7a-7f00-8a11-000000000402"
const firstSourceTestReceipt = "0198f2c0-7c7a-7f00-8a11-000000000403"
const firstSourceTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type firstSourceCommandFake struct {
	authorizeErr, mutationErr error
	calls                     int
	actor                     string
	resource                  ValidationResource
	request                   FirstSourcePreparationCommandRequest
	preparation, receipt      string
}

func (f *firstSourceCommandFake) Authorize(context.Context, string, ValidationResource) error {
	return f.authorizeErr
}
func (f *firstSourceCommandFake) Prepare(_ context.Context, actor string, resource ValidationResource, request FirstSourcePreparationCommandRequest) (FirstSourcePreparationCommandResult, error) {
	f.calls++
	f.actor, f.resource, f.request = actor, resource, request
	return FirstSourcePreparationCommandResult{PreparationID: request.PreparationID, IntentDigest: firstSourceTestDigest, PlanRequestDigest: firstSourceTestDigest, CreatedAt: time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)}, f.mutationErr
}
func (f *firstSourceCommandFake) Renew(_ context.Context, actor string, resource ValidationResource, preparation, receipt string) (FirstSourcePreparationCommandResult, error) {
	f.calls++
	f.actor, f.resource, f.preparation, f.receipt = actor, resource, preparation, receipt
	return FirstSourcePreparationCommandResult{PreparationID: preparation, IntentDigest: firstSourceTestDigest, PlanRequestDigest: firstSourceTestDigest, CreatedAt: time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)}, f.mutationErr
}

func firstSourceCommandBody() string {
	return `{"preparationId":"` + firstSourceTestPreparation + `","receiptId":"` + firstSourceTestReceipt + `","sourceDigest":"` + firstSourceTestDigest + `","sourceAttestationDigest":"` + firstSourceTestDigest + `","planIdempotencyKey":"retained-plan-key","expectedTargetRevision":3}`
}

func callFirstSourceBrowserCommand(t *testing.T, service *firstSourceCommandFake, operation, identity, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	config := CredentialDraftAPIGenConfig{FirstSourcePreparation: service, Environment: "prod", CurrentPrincipal: func(*http.Request) (string, bool) { return "real-actor", true }}
	require.True(t, DispatchFirstSourcePreparationBrowser(config, operation, w, r, "project:first", "lvinst_first", "connection:warehouse", identity))
	return w
}

func TestFirstSourcePreparationBrowserPropagatesExactIntentAndSafeMetadata(t *testing.T) {
	service := new(firstSourceCommandFake)
	w := callFirstSourceBrowserCommand(t, service, "prepareFirstSourceCredential", firstSourceTestVersion, firstSourceCommandBody())
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, 1, service.calls)
	require.Equal(t, "real-actor", service.actor)
	require.Equal(t, ValidationResource{ScopeKind: "connection", ProjectID: "project:first", TargetID: "lvinst_first", Environment: "prod", ResourceID: "connection:warehouse"}, service.resource)
	require.Equal(t, FirstSourcePreparationCommandRequest{PreparationID: firstSourceTestPreparation, VersionID: firstSourceTestVersion, ReceiptID: firstSourceTestReceipt, SourceDigest: firstSourceTestDigest, SourceAttestationDigest: firstSourceTestDigest, PlanIdempotencyKey: "retained-plan-key", ExpectedTargetRevision: 3}, service.request)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metadata))
	require.Len(t, metadata, 4)
	require.Equal(t, firstSourceTestPreparation, metadata["preparationId"])
	require.NotContains(t, w.Body.String(), firstSourceTestReceipt)
	w = callFirstSourceBrowserCommand(t, service, "renewFirstSourceCredentialPreparation", firstSourceTestPreparation, `{"receiptId":"`+firstSourceTestReceipt+`"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 2, service.calls)
	require.Equal(t, firstSourceTestPreparation, service.preparation)
	require.Equal(t, firstSourceTestReceipt, service.receipt)
}

func TestFirstSourcePreparationBrowserRejectsUnboundedOrForeignInput(t *testing.T) {
	for _, body := range []string{"", "null", "[]", firstSourceCommandBody() + "{}", strings.Replace(firstSourceCommandBody(), `"expectedTargetRevision":3`, `"expectedTargetRevision":0`, 1), strings.Replace(firstSourceCommandBody(), `"expectedTargetRevision":3`, `"expectedTargetRevision":3.5`, 1), strings.Replace(firstSourceCommandBody(), `"planIdempotencyKey":"retained-plan-key"`, `"planIdempotencyKey":""`, 1), strings.Replace(firstSourceCommandBody(), `"planIdempotencyKey":"retained-plan-key"`, `"planIdempotencyKey":"\uD800"`, 1), strings.Replace(firstSourceCommandBody(), `"preparationId":`, `"PreparationId":`, 1), strings.Replace(firstSourceCommandBody(), `"expectedTargetRevision":3`, `"expectedTargetRevision":3,"publisherId":"other"`, 1), strings.Replace(firstSourceCommandBody(), `"expectedTargetRevision":3`, `"expectedTargetRevision":3,"sourceOwnerId":"other"`, 1), strings.Replace(firstSourceCommandBody(), `"expectedTargetRevision":3`, `"expectedTargetRevision":3,"expectedTargetRevision":3`, 1)} {
		service := new(firstSourceCommandFake)
		w := callFirstSourceBrowserCommand(t, service, "prepareFirstSourceCredential", firstSourceTestVersion, body)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Zero(t, service.calls)
	}
	service := new(firstSourceCommandFake)
	w := callFirstSourceBrowserCommand(t, service, "prepareFirstSourceCredential", firstSourceTestVersion, strings.Repeat("x", int(maxFirstSourcePreparationBodyBytes+1)))
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Zero(t, service.calls)
}

func TestFirstSourcePreparationCommandsPreserveGeneratedAuditAndExposure(t *testing.T) {
	for operation, action := range map[string]string{"prepareFirstSourceCredential": "credential.first_source.prepared", "renewFirstSourceCredentialPreparation": "credential.first_source.receipt_renewed"} {
		contract, found := credentialgen.GetAPIGenOperationContracts()[operation]
		require.True(t, found)
		require.Equal(t, "connection.manage", contract.Authz.Action)
		require.Equal(t, "connection", contract.Authz.Resolver)
		require.Equal(t, "forbidden", contract.Command.Idempotency)
		require.Equal(t, "transactional", contract.Command.Audit.Guarantee)
		require.Equal(t, action, contract.Command.Audit.SuccessAction)
		require.Equal(t, []credentialgen.GenOperationSurface{credentialgen.GenOperationSurfaceUI}, contract.Command.AdditionalExposures)
	}
}

func TestFirstSourcePreparationFailureNeverReflectsProviderDetails(t *testing.T) {
	service := &firstSourceCommandFake{mutationErr: errors.New("private-first-source-password upstream host")}
	w := callFirstSourceBrowserCommand(t, service, "prepareFirstSourceCredential", firstSourceTestVersion, firstSourceCommandBody())
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "private-first-source-password")
}

type firstSourceTransportReadSpy struct{ reads int }

func (b *firstSourceTransportReadSpy) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*firstSourceTransportReadSpy) Close() error               { return nil }

func TestFirstSourcePreparationAuthorizationAndHeadersPrecedeBody(t *testing.T) {
	for _, scenario := range []string{"denied", "missing-principal", "missing-service", "idempotency-empty", "idempotency-repeated"} {
		t.Run(scenario, func(t *testing.T) {
			service := new(firstSourceCommandFake)
			config := CredentialDraftAPIGenConfig{FirstSourcePreparation: service, Environment: "prod", CurrentPrincipal: func(*http.Request) (string, bool) { return "real-actor", true }}
			r := httptest.NewRequest(http.MethodPost, "/command", nil)
			spy := new(firstSourceTransportReadSpy)
			r.Body = spy
			want := http.StatusBadRequest
			switch scenario {
			case "denied":
				service.authorizeErr = ErrValidationForbidden
				want = http.StatusForbidden
			case "missing-principal":
				config.CurrentPrincipal = nil
				want = http.StatusUnauthorized
			case "missing-service":
				config.FirstSourcePreparation = nil
				want = http.StatusServiceUnavailable
			case "idempotency-empty":
				r.Header["Idempotency-Key"] = []string{""}
			case "idempotency-repeated":
				r.Header["Idempotency-Key"] = []string{"", "do-not-reflect-private-key"}
			}
			w := httptest.NewRecorder()
			require.True(t, DispatchFirstSourcePreparationBrowser(config, "prepareFirstSourceCredential", w, r, "project:first", "lvinst_first", "connection:warehouse", firstSourceTestVersion))
			require.Equal(t, want, w.Code)
			require.Zero(t, spy.reads)
			require.Zero(t, service.calls)
			require.NotContains(t, w.Body.String(), "do-not-reflect-private-key")
		})
	}
}

func TestFirstSourcePreparationAPIAndBrowserUseSameServiceContract(t *testing.T) {
	service := new(firstSourceCommandFake)
	config := CredentialDraftAPIGenConfig{FirstSourcePreparation: service, Environment: "prod", CurrentPrincipal: func(*http.Request) (string, bool) { return "real-actor", true }}
	r := withCredentialDraftRouteParams(httptest.NewRequest(http.MethodPost, "/api/v1/prepare", strings.NewReader(firstSourceCommandBody())), "project:first", "lvinst_first", "connection:warehouse", firstSourceTestVersion)
	w := httptest.NewRecorder()
	require.True(t, DispatchAPIGenOperation(config, "prepareFirstSourceCredential", nil, w, r))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, service.calls)
	request := service.request
	w = callFirstSourceBrowserCommand(t, service, "prepareFirstSourceCredential", firstSourceTestVersion, firstSourceCommandBody())
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, request, service.request)
}
