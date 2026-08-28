package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
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
	provenance, err := source.provenance(ctx, servingStateID, projectID)
	if err != nil {
		return nil, err
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
			ValidatedVersion: evidence.ValidatedVersion, EndpointConfigHash: evidence.EndpointConfigHash, Access: evidence.Access,
		}
	}
	return result, nil
}

func (source activeConnectionEvidenceSource) ResultIdentityEvidence(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
) (appruntimefactory.ActivationEvidence, error) {
	if err := identity.Validate(); err != nil || identity.Environment != source.environment {
		return appruntimefactory.ActivationEvidence{}, fmt.Errorf("%w: result identity serving scope does not match runtime target", releasemodule.ErrProvenanceInvalid)
	}
	provenance, err := source.provenance(ctx, identity.GenerationID, identity.ProjectID.String())
	if err != nil {
		return appruntimefactory.ActivationEvidence{}, err
	}
	kinds := make(map[string]string, len(provenance.Plan.Bindings)+len(provenance.Plan.AuthoredConnections)+len(provenance.Plan.ManagedDataPins))
	for _, binding := range provenance.Plan.Bindings {
		kinds[binding.ConnectionID] = binding.ConnectorKind
	}
	for _, authored := range provenance.Plan.AuthoredConnections {
		kinds[authored.ConnectionID] = authored.ConnectorKind
	}
	for _, managed := range provenance.Plan.ManagedDataPins {
		kinds[managed.ConnectionID] = "managed"
	}
	return appruntimefactory.ActivationEvidence{
		RuntimeVersion:     provenance.Plan.RuntimeVersion,
		BindingFingerprint: release.BindingFingerprint(provenance.Plan.Bindings),
		BindingKinds:       kinds,
		Capabilities:       deployment.RuntimeCapabilityEvidence(provenance.Plan.Extensions),
	}, nil
}

func (source activeConnectionEvidenceSource) provenance(
	ctx context.Context,
	servingStateID string,
	projectID string,
) (releasemodule.Provenance, error) {
	if source.releases == nil {
		return releasemodule.Provenance{}, releasemodule.ErrNotFound
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), source.environment, servingStateID)
	if err != nil {
		return releasemodule.Provenance{}, err
	}
	provenance, err := source.releases.ProvenanceForServingState(ctx, identity)
	if err != nil {
		return releasemodule.Provenance{}, err
	}
	if provenance.Plan.TargetID != strings.TrimSpace(source.targetID) ||
		provenance.Plan.Identity != identity {
		return releasemodule.Provenance{}, fmt.Errorf("%w: release target does not match runtime target", releasemodule.ErrProvenanceInvalid)
	}
	return provenance, nil
}
