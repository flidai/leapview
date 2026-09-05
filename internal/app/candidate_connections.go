package app

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/release"
)

type candidateConnectionLeaser struct {
	leaser *analyticsmodule.RuntimeBindingLeaser
	module *analyticsmodule.Module
}

var _ deploymentmodule.CandidateConnectionEvidenceResolver = candidateConnectionLeaser{}

func (adapter candidateConnectionLeaser) Acquire(
	ctx context.Context,
	request deploymentmodule.CandidateConnectionRequest,
) (deploymentmodule.CandidateConnectionLeases, error) {
	requirements := make([]analyticsmodule.ConnectionRequirement, 0, len(request.Requirements))
	for _, requirement := range request.Requirements {
		requirements = append(requirements, analyticsmodule.ConnectionRequirement{
			ConnectionID: requirement.ConnectionID, ConnectorKind: requirement.ConnectorKind, Access: requirement.Access,
		})
	}
	leases, err := adapter.leaser.Acquire(ctx, analyticsmodule.RuntimeBindingRequest{
		Actor:    request.Actor,
		Identity: request.Identity, TargetID: connectionbinding.TargetID(request.TargetID), Requirements: requirements,
	})
	if err != nil {
		return nil, err
	}
	registration, err := adapter.module.BindCandidateRuntime(
		request.CandidateID,
		request.Identity.ProjectID,
		leases,
		candidateAuthoredConnections(request.AuthoredConnections),
	)
	if err != nil {
		_ = leases.Close()
		return nil, err
	}
	return candidateConnectionLeases{RuntimeBindingRegistration: registration}, nil
}

// Resolve returns durable, non-secret binding evidence for a candidate
// request. It deliberately bypasses pool acquisition and candidate-runtime
// registration; callers use it only for compatibility or fingerprint checks.
func (adapter candidateConnectionLeaser) Resolve(
	ctx context.Context,
	request deploymentmodule.CandidateConnectionRequest,
) ([]deploymentmodule.CandidateConnectionEvidence, error) {
	requirements := make([]analyticsmodule.ConnectionRequirement, 0, len(request.Requirements))
	for _, requirement := range request.Requirements {
		requirements = append(requirements, analyticsmodule.ConnectionRequirement{
			ConnectionID: requirement.ConnectionID, ConnectorKind: requirement.ConnectorKind, Access: requirement.Access,
		})
	}
	evidence, err := adapter.leaser.Inspect(ctx, analyticsmodule.RuntimeBindingRequest{
		Actor:    request.Actor,
		Identity: request.Identity, TargetID: connectionbinding.TargetID(request.TargetID), Requirements: requirements,
	})
	if err != nil {
		return nil, err
	}
	return candidateRuntimeConnectionEvidence(evidence), nil
}

func candidateAuthoredConnections(
	values []deploymentmodule.CandidateAuthoredConnection,
) []analyticsmodule.CandidateAuthoredConnection {
	result := make([]analyticsmodule.CandidateAuthoredConnection, len(values))
	for index, value := range values {
		result[index] = analyticsmodule.CandidateAuthoredConnection{
			ConnectionID:  value.ConnectionID,
			ConnectorKind: value.ConnectorKind,
			Access:        value.Access,
		}
	}
	return result
}

func candidateReleaseAuthoredConnections(values []release.CandidateAuthoredConnection) []deploymentmodule.CandidateAuthoredConnection {
	result := make([]deploymentmodule.CandidateAuthoredConnection, len(values))
	for i, value := range values {
		result[i] = deploymentmodule.CandidateAuthoredConnection{ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind, Access: value.Access}
	}
	return result
}

func candidateConnectionRequirements(values []release.CandidateConnectionRequirement) []deploymentmodule.CandidateConnectionRequirement {
	result := make([]deploymentmodule.CandidateConnectionRequirement, len(values))
	for i, value := range values {
		result[i] = deploymentmodule.CandidateConnectionRequirement{ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind, Access: value.Access}
	}
	return result
}

func candidateManagedDataConnections(values []release.ManagedDataPin) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.ConnectionID
	}
	return result
}

func candidateRuntimeRestrictions(values []release.CandidateRestriction) []deploymentmodule.CandidateRestriction {
	result := make([]deploymentmodule.CandidateRestriction, len(values))
	for i, value := range values {
		result[i] = deploymentmodule.CandidateRestriction{ID: value.ID, ObjectID: value.ObjectID, ObjectKind: value.ObjectKind, Subject: value.Subject, PolicyType: value.PolicyType, ExpressionJSON: value.ExpressionJSON}
	}
	return result
}

type candidateConnectionLeases struct {
	*analyticsmodule.RuntimeBindingRegistration
}

func (leases candidateConnectionLeases) Evidence() []deploymentmodule.CandidateConnectionEvidence {
	source := leases.RuntimeBindingRegistration.Evidence()
	return candidateConnectionEvidence(source)
}

func candidateConnectionEvidence(source []analyticsmodule.ConnectionBindingEvidence) []deploymentmodule.CandidateConnectionEvidence {
	result := make([]deploymentmodule.CandidateConnectionEvidence, 0, len(source))
	for _, evidence := range source {
		result = append(result, deploymentmodule.CandidateConnectionEvidence{
			BindingID: evidence.BindingID.String(), ConnectionID: evidence.ConnectionID,
			ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision, Access: evidence.Access,
			ProviderVersion: evidence.ValidatedVersion, EndpointConfigHash: evidence.EndpointConfigHash,
		})
	}
	return result
}

func candidateRuntimeConnectionEvidence(source []connectionbinding.RuntimeBindingEvidence) []deploymentmodule.CandidateConnectionEvidence {
	values := make([]analyticsmodule.ConnectionBindingEvidence, 0, len(source))
	for _, evidence := range source {
		values = append(values, evidence.BindingEvidence)
	}
	return candidateConnectionEvidence(values)
}
