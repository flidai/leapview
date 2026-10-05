package configreload

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestFileReloaderAppliesChangesAndRetainsLastKnownGoodConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	writeConfig(t, path, `{"enabled":true,"apiKey":"secret-one","model":"model-a","baseUrl":"https://provider.example/v1","reasoningEffort":"high"}`)

	service := agent.NewService(nil, agent.Config{})
	var mu sync.Mutex
	var seen []agent.Config
	service.ConfigureDefaultModel(func(config agent.Config) agentcore.Model {
		mu.Lock()
		seen = append(seen, config)
		mu.Unlock()
		return responseModel(config.Model)
	})
	reloader, err := NewFileReloader(path, service, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	if changed, err := reloader.Reload(); err != nil || !changed {
		t.Fatalf("initial reload changed=%v err=%v", changed, err)
	}
	assertStatus(t, service.RuntimeStatus(), agent.AgentRuntimeEnabled, true, true, "model-a", "high")

	writeConfig(t, path, `{"enabled":true,"apiKey":"secret-two","model":"model-a","baseUrl":"https://provider.example/v1","reasoningEffort":"high"}`)
	if changed, err := reloader.Reload(); err != nil || !changed {
		t.Fatalf("secret rotation changed=%v err=%v", changed, err)
	}
	mu.Lock()
	last := seen[len(seen)-1]
	mu.Unlock()
	if last.APIKey != "secret-two" {
		t.Fatalf("model factory did not receive rotated secret")
	}

	writeConfig(t, path, `{"enabled":true,"apiKey":"secret-three","model":"model-b","reasoningEffort":"max"}`)
	if _, err := reloader.Reload(); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, service.RuntimeStatus(), agent.AgentRuntimeEnabled, true, true, "model-b", "max")

	writeConfig(t, path, `{"enabled":true,"model":"invalid-model","reasoningEffort":"high"}`)
	if changed, err := reloader.Reload(); err == nil || changed {
		t.Fatalf("invalid reload changed=%v err=%v", changed, err)
	}
	assertStatus(t, service.RuntimeStatus(), agent.AgentRuntimeDegraded, true, true, "model-b", "max")

	writeConfig(t, path, `{"enabled":false,"apiKey":"secret-four","model":"model-c","reasoningEffort":"low"}`)
	if _, err := reloader.Reload(); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, service.RuntimeStatus(), agent.AgentRuntimeDisabled, true, false, "model-c", "low")
}

func TestFileReloaderWatchesForAtomicReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.json")
	writeConfig(t, path, `{"enabled":false}`)
	service := agent.NewService(nil, agent.Config{})
	service.ConfigureDefaultModel(func(config agent.Config) agentcore.Model { return responseModel(config.Model) })
	reloader, err := NewFileReloader(path, service, nil, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloader.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reloader.Run(ctx)

	replacement := filepath.Join(directory, "agent.next")
	writeConfig(t, replacement, `{"enabled":true,"apiKey":"rotated-secret","model":"model-live","reasoningEffort":"xhigh"}`)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := service.RuntimeStatus()
		if status.State == agent.AgentRuntimeEnabled && status.Model == "model-live" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher did not apply replacement: %+v", service.RuntimeStatus())
}

func TestFileReloaderRejectsWritableSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	writeConfig(t, path, `{"enabled":false}`)
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	service := agent.NewService(nil, agent.Config{})
	reloader, err := NewFileReloader(path, service, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloader.Reload(); err == nil {
		t.Fatal("group/world-writable agent configuration was accepted")
	}
}

func responseModel(content string) agentcore.Model {
	return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: content}, nil
	})
}

func writeConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertStatus(t *testing.T, status agent.AgentRuntimeStatus, state agent.AgentRuntimeState, configured, enabled bool, model, effort string) {
	t.Helper()
	if status.State != state || status.Configured != configured || status.Enabled != enabled || status.Model != model || status.ReasoningEffort != effort {
		t.Fatalf("status = %+v", status)
	}
}

func TestFileReloaderCannotOverrideAdministratorOwnership(t *testing.T) {
	service := agent.NewService(nil, agent.Config{})
	service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			return agentcore.ModelResponse{}, nil
		})
	})
	if err := service.ApplyRuntimeConfig(agent.Config{Revision: 1, Model: "admin-model", APIKey: "key"}, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "missing.json")
	reloader, err := NewFileReloader(path, service, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := reloader.Reload()
	if err != nil || changed {
		t.Fatalf("admin-owned agent still read legacy file: changed=%t err=%v", changed, err)
	}
	if service.RuntimeStatus().State != agent.AgentRuntimeDisabled || service.RuntimeStatus().Model != "admin-model" {
		t.Fatal("file watcher changed administrator configuration")
	}
}
