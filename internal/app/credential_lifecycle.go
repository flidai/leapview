package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/flidai/leapview/internal/access"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/credentialagent"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
)

type credentialLifecycle struct {
	services    *credentialmodule.Services
	gate        *credentialmodule.ProviderAdmission
	agent       *credentialagent.AgentCredentials
	coordinator *credentialmodule.ActivationCoordinator
}

func newCredentialLifecycle(ctx context.Context, services *credentialmodule.Services, config credentialagent.AgentCredentialConfig, analytics *analyticsmodule.Module) (*credentialLifecycle, error) {
	if services == nil {
		return nil, nil
	}
	config.AuthorizeTx = newAgentCredentialAuthority(config.InstanceID)
	config.Authorize = func(ctx context.Context, actor string, pair access.PermissionPair) error {
		tx, err := config.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		return config.AuthorizeTx(ctx, tx, actor, pair)
	}
	config.Probe = agentmodule.TestProviderConnection
	agent, err := credentialagent.NewAgentCredentials(ctx, services.ActivationRepository(), config)
	if err != nil {
		return nil, err
	}
	lifecycle := &credentialLifecycle{services: services, gate: credentialmodule.NewProviderAdmission(), agent: agent}
	if err = analytics.ConfigureLocalCredentials(func(ctx context.Context, binding connectionbinding.TargetBinding, version string, consume func(map[string]string) error) error {
		return services.Runtime.UseVersion(ctx, credentialmodule.ValidationResource{ScopeKind: "connection", ProjectID: binding.Scope.ProjectID.String(), Environment: binding.Scope.Environment, TargetID: binding.TargetID.String(), ResourceID: binding.ConnectionID.String()}, version, consume)
	}, lifecycle.gate); err != nil {
		return nil, err
	}
	return lifecycle, nil
}
func (l *credentialLifecycle) configure(source credentialmodule.ActivationAuthority, runtime credentialmodule.ActivationRuntime, instanceID string) error {
	if l == nil {
		return nil
	}
	routing, err := credentialmodule.NewActivationRouting(l.services, instanceID, source, l.agent, runtime, l.agent)
	if err != nil {
		return err
	}
	l.coordinator, err = credentialmodule.NewActivationCoordinator(routing, routing, l.gate, 30*time.Second)
	if err != nil {
		return err
	}
	return l.agent.SetCoordinator(l.coordinator)
}
func (l *credentialLifecycle) Start(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if l.coordinator == nil {
		return errors.New("credential lifecycle is not configured")
	}
	if err := l.coordinator.Reconcile(ctx); err != nil {
		// Keep authenticated operation status and recovery available. Provider work
		// and readiness remain closed; retries can repair the exact durable state.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("customer credential reconciliation requires operator recovery")
	}
	return nil
}
func (l *credentialLifecycle) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return l.gate.Pause(ctx)
}
func (l *credentialLifecycle) configureHealth(h *health) {
	if l == nil || h == nil {
		return
	}
	if h.config.Checks == nil {
		h.config.Checks = map[string]func(context.Context) error{}
	}
	h.config.Checks["customerCredentials"] = func(context.Context) error {
		if !l.gate.Ready() {
			return errors.New("credential provider work paused")
		}
		return nil
	}
}

func (l *credentialLifecycle) agentCredentials() agentmodule.ConfigurationCredentials {
	if l == nil {
		return nil
	}
	return l.agent
}
func (l *credentialLifecycle) providerAdmission() agentmodule.ProviderAdmission {
	if l == nil {
		return nil
	}
	return l.gate
}
func (l *credentialLifecycle) activationService() credentialmodule.ActivationService {
	if l == nil {
		return nil
	}
	return l.coordinator
}
