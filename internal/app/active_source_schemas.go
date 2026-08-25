package app

import (
	"context"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

// activeSourceSchemaEvidenceSource projects qualification evidence from the
// exact published serving generation. Source details never open a connection
// or inspect mutable external data during a browser request.
type activeSourceSchemaEvidenceSource struct {
	releases servingStateProvenanceReader
	targetID string
}

func (source activeSourceSchemaEvidenceSource) SourceSchemaObservation(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	environment string,
	servingStateID string,
	sourceID projectgraph.ResourceID,
) (projecthttp.SourceSchemaObservation, bool, error) {
	if source.releases == nil {
		return projecthttp.SourceSchemaObservation{}, false, releasemodule.ErrNotFound
	}
	identity, err := projectgraph.NewServingIdentity(projectID, environment, servingStateID)
	if err != nil {
		return projecthttp.SourceSchemaObservation{}, false, err
	}
	provenance, err := source.releases.ProvenanceForServingState(ctx, identity)
	if err != nil {
		return projecthttp.SourceSchemaObservation{}, false, err
	}
	if provenance.Plan.TargetID != strings.TrimSpace(source.targetID) || provenance.Plan.Identity != identity {
		return projecthttp.SourceSchemaObservation{}, false, fmt.Errorf("%w: release target does not match source schema request", releasemodule.ErrProvenanceInvalid)
	}
	if provenance.Plan.GateEvidence == nil {
		return projecthttp.SourceSchemaObservation{}, false, releasemodule.ErrProvenanceInvalid
	}
	for _, evidence := range provenance.Plan.GateEvidence.Sources {
		if evidence.ID != sourceID.String() {
			continue
		}
		return projecthttp.SourceSchemaObservation{
			Schema: semanticmodel.TableSchema{Columns: append([]semanticmodel.ColumnSchema(nil), evidence.ObservedSchema...)},
			Mode:   evidence.Mode, Status: string(evidence.SchemaOutcome),
			ObservedAt: provenance.Plan.GateEvidence.EvaluatedAt, SchemaDigest: evidence.SchemaDigest,
		}, true, nil
	}
	return projecthttp.SourceSchemaObservation{}, false, nil
}
