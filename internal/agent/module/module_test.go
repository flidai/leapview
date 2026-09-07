package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBuildRejectsEnabledAgentCommandsWithoutAuditRecorder(t *testing.T) {
	service := agent.NewService(nil, agent.Config{APIKey: "test", Model: "test"})
	if _, err := Build(t.Context(), Config{Service: service, ProjectID: projectgraph.ResourceID("project:agent-test")}); err == nil {
		t.Fatal("agent module accepted an enabled command service without an audit recorder")
	}
}

func TestBuildAllowsUnboundProjectUntilActiveResolverBinds(t *testing.T) {
	var active projectgraph.ResourceID
	module, err := Build(t.Context(), Config{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return active, nil },
	})
	if err != nil {
		t.Fatalf("unbound build failed: %v", err)
	}
	if _, err := module.activeProjectID(t.Context()); err == nil {
		t.Fatal("unbound project-dependent operation unexpectedly succeeded")
	}
	active = projectgraph.ResourceID("project:activated")
	if got, err := module.activeProjectID(t.Context()); err != nil || got != active.String() {
		t.Fatalf("resolved active project = %q, err=%v; want %q", got, err, active)
	}
}
