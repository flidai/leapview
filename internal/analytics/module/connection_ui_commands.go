package module

import (
	"context"
	"errors"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// ConnectionUICommandBindings is the analytics-owned identity surface used by
// the project UI adapter. The generated bindings keep browser mutations
// tied to their audited API operation contracts.
type ConnectionUICommandBindings struct {
	Create  uicommand.Binding
	Update  uicommand.Binding
	Test    uicommand.Binding
	Refresh uicommand.Binding
	Enable  uicommand.Binding
	Disable uicommand.Binding
}

type ConnectionUICommandInvocation struct {
	Action         string
	Project        string
	Connection     string
	IdempotencyKey string
	RequestID      string
	CorrelationID  string
}

func (*Module) ConnectionUICommandBindings() ConnectionUICommandBindings {
	return ConnectionUICommandBindings{
		Create:  analyticsgen.GenUIActionCreateTargetConnectionBinding(),
		Update:  analyticsgen.GenUIActionUpdateTargetConnectionBinding(),
		Test:    analyticsgen.GenUIActionTestTargetConnectionBinding(),
		Refresh: analyticsgen.GenUIActionRefreshTargetConnectionBinding(),
		Enable:  analyticsgen.GenUIActionEnableTargetConnectionBinding(),
		Disable: analyticsgen.GenUIActionDisableTargetConnectionBinding(),
	}
}

func (*Module) BeginConnectionUICommand(ctx context.Context, invocation ConnectionUICommandInvocation) (context.Context, error) {
	surface := apigencommand.SurfaceUI
	switch invocation.Action {
	case "create":
		started, _, err := analyticsgen.BeginGenCreateTargetConnectionBindingCommand(ctx, analyticsgen.GenCreateTargetConnectionBindingCommandInvocation{Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	case "update":
		started, _, err := analyticsgen.BeginGenUpdateTargetConnectionBindingCommand(ctx, analyticsgen.GenUpdateTargetConnectionBindingCommandInvocation{Surface: surface, Connection: invocation.Connection, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	case "test":
		started, _, err := analyticsgen.BeginGenTestTargetConnectionBindingCommand(ctx, analyticsgen.GenTestTargetConnectionBindingCommandInvocation{Surface: surface, Connection: invocation.Connection, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	case "refresh":
		started, _, err := analyticsgen.BeginGenRefreshTargetConnectionBindingCommand(ctx, analyticsgen.GenRefreshTargetConnectionBindingCommandInvocation{Surface: surface, Connection: invocation.Connection, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	case "enable":
		started, _, err := analyticsgen.BeginGenEnableTargetConnectionBindingCommand(ctx, analyticsgen.GenEnableTargetConnectionBindingCommandInvocation{Surface: surface, Connection: invocation.Connection, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	case "disable":
		started, _, err := analyticsgen.BeginGenDisableTargetConnectionBindingCommand(ctx, analyticsgen.GenDisableTargetConnectionBindingCommandInvocation{Surface: surface, Connection: invocation.Connection, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	default:
		return ctx, errors.New("unsupported connection UI command")
	}
}
