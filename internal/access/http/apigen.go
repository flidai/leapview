package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

// APIGenDispatcher adapts Access's HTTP handler to its generated transport
// contract. It lives with the capability transport instead of application
// composition.
type APIGenDispatcher struct {
	handler Handler
}

func NewAPIGenDispatcher(handler Handler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

type APIGenTransportErrorResponder struct {
	Logger *slog.Logger
}

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure accessgen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func (d *APIGenDispatcher) GetCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.GetCurrentPrincipal(w, r)
}

func (d *APIGenDispatcher) UpdateCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, headers accessgen.GenUpdateCurrentPrincipalHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateCurrentPrincipal(w, r)
}

func (d *APIGenDispatcher) ChangeCurrentPassword(w stdhttp.ResponseWriter, r *stdhttp.Request, headers accessgen.GenChangeCurrentPasswordHeaders) {
	r.Header.Set("Idempotency-Key", headers.IdempotencyKey)
	d.handler.ChangeCurrentPassword(w, r)
}

func (d *APIGenDispatcher) ListPlatformAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListPlatformAuditEventsParams) {
	d.handler.ListPlatformAuditEvents(w, r)
}

func (d *APIGenDispatcher) DecideDeviceAuthorization(w stdhttp.ResponseWriter, r *stdhttp.Request, headers accessgen.GenDecideDeviceAuthorizationHeaders) {
	r.Header.Set("Idempotency-Key", headers.IdempotencyKey)
	d.handler.DecideDeviceAuthorization(w, r)
}

func (d *APIGenDispatcher) ListCurrentAuthoringSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentAuthoringSessionsParams) {
	d.handler.ListCurrentAuthoringSessions(w, r)
}

func (d *APIGenDispatcher) RevokeCurrentAuthoringSession(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.RevokeCurrentAuthoringSession(w, r)
}

func (d *APIGenDispatcher) ListCurrentAPITokens(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentAPITokensParams) {
	d.handler.ListCurrentAPITokens(w, r)
}

func (d *APIGenDispatcher) CreateCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreateCurrentAPITokenHeaders) {
	d.handler.CreateCurrentAPIToken(w, r)
}

func (d *APIGenDispatcher) RevokeCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.RevokeCurrentAPIToken(w, r)
}

func (d *APIGenDispatcher) ListCurrentEffectivePrivileges(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentEffectivePrivilegesParams) {
	d.handler.ListCurrentEffectivePrivileges(w, r)
}

func (d *APIGenDispatcher) ListCurrentSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentSessionsParams) {
	d.handler.ListCurrentSessions(w, r)
}

func (d *APIGenDispatcher) RevokeCurrentSession(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.RevokeCurrentSession(w, r)
}

func (d *APIGenDispatcher) UploadCurrentAvatar(w stdhttp.ResponseWriter, r *stdhttp.Request, headers accessgen.GenUploadCurrentAvatarHeaders) {
	d.handler.UploadCurrentAvatar(w, r, headers.ContentType)
}

func (d *APIGenDispatcher) DeleteCurrentAvatar(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.DeleteCurrentAvatar(w, r)
}

func (d *APIGenDispatcher) GetPrincipalAvatar(w stdhttp.ResponseWriter, r *stdhttp.Request, principal, digest string) {
	d.handler.GetPrincipalAvatar(w, r, principal, digest)
}

func (d *APIGenDispatcher) DisablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenDisablePrincipalHeaders) {
	d.handler.DisablePrincipal(w, r)
}

func (d *APIGenDispatcher) EnablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenEnablePrincipalHeaders) {
	d.handler.EnablePrincipal(w, r)
}

func (d *APIGenDispatcher) ListPrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListPrincipalsParams) {
	d.handler.ListPrincipals(w, r)
}

func (d *APIGenDispatcher) CreatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreatePrincipalHeaders) {
	d.handler.CreatePrincipal(w, r)
}

func (d *APIGenDispatcher) DeletePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.DeletePrincipal(w, r)
}

func (d *APIGenDispatcher) GetPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetPrincipal(w, r)
}

func (d *APIGenDispatcher) UpdatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenUpdatePrincipalHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdatePrincipal(w, r)
}

func (d *APIGenDispatcher) ResetPrincipalPassword(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenResetPrincipalPasswordHeaders) {
	d.handler.ResetPrincipalPassword(w, r)
}

func (d *APIGenDispatcher) ListPrincipalSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListPrincipalSessionsParams) {
	d.handler.ListPrincipalSessions(w, r)
}

func (d *APIGenDispatcher) RevokePrincipalSession(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.RevokePrincipalSession(w, r)
}

func (d *APIGenDispatcher) ListServicePrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListServicePrincipalsParams) {
	d.handler.ListServicePrincipals(w, r)
}

func (d *APIGenDispatcher) CreateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreateServicePrincipalHeaders) {
	d.handler.CreateServicePrincipal(w, r)
}

func (d *APIGenDispatcher) DeleteServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.DeleteServicePrincipal(w, r)
}

func (d *APIGenDispatcher) GetServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetServicePrincipal(w, r)
}

func (d *APIGenDispatcher) UpdateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenUpdateServicePrincipalHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateServicePrincipal(w, r)
}

func (d *APIGenDispatcher) ListServicePrincipalSecrets(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListServicePrincipalSecretsParams) {
	d.handler.ListServicePrincipalSecrets(w, r)
}

func (d *APIGenDispatcher) CreateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateServicePrincipalSecretHeaders) {
	d.handler.CreateServicePrincipalSecret(w, r)
}

func (d *APIGenDispatcher) RevokeServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.RevokeServicePrincipalSecret(w, r)
}

func (d *APIGenDispatcher) GetServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetServicePrincipalSecret(w, r)
}

func (d *APIGenDispatcher) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListAuditEventsParams) {
	d.handler.ListAuditEvents(w, r)
}

func (d *APIGenDispatcher) CheckAuthorizationBatch(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.CheckAuthorizationBatch(w, r)
}

func (d *APIGenDispatcher) ListDataPolicies(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListDataPoliciesParams) {
	d.handler.ListDataPolicies(w, r)
}

func (d *APIGenDispatcher) CreateDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateDataPolicyHeaders) {
	d.handler.CreateDataPolicy(w, r)
}

func (d *APIGenDispatcher) DeleteDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.DeleteDataPolicy(w, r)
}

func (d *APIGenDispatcher) GetDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetDataPolicy(w, r)
}

func (d *APIGenDispatcher) UpdateDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenUpdateDataPolicyHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateDataPolicy(w, r)
}

func (d *APIGenDispatcher) ListEffectivePrivileges(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListEffectivePrivilegesParams) {
	d.handler.ListEffectivePrivileges(w, r)
}

func (d *APIGenDispatcher) ListGrants(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListGrantsParams) {
	d.handler.ListGrants(w, r)
}

func (d *APIGenDispatcher) CreateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateGrantHeaders) {
	d.handler.CreateGrant(w, r)
}

func (d *APIGenDispatcher) DeleteGrant(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.DeleteGrant(w, r)
}

func (d *APIGenDispatcher) GetGrant(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetGrant(w, r)
}

func (d *APIGenDispatcher) UpdateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenUpdateGrantHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateGrant(w, r)
}

func (d *APIGenDispatcher) ListGroups(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListGroupsParams) {
	d.handler.ListGroups(w, r)
}

func (d *APIGenDispatcher) CreateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateGroupHeaders) {
	d.handler.CreateGroup(w, r)
}

func (d *APIGenDispatcher) DeleteGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.DeleteGroup(w, r)
}

func (d *APIGenDispatcher) GetGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetGroup(w, r)
}

func (d *APIGenDispatcher) UpdateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenUpdateGroupHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateGroup(w, r)
}

func (d *APIGenDispatcher) ListGroupMembers(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ accessgen.GenListGroupMembersParams) {
	d.handler.ListGroupMembers(w, r)
}

func (d *APIGenDispatcher) RemoveGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.RemoveGroupMember(w, r)
}

func (d *APIGenDispatcher) AddGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.AddGroupMember(w, r)
}

func (d *APIGenDispatcher) TransferOwnership(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenTransferOwnershipHeaders) {
	d.handler.TransferOwnership(w, r)
}

func (d *APIGenDispatcher) ListRoleBindings(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListRoleBindingsParams) {
	d.handler.ListRoleBindings(w, r)
}

func (d *APIGenDispatcher) CreateRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateRoleBindingHeaders) {
	d.handler.CreateRoleBinding(w, r)
}

func (d *APIGenDispatcher) DeleteRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.DeleteRoleBinding(w, r)
}

func (d *APIGenDispatcher) GetRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetRoleBinding(w, r)
}

func (d *APIGenDispatcher) UpdateRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenUpdateRoleBindingHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateRoleBinding(w, r)
}

func (d *APIGenDispatcher) ListWorkspaceRoles(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListWorkspaceRolesParams) {
	d.handler.ListWorkspaceRoles(w, r)
}
