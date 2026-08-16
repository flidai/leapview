package module

import (
	"net/http"
)

func (m *Module) DispatchAPIGenOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	if m == nil {
		return false
	}
	switch operationID {
	case "getCurrentPrincipal":
		m.handler.GetCurrentPrincipal(w, r)
	case "updateCurrentPrincipal":
		m.handler.UpdateCurrentPrincipal(w, r)
	case "changeCurrentPassword":
		m.handler.ChangeCurrentPassword(w, r)
	case "updateCurrentTheme":
		m.handler.UpdateCurrentTheme(w, r)
	case "listCurrentAPITokens":
		m.handler.ListCurrentAPITokens(w, r)
	case "createCurrentAPIToken":
		m.handler.CreateCurrentAPIToken(w, r)
	case "revokeCurrentAPIToken":
		m.handler.RevokeCurrentAPIToken(w, r)
	case "listCurrentSessions":
		m.handler.ListCurrentSessions(w, r)
	case "revokeCurrentSession":
		m.handler.RevokeCurrentSession(w, r)
	case "listCurrentAuthoringSessions":
		m.handler.ListCurrentAuthoringSessions(w, r)
	case "revokeCurrentAuthoringSession":
		m.handler.RevokeCurrentAuthoringSession(w, r)
	case "decideDeviceAuthorization":
		m.handler.DecideDeviceAuthorization(w, r)
	case "deleteCurrentAvatar":
		m.handler.DeleteCurrentAvatar(w, r)
	case "uploadCurrentAvatar":
		m.handler.UploadCurrentAvatar(w, r, r.Header.Get("Content-Type"))
	case "listPrincipals":
		m.handler.ListPrincipals(w, r)
	case "createPrincipal":
		m.handler.CreatePrincipal(w, r)
	case "getPrincipal":
		m.handler.GetPrincipal(w, r)
	case "updatePrincipal":
		m.handler.UpdatePrincipal(w, r)
	case "deletePrincipal":
		m.handler.DeletePrincipal(w, r)
	case "disablePrincipal":
		m.handler.DisablePrincipal(w, r)
	case "enablePrincipal":
		m.handler.EnablePrincipal(w, r)
	case "resetPrincipalPassword":
		m.handler.ResetPrincipalPassword(w, r)
	case "listPrincipalSessions":
		m.handler.ListPrincipalSessions(w, r)
	case "revokePrincipalSession":
		m.handler.RevokePrincipalSession(w, r)
	case "listServicePrincipals":
		m.handler.ListServicePrincipals(w, r)
	case "createServicePrincipal":
		m.handler.CreateServicePrincipal(w, r)
	case "getServicePrincipal":
		m.handler.GetServicePrincipal(w, r)
	case "updateServicePrincipal":
		m.handler.UpdateServicePrincipal(w, r)
	case "deleteServicePrincipal":
		m.handler.DeleteServicePrincipal(w, r)
	case "listServicePrincipalSecrets":
		m.handler.ListServicePrincipalSecrets(w, r)
	case "createServicePrincipalSecret":
		m.handler.CreateServicePrincipalSecret(w, r)
	case "getServicePrincipalSecret":
		m.handler.GetServicePrincipalSecret(w, r)
	case "revokeServicePrincipalSecret":
		m.handler.RevokeServicePrincipalSecret(w, r)
	case "listGroups":
		m.handler.ListGroups(w, r)
	case "createGroup":
		m.handler.CreateGroup(w, r)
	case "getGroup":
		m.handler.GetGroup(w, r)
	case "updateGroup":
		m.handler.UpdateGroup(w, r)
	case "deleteGroup":
		m.handler.DeleteGroup(w, r)
	case "listGroupMembers":
		m.handler.ListGroupMembers(w, r)
	case "addGroupMember":
		m.handler.AddGroupMember(w, r)
	case "removeGroupMember":
		m.handler.RemoveGroupMember(w, r)
	case "listAuditEvents", "listPlatformAuditEvents":
		m.handler.ListAuditEvents(w, r)
	default:
		return false
	}
	return true
}
