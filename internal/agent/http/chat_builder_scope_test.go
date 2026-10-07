package http

import (
	"testing"

	"github.com/flidai/leapview/internal/agent"
)

func TestBuilderToolScopeRequiresResolvedBuilderContext(t *testing.T) {
	base := agent.Scope{BuilderDashboardID: "stale", BuilderDraftID: "stale"}
	chat := builderToolScope(base, nil)
	if chat.BuilderDashboardID != "" || chat.BuilderDraftID != "" {
		t.Fatalf("chat inherited builder authoring target: %#v", chat)
	}
	other := builderToolScope(base, &agent.TurnContext{Surface: "dashboard", DashboardID: "dashboard-1", DraftID: "draft-1"})
	if other.BuilderDashboardID != "" || other.BuilderDraftID != "" {
		t.Fatalf("non-builder context gained authoring target: %#v", other)
	}
	builder := builderToolScope(base, &agent.TurnContext{Surface: "dashboard_builder", DashboardID: "dashboard-1", DraftID: "draft-1"})
	if builder.BuilderDashboardID != "dashboard-1" || builder.BuilderDraftID != "draft-1" {
		t.Fatalf("resolved builder target was not bound: %#v", builder)
	}
}
