package module

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
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
	// The governed runtime owns semantic projection decisions and forwards
	// this capability through its admission and audit decorators.
	authority, ok := metrics.(interface {
		AuthorizeSemanticModelProjection(context.Context, string) error
	})
	if !ok {
		return errors.New("semantic projection authority is unavailable")
	}
	return authority.AuthorizeSemanticModelProjection(ctx, resolvedModel.String())
}
