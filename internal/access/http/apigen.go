package http

import (
	stdhttp "net/http"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

// APIGenDispatcher contains only identity, credential, group, audit, avatar,
// and authoring operations. Project authorization endpoints are owned by the
// immutable serving-state authorization surface.
type APIGenDispatcher struct{ handler Handler }

func NewAPIGenDispatcher(handler Handler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) GetCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.GetCurrentPrincipal(w, r)
}
func (d *APIGenDispatcher) ListCurrentEffectiveCapabilities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.ListCurrentEffectiveCapabilities(w, r)
}
func (d *APIGenDispatcher) UpdateCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenUpdateCurrentPrincipalHeaders) {
	d.handler.UpdateCurrentPrincipal(w, r)
}
func (d *APIGenDispatcher) ChangeCurrentPassword(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenChangeCurrentPasswordHeaders) {
	d.handler.ChangeCurrentPassword(w, r)
}
func (d *APIGenDispatcher) UpdateCurrentTheme(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.UpdateCurrentTheme(w, r)
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
func (d *APIGenDispatcher) ListCurrentSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentSessionsParams) {
	d.handler.ListCurrentSessions(w, r)
}
func (d *APIGenDispatcher) RevokeCurrentSession(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.RevokeCurrentSession(w, r)
}
func (d *APIGenDispatcher) ListPrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListPrincipalsParams) {
	d.handler.ListPrincipals(w, r)
}
func (d *APIGenDispatcher) CreatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreatePrincipalHeaders) {
	d.handler.CreatePrincipal(w, r)
}
func (d *APIGenDispatcher) GetPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetPrincipal(w, r)
}
func (d *APIGenDispatcher) DeletePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.DeletePrincipal(w, r)
}
func (d *APIGenDispatcher) UpdatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenUpdatePrincipalHeaders) {
	d.handler.UpdatePrincipal(w, r)
}
func (d *APIGenDispatcher) ResetPrincipalPassword(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenResetPrincipalPasswordHeaders) {
	d.handler.ResetPrincipalPassword(w, r)
}
func (d *APIGenDispatcher) DisablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenDisablePrincipalHeaders) {
	d.handler.DisablePrincipal(w, r)
}
func (d *APIGenDispatcher) EnablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenEnablePrincipalHeaders) {
	d.handler.EnablePrincipal(w, r)
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
func (d *APIGenDispatcher) GetServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetServicePrincipal(w, r)
}
func (d *APIGenDispatcher) UpdateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenUpdateServicePrincipalHeaders) {
	d.handler.UpdateServicePrincipal(w, r)
}
func (d *APIGenDispatcher) DeleteServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.DeleteServicePrincipal(w, r)
}
func (d *APIGenDispatcher) CreateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateServicePrincipalSecretHeaders) {
	d.handler.CreateServicePrincipalSecret(w, r)
}
func (d *APIGenDispatcher) ListServicePrincipalSecrets(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListServicePrincipalSecretsParams) {
	d.handler.ListServicePrincipalSecrets(w, r)
}
func (d *APIGenDispatcher) GetServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetServicePrincipalSecret(w, r)
}
func (d *APIGenDispatcher) RevokeServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.RevokeServicePrincipalSecret(w, r)
}
func (d *APIGenDispatcher) ListGroups(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListGroupsParams) {
	d.handler.ListGroups(w, r)
}
func (d *APIGenDispatcher) CreateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreateGroupHeaders) {
	d.handler.CreateGroup(w, r)
}
func (d *APIGenDispatcher) GetGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetGroup(w, r)
}
func (d *APIGenDispatcher) UpdateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ accessgen.GenUpdateGroupHeaders) {
	d.handler.UpdateGroup(w, r)
}
func (d *APIGenDispatcher) DeleteGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.DeleteGroup(w, r)
}
func (d *APIGenDispatcher) ListGroupMembers(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ accessgen.GenListGroupMembersParams) {
	d.handler.ListGroupMembers(w, r)
}
func (d *APIGenDispatcher) AddGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.AddGroupMember(w, r)
}
func (d *APIGenDispatcher) RemoveGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.RemoveGroupMember(w, r)
}
func (d *APIGenDispatcher) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ accessgen.GenListAuditEventsParams) {
	d.handler.ListAuditEvents(w, r)
}
func (d *APIGenDispatcher) ListPlatformAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListPlatformAuditEventsParams) {
	d.handler.ListPlatformAuditEvents(w, r)
}
