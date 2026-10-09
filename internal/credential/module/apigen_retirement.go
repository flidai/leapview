package module

import (
	"context"
	"net/http"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

func (d credentialDraftAPIGenDispatcher) GetCredentialVersionStatus(w http.ResponseWriter, r *http.Request, project, target, connection, version string) {
	d.versionStatus(w, r, d.resource(project, target, connection), version)
}
func (d credentialDraftAPIGenDispatcher) GetAgentCredentialVersionStatus(w http.ResponseWriter, r *http.Request, version string) {
	d.versionStatus(w, r, credential.Resource{ScopeKind: "agent", ResourceID: d.config.InstanceID}, version)
}
func (d credentialDraftAPIGenDispatcher) RetireCredentialVersion(w http.ResponseWriter, r *http.Request, project, target, connection, version string) {
	d.retireVersion(w, r, d.resource(project, target, connection), version, credentialgen.GenCommandOperationRetireCredentialVersion())
}
func (d credentialDraftAPIGenDispatcher) RetireAgentCredentialVersion(w http.ResponseWriter, r *http.Request, version string) {
	d.retireVersion(w, r, credential.Resource{ScopeKind: "agent", ResourceID: d.config.InstanceID}, version, credentialgen.GenCommandOperationRetireAgentCredentialVersion())
}
func (d credentialDraftAPIGenDispatcher) versionStatus(w http.ResponseWriter, r *http.Request, resource credential.Resource, version string) {
	actor, ok := d.activationPrincipal(w, r)
	if !ok {
		return
	}
	service, ok := d.config.Activation.(credential.RetirementAuthority)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "CREDENTIAL_SERVICE_UNAVAILABLE", "Credential version inspection is unavailable.", nil)
		return
	}
	status, err := service.InspectVersion(r.Context(), actor, resource, version)
	if err != nil {
		code, name := credentialActivationFailure(err)
		apitransport.WriteProblem(w, r, code, name, "Credential version inspection failed.", nil)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, credentialVersionResponse(status))
}
func (d credentialDraftAPIGenDispatcher) retireVersion(w http.ResponseWriter, r *http.Request, resource credential.Resource, version string, operationID credentialgen.GenCommandOperationID) {
	operation := operationID.APIGenOperationID()
	actor, ok := d.activationPrincipal(w, r)
	if !ok {
		return
	}
	body, ok := decodeCredentialActivationObject(w, r)
	if !ok {
		return
	}
	if len(body) != 0 || !credentialActivationUUID(version) {
		writeCredentialDraftInvalidBody(w, r)
		return
	}
	service, ok := d.config.Activation.(credential.RetirementAuthority)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "CREDENTIAL_SERVICE_UNAVAILABLE", "Credential retirement is unavailable.", nil)
		return
	}
	contract, ok := credentialgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok {
		writeCredentialDraftInvalidBody(w, r)
		return
	}
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		writeCredentialDraftInvalidBody(w, r)
		return
	}
	targets := map[string]string{}
	if resource.ScopeKind == "connection" {
		targets["connection"] = resource.ResourceID
	}
	var status credential.VersionStatus
	err = apigencommand.ExecuteInvocation(r.Context(), executor, contract, apigencommand.Invocation{OperationID: operation, Surface: apigencommand.SurfaceAPI, TargetValues: targets, RequestID: r.Header.Get("X-Request-ID"), CorrelationID: r.Header.Get("X-Correlation-ID")}, apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
		var err error
		status, err = service.RetireVersion(ctx, actor, resource, version)
		return err
	}})
	if err != nil {
		kind := "provider_unavailable"
		switch code, _ := credentialActivationFailure(err); code {
		case 400:
			kind = "invalid"
		case 403:
			kind = "unauthorized"
		case 404:
			kind = "not_found"
		case 409:
			kind = "conflict"
		}
		apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, credentialgen.GetAPIGenCommandFailureContracts, apigenfailure.New(kind, "credential retirement failed"))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, credentialVersionResponse(status))
}
func credentialVersionResponse(status credential.VersionStatus) credentialgen.CredentialVersionStatusResponse {
	response := credentialgen.CredentialVersionStatusResponse{VersionId: status.VersionID, State: status.State, Dependencies: []credentialgen.CredentialVersionDependencyResponse{}, MoreDependencies: status.MoreDependencies}
	for _, dependency := range status.Dependencies {
		response.Dependencies = append(response.Dependencies, credentialgen.CredentialVersionDependencyResponse{Kind: dependency.Kind, Id: dependency.ID})
	}
	if !status.RetiredAt.IsZero() {
		value := status.RetiredAt.UTC().Format(time.RFC3339Nano)
		response.RetiredAt = &value
	}
	return response
}
