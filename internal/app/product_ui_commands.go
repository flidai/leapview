package app

import (
	"context"
	"fmt"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	appgen "github.com/flidai/leapview/internal/app/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func productUICommandContract() adminmodule.ProductUICommandContract {
	return adminmodule.ProductUICommandContract{
		Bindings: map[string]uicommand.Binding{
			adminmodule.ProductCommandUpdateIdentity: appgen.GenUIActionUpdateProductSettings(),
			adminmodule.ProductCommandResetIdentity:  appgen.GenUIActionResetProductSettings(),
			adminmodule.ProductCommandDeleteLogo:     appgen.GenUIActionDeleteProductLogo(),
			adminmodule.ProductCommandUploadLogo:     appgen.GenUIActionUploadProductLogo(),
		},
		Begin: beginProductUICommand,
	}
}

func beginProductUICommand(ctx context.Context, command string, invocation adminmodule.ProductUICommandInvocation) (context.Context, error) {
	switch command {
	case adminmodule.ProductCommandUpdateIdentity:
		started, _, err := appgen.BeginGenUpdateProductSettingsCommand(ctx, appgen.GenUpdateProductSettingsCommandInvocation{
			Surface: apigencommand.SurfaceUI, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	case adminmodule.ProductCommandResetIdentity:
		started, _, err := appgen.BeginGenResetProductSettingsCommand(ctx, appgen.GenResetProductSettingsCommandInvocation{
			Surface: apigencommand.SurfaceUI, IdempotencyKey: invocation.IdempotencyKey, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	case adminmodule.ProductCommandDeleteLogo:
		started, _, err := appgen.BeginGenDeleteProductLogoCommand(ctx, appgen.GenDeleteProductLogoCommandInvocation{
			Surface: apigencommand.SurfaceUI, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	case adminmodule.ProductCommandUploadLogo:
		started, _, err := appgen.BeginGenUploadProductLogoCommand(ctx, appgen.GenUploadProductLogoCommandInvocation{
			Surface: apigencommand.SurfaceUI, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	default:
		return ctx, fmt.Errorf("unknown product UI command %q", command)
	}
}
