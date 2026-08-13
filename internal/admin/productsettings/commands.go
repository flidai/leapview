package productsettings

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

const (
	CommandUpdateIdentity = "update_identity"
	CommandResetIdentity  = "reset_identity"
	CommandDeleteLogo     = "delete_logo"
	CommandUploadLogo     = "upload_logo"
)

type CommandInvocation struct {
	IdempotencyKey   string
	ConcurrencyToken string
	RequestID        string
	CorrelationID    string
}

type CommandContract struct {
	Bindings map[string]uicommand.Binding
	Begin    func(context.Context, string, CommandInvocation) (context.Context, error)
}

func (contract CommandContract) Binding(command string) (uicommand.Binding, error) {
	binding, ok := contract.Bindings[command]
	if !ok || binding.OperationID() == "" {
		return uicommand.Binding{}, fmt.Errorf("product command %q has no generated UI binding", command)
	}
	return binding, nil
}

func (contract CommandContract) BeginInvocation(ctx context.Context, command string, invocation CommandInvocation) (context.Context, error) {
	if contract.Begin == nil {
		return ctx, fmt.Errorf("product command %q has no generated invocation adapter", command)
	}
	return contract.Begin(ctx, command, invocation)
}
