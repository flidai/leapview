package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	"github.com/go-chi/chi/v5"
	toon "github.com/toon-format/toon-go"
)

func (h *Handler) ChatActionOpen(w nethttp.ResponseWriter, r *nethttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	conversationID := strings.TrimSpace(chi.URLParam(r, "conversation"))
	callID := strings.TrimSpace(chi.URLParam(r, "toolcall"))
	runID := strings.TrimSpace(r.URL.Query().Get("run"))
	state, err := service.ConversationTranscriptState(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, "Action is unavailable", statusForNotFound(err))
		return
	}
	var item *agent.ChatTranscriptItem
	for i := range state.Transcript {
		candidate := &state.Transcript[i]
		if candidate.ToolCallID == callID && (runID == "" || candidate.RunID == runID) && candidate.Kind == "tool" && candidate.Status == "complete" && candidate.Error == "" && dashboardBuilderTool(candidate.Name) {
			item = candidate
			break
		}
	}
	// Live tool output is stored as events before the final transcript commit.
	// Resolve those receipts only after checking this run belongs to the
	// authorized conversation; never accept browser-supplied dashboard IDs.
	if item == nil && runID != "" {
		if _, err := service.GetRun(r.Context(), scope, conversationID, runID); err == nil {
			if events, err := service.ListEvents(r.Context(), scope, runID); err == nil {
				item = actionFromCompletedEvents(events, runID, callID)
			}
		}
	}
	if item == nil {
		nethttp.NotFound(w, r)
		return
	}
	messages, err := service.ListMessages(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, "Action is unavailable", statusForNotFound(err))
		return
	}
	if createdBy := strings.TrimSpace(r.URL.Query().Get("createdBy")); createdBy != "" {
		createdDashboard := ""
		for _, creation := range state.Transcript {
			if creation.RunID != item.RunID || creation.ToolCallID != createdBy || creation.Kind != "tool" || creation.Status != "complete" || creation.Error != "" || (creation.Name != "create_dashboard_draft" && creation.Name != "fork_dashboard") {
				continue
			}
			hydrateRetainedTool(&creation, messages)
			if creation.Error == "" {
				createdDashboard = dashboardCreationID(creation.ResultJSON)
			}
			break
		}
		// Bounded/TOON browser previews may omit identities. Resolve the last
		// preview for the created dashboard from the full retained arguments.
		var preview *agent.ChatTranscriptItem
		for i := len(state.Transcript) - 1; createdDashboard != "" && i >= 0; i-- {
			candidate := &state.Transcript[i]
			if candidate.RunID != item.RunID || candidate.Kind != "tool" || candidate.Name != "preview_dashboard_draft" {
				continue
			}
			hydrateRetainedTool(candidate, messages)
			var input struct {
				DashboardID string `json:"dashboardId"`
			}
			if json.Unmarshal([]byte(candidate.ArgumentsJSON), &input) == nil && input.DashboardID == createdDashboard {
				preview = candidate
				break
			}
		}
		if preview == nil || preview.Status != "complete" || preview.Error != "" {
			nethttp.NotFound(w, r)
			return
		}
		item = preview
	}
	hydrateRetainedTool(item, messages)
	var result struct {
		Error        json.RawMessage   `json:"error"`
		VisualErrors map[string]string `json:"visualErrors"`
		DashboardID  string            `json:"dashboardId"`
		DraftID      string            `json:"draftId"`
		Lifecycle    struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Draft  struct {
				ID string `json:"id"`
			} `json:"draft"`
		} `json:"lifecycle"`
		Revision struct {
			DashboardID string `json:"dashboardId"`
			Document    struct {
				Spec struct {
					Pages []struct {
						ID         string `json:"id"`
						Components []struct {
							ID     string `json:"id"`
							Visual string `json:"visual"`
						} `json:"components"`
					} `json:"pages"`
				} `json:"spec"`
			} `json:"document"`
		} `json:"revision"`
	}
	var input struct {
		DashboardID string          `json:"dashboardId"`
		DraftID     string          `json:"draftId"`
		PageID      string          `json:"pageId"`
		Page        string          `json:"page"`
		ComponentID string          `json:"componentId"`
		VisualID    string          `json:"visualId"`
		Archive     json.RawMessage `json:"archive"`
	}
	if json.Unmarshal([]byte(item.ResultJSON), &result) != nil || json.Unmarshal([]byte(item.ArgumentsJSON), &input) != nil {
		nethttp.NotFound(w, r)
		return
	}
	if item.Error != "" || (len(result.Error) > 0 && string(result.Error) != "null") || result.Lifecycle.Status == "archived" || (len(input.Archive) > 0 && string(input.Archive) != "null") {
		nethttp.NotFound(w, r)
		return
	}
	dashboardID := firstNonEmptyString(result.Lifecycle.ID, result.DashboardID, result.Revision.DashboardID)
	draftID := firstNonEmptyString(result.Lifecycle.Draft.ID, result.DraftID)
	if item.Name == "create_dashboard_draft" || item.Name == "fork_dashboard" {
		dashboardID = firstNonEmptyString(dashboardID, dashboardCreationID(item.ResultJSON))
	}
	if item.Name != "create_dashboard_draft" && item.Name != "fork_dashboard" {
		dashboardID = firstNonEmptyString(dashboardID, input.DashboardID)
		draftID = firstNonEmptyString(draftID, input.DraftID)
	}
	if dashboardID == "" {
		nethttp.NotFound(w, r)
		return
	}
	if (r.URL.Query().Get("createdBy") != "" || r.URL.Query().Get("mode") == "preview") && len(result.VisualErrors) > 0 {
		nethttp.NotFound(w, r)
		return
	}
	query := url.Values{}
	query.Set("returnChat", conversationID)
	if r.URL.Query().Get("embed") == "chat" {
		query.Set("embed", "chat")
		if r.URL.Query().Get("mode") == "preview" {
			query.Set("mode", "preview")
		}
	}
	if draftID != "" {
		query.Set("draft", draftID)
	}
	pageID := firstNonEmptyString(input.PageID, input.Page)
	if pageID == "" && len(result.Revision.Document.Spec.Pages) > 0 {
		pageIndex := 0
		if item.Name == "add_dashboard_page" {
			pageIndex = len(result.Revision.Document.Spec.Pages) - 1
		}
		pageID = result.Revision.Document.Spec.Pages[pageIndex].ID
	}
	if pageID != "" {
		query.Set("page", pageID)
	}
	visualID := firstNonEmptyString(input.ComponentID, input.VisualID)
	// Builder selection uses the placement identity, which can differ from
	// the reusable visual definition named in the agent operation.
	if input.ComponentID == "" && input.VisualID != "" {
		for _, page := range result.Revision.Document.Spec.Pages {
			if page.ID != pageID {
				continue
			}
			for _, component := range page.Components {
				if component.ID == input.VisualID {
					visualID = component.ID
					break
				}
				if component.Visual == input.VisualID && visualID == input.VisualID {
					visualID = component.ID
				}
			}
		}
	}
	if visualID == "" && item.Name == "add_dashboard_visual" {
		for _, page := range result.Revision.Document.Spec.Pages {
			if page.ID != pageID {
				continue
			}
			for _, component := range page.Components {
				if component.Visual != "" {
					visualID = component.ID
				}
			}
		}
	}
	if visualID != "" {
		query.Set("visual", visualID)
	}
	href := "/dashboards/" + url.PathEscape(dashboardID) + "/edit"
	if len(query) > 0 {
		href += "?" + query.Encode()
	}
	w.Header().Set("Cache-Control", "no-store")
	nethttp.Redirect(w, r, href, nethttp.StatusSeeOther)
}

// Current create/fork receipts contain id and status; retained conversations
// may still contain the earlier lifecycle result.
func dashboardCreationID(raw string) string {
	var result struct {
		ID        string `json:"id"`
		Lifecycle struct {
			ID string `json:"id"`
		} `json:"lifecycle"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil {
		return ""
	}
	return firstNonEmptyString(result.Lifecycle.ID, result.ID)
}

func actionFromCompletedEvents(events []agent.Event, runID, callID string) *agent.ChatTranscriptItem {
	var item *agent.ChatTranscriptItem
	for _, event := range events {
		if event.RunID != runID {
			continue
		}
		var payload struct {
			CallID    string `json:"tool_call_id"`
			Name      string `json:"tool_name"`
			Arguments string `json:"tool_arguments"`
			Result    string `json:"tool_result"`
			Error     string `json:"error"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.CallID != callID {
			continue
		}
		switch event.EventType {
		case "tool_execution_start":
			item = nil
			if dashboardBuilderTool(payload.Name) && json.Valid([]byte(payload.Arguments)) {
				item = &agent.ChatTranscriptItem{RunID: runID, ToolCallID: callID, Kind: "tool", Name: payload.Name, ArgumentsJSON: payload.Arguments}
			}
		case "tool_execution_end":
			if item == nil || item.Name != payload.Name || payload.Error != "" || event.Severity == "error" {
				return nil
			}
			item.Status = "complete"
			hydrateRetainedTool(item, []agent.Message{{RunID: runID, ToolCallID: callID, Role: agent.MessageRoleTool, ContentText: payload.Result}})
		}
	}
	if item == nil || item.Status != "complete" {
		return nil
	}
	return item
}

func dashboardBuilderTool(name string) bool {
	switch name {
	case "create_dashboard_draft", "fork_dashboard", "get_dashboard_draft", "read_dashboard_source", "edit_dashboard_source", "set_dashboard_visibility", "add_dashboard_page", "add_dashboard_visual", "assign_dashboard_field", "preview_dashboard_draft", "execute_dashboard_command":
		return true
	}
	return false
}

// Call IDs are scoped to the retained run, so editing a conversation or reusing
// a model-provided call ID cannot substitute another operation's identities.
func hydrateRetainedTool(item *agent.ChatTranscriptItem, messages []agent.Message) {
	for _, message := range messages {
		if message.RunID != item.RunID {
			continue
		}
		var payload struct {
			ToolCalls []struct {
				ID        string          `json:"id"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal([]byte(message.ContentJSON), &payload) == nil {
			for _, call := range payload.ToolCalls {
				if call.ID == item.ToolCallID && json.Valid(call.Arguments) {
					item.ArgumentsJSON = string(call.Arguments)
				}
			}
		}
		if message.ToolCallID == item.ToolCallID && message.Role == agent.MessageRoleTool {
			if message.IsError {
				item.Error = "Action failed"
			}
			// Visual tools retain a full structured receipt alongside their
			// rendering envelope. Prefer it over the model-facing text, which
			// may be compacted or encoded as TOON with nested data rows.
			if item.Name == "query_visual" && item.Artifact != nil {
				var retained struct {
					Display struct {
						Result json.RawMessage `json:"result"`
					} `json:"display_content"`
				}
				var receipt struct {
					ID string `json:"id"`
				}
				if json.Unmarshal([]byte(message.ContentJSON), &retained) == nil && json.Unmarshal(retained.Display.Result, &receipt) == nil && receipt.ID == item.Artifact.ID {
					item.ResultJSON = string(retained.Display.Result)
					continue
				}
			}
			item.ResultJSON = message.ContentText
			if !json.Valid([]byte(item.ResultJSON)) {
				content := item.ResultJSON
				if item.Name == "preview_dashboard_draft" {
					content = previewActionReceipt(content)
				}
				var decoded any
				decodeErr := toon.UnmarshalString(content, &decoded)
				if decodeErr != nil {
					decodeErr = toon.UnmarshalString(actionIdentityReceipt(content), &decoded)
				}
				if decodeErr == nil {
					if encoded, err := json.Marshal(decoded); err == nil {
						item.ResultJSON = string(encoded)
					}
				}
			}
		}
	}
}

// Some retained authored documents contain nested TOON lists the decoder
// cannot round-trip. Keep their authoritative identities and error outcome;
// the editor itself resolves and authorizes the full current document.
func actionIdentityReceipt(content string) string {
	var receipt strings.Builder
	skipDocument, inRevision := false, false
	for _, line := range strings.Split(previewActionReceipt(content), "\n") {
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			inRevision = line == "revision:"
			skipDocument = false
		}
		if inRevision && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			skipDocument = line == "  document:"
		}
		if !skipDocument {
			receipt.WriteString(line)
			receipt.WriteByte('\n')
		}
	}
	return receipt.String()
}

// Preview render trees can contain nested lists emitted by the TOON encoder
// that its decoder cannot round-trip. They are not navigation authority: the
// retained call arguments identify the dashboard and page. Decode only the
// receipt, keeping errors and revision metadata intact so failed previews
// cannot be mistaken for successful ones.
func previewActionReceipt(content string) string {
	var receipt strings.Builder
	skip := false
	for _, line := range strings.Split(content, "\n") {
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			switch line {
			case "definition:", "pagePatch:", "semanticEvidence:":
				skip = true
			default:
				skip = false
			}
		}
		if !skip {
			receipt.WriteString(line)
			receipt.WriteByte('\n')
		}
	}
	return receipt.String()
}
