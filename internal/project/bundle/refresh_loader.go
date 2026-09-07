package bundle

import (
	"context"
	"os"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/servingstate"
)

// RefreshArtifactLoader decodes the refresh projection from a persisted
// project bundle without exposing the bundle layout to the refresh capability.
type RefreshArtifactLoader struct {
	Serving ServingArtifactLoader
}

func (l RefreshArtifactLoader) Load(ctx context.Context, artifact servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
	root, err := os.MkdirTemp("", "leapview-refresh-artifact-*")
	if err != nil {
		return refreshrun.LoadedArtifact{}, err
	}
	defer os.RemoveAll(root)
	compiled, err := l.Serving.LoadCompiled(ctx, artifact, root)
	if err != nil {
		return refreshrun.LoadedArtifact{}, err
	}
	return refreshrun.LoadedArtifact{
		Definition: projectartifact.RefreshProjection(compiled.Manifest),
		Graph:      compiled.Graph,
	}, nil
}
