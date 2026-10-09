package app

import (
	"context"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
)

// The access boundary admits exact claimed-project profile applications before
// the first generation exists. Preserve that request-local authority through
// the profile service's nested binding operations; ordinary binding commands
// and production instances continue to require active-generation authorization.
func developmentProfileBootstrapAuthority(ctx context.Context, production bool) (accessmodule.BootstrapAuthorization, bool) {
	marker, marked := accessmodule.BootstrapAuthorizationFromContext(ctx)
	operation, started := apigencommand.OperationID(ctx)
	return marker, marked && started && developmentProfileBootstrapOperationMatches(production, operation, marker)
}

func developmentProfileBootstrapOperationMatches(production bool, operation string, marker accessmodule.BootstrapAuthorization) bool {
	return !production && operation == "applyDevelopmentProfile" && marker.Capability == access.CapabilityResourceManage &&
		marker.PrincipalID != "" && marker.ProjectID.Validate() == nil
}

func developmentProfileBootstrapBindingMatches(marker accessmodule.BootstrapAuthorization, actor string, permission analyticsmodule.ConnectionAdministrationPermission, binding analyticsmodule.ConnectionTargetBinding, target, environment string) bool {
	return actor != "" && marker.PrincipalID == actor && marker.ProjectID == binding.Scope.ProjectID &&
		marker.Capability == access.CapabilityResourceManage && marker.ProjectID.Validate() == nil &&
		target != "" && binding.TargetID.String() == target && environment != "" && binding.Scope.Environment == environment &&
		(permission == analyticsmodule.PermissionManageConnectionMetadata || permission == analyticsmodule.PermissionUseConnection)
}

func developmentProfileBootstrapCredentialAllows(credential access.APICredential, actor string, permission analyticsmodule.ConnectionAdministrationPermission, binding analyticsmodule.ConnectionTargetBinding, target string) bool {
	if permission == analyticsmodule.PermissionUseConnection {
		return bootstrapBindingCredentialAllows(credential, actor, binding, target, access.ActionConnectionUse)
	}
	if permission != analyticsmodule.PermissionManageConnectionMetadata || actor == "" || credential.Token.ID == "" ||
		credential.Principal.ID != actor || credential.Token.PrincipalID != actor || binding.ConnectionID.Validate() != nil {
		return false
	}
	// The admitted, digest-pinned profile command owns metadata installation
	// under its project.settings.update contract. This does not grant an
	// ordinary connection.manage action; probes still require exact use above.
	pair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsUpdate, binding.Scope.ProjectID)
	if err != nil {
		return false
	}
	if credential.Authoring != nil {
		return credential.Authoring.Scope.AuthorizePairs(target, binding.Scope.ProjectID.String(), []access.PermissionPair{pair}) == nil
	}
	return credential.Token.PermissionProfile == access.PermissionCatalogProfile &&
		access.ValidatePermissionPairs(credential.Token.Permissions) == nil && access.PermissionSetAllows(credential.Token.Permissions, pair)
}
