package module

import "context"

import projectgraph "github.com/flidai/leapview/internal/project/graph"

// Publisher adapts refresh-owned workspace presentation and an optional
// semantic-model notification port to the durable refresh workflow.
type Publisher struct {
	Workspace            func() WorkspaceSupport
	SemanticModelVersion func(context.Context, string, string, string)
}

func (p Publisher) PublishRefreshTarget(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) {
	if p.Workspace != nil {
		p.Workspace().PublishWorkspaceAssetRefreshPatchesForTarget(ctx, identity.ProjectID.String(), identity.Environment, targetType, targetID.String())
	}
}

func (p Publisher) PublishSemanticModelVersion(ctx context.Context, workspaceID, environment, modelID string) {
	if p.SemanticModelVersion != nil {
		p.SemanticModelVersion(ctx, workspaceID, environment, modelID)
	}
}
