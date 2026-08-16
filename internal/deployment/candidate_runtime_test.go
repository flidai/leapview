package deployment

import (
	"context"
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/stretchr/testify/require"
)

func candidateRuntimeGeneration(identity projectgraph.ServingIdentity, mode CandidateDataMode, revision string) CandidateGenerationRuntime {
	return CandidateGenerationRuntime{Identity: identity, ArtifactDigest: "sha256:" + strings.Repeat("b", 64), DataRevision: revision, DataMode: mode}
}

func TestCandidateRuntimeServicePreparesProjectGenerationWithConnectionEvidence(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	candidate := candidateRuntimeTestCandidate(t, now)
	connections := &candidateRuntimeConnections{}
	host := &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	identity := projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}
	generation := candidateRuntimeGeneration(identity, CandidateDataRefreshSources, "sources:managed")
	generation.Connections = []CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}}
	receipt, err := service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidate, AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.Equal(t, "leapview:test", receipt.RuntimeVersion)
	require.Len(t, receipt.Bindings, 1)
	require.Equal(t, "binding_warehouse", receipt.Bindings[0].BindingID)
	require.Len(t, connections.requests, 1)
	require.Equal(t, candidate.ID, connections.requests[0].CandidateID)
	require.Len(t, host.inputs, 1)
	require.Equal(t, "policy:v1", host.inputs[0].Registration.Compatibility.AuthorizationFingerprint)
}

func TestCandidateRuntimeServiceAllowsManagedOnlyRefreshWithoutSecretBinding(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections, host := &candidateRuntimeConnections{}, &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataRefreshSources, "sources:managed")
	generation.ManagedDataConnections = []string{"olist"}
	receipt, err := service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidateRuntimeTestCandidate(t, now), AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.Empty(t, receipt.Bindings)
	require.Empty(t, connections.requests[0].Requirements)
}

func TestCandidateRuntimeServiceAllowsAuthoredOnlyRefreshWithoutSecretBinding(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections, host := &candidateRuntimeConnections{}, &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataRefreshSources, "sources:authored")
	generation.AuthoredConnections = []CandidateAuthoredConnection{{ConnectionID: "public_http", ConnectorKind: "http"}}
	_, err = service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidateRuntimeTestCandidate(t, now), AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.Empty(t, connections.requests[0].Requirements)
	require.Equal(t, []runtimehost.CandidateAuthoredConnection{{LogicalConnection: "public_http", ConnectorKind: "http"}}, host.inputs[0].Registration.Compatibility.AuthoredConnections)
}

func TestCandidateRuntimeServiceRetainsBindingEvidenceWhenReusingSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections, host := &candidateRuntimeConnections{}, &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataReuseSnapshot, "snapshot:42")
	generation.Connections = []CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}}
	receipt, err := service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidateRuntimeTestCandidate(t, now), AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.Len(t, receipt.Bindings, 1)
	require.Equal(t, runtimehost.CandidateDataReuseSnapshot, host.inputs[0].Registration.Compatibility.DataMode)
}

func TestCandidateRuntimeServiceReleasesConnectionOnFailure(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections := &candidateRuntimeConnections{err: ErrCandidateUnavailable}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: &candidateRuntimeHost{}, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataRefreshSources, "sources:managed")
	generation.Connections = []CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}}
	_, err = service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidateRuntimeTestCandidate(t, now), AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.ErrorIs(t, err, ErrCandidateUnavailable)
}

func TestCandidateRuntimeServiceRejectsInvalidRestrictionBeforeAcquisition(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections := &candidateRuntimeConnections{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: &candidateRuntimeHost{}, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataRefreshSources, "sources:managed")
	generation.Restrictions = []CandidateRestriction{{ID: "policy_1", ObjectID: "object_1", ObjectKind: projectgraph.Kind("unknown"), PolicyType: "row_filter", ExpressionJSON: `{}`}}
	_, err = service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidateRuntimeTestCandidate(t, now), AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.ErrorIs(t, err, ErrCandidateInvalid)
	require.Empty(t, connections.requests)
}

func candidateRuntimeTestCandidate(t *testing.T, now time.Time) Candidate {
	t.Helper()
	candidate, err := NewCandidate(CandidateStartInput{ID: "cand_1", TargetID: "target_1", OwnerID: "author_1", Scope: CandidateScope{ProjectID: "project_1", Environment: "prod", BaseGenerationID: "generation_1"}, ArtifactDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Hour), Now: now})
	require.NoError(t, err)
	return candidate
}

type candidateRuntimeConnections struct {
	requests []CandidateConnectionRequest
	leases   []*candidateRuntimeConnectionLeases
	err      error
}

func (connections *candidateRuntimeConnections) Acquire(_ context.Context, request CandidateConnectionRequest) (CandidateConnectionLeases, error) {
	connections.requests = append(connections.requests, request)
	if connections.err != nil {
		return nil, connections.err
	}
	evidence := []CandidateConnectionEvidence{}
	if len(request.Requirements) > 0 {
		evidence = append(evidence, CandidateConnectionEvidence{BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: "sha256:" + strings.Repeat("9", 64)})
	}
	lease := &candidateRuntimeConnectionLeases{evidence: evidence}
	connections.leases = append(connections.leases, lease)
	return lease, nil
}

type candidateRuntimeConnectionLeases struct {
	evidence []CandidateConnectionEvidence
	closes   int
}

func (leases *candidateRuntimeConnectionLeases) Evidence() []CandidateConnectionEvidence {
	return append([]CandidateConnectionEvidence(nil), leases.evidence...)
}
func (leases *candidateRuntimeConnectionLeases) Close() error { leases.closes++; return nil }

type candidateRuntimeHost struct {
	inputs []runtimehost.CandidatePreparation
}

func (host *candidateRuntimeHost) PrepareAndRegisterCandidateSet(_ context.Context, inputs []runtimehost.CandidatePreparation) error {
	host.inputs = append(host.inputs, inputs...)
	return nil
}
