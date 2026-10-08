package module

import (
	"context"
	"errors"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// BrowserInput carries one explicit UI submission. Secret fields are consumed
// only by SaveDraft; they never enter an invocation, audit or response.
type BrowserInput struct {
	Action, VersionID, ReceiptID, OperationID string
	Username, Password, BeforeVersionID       string
	RequestID, CorrelationID                  string
	ExpectedRevision                          int64
}

type BrowserDraft struct{ VersionID, CreatedAt string }
type BrowserState struct {
	Drafts                         []BrowserDraft
	VersionID, NextBeforeVersionID string
	ReceiptID, ReceiptExpiresAt    string
	OperationID, Phase, Message    string
	BindingRevision                int64
	RuntimeReady                   bool
}

func CredentialBrowserBindings() map[string]uicommand.Binding {
	return map[string]uicommand.Binding{
		"save":     credentialgen.GenUIActionSaveCredentialDraft(),
		"validate": credentialgen.GenUIActionValidateCredentialDraft(),
		"prepare":  credentialgen.GenUIActionStartCredentialActivation(),
		"retry":    credentialgen.GenUIActionRetryCredentialActivation(),
		"abort":    credentialgen.GenUIActionAbortCredentialActivation(),
	}
}

func executeCredentialBrowserCommand(ctx context.Context, action, connection, requestID, correlationID string, mutate func(context.Context) error) error {
	binding, ok := CredentialBrowserBindings()[action]
	if !ok {
		return credential.ErrInvalid
	}
	contract, ok := credentialgen.GetAPIGenCommandRuntimeContract(binding.OperationID())
	if !ok {
		return credential.ErrUnavailable
	}
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return apigencommand.ExecuteInvocation(ctx, executor, contract, apigencommand.Invocation{
		OperationID: binding.OperationID(), Surface: apigencommand.SurfaceUI,
		TargetValues: map[string]string{"connection": connection}, RequestID: requestID, CorrelationID: correlationID,
	}, apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
		return mutate(ctx)
	}})
}

// RunCredentialBrowser uses the same services and generated transactional
// command contracts as the API. All scope arguments come from server composition.
func RunCredentialBrowser(ctx context.Context, config CredentialDraftAPIGenConfig, actor, project, target, connection string, input BrowserInput) (BrowserState, error) {
	state := BrowserState{Drafts: []BrowserDraft{}}
	if config.Service == nil || config.Validation == nil || config.Activation == nil {
		return state, credential.ErrUnavailable
	}
	resource := credential.Resource{ScopeKind: "connection", ProjectID: project, TargetID: target, Environment: config.Environment, ResourceID: connection}
	if resource.Validate() != nil {
		return state, credential.ErrInvalid
	}
	if input.Action == "list" {
		page, err := config.Service.ListDrafts(ctx, actor, resource, 20, input.BeforeVersionID)
		if err != nil {
			return state, err
		}
		for _, draft := range page.Items {
			state.Drafts = append(state.Drafts, BrowserDraft{draft.Binding.VersionID, draft.CreatedAt.UTC().Format(time.RFC3339Nano)})
		}
		state.NextBeforeVersionID = page.NextBeforeVersionID
		return state, nil
	}
	setActivation := func(status credential.ActivationStatus) {
		state.VersionID, state.OperationID, state.Phase = status.VersionID, status.OperationID, status.State
		state.BindingRevision, state.RuntimeReady = status.BindingRevision, status.RuntimeReady
	}
	if input.Action == "status" {
		status, err := config.Activation.GetActivation(ctx, actor, resource, input.OperationID)
		if errors.Is(err, credential.ErrNotFound) {
			// The service has checked current authority and exact actor/resource
			// scope. Only this confirmed absence allows the UI to discard an
			// operation ID whose preparation was never acknowledged.
			state.OperationID, state.Phase = input.OperationID, "not_found"
			state.Message = "No activation was found for this operation in the current connection. Start again to test the draft and prepare a new activation."
			return state, nil
		}
		if err == nil {
			setActivation(status)
		}
		return state, err
	}
	err := executeCredentialBrowserCommand(ctx, input.Action, connection, input.RequestID, input.CorrelationID, func(ctx context.Context) error {
		switch input.Action {
		case "save":
			if input.Username != "" {
				return credential.ErrInvalid
			}
			fields := map[string]string{"password": input.Password}
			defer clear(fields)
			metadata, err := config.Service.SaveDraft(ctx, actor, resource, fields)
			if err != nil {
				return err
			}
			state.VersionID = metadata.Binding.VersionID
			state.Drafts = []BrowserDraft{{metadata.Binding.VersionID, metadata.CreatedAt.UTC().Format(time.RFC3339Nano)}}
			state.Message = "Credential draft saved. Test it before preparing activation."
		case "validate":
			receipt, err := config.Validation.ValidateDraft(ctx, actor, resource, input.VersionID, input.ExpectedRevision)
			if err != nil {
				return err
			}
			state.VersionID, state.ReceiptID = receipt.Binding.VersionID, receipt.ReceiptID
			state.BindingRevision, state.ReceiptExpiresAt = receipt.BindingRevision, receipt.ExpiresAt.UTC().Format(time.RFC3339Nano)
			if input.OperationID != "" {
				status, err := config.Activation.GetActivation(ctx, actor, resource, input.OperationID)
				if err != nil {
					return err
				}
				if status.VersionID != input.VersionID {
					return credential.ErrConflict
				}
				state.OperationID, state.Phase, state.RuntimeReady = status.OperationID, status.State, status.RuntimeReady
			}
			state.Message = "Credential test passed. The running connection has not changed."
		case "prepare", "retry", "abort":
			var status credential.ActivationStatus
			var err error
			switch input.Action {
			case "prepare":
				status, err = config.Activation.StartActivation(ctx, actor, resource, credential.ActivationRequest{OperationID: input.OperationID, VersionID: input.VersionID, ReceiptID: input.ReceiptID, ExpectedBindingRevision: input.ExpectedRevision})
			case "retry":
				status, err = config.Activation.RetryActivation(ctx, actor, resource, input.OperationID, input.ReceiptID)
			case "abort":
				status, err = config.Activation.AbortActivation(ctx, actor, resource, input.OperationID)
			}
			if err != nil {
				return err
			}
			setActivation(status)
		}
		return nil
	})
	return state, err
}

func CredentialBrowserError(err error) string {
	switch {
	case errors.Is(err, credential.ErrForbidden):
		return "You do not have permission to manage and use this connection."
	case errors.Is(err, credential.ErrInvalid):
		return "The credential request is invalid."
	case errors.Is(err, credential.ErrNotFound):
		return "The credential draft or operation was not found."
	case errors.Is(err, credential.ErrConflict):
		return "Connection state changed. Check activation status, then test the selected draft again if needed."
	default:
		return "The credential operation did not finish. Check activation status before continuing."
	}
}
