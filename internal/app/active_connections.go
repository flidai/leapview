package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

type servingStateProvenanceReader interface {
	ProvenanceForServingState(context.Context, projectgraph.ServingIdentity) (releasemodule.Provenance, error)
}

type activeConnectionEvidenceSource struct {
	releases    servingStateProvenanceReader
	targetID    string
	environment string
}

func (source activeConnectionEvidenceSource) BindingEvidence(
	ctx context.Context,
	servingStateID string,
	projectID string,
) ([]analyticsmodule.ActiveRuntimeBindingEvidence, error) {
	if source.releases == nil {
		return nil, releasemodule.ErrNotFound
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), source.environment, servingStateID)
	if err != nil {
		return nil, err
	}
	provenance, err := source.releases.ProvenanceForServingState(ctx, identity)
	if err != nil {
		return nil, err
	}
	if provenance.Plan.TargetID != strings.TrimSpace(source.targetID) ||
		provenance.Plan.Identity.ProjectID != identity.ProjectID ||
		provenance.Plan.Identity.Environment != identity.Environment {
		return nil, fmt.Errorf("%w: release target does not match runtime target", releasemodule.ErrProvenanceInvalid)
	}
	result := make([]analyticsmodule.ActiveRuntimeBindingEvidence, len(provenance.Plan.Bindings))
	for index, evidence := range provenance.Plan.Bindings {
		bindingID, parseErr := connectionbinding.ParseBindingID(evidence.BindingID)
		connectionID, connectionErr := connectionbinding.ParseConnectionID(evidence.ConnectionID)
		if parseErr != nil || connectionErr != nil {
			return nil, fmt.Errorf("%w: invalid release binding evidence", releasemodule.ErrProvenanceInvalid)
		}
		result[index] = analyticsmodule.ActiveRuntimeBindingEvidence{
			BindingID: bindingID, ConnectionID: connectionID,
			ConnectorKind: evidence.ConnectorKind, Revision: evidence.Revision,
			ValidatedVersion: evidence.ValidatedVersion, EndpointConfigHash: evidence.EndpointConfigHash,
		}
	}
	return result, nil
}
