package module

import (
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// PlatformAdministratorUIActions keeps generated access contracts behind the
// access module boundary while giving the application composition root the
// exact bindings needed by the Settings transport.
func PlatformAdministratorUIActions() map[string]uicommand.Binding {
	return map[string]uicommand.Binding{
		"grant_platform_administrator":  accessgen.GenUIActionGrantPlatformAdministrator(),
		"revoke_platform_administrator": accessgen.GenUIActionRevokePlatformAdministrator(),
	}
}
