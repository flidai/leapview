package app

import (
	"context"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projecthttp "github.com/flidai/leapview/internal/project/http"
)

func connectionCredentialBrowser(config credentialmodule.CredentialDraftAPIGenConfig, target string) projecthttp.ConnectionCredentialCommand {
	return func(ctx context.Context, actor, project, connection string, command projecthttp.ConnectionCredentialInput) (projecthttp.ConnectionCredentialResult, error) {
		state, err := credentialmodule.RunCredentialBrowser(ctx, config, actor, project, target, connection, credentialmodule.BrowserInput{
			Action: command.Action, VersionID: command.VersionID, ReceiptID: command.ReceiptID,
			OperationID: command.OperationID, Username: command.Username, Password: command.Password,
			BeforeVersionID: command.BeforeVersionID, ExpectedRevision: command.ExpectedRevision,
		})
		command.Username, command.Password = "", ""
		if state.VersionID != "" {
			command.VersionID = state.VersionID
		}
		result := projecthttp.ConnectionCredentialResult{
			Command: command, Drafts: []projecthttp.ConnectionCredentialDraft{},
			NextBeforeVersionID: state.NextBeforeVersionID, ReceiptID: state.ReceiptID,
			ReceiptExpiresAt: state.ReceiptExpiresAt, BindingRevision: state.BindingRevision,
			OperationID: state.OperationID, Phase: state.Phase, RuntimeReady: state.RuntimeReady,
			Status: projecthttp.ConnectionCredentialStatus{Message: state.Message},
		}
		for _, draft := range state.Drafts {
			result.Drafts = append(result.Drafts, projecthttp.ConnectionCredentialDraft{VersionID: draft.VersionID, CreatedAt: draft.CreatedAt})
		}
		return result, err
	}
}
