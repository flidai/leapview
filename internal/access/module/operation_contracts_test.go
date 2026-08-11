package module

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

func TestGeneratedAccessMutationContractsAreComplete(t *testing.T) {
	expected := map[string]struct {
		audit  string
		target string
		ui     bool
	}{
		"decideDeviceAuthorization":     {audit: "authoring.device.decided"},
		"revokeCurrentAuthoringSession": {audit: "authoring.session.revoked", target: "session"},
		"createPrincipal":               {audit: "principal.local_user.created"},
		"updatePrincipal":               {audit: "principal.updated", target: "principal"},
		"deletePrincipal":               {audit: "principal.deleted", target: "principal"},
		"resetPrincipalPassword":        {audit: "principal.local_password.reset", target: "principal"},
		"revokePrincipalSession":        {audit: "session.revoked", target: "principal"},
		"createServicePrincipal":        {audit: "service_principal.created"},
		"updateServicePrincipal":        {audit: "service_principal.updated", target: "servicePrincipal"},
		"deleteServicePrincipal":        {audit: "service_principal.deleted", target: "servicePrincipal"},
		"createServicePrincipalSecret":  {audit: "service_principal_secret.created", target: "servicePrincipal"},
		"revokeServicePrincipalSecret":  {audit: "service_principal_secret.revoked", target: "servicePrincipal"},
		"createGroup":                   {audit: "group.created", target: "workspace"},
		"updateGroup":                   {audit: "group.updated", target: "workspace"},
		"deleteGroup":                   {audit: "group.deleted", target: "workspace"},
		"addGroupMember":                {audit: "group.member_added", target: "workspace"},
		"removeGroupMember":             {audit: "group.member_removed", target: "workspace"},
		"createRoleBinding":             {audit: "role_binding.created", target: "workspace", ui: true},
		"updateRoleBinding":             {audit: "role_binding.updated", target: "workspace", ui: true},
		"deleteRoleBinding":             {audit: "role_binding.deleted", target: "workspace", ui: true},
		"createGrant":                   {audit: "grant.created", target: "workspace", ui: true},
		"updateGrant":                   {audit: "grant.updated", target: "workspace"},
		"deleteGrant":                   {audit: "grant.deleted", target: "workspace", ui: true},
		"transferOwnership":             {audit: "ownership.transferred", target: "workspace"},
		"createDataPolicy":              {audit: "data_policy.created", target: "workspace"},
		"updateDataPolicy":              {audit: "data_policy.updated", target: "workspace"},
		"deleteDataPolicy":              {audit: "data_policy.deleted", target: "workspace"},
		"createCurrentAPIToken":         {audit: "api_token.created"},
		"revokeCurrentAPIToken":         {audit: "api_token.revoked", target: "token"},
		"revokeCurrentSession":          {audit: "session.revoked", target: "session"},
	}

	contracts := accessgen.GetAPIGenOperationContracts()
	commands := 0
	for operationID, contract := range contracts {
		if contract.Namespace != "LeapViewAPI.Access" || contract.Command == nil {
			continue
		}
		commands++
		want, ok := expected[operationID]
		if !ok {
			t.Errorf("unexpected Access command %q", operationID)
			continue
		}
		command := contract.Command
		if !command.Audit.Required || command.Audit.SuccessAction != want.audit || command.Audit.Guarantee != "transactional" {
			t.Errorf("%s audit = %#v, want required transactional action %q", operationID, command.Audit, want.audit)
		}
		if command.Audit.Payload == nil || command.Audit.Payload.Schema == "" || command.Audit.Payload.SchemaVersion != 1 || command.Audit.Payload.Retention != "security" {
			t.Errorf("%s audit payload = %#v, want versioned security payload", operationID, command.Audit.Payload)
		}
		gotTarget := ""
		if command.Target != nil {
			gotTarget = command.Target.Parameter
		}
		if gotTarget != want.target {
			t.Errorf("%s target parameter = %q, want %q", operationID, gotTarget, want.target)
		}
		gotUI := false
		for _, exposure := range command.AdditionalExposures {
			if exposure == accessgen.GenOperationSurfaceUI {
				gotUI = true
			}
		}
		if gotUI != want.ui {
			t.Errorf("%s UI exposure = %t, want %t", operationID, gotUI, want.ui)
		}
	}
	if commands != len(expected) {
		t.Fatalf("Access command count = %d, want %d", commands, len(expected))
	}
	if contract, ok := contracts["checkAuthorizationBatch"]; !ok {
		t.Fatal("checkAuthorizationBatch contract is missing")
	} else if contract.Command != nil {
		t.Fatal("checkAuthorizationBatch is an authorization query, not a command")
	}
}

func TestGeneratedRoleBindingCatalogDrivesApplicationDescriptors(t *testing.T) {
	catalog, err := generatedRoleBindingCatalog()
	if err != nil {
		t.Fatalf("build generated catalog: %v", err)
	}
	expectedAudit := map[access.OperationID]string{
		access.OperationCreateRoleBinding: "role_binding.created",
		access.OperationUpdateRoleBinding: "role_binding.updated",
		access.OperationDeleteRoleBinding: "role_binding.deleted",
	}
	for operationID, auditEvent := range expectedAudit {
		descriptor, ok := catalog.DescribeOperation(operationID)
		if !ok {
			t.Fatalf("generated operation %q is missing", operationID)
		}
		if descriptor.Owner != "LeapViewAPI.Access" || descriptor.Privilege != access.PrivilegeManageGrants || descriptor.AuditEvent != auditEvent {
			t.Errorf("generated operation %q descriptor = %#v", operationID, descriptor)
		}
		if descriptor.Target.Type != access.SecurableWorkspace || descriptor.Target.Parameter != "workspace" {
			t.Errorf("generated operation %q target = %#v", operationID, descriptor.Target)
		}
		for _, surface := range []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceCLI, access.OperationSurfaceUI} {
			if !descriptor.Exposes(surface) {
				t.Errorf("generated operation %q does not expose %q", operationID, surface)
			}
		}
	}
}
