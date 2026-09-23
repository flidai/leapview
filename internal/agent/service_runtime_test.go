package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestServiceRuntimeEnablementDoesNotRequireRestart(t *testing.T) {
	service := NewService(nil, Config{APIKey: "key", Model: "model"}, WithModel(newRecordingAgentModel()))
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	if !service.Configured() || !service.Enabled() {
		t.Fatal("configured service did not start enabled")
	}
	if err := service.ApplyRuntimeConfig(Config{APIKey: "key", Model: "model"}, false); err != nil {
		t.Fatal(err)
	}
	if service.Enabled() {
		t.Fatal("runtime-disabled service remained enabled")
	}
	if err := service.ApplyRuntimeConfig(Config{APIKey: "key", Model: "model"}, true); err != nil {
		t.Fatal(err)
	}
	if !service.Enabled() {
		t.Fatal("runtime-enabled service remained disabled")
	}

	unconfigured := NewService(nil, Config{Model: "model"})
	unconfigured.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	if err := unconfigured.ApplyRuntimeConfig(Config{Model: "model"}, true); err == nil {
		t.Fatal("invalid enabled configuration succeeded")
	}
	if unconfigured.Configured() || unconfigured.Enabled() {
		t.Fatal("runtime setting enabled a service without provider credentials")
	}
}

func TestServiceRuntimeReloadKeepsActiveRequestsOnTheirCapturedModel(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	service := NewService(nil, Config{})
	service.ConfigureDefaultModel(func(config Config) agentcore.Model {
		if config.Model == "model-a" {
			return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
				close(entered)
				<-release
				return agentcore.ModelResponse{Content: "model-a"}, nil
			})
		}
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			return agentcore.ModelResponse{Content: config.Model}, nil
		})
	})
	if err := service.ApplyRuntimeConfig(Config{APIKey: "secret-a", Model: "model-a"}, true); err != nil {
		t.Fatal(err)
	}
	active := service.runtimeSnapshot()
	activeResult := make(chan agentcore.ModelResponse, 1)
	go func() {
		response, _ := active.model.Complete(context.Background(), agentcore.ModelRequest{}, nil)
		activeResult <- response
	}()
	<-entered

	if err := service.ApplyRuntimeConfig(Config{APIKey: "secret-b", Model: "model-b"}, true); err != nil {
		t.Fatal(err)
	}
	current := service.runtimeSnapshot()
	response, err := current.model.Complete(context.Background(), agentcore.ModelRequest{}, nil)
	if err != nil || response.Content != "model-b" {
		t.Fatalf("new request response=%+v err=%v", response, err)
	}
	close(release)
	if response := <-activeResult; response.Content != "model-a" {
		t.Fatalf("active request moved to replacement model: %+v", response)
	}
}

func TestServiceProviderFailureDegradesWithoutDisabling(t *testing.T) {
	failing := true
	service := NewService(nil, Config{})
	service.ConfigureDefaultModel(func(Config) agentcore.Model {
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			if failing {
				return agentcore.ModelResponse{}, errors.New("temporary provider failure")
			}
			return agentcore.ModelResponse{Content: "recovered"}, nil
		})
	})
	if err := service.ApplyRuntimeConfig(Config{APIKey: "secret", Model: "model"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.runtimeSnapshot().model.Complete(context.Background(), agentcore.ModelRequest{}, nil); err == nil {
		t.Fatal("provider failure was not returned")
	}
	status := service.RuntimeStatus()
	if status.State != AgentRuntimeDegraded || !status.Enabled {
		t.Fatalf("provider failure status = %+v", status)
	}
	failing = false
	if _, err := service.runtimeSnapshot().model.Complete(context.Background(), agentcore.ModelRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if status := service.RuntimeStatus(); status.State != AgentRuntimeEnabled || !status.Enabled {
		t.Fatalf("recovered provider status = %+v", status)
	}

	service.ReportRuntimeConfigError()
	failing = true
	_, _ = service.runtimeSnapshot().model.Complete(context.Background(), agentcore.ModelRequest{}, nil)
	failing = false
	_, _ = service.runtimeSnapshot().model.Complete(context.Background(), agentcore.ModelRequest{}, nil)
	if status := service.RuntimeStatus(); status.State != AgentRuntimeDegraded || !strings.Contains(status.Detail, "last known-good") {
		t.Fatalf("provider recovery cleared configuration error: %+v", status)
	}
	if err := service.ApplyRuntimeConfig(Config{APIKey: "secret", Model: "model"}, true); err != nil {
		t.Fatal(err)
	}
	if status := service.RuntimeStatus(); status.State != AgentRuntimeEnabled {
		t.Fatalf("valid reload did not clear prior degradation: %+v", status)
	}
}
