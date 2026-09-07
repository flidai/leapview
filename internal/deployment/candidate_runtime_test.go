package deployment

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/extension"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/stretchr/testify/require"
)

func candidateRuntimeGeneration(identity projectgraph.ServingIdentity, mode CandidateDataMode, revision string) CandidateGenerationRuntime {
	binding, _ := BindingFingerprint(nil)
	evidence, _ := (release.GateEvidence{Version: 1, CandidateID: "cand_1", SourceDigest: "sha256:" + strings.Repeat("a", 64), BindingGeneration: binding, RuntimeVersion: "leapview:test", DuckDBVersion: "duckdb:1", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 2, MaxMillis: 100}}).Canonical()
	return CandidateGenerationRuntime{
		Identity: identity, ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
		DataRevision: revision, DataMode: mode, GateEvidence: &evidence, BindingFingerprint: binding,
		Extensions: []extension.Evidence{{
			Name: "ducklake", Identity: "sha256:" + strings.Repeat("c", 64), Digest: "sha256:" + strings.Repeat("d", 64),
			DuckDBVersion: "duckdb:1", ExtensionVersion: "extension:1", GOOS: "linux", GOARCH: "amd64",
			Platform: "linux-amd64", SupportProfile: "stable",
		}},
	}
}

func candidateRuntimeSetGateBinding(t *testing.T, generation *CandidateGenerationRuntime, bindings []CandidateConnectionEvidence) {
	t.Helper()
	binding, err := BindingFingerprint(bindings)
	require.NoError(t, err)
	evidence := *generation.GateEvidence
	evidence.BindingGeneration = binding
	evidence, err = evidence.Canonical()
	require.NoError(t, err)
	generation.BindingFingerprint = binding
	generation.GateEvidence = &evidence
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
	generation.ManagedDataConnections = []string{"managed_1"}
	generation.Connections = []CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}}
	candidateRuntimeSetGateBinding(t, &generation, []CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: "sha256:" + strings.Repeat("9", 64)}})
	receipt, err := service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidate, AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.Equal(t, "leapview:test", receipt.RuntimeVersion)
	require.Len(t, receipt.Bindings, 1)
	require.Equal(t, "binding_warehouse", receipt.Bindings[0].BindingID)
	require.Len(t, connections.requests, 1)
	require.Equal(t, candidate.ID, connections.requests[0].CandidateID)
	require.Len(t, host.inputs, 1)
	require.Equal(t, "policy:v1", host.inputs[0].Registration.Compatibility.AuthorizationFingerprint)
	require.Equal(t, "sha256:"+strings.Repeat("d", 64), host.inputs[0].Registration.Compatibility.Capabilities[0].Digest)
}

func TestCandidateRuntimeServiceBindsGateEvidenceIntoReceiptAndCompatibility(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	candidate := candidateRuntimeTestCandidate(t, now)
	connections, host := &candidateRuntimeConnections{}, &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	identity := projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}
	bindingEvidence := []CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: "sha256:" + strings.Repeat("9", 64)}}
	bindingFingerprint, err := BindingFingerprint(bindingEvidence)
	require.NoError(t, err)
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: candidate.ID, SourceDigest: candidate.ArtifactDigest, BindingGeneration: bindingFingerprint, RuntimeVersion: "leapview:test", DuckDBVersion: "duckdb:1", Outcome: release.GateSuccess, EvaluatedAt: now, Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 2, MaxMillis: 100}}).Canonical()
	require.NoError(t, err)
	generation := candidateRuntimeGeneration(identity, CandidateDataRefreshSources, "sources:managed")
	generation.ManagedDataConnections = []string{"managed_1"}
	generation.Connections = []CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}}
	generation.GateEvidence = &evidence
	generation.BindingFingerprint = evidence.BindingGeneration
	receipt, err := service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidate, AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.NotNil(t, receipt.GateEvidence)
	require.Equal(t, evidence.Digest, receipt.GateEvidence.Digest)
	require.Equal(t, evidence.Digest, host.inputs[0].Registration.Compatibility.GateEvidenceDigest)
}

func TestCandidateRuntimeServiceRequiresQualifyingGateEvidence(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections, host := &candidateRuntimeConnections{}, &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	identity := projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}
	requestCandidate := candidateRuntimeTestCandidate(t, now)
	missing := candidateRuntimeGeneration(identity, CandidateDataRefreshSources, "sources:managed")
	missing.GateEvidence = nil
	_, err = service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: requestCandidate, AuthorizationFingerprint: "policy:v1", Generation: missing})
	require.ErrorIs(t, err, ErrCandidateInvalid)

	failed := candidateRuntimeGeneration(identity, CandidateDataRefreshSources, "sources:managed")
	evidence := *failed.GateEvidence
	evidence.Outcome = release.GateBlocking
	evidence.Sources = []release.GateSourceEvidence{{ID: "source-1", Mode: "inferred", SourceDigest: evidence.SourceDigest, SchemaOutcome: release.GateBlocking}}
	evidence, err = evidence.Canonical()
	require.NoError(t, err)
	failed.GateEvidence = &evidence
	_, err = service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: requestCandidate, AuthorizationFingerprint: "policy:v1", Generation: failed})
	require.ErrorIs(t, err, ErrCandidateInvalid)
	require.Empty(t, connections.requests)
}

func TestCandidateRuntimeServiceRejectsGateEvidenceFromAnotherArtifact(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	connections, host := &candidateRuntimeConnections{}, &candidateRuntimeHost{}
	service, err := NewCandidateRuntimeService(CandidateRuntimeServiceConfig{Connections: connections, Runtime: host, RuntimeVersion: "leapview:test"})
	require.NoError(t, err)
	candidate := candidateRuntimeTestCandidate(t, now)
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataRefreshSources, "sources:managed")
	evidence := *generation.GateEvidence
	evidence.SourceDigest = "sha256:" + strings.Repeat("c", 64)
	evidence, err = evidence.Canonical()
	require.NoError(t, err)
	generation.GateEvidence = &evidence
	_, err = service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidate, AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.ErrorIs(t, err, ErrCandidateInvalid)
	require.Empty(t, connections.requests)
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
	generation := candidateRuntimeGeneration(projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_2"}, CandidateDataReuseBase, "snapshot:42")
	generation.Connections = []CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}}
	candidateRuntimeSetGateBinding(t, &generation, []CandidateConnectionEvidence{{BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: "sha256:" + strings.Repeat("9", 64)}})
	receipt, err := service.Prepare(t.Context(), CandidateRuntimeRequest{Candidate: candidateRuntimeTestCandidate(t, now), AuthorizationFingerprint: "policy:v1", Generation: generation})
	require.NoError(t, err)
	require.Len(t, receipt.Bindings, 1)
	require.Equal(t, runtimehost.CandidateDataReuseBase, host.inputs[0].Registration.Compatibility.DataMode)
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
	return Candidate{ID: "cand_1", TargetID: "target_1", OwnerID: "author_1", Scope: CandidateScope{ProjectID: "project_1", Environment: "prod", BaseGenerationID: "generation_1"}, ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Status: CandidatePreparing, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Revision: 1}
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
