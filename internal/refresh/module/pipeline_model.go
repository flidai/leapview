package module

import (
	"context"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/servingstate"
)

type ActiveArtifactReader interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
}

func PipelineModelResolver(states ActiveArtifactReader, artifacts refreshrun.ArtifactLoader) func(context.Context, projectgraph.ServingIdentity, string) (string, bool, error) {
	return func(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID string) (string, bool, error) {
		if states == nil || artifacts == nil {
			return "", false, nil
		}
		_, artifact, err := states.ActiveArtifact(ctx, identity.ProjectID, servingstate.Environment(identity.Environment))
		if err != nil {
			return "", false, err
		}
		loaded, err := artifacts.Load(ctx, artifact)
		if err != nil {
			return "", false, err
		}
		if loaded.Definition == nil {
			return "", false, nil
		}
		pipeline, ok := loaded.Definition.Pipelines[pipelineID]
		if !ok {
			return "", false, nil
		}
		return pipeline.SemanticModelID.String(), true, nil
	}
}
