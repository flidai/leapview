package http

import (
	"errors"
	"fmt"
	"net/http"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/api"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/pkg/pagestream"
)

func (h *Handler) updateProviderConfig(w http.ResponseWriter, r *http.Request, input api.AdminAgentConfigPatchRequest) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	if input.SystemPrompt != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", fmt.Errorf("update provider configuration separately from instructions")))
		return
	}
	if h.options.Service == nil || h.options.Service.ConfigurationManager() == nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", fmt.Errorf("admin configuration requires deployment credential encryption setup")))
		return
	}
	current, err := h.AdminDetails(r.Context())
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	executor, err := apigencommand.NewExecutor(agentgen.GetAPIGenCommandRuntimeContract, h.options.Logger)
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	if err := executor.CheckConcurrency(r.Context(), updateAgentConfigOperation.APIGenOperationID(), r.Header.Get("If-Match"), agentResourceETag(current)); err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, err)
		return
	}
	manager := h.options.Service.ConfigurationManager()
	if input.RestoreRevision > 0 {
		previous, err := manager.RestoreInput(r.Context(), input.RestoreRevision)
		if err != nil {
			h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", fmt.Errorf("previous configuration is unavailable")))
			return
		}
		input.Provider = &previous
	}

	principal, _ := h.options.CurrentPrincipal(r)
	var token, message string
	switch input.Action {
	case "test":
		token, err = manager.Test(r.Context(), principal.ID, input.ExpectedRevision, *input.Provider)
		message = "Connection verified. Save and activate to apply these settings."
		if !input.Provider.Enabled {
			message = "Settings validated. Save to disable new agent requests."
		}
		if input.RestoreRevision > 0 {
			message = fmt.Sprintf("Revision %d verified: model %s at %s (enabled: %t). Restore tested revision to apply it.", input.RestoreRevision, input.Provider.Model, input.Provider.BaseURL, input.Provider.Enabled)
		}
	case "save":
		_, err = manager.Save(r.Context(), principal.ID, input.ExpectedRevision, *input.Provider, input.TestToken)
		message = "Configuration saved and activated."
	default:
		err = fmt.Errorf("action must be test or save")
	}
	if err != nil {
		if errors.Is(err, agent.ErrConfigurationConflict) {
			h.writeCommandFailure(w, r, updateAgentConfigOperation, errors.Join(apigencommand.ErrPreconditionFailed, err))
			return
		}
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	details, err := h.AdminDetails(r.Context())
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	details.TestToken, details.TestMessage = token, message
	if input.Action == "test" {
		h.recordCommandAudit(r, updateAgentConfigOperation, h.chatScope(r), "agent_config_test", "candidate")
	}
	if input.Action == "save" {
		h.recordCommandAudit(r, updateAgentConfigOperation, h.chatScope(r), "agent_config", fmt.Sprintf("revision:%d", details.ConfigurationRevision))
	}
	revision := agentResourceETag(details)
	w.Header().Set("ETag", revision)
	if agentAcceptsEventStream(r.Header.Get("Accept")) {
		patch := map[string]any{"configured": details.Configured, "enabled": details.Enabled, "model": details.Model, "reasoningEffort": details.ReasoningEffort, "baseUrl": details.BaseURL, "apiMode": details.APIMode, "configurationRevision": details.ConfigurationRevision, "adminManaged": details.AdminManaged, "credentialConfigured": details.CredentialConfigured, "configurationAvailable": details.ConfigurationAvailable, "status": details.Status, "statusDetail": details.StatusDetail, "revision": revision, "testToken": token, "testMessage": message}
		_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"page": map[string]any{"agent": patch}, "adminAgentCommand": map[string]any{"provider": nil, "testToken": ""}})
		return
	}
	writeJSON(w, http.StatusOK, agentConfigResponse(details))
}
