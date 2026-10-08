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
	return marker, !production && marked && started && operation == "applyDevelopmentProfile" && marker.Capability == access.CapabilityResourceManage
}

func developmentProfileBootstrapBindingMatches(marker accessmodule.BootstrapAuthorization, actor string, permission analyticsmodule.ConnectionAdministrationPermission, binding analyticsmodule.ConnectionTargetBinding, target, environment string) bool {
	return actor != "" && marker.PrincipalID == actor && marker.ProjectID == binding.Scope.ProjectID &&
		marker.Capability == access.CapabilityResourceManage && marker.ProjectID.Validate() == nil &&
		target != "" && binding.TargetID.String() == target && environment != "" && binding.Scope.Environment == environment &&
		(permission == analyticsmodule.PermissionManageConnectionMetadata || permission == analyticsmodule.PermissionUseConnection)
}
