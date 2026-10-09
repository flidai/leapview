package module

import (
	"context"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func FirstSourceBrowserBindings() map[string]uicommand.Binding {
	bindings := CredentialBrowserBindings()
	delete(bindings, "retry")
	bindings["prepare"] = credentialgen.GenUIActionPrepareFirstSourceCredential()
	bindings["renew"] = credentialgen.GenUIActionRenewFirstSourceCredentialPreparation()
	return bindings
}

// RunFirstSourceBrowser preserves the same credential service and generated
// command contracts, while publication remains the normal reviewed native path.
func RunFirstSourceBrowser(ctx context.Context, config CredentialDraftAPIGenConfig, actor string, resource ValidationResource, input BrowserInput, preparation FirstSourcePreparationCommandRequest) (BrowserState, error) {
	if config.FirstSourcePreparation == nil {
		return BrowserState{}, credential.ErrUnavailable
	}
	if err := config.FirstSourcePreparation.Authorize(ctx, actor, resource); err != nil {
		return BrowserState{}, err
	}
	if input.Action != "prepare" && input.Action != "renew" {
		if input.Action != "save" && input.Action != "validate" && input.Action != "list" && input.Action != "status" && input.Action != "abort" {
			return BrowserState{}, credential.ErrInvalid
		}
		return RunCredentialBrowser(ctx, config, actor, resource.ProjectID, resource.TargetID, resource.ResourceID, input)
	}
	binding := FirstSourceBrowserBindings()[input.Action]
	contract, ok := credentialgen.GetAPIGenCommandRuntimeContract(binding.OperationID())
	if !ok {
		return BrowserState{}, credential.ErrUnavailable
	}
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return BrowserState{}, err
	}
	var result FirstSourcePreparationCommandResult
	err = apigencommand.ExecuteInvocation(ctx, executor, contract, apigencommand.Invocation{
		OperationID: binding.OperationID(), Surface: apigencommand.SurfaceUI, TargetValues: map[string]string{"connection": resource.ResourceID}, RequestID: input.RequestID, CorrelationID: input.CorrelationID,
	}, apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
		var err error
		if input.Action == "prepare" {
			result, err = config.FirstSourcePreparation.Prepare(ctx, actor, resource, preparation)
		} else {
			result, err = config.FirstSourcePreparation.Renew(ctx, actor, resource, input.OperationID, input.ReceiptID)
		}
		return err
	}})
	if err != nil {
		return BrowserState{}, err
	}
	return BrowserState{VersionID: input.VersionID, OperationID: result.PreparationID, Phase: "preparing", Message: "Credential preparation retained. Build and publish the matching source through the normal delivery workflow; an independent reviewer must approve it."}, nil
}
