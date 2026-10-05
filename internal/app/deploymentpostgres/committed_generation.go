package deploymentpostgres

import (
	"context"

	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type CommittedGenerationEvidence = nativepostgres.CommittedGenerationEvidence

// CommittedGeneration exposes delivery's durable commit proof at composition.
// It does not establish current workload authority or process readiness.
func (r *NativeReader) CommittedGeneration(ctx context.Context, targetID string, identity projectgraph.ServingIdentity) (CommittedGenerationEvidence, error) {
	authority, err := r.authority()
	if err != nil {
		return CommittedGenerationEvidence{}, err
	}
	return authority.CommittedGeneration(ctx, targetID, identity)
}
