package module

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Retained envelopes contain data, so conversation ownership alone is not
// sufficient. Recheck current resource and semantic projection access using
// the same consumer boundary as dashboard projections. A visual may span
// multiple datasets and need not have a single physical dataset identity.
func (m *Module) authorizeRetainedVisual(ctx context.Context, scope agent.Scope, modelID string) error {
	projectID, err := m.activeProjectID(ctx)
	if err != nil {
		return err
	}
	scope.ProjectID = projectID
	resolvedModel, err := m.resolveContextResource(ctx, scope, modelID, projectgraph.KindSemanticModel, access.CapabilityResourceUse)
	if err != nil {
		return errors.New("semantic model is unknown or unauthorized")
	}
	if !CredentialAllowsResource(contextModuleScope(scope, projectID), resolvedModel, projectgraph.KindSemanticModel, access.CapabilityResourceUse) {
		return errors.New("credential cannot view this visual")
	}
	if m.dashboardMetrics == nil {
		return errors.New("semantic runtime is unavailable")
	}
	metrics, ok := m.dashboardMetrics(projectID)
	if !ok || metrics == nil {
		return errors.New("semantic runtime is unavailable")
	}
	authority := queryauthz.SemanticAuthorizationAdapter{
		Model: metrics.SemanticModel, RequireConsumerModelID: true,
	}
	if provider, ok := metrics.(interface {
		SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
	}); ok {
		authority.Consumer = provider.SemanticConsumer
	}
	return authority.AuthorizeSemanticModelProjection(ctx, resolvedModel.String())
}
