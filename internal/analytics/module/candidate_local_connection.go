package module

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// WithLocalRuntimeConnection uses one exact local credential snapshot through
// the private target-pool factory. SourceRuntime owns source-work admission;
// this method validates the target binding but does not acquire another gate.
func (m *Module) WithLocalRuntimeConnection(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
	logical semanticmodel.Connection,
	consume func(semanticmodel.Connection) error,
) error {
	if ctx == nil || m == nil || m.connectionBindings == nil || m.connectionFactory == nil || consume == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	localFactory, ok := m.connectionFactory.(localRuntimeConnectionConsumer)
	if !ok {
		return connectionbinding.ErrProviderUnavailable
	}
	identity := snapshot.Identity()
	if identity.Validate() != nil || identity.CredentialVersionID == "" {
		return connectionbinding.ErrInvalidCredentialBundle
	}
	if err := validateLocalRuntimeCredentialInput(m, binding, identity.CredentialVersionID); err != nil {
		return err
	}
	if logical.Kind != binding.ConnectorKind || logical.Access != "" {
		return connectionbinding.ErrIncompatibleBinding
	}
	current, err := m.currentCredentialProbeBinding(ctx, binding)
	if err != nil {
		return err
	}
	return localFactory.WithLocalConnection(ctx, current, snapshot, logical, consume)
}
