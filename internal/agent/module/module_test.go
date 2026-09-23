package module

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
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

func TestBuildLoadsDeploymentManagedRuntimeAgentConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"enabled":true,"apiKey":"deployment-secret","model":"gpt-6-luna","reasoningEffort":"high"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := agent.NewService(nil, agent.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	module, err := Build(ctx, Config{
		Service:         service,
		ModelConfigFile: path,
		ProjectID:       projectgraph.ResourceID("project:agent-live-config"),
		RecordAudit:     func(context.Context, access.AuditEventInput) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if module.service != service {
		t.Fatal("module did not retain the configured service")
	}
	status := service.RuntimeStatus()
	if status.State != agent.AgentRuntimeEnabled || status.Model != "gpt-6-luna" || status.ReasoningEffort != "high" {
		t.Fatalf("runtime status = %+v", status)
	}
}
