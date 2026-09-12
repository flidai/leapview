package module

import (
	"context"
	"encoding/json"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func chatSignalWithConversations(conversations []ui.ChatConversationSummary, activeID string, transcript []agent.ChatTranscriptItem, artifacts agent.ChatArtifactSignals, statusErr string, running, enabled bool, runIDs ...string) ui.ChatViewState {
	if !enabled && statusErr == "" {
		statusErr = "Agent is not configured"
	}
	if conversations == nil {
		conversations = []ui.ChatConversationSummary{}
	}
	artifacts = normalizeChatArtifacts(artifacts)
	status := ui.ChatStatus{Enabled: enabled, Running: running, Error: ui.Optional(statusErr)}
	if running && len(runIDs) > 0 {
		status.RunID = ui.Optional(runIDs[0])
	}
	return ui.ChatViewState{
		Visuals: TypedChatArtifacts(artifacts),
		Agent: ui.ChatSignal{
			Conversations:        conversations,
			ActiveConversationID: activeID,
			Transcript:           ui.ChatTranscriptItems(transcript),
			Status:               status,
			Composer: ui.ComposerSignal{
				Value:       "",
				Disabled:    !enabled || running,
				Placeholder: chatPlaceholder(enabled, running),
			},
		},
	}
}

func (m *Module) chatSignal(ctx context.Context, scope agent.Scope, activeID, statusErr string, running bool) ui.ChatViewState {
	transcript := []agent.ChatTranscriptItem{}
	artifacts := agent.ChatArtifactSignals{}
	if activeID != "" && m.service != nil && scope.PrincipalID != "" {
		if loaded, err := m.service.ConversationTranscriptState(ctx, scope, activeID); err == nil {
			transcript = loaded.Transcript
			artifacts = loaded.Artifacts
		}
	}
	return m.ChatSignalWith(ctx, scope, activeID, transcript, artifacts, statusErr, running)
}

func (m *Module) ChatSignalWith(ctx context.Context, scope agent.Scope, activeID string, transcript []agent.ChatTranscriptItem, artifacts agent.ChatArtifactSignals, statusErr string, running bool) ui.ChatViewState {
	conversations := m.chatConversations(ctx, scope)
	enabled := m.service != nil && m.service.Enabled()
	if !enabled && statusErr == "" {
		statusErr = "Agent is not configured"
	}
	artifacts = normalizeChatArtifacts(artifacts)
	effectiveRunning := running
	status := ui.ChatStatus{Enabled: enabled, Running: effectiveRunning, Error: ui.Optional(statusErr)}
	// The in-memory running map is intentionally lost across a process
	// restart. Derive the browser's run identity and continue affordance from
	// the newest durable run so the browser reflects the durable lifecycle.
	if activeID != "" && m.service != nil && scope.PrincipalID != "" {
		if runs, err := m.service.ListRunsPage(ctx, scope, activeID, agent.Page{Limit: 1}); err == nil && len(runs) > 0 {
			latest := runs[0]
			effectiveRunning = latest.Status == agent.RunStatusRunning || latest.Status == agent.RunStatusPreparing
			status.Running = effectiveRunning
			if effectiveRunning {
				status.RunID = ui.Optional(latest.ID)
			}
			if latest.Status == agent.RunStatusCanceled {
				status.Error = nil
				status.CanContinue = ui.Pointer(true)
			}
		}
	}
	return ui.ChatViewState{
		Visuals: TypedChatArtifacts(artifacts),
		Agent: ui.ChatSignal{
			Conversations:        conversations,
			ActiveConversationID: activeID,
			Transcript:           ui.ChatTranscriptItems(transcript),
			Status:               status,
			Composer: ui.ComposerSignal{
				Value:       "",
				Disabled:    !enabled || effectiveRunning,
				Placeholder: chatPlaceholder(enabled, effectiveRunning),
			},
		},
	}
}

func normalizeChatArtifacts(artifacts agent.ChatArtifactSignals) agent.ChatArtifactSignals {
	if artifacts.Visuals == nil {
		artifacts.Visuals = map[string]any{}
	}
	return artifacts
}

func TypedChatArtifacts(artifacts agent.ChatArtifactSignals) map[string]visualizationir.VisualizationEnvelope {
	visuals := map[string]visualizationir.VisualizationEnvelope{}
	for key, value := range artifacts.Visuals {
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		var envelope visualizationir.VisualizationEnvelope
		if err := json.Unmarshal(raw, &envelope); err == nil &&
			envelope.VisualID == key &&
			visualizationir.ValidateEnvelope(envelope) == nil {
			visuals[key] = envelope
		}
	}
	return visuals
}

func chatSignalPatch(signal ui.ChatViewState) map[string]any {
	return map[string]any{"agent": signal.Agent, "visuals": signal.Visuals}
}

func (m *Module) chatConversations(ctx context.Context, scope agent.Scope) []ui.ChatConversationSummary {
	conversations := []ui.ChatConversationSummary{}
	if m.service == nil || scope.PrincipalID == "" {
		return conversations
	}
	rows, err := m.service.ListConversations(ctx, scope)
	if err != nil {
		return conversations
	}
	for _, row := range rows {
		out := chatConversationSummary(row)
		out.TitlePending = ui.Pointer(m.isChatTitlePending(row.ID))
		conversations = append(conversations, out)
	}
	return conversations
}

func chatConversationSummary(row agent.Conversation) ui.ChatConversationSummary {
	return ui.ChatConversationSummary{
		ID:          row.ID,
		PrincipalID: row.PrincipalID,
		Title:       row.Title,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		ArchivedAt:  ui.Optional(row.ArchivedAt),
		Pinned:      ui.Pointer(row.Pinned),
	}
}

func chatPlaceholder(enabled, running bool) string {
	if !enabled {
		return "Agent is not configured"
	}
	if running {
		return "Waiting for the current answer..."
	}
	return "Ask about dashboards, metrics, or models..."
}
