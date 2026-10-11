package app

import (
	"bytes"
	"html"
	"strings"
	"testing"

	catalog "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestExplorerAgentCommandsResolveAfterRuntimeModuleConstruction(t *testing.T) {
	// The default composition starts without an agent module. Its browser handler
	// is constructed first; static identities must remain valid while the module is built.
	server, err := assembleRuntimeChecked(t.Context(), fakeMetrics{}, assemblyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if server.routes.agentModule == nil || server.routes.projectBrowser == nil {
		t.Fatal("runtime did not compose the agent and Explorer")
	}
	commands := server.routes.projectBrowser.AgentCommands
	if commands.CreateConversation.OperationID() != "createAgentConversation" || commands.CreateRun.OperationID() != "createAgentRun" || commands.CancelRun.OperationID() != "cancelAgentRun" {
		t.Fatalf("Explorer bound commands before agent initialization: create=%q run=%q cancel=%q", commands.CreateConversation.OperationID(), commands.CreateRun.OperationID(), commands.CancelRun.OperationID())
	}
	var rendered bytes.Buffer
	page := projectui.DataExplorerPageWithAgentAndSavedExplorationsAndDashboard(catalog.Catalog{}, projectsignals.DataExplorerPageSignal{Kind: "data", Title: "Data Explorer"}, projectsignals.DataExplorerSignal{}, projectui.DataExplorerAgentBootstrap{}, commands, projectui.DataExplorerSavedExplorationBootstrap{}, projectui.DataExplorerDashboardBootstrap{}, "synthetic-csrf")
	if err := page.Render(&rendered); err != nil {
		t.Fatal(err)
	}
	document := html.UnescapeString(rendered.String())
	for _, expected := range []string{"$agent.activeConversationId ? ['createAgentRun'] : ['createAgentConversation', 'createAgentRun']", "headers('cancelAgentRun')"} {
		if !strings.Contains(document, expected) {
			t.Errorf("rendered Explorer command is missing %q", expected)
		}
	}
	if strings.Contains(document, "headers(($agent.activeConversationId ? [''] : ['']))") {
		t.Fatal("Explorer still emits empty command identities")
	}
}
