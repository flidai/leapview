package module

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
)

type nativeApprovalRouteStub struct {
	requestErr, getErr, approveErr, denyErr, revokeErr error
}

func (s nativeApprovalRouteStub) RequestPublicationApproval(context.Context, NativeApprovalRequest) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, s.requestErr
}
func (s nativeApprovalRouteStub) GetPublicationApproval(context.Context, NativeApprovalLookup) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, s.getErr
}
func (s nativeApprovalRouteStub) ApprovePublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, s.approveErr
}
func (s nativeApprovalRouteStub) DenyPublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, s.denyErr
}
func (s nativeApprovalRouteStub) RevokePublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, s.revokeErr
}

func nativeApprovalRouteModule(stub NativeDeliveryApprovalPort, principal bool) *Module {
	now := time.Now().UTC()
	return &Module{
		nativeDeliveryApproval: stub,
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, principal
			},
		}),
		currentApprovalActor: func(*http.Request) (deployment.ApprovalActor, bool) {
			return deployment.ApprovalActor{PrincipalID: "operator", CredentialClass: deployment.CredentialClassHuman, CredentialID: "credential", CredentialExpiresAt: now.Add(time.Hour)}, true
		},
	}
}

var nativeApprovalPublicationID = uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000001")

func TestNativeApprovalRequestErrorsUseDeclaredContracts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "not found", err: nativepostgres.ErrApprovalNotFound, code: "DEPLOYMENT_APPROVAL_NOT_FOUND"},
		{name: "conflict", err: nativepostgres.ErrApprovalConflict, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "invalid", err: nativepostgres.ErrApprovalInvalid, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "unauthorized", err: nativepostgres.ErrApprovalUnauthorized, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "separation", err: nativepostgres.ErrApprovalSeparationOfDuty, code: "SEPARATION_OF_DUTY_REQUIRED"},
		{name: "expired", err: nativepostgres.ErrApprovalExpired, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "unknown", err: errors.New("database unavailable"), code: "DEPLOYMENT_APPROVAL_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := nativeApprovalRouteModule(nativeApprovalRouteStub{requestErr: test.err}, true)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			m.RequestDeliveryPublicationApproval(response, request, "finance", nativeApprovalPublicationID.String(), "request-1")
			if response.Code == http.StatusInternalServerError || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("response = %d %s, want declared %s", response.Code, response.Body.String(), test.code)
			}
		})
	}
}

func TestNativeApprovalDecisionErrorsUseDeclaredContracts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "not found", err: nativepostgres.ErrApprovalNotFound, code: "DEPLOYMENT_APPROVAL_NOT_FOUND"},
		{name: "conflict", err: nativepostgres.ErrApprovalConflict, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "invalid", err: nativepostgres.ErrApprovalInvalid, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "unauthorized", err: nativepostgres.ErrApprovalUnauthorized, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "separation", err: nativepostgres.ErrApprovalSeparationOfDuty, code: "SEPARATION_OF_DUTY_REQUIRED"},
		{name: "expired", err: nativepostgres.ErrApprovalExpired, code: "DEPLOYMENT_APPROVAL_CONFLICT"},
		{name: "unknown", err: errors.New("database unavailable"), code: "DEPLOYMENT_APPROVAL_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := nativeApprovalRouteModule(nativeApprovalRouteStub{approveErr: test.err}, true)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"expectedRevision":0}`))
			m.ApproveDeliveryPublicationApproval(response, request, "finance", nativeApprovalPublicationID.String(), "0198f2c0-7c7a-7f00-8a11-000000000002", "decision-1")
			if response.Code == http.StatusInternalServerError || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("response = %d %s, want declared %s", response.Code, response.Body.String(), test.code)
			}
		})
	}
}

func TestNativeApprovalGetErrorsHideInvalidScope(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "not found", err: nativepostgres.ErrApprovalNotFound, code: "DEPLOYMENT_APPROVAL_NOT_FOUND"},
		{name: "invalid", err: nativepostgres.ErrApprovalInvalid, code: "DEPLOYMENT_APPROVAL_NOT_FOUND"},
		{name: "unauthorized", err: nativepostgres.ErrApprovalUnauthorized, code: "DEPLOYMENT_APPROVAL_NOT_FOUND"},
		{name: "unknown", err: errors.New("database unavailable"), code: "DEPLOYMENT_APPROVAL_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := nativeApprovalRouteModule(nativeApprovalRouteStub{getErr: test.err}, true)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			m.GetDeliveryPublicationApproval(response, request, "finance", nativeApprovalPublicationID.String(), "0198f2c0-7c7a-7f00-0000-000000000001")
			if response.Code == http.StatusInternalServerError || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("response = %d %s, want declared %s", response.Code, response.Body.String(), test.code)
			}
		})
	}
}

func TestNativeApprovalRouteRejectsUnauthenticatedRequest(t *testing.T) {
	m := nativeApprovalRouteModule(nativeApprovalRouteStub{requestErr: nativepostgres.ErrApprovalConflict}, false)
	response := httptest.NewRecorder()
	m.RequestDeliveryPublicationApproval(response, httptest.NewRequest(http.MethodPost, "/", nil), "finance", nativeApprovalPublicationID.String(), "request-1")
	if response.Code == http.StatusInternalServerError {
		t.Fatalf("unauthenticated response leaked as 500: %s", response.Body.String())
	}
}

func TestNativeApprovalMapperPreservesSentinelIdentity(t *testing.T) {
	if !errors.Is(mapNativeApprovalError(nativepostgres.ErrApprovalConflict), deployment.ErrApprovalConflict) {
		t.Fatal("native conflict was not projected to deployment conflict")
	}
	if !errors.Is(mapNativeApprovalReadError(nativepostgres.ErrApprovalInvalid), deployment.ErrApprovalNotFound) {
		t.Fatal("native invalid read was not projected to not-found scope")
	}
}
