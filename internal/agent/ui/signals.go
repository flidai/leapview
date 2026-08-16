package ui

import (
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	signalcontracts "github.com/flidai/leapview/internal/agent/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type RouteKind = signalcontracts.RouteKind
type RouteRuntimeSignal = signalcontracts.RouteRuntimeSignal
type AgentReferenceKeySignal = signalcontracts.AgentReferenceKeySignal
type AgentReferenceLocationSignal = signalcontracts.AgentReferenceLocationSignal
type AgentReferenceSearchSignal = signalcontracts.AgentReferenceSearchSignal
type AgentReferenceSignal = signalcontracts.AgentReferenceSignal
type AgentReferenceProjectSignal = signalcontracts.AgentReferenceProjectSignal
type ChatArtifactSignal = signalcontracts.ChatArtifactSignal
type ChatConversationSummary = signalcontracts.ChatConversationSummary
type ChatSignal = signalcontracts.ChatSignal
type ChatStatus = signalcontracts.ChatStatus
type ChatTranscriptItemSignal = signalcontracts.ChatTranscriptItemSignal
type ComposerSignal = signalcontracts.ComposerSignal
type AgentContextSignal = signalcontracts.AgentContextSignal
type DashboardFilterState = signalcontracts.DashboardFilterState
type ChatPageSignal = signalcontracts.ChatPageSignal
type ChromeSignal = signalcontracts.ChromeSignal
type SidebarActionSignal = signalcontracts.SidebarActionSignal
type SidebarGroupSignal = signalcontracts.SidebarGroupSignal
type SidebarHistoryItemSignal = signalcontracts.SidebarHistoryItemSignal
type SidebarHistorySignal = signalcontracts.SidebarHistorySignal
type SidebarItemSignal = signalcontracts.SidebarItemSignal
type SidebarSignal = signalcontracts.SidebarSignal

const RouteChat RouteKind = "chat"

type ChatViewState struct {
	Agent   ChatSignal
	Visuals map[string]visualizationir.VisualizationEnvelope
}

func Optional[T comparable](value T) *T {
	var zero T
	if value == zero {
		return nil
	}
	return &value
}

func Pointer[T any](value T) *T {
	return &value
}

func ChatTranscriptItems(items []agent.ChatTranscriptItem) []ChatTranscriptItemSignal {
	out := make([]ChatTranscriptItemSignal, 0, len(items))
	for _, item := range items {
		out = append(out, chatTranscriptItem(item))
	}
	return out
}

func chatTranscriptItem(item agent.ChatTranscriptItem) ChatTranscriptItemSignal {
	out := ChatTranscriptItemSignal{
		ID: item.ID, Kind: item.Kind, Text: Optional(item.Text), Markdown: Optional(item.Markdown),
		ToolCallID: Optional(item.ToolCallID), Name: Optional(item.Name), Title: Optional(item.Title),
		Status: Optional(item.Status), Summary: Optional(item.Summary), ResultSummary: Optional(item.ResultSummary),
		InputJSON: Optional(item.InputJSON), InputFormat: Optional(item.InputFormat), ArgumentsJSON: Optional(item.ArgumentsJSON),
		ResultJSON: Optional(item.ResultJSON), ResultFormat: Optional(item.ResultFormat), Error: Optional(item.Error),
		ConversationID: Optional(item.ConversationID), RunID: Optional(item.RunID), CreatedAt: Optional(item.CreatedAt),
	}
	if len(item.References) > 0 {
		references := make([]AgentReferenceSignal, 0, len(item.References))
		for _, reference := range item.References {
			references = append(references, referenceSignalFromTurn(reference))
		}
		out.References = &references
	}
	if item.Artifact != nil {
		out.Artifact = &ChatArtifactSignal{Type: item.Artifact.Type, ID: item.Artifact.ID, Summary: Optional(item.Artifact.Summary)}
	}
	return out
}

func referenceSignalFromTurn(reference agent.TurnReference) AgentReferenceSignal {
	locations := make([]AgentReferenceLocationSignal, 0, len(reference.Locations))
	for _, location := range reference.Locations {
		locations = append(locations, AgentReferenceLocationSignal{
			DashboardID: Optional(location.DashboardID), DashboardName: Optional(location.DashboardName),
			PageID: Optional(location.PageID), PageName: Optional(location.PageName), Href: location.Href,
		})
	}
	hierarchy := append([]string(nil), reference.Hierarchy...)
	if len(hierarchy) == 0 {
		appendUnique := func(value string) {
			value = strings.TrimSpace(value)
			if value != "" && (len(hierarchy) == 0 || hierarchy[len(hierarchy)-1] != value) {
				hierarchy = append(hierarchy, value)
			}
		}
		appendUnique(reference.Resource.Name)
		if len(reference.Locations) > 0 {
			if reference.Reference.Kind == "page" || reference.Reference.Kind == "visual" {
				appendUnique(reference.Locations[0].DashboardName)
			}
			if reference.Reference.Kind == "visual" {
				appendUnique(reference.Locations[0].PageName)
			}
		}
	}
	return AgentReferenceSignal{
		Reference: AgentReferenceKeySignal{ProjectID: reference.Resource.ID, Type: reference.Reference.Kind, ID: reference.Reference.ID},
		Name:      reference.Name, Description: Optional(reference.Description), VisualType: Optional(reference.VisualType),
		Project:   AgentReferenceProjectSignal{ID: reference.Resource.ID, Name: reference.Resource.Name},
		Hierarchy: hierarchy, Href: reference.Href, Locations: locations, Context: append([]string(nil), reference.Context...),
	}
}

func chatInitialSignals(projectID, view string, state ChatViewState) map[string]any {
	return map[string]any{
		"page": ChatPageSignal{
			Kind: RouteChat, View: normalizedView(view), Title: "Chats",
			Description: "Ask about governed BI or make authorized dashboard changes.",
		},
		"runtime": RouteRuntimeSignal{Kind: RouteChat, ProjectID: Optional(projectID)},
		"agent":   state.Agent,
		"agentContext": AgentContextSignal{
			Surface: "chat",
			Filters: DashboardFilterState{
				AppliedControls: map[string]signalcontracts.DashboardAppliedFilterState{},
				DraftControls:   map[string]signalcontracts.DashboardFilterExpression{},
				DirtyBindings:   []string{},
			},
			ReferenceLimit: agent.MaxTurnReferences, References: []AgentReferenceSignal{},
		},
		"agentReferenceSearch": AgentReferenceSearchSignal{Results: []AgentReferenceSignal{}},
		"visuals":              state.Visuals,
	}
}

func chatHistoryItems(state ChatSignal) []SidebarHistoryItemSignal {
	items := make([]SidebarHistoryItemSignal, 0, len(state.Conversations))
	for _, conversation := range state.Conversations {
		title := conversation.Title
		if title == "" {
			title = "Conversation"
		}
		items = append(items, SidebarHistoryItemSignal{
			ID: conversation.ID, Title: title, Href: chatPath(conversation.ID),
			Active: conversation.ID == state.ActiveConversationID, Pending: conversation.TitlePending,
		})
	}
	return items
}

func chatPath(parts ...string) string {
	path := "/chats"
	for _, part := range parts {
		if part = strings.Trim(part, "/"); part != "" {
			path += "/" + url.PathEscape(part)
		}
	}
	return path
}

func normalizedView(view string) string {
	if strings.TrimSpace(view) == "" {
		return "conversation"
	}
	return view
}
