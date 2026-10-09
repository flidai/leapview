package http

import (
	"context"
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
)

// ConnectionCredentialCommand is supplied by application composition. The
// browser resolves the active project and connection before invoking it.
type ConnectionCredentialCommand func(context.Context, string, string, string, projectsignals.ConnectionCredentialCommandSignal) (projectsignals.ConnectionCredentialSignal, error)

type ConnectionCredentialInput = projectsignals.ConnectionCredentialCommandSignal
type ConnectionCredentialResult = projectsignals.ConnectionCredentialSignal
type ConnectionCredentialDraft = projectsignals.ConnectionCredentialDraftSignal
type CredentialVersionStatus = projectsignals.CredentialVersionStatusSignal
type CredentialVersionDependency = projectsignals.CredentialVersionDependencySignal
type ConnectionCredentialStatus = projectsignals.ConnectionAdministrationStatusSignal

func (h *BrowserHandler) ConnectionCredentialQuery(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.connectionCredentialCommand(w, r, false)
}

func (h *BrowserHandler) ConnectionCredentialMutation(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.connectionCredentialCommand(w, r, true)
}

func (h *BrowserHandler) connectionCredentialCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, mutation bool) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = stdhttp.MaxBytesReader(w, r.Body, 32<<10)
	var payload struct {
		Administration projectsignals.ConnectionAdministrationSignal `json:"connectionAdmin"`
	}
	if err := pagestream.ReadSignals(r, &payload); err != nil || payload.Administration.Credentials == nil {
		stdhttp.Error(w, "Credential request is invalid.", stdhttp.StatusBadRequest)
		return
	}
	command := payload.Administration.Credentials.Command
	patch := func(result projectsignals.ConnectionCredentialSignal) {
		result.Command.Username, result.Command.Password = "", ""
		if result.Drafts == nil {
			result.Drafts = []projectsignals.ConnectionCredentialDraftSignal{}
		}
		_ = pagestream.PatchResponse(credentialNoStoreWriter{w}, r, pagestream.SignalPatch{"connectionAdmin": projectsignals.ConnectionAdministrationSignal{Credentials: &result}})
	}
	fail := func(message string) {
		patch(projectsignals.ConnectionCredentialSignal{Command: command, OperationID: command.OperationID, Status: projectsignals.ConnectionAdministrationStatusSignal{Error: message}})
	}
	if mutation {
		binding, ok := h.ConnectionCommands.Credentials[command.Action]
		if !ok || uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()) != nil {
			fail("The credential command is invalid.")
			return
		}
	} else if (command.Action != "list" && command.Action != "status" && command.Action != "version_status") || command.Username != "" || command.Password != "" {
		fail("The credential query is invalid.")
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || h.ConnectionCredentials == nil {
		fail("Connection credentials are unavailable.")
		return
	}
	projectID, assets, _, ok := h.assets(w, r)
	if !ok {
		return
	}
	asset, exists := projectview.AssetByID(assets, command.AssetID)
	if !exists || asset.Type != string(projectview.AssetTypeConnection) {
		fail("Connection was not found.")
		return
	}
	command.AssetID = asset.ID
	command.LogicalConnection = projectui.ConnectionLogicalName(asset, assets, nil)
	// Permission, current binding, owner and credential version checks remain
	// service-owned and run again for every operation, including metadata reads.
	result, err := h.ConnectionCredentials(r.Context(), principal.ID, projectID.String(), asset.ID, command)
	if err != nil {
		message := "The credential operation did not finish. Check activation status before continuing."
		if h.ConnectionCredentialError != nil {
			message = h.ConnectionCredentialError(err)
		}
		fail(message)
		return
	}
	// Saving/listing can follow a completed activation without reloading the
	// document. Return the current binding revision for the next explicit test;
	// validation/activation responses retain their own exact receipt revision.
	if (command.Action == "list" || command.Action == "save") && h.ConnectionAdministration != nil {
		bindings, readErr := h.ConnectionAdministration.List(r.Context(), principal.ID, connectionadmin.BindingScope{ProjectID: projectID, Environment: h.Environment}, connectionadmin.TargetID(h.TargetID))
		if readErr == nil {
			for _, binding := range bindings {
				if binding.ConnectionID == projectgraph.ResourceID(asset.ID) {
					result.BindingRevision = binding.Revision
					break
				}
			}
		}
	}
	patch(result)
}

// Datastar sets no-cache while opening SSE. Credential metadata responses also
// prohibit storage, including the initial flush before the signal patch.
type credentialNoStoreWriter struct{ stdhttp.ResponseWriter }

func (w credentialNoStoreWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}
func (w credentialNoStoreWriter) FlushError() error {
	w.Header().Set("Cache-Control", "no-store")
	return stdhttp.NewResponseController(w.ResponseWriter).Flush()
}
func (w credentialNoStoreWriter) Unwrap() stdhttp.ResponseWriter { return w.ResponseWriter }
