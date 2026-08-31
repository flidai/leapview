package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/release"
)

// acquireNativeCandidateBindings resolves the exact, non-secret connection
// evidence used by a candidate generation. Callers own the returned leases.
// Planning closes them immediately after fingerprinting; build keeps them only
// through physical materialization so qualification and recovery never retain
// or reacquire target credentials.
func acquireNativeCandidateBindings(
	ctx context.Context,
	connections deployment.CandidateConnectionLeaser,
	request deployment.CandidateConnectionRequest,
) (deployment.CandidateConnectionLeases, string, error) {
	empty, err := deployment.BindingFingerprint(nil)
	if err != nil {
		return nil, "", err
	}
	if len(request.Requirements) == 0 && len(request.AuthoredConnections) == 0 {
		return nil, empty, nil
	}
	if nativeBuildAuthorityNil(connections) {
		return nil, "", fmt.Errorf("%w: native candidate connection authority is required", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	leases, err := connections.Acquire(ctx, request)
	if err != nil {
		return nil, "", fmt.Errorf("acquire native candidate connections: %w", err)
	}
	if nativeBuildAuthorityNil(leases) {
		return nil, "", fmt.Errorf("%w: native candidate connection authority returned no leases", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	evidence := leases.Evidence()
	if err := validateNativeCandidateBindingEvidence(request, evidence); err != nil {
		_ = leases.Close()
		return nil, "", err
	}
	fingerprint, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		_ = leases.Close()
		return nil, "", fmt.Errorf("fingerprint native candidate connections: %w", err)
	}
	return leases, fingerprint, nil
}

func resolveNativeCandidateBindingDigest(
	ctx context.Context,
	resolver deployment.CandidateConnectionEvidenceResolver,
	request deployment.CandidateConnectionRequest,
) (string, error) {
	empty, err := deployment.BindingFingerprint(nil)
	if err != nil {
		return "", err
	}
	if len(request.Requirements) == 0 && len(request.AuthoredConnections) == 0 {
		return empty, nil
	}
	if nativeBuildAuthorityNil(resolver) {
		return "", fmt.Errorf("%w: native candidate binding evidence resolver is required", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	evidence, err := resolver.Resolve(ctx, request)
	if err != nil {
		return "", fmt.Errorf("resolve native candidate binding evidence: %w", err)
	}
	if err := validateNativeCandidateBindingEvidence(request, evidence); err != nil {
		return "", err
	}
	fingerprint, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		return "", fmt.Errorf("fingerprint native candidate binding evidence: %w", err)
	}
	return fingerprint, nil
}

func validateNativeCandidateBindingEvidence(request deployment.CandidateConnectionRequest, evidence []deployment.CandidateConnectionEvidence) error {
	if len(evidence) != len(request.Requirements) {
		return fmt.Errorf("%w: candidate binding evidence does not cover every connection requirement", deployment.ErrDeliveryConflict)
	}
	requirements := make(map[string]deployment.CandidateConnectionRequirement, len(request.Requirements))
	for _, requirement := range request.Requirements {
		key := requirement.ConnectionID.String()
		if _, exists := requirements[key]; exists {
			return fmt.Errorf("%w: duplicate candidate connection requirement", deployment.ErrDeliveryConflict)
		}
		requirements[key] = requirement
	}
	for _, value := range evidence {
		requirement, exists := requirements[value.ConnectionID.String()]
		if !exists || requirement.ConnectorKind != value.ConnectorKind || requirement.Access != value.Access {
			return fmt.Errorf("%w: candidate binding evidence differs from connection requirements", deployment.ErrDeliveryConflict)
		}
		delete(requirements, value.ConnectionID.String())
	}
	if len(requirements) != 0 {
		return fmt.Errorf("%w: candidate binding evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	return nil
}

func nativeCandidateConnectionRequest(
	candidateID, actor, targetID string,
	artifacts release.CandidateArtifactSet,
) deployment.CandidateConnectionRequest {
	requirements := make([]deployment.CandidateConnectionRequirement, len(artifacts.Generation.Connections))
	for index, value := range artifacts.Generation.Connections {
		requirements[index] = deployment.CandidateConnectionRequirement{
			ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind, Access: value.Access,
		}
	}
	authored := make([]deployment.CandidateAuthoredConnection, len(artifacts.Generation.AuthoredConnections))
	for index, value := range artifacts.Generation.AuthoredConnections {
		authored[index] = deployment.CandidateAuthoredConnection{
			ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind, Access: value.Access,
		}
	}
	return deployment.CandidateConnectionRequest{
		CandidateID: candidateID, Actor: actor, TargetID: targetID, Identity: artifacts.Generation.Identity,
		Requirements: requirements, AuthoredConnections: authored,
	}
}

func closeNativeCandidateBindings(leases deployment.CandidateConnectionLeases) error {
	if nativeBuildAuthorityNil(leases) {
		return nil
	}
	if err := leases.Close(); err != nil {
		return fmt.Errorf("release native candidate connections: %w", errors.Join(deployment.ErrDeliveryConflict, err))
	}
	return nil
}

// buildNativePhysicalWithCandidateBindings is the complete credential-bearing
// scope of a native build. It registers the exact candidate resolver only
// while BuildNativePhysical is running, then releases it before returning any
// detached snapshot evidence to qualification or recovery.
func buildNativePhysicalWithCandidateBindings(
	ctx context.Context,
	connections deployment.CandidateConnectionLeaser,
	bindingRequest deployment.CandidateConnectionRequest,
	expectedBindingDigest string,
	physicalInput NativePhysicalBuildInput,
	physicalFactory NativePhysicalBuildEnvironmentFactory,
) (NativePhysicalBuildEvidence, error) {
	leases, acquiredBindingDigest, err := acquireNativeCandidateBindings(ctx, connections, bindingRequest)
	if err != nil {
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildDeterministicFailure(NativePhysicalBuildPhaseValidation, err)
	}
	if acquiredBindingDigest != expectedBindingDigest {
		closeErr := closeNativeCandidateBindings(leases)
		return NativePhysicalBuildEvidence{}, nativePhysicalBuildDeterministicFailure(NativePhysicalBuildPhaseValidation, errors.Join(
			fmt.Errorf("%w: acquired candidate connection evidence differs from planned binding identity", deployment.ErrDeliveryConflict), closeErr,
		))
	}
	physical, buildErr := BuildNativePhysical(ctx, physicalInput, physicalFactory)
	closeErr := closeNativeCandidateBindings(leases)
	if buildErr != nil {
		if closeErr != nil {
			phase := NativePhysicalBuildPhaseEvidence
			if failure, ok := NativePhysicalBuildFailureOf(buildErr); ok {
				phase = failure.Phase
			}
			return physical, nativePhysicalBuildIndeterminateFailure(phase, errors.Join(buildErr, closeErr))
		}
		return physical, buildErr
	}
	if closeErr != nil {
		return physical, nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, closeErr)
	}
	return physical, nil
}
