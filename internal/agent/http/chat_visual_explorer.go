package http

import (
	"encoding/json"
	nethttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/go-chi/chi/v5"
)

// ChatVisualExplorer opens the retained visual, including its exact data and
// query, rather than reconstructing a different chart from today's defaults.
func (h *Handler) ChatVisualExplorer(w nethttp.ResponseWriter, r *nethttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	conversationID := strings.TrimSpace(chi.URLParam(r, "conversation"))
	visualID := strings.TrimSpace(chi.URLParam(r, "visual"))
	runID := strings.TrimSpace(r.URL.Query().Get("run"))
	state, err := service.ConversationTranscriptState(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, "Visual is unavailable", statusForNotFound(err))
		return
	}
	var item *agent.ChatTranscriptItem
	for i := len(state.Transcript) - 1; i >= 0; i-- {
		candidate := &state.Transcript[i]
		if candidate.Kind == "tool" && candidate.Status == "complete" && candidate.Error == "" && candidate.Artifact != nil && candidate.Artifact.ID == visualID && (runID == "" || candidate.RunID == runID) {
			item = candidate
			break
		}
	}
	if item == nil || state.Artifacts.Visuals[visualID] == nil {
		nethttp.NotFound(w, r)
		return
	}
	// The transcript has bounded previews. Recover the full original arguments
	// only after ownership and membership in the active transcript are checked.
	messages, err := service.ListMessages(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, "Visual is unavailable", statusForNotFound(err))
		return
	}
	hydrateRetainedTool(item, messages)
	if item.Error != "" {
		nethttp.NotFound(w, r)
		return
	}
	var input struct {
		SemanticModelID string `json:"semanticModelId"`
	}
	var result struct {
		DatasetID string `json:"datasetId"`
	}
	_ = json.Unmarshal([]byte(item.ArgumentsJSON), &input)
	_ = json.Unmarshal([]byte(item.ResultJSON), &result)
	// A retained visual can span several physical datasets. Authorize the
	// canonical semantic projection rather than requiring one dataset identity.
	if item.Name != "query_visual" || input.SemanticModelID == "" || h.options.AuthorizeRetainedVisual == nil {
		nethttp.NotFound(w, r)
		return
	}
	if err := h.options.AuthorizeRetainedVisual(r.Context(), scope, input.SemanticModelID); err != nil {
		nethttp.NotFound(w, r)
		return
	}

	// Artifact IDs originate in model tool-call IDs and can recur in later
	// runs. Load the envelope from this operation, not the latest merged map.
	var encoded json.RawMessage
	for _, message := range messages {
		if message.Role != agent.MessageRoleTool || message.RunID != item.RunID || message.ToolCallID != item.ToolCallID {
			continue
		}
		var payload struct {
			Display struct {
				Patch struct {
					Visuals map[string]json.RawMessage `json:"visuals"`
				} `json:"patch"`
			} `json:"display_content"`
		}
		if json.Unmarshal([]byte(message.ContentJSON), &payload) == nil {
			encoded = payload.Display.Patch.Visuals[visualID]
		}
	}
	var envelope visualizationir.VisualizationEnvelope
	if json.Unmarshal(encoded, &envelope) != nil || visualizationir.ValidateEnvelope(envelope) != nil {
		nethttp.Error(w, "Visual is unavailable", nethttp.StatusInternalServerError)
		return
	}
	page, err := ui.ChatVisualExplorerPage(conversationID, input.SemanticModelID, result.DatasetID, h.csrfToken(r), *item, envelope, h.layout(r))
	if err != nil {
		nethttp.Error(w, "Visual is unavailable", nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := page.Render(w); err != nil {
		nethttp.Error(w, "Visual is unavailable", nethttp.StatusInternalServerError)
	}
}
