package module

import "context"

import projectgraph "github.com/flidai/leapview/internal/project/graph"

// Publisher adapts refresh publication notifications to durable serving identity.
type Publisher struct {
	RefreshTarget        func(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID)
	SemanticModelVersion func(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID)
}

func (p Publisher) PublishRefreshTarget(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) {
	if p.RefreshTarget != nil {
		p.RefreshTarget(ctx, identity, targetType, targetID)
	}
}

func (p Publisher) PublishSemanticModelVersion(ctx context.Context, identity projectgraph.ServingIdentity, modelID projectgraph.ResourceID) {
	if p.SemanticModelVersion != nil {
		p.SemanticModelVersion(ctx, identity, modelID)
	}
}
