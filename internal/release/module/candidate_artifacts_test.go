package module

import (
	"errors"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestCandidateConnectionRequirementsAreProjectScoped(t *testing.T) {
	values, managed, authored, err := candidateConnectionRequirements([]projectartifact.ConnectionActivation{
		{LogicalConnectionID: "warehouse", ConnectorKind: "postgres", Mode: projectartifact.TargetBindingActivation},
		{LogicalConnectionID: "olist", ConnectorKind: "quack", Mode: projectartifact.ManagedActivation},
		{LogicalConnectionID: "public_http", ConnectorKind: "http", Mode: projectartifact.AuthoredActivation},
	})
	require.NoError(t, err)
	warehouse, _ := projectgraph.NewResourceID("warehouse")
	publicHTTP, _ := projectgraph.NewResourceID("public_http")
	require.Equal(t, []release.CandidateConnectionRequirement{{ConnectionID: warehouse, ConnectorKind: "postgres"}}, values)
	require.Equal(t, []string{"olist"}, managed)
	require.Equal(t, []release.CandidateAuthoredConnection{{ConnectionID: publicHTTP, ConnectorKind: "http"}}, authored)
}

func TestCandidateConnectionRequirementsRejectInvalidConnectionIdentity(t *testing.T) {
	_, _, _, err := candidateConnectionRequirements([]projectartifact.ConnectionActivation{{LogicalConnectionID: "warehouse with space", ConnectorKind: "postgres", Mode: projectartifact.TargetBindingActivation}})
	require.Error(t, err)
}

func TestCandidateManagedPinsSortAndFindMissingConnections(t *testing.T) {
	pins := map[string]string{"z_connection": "revision_z", "a_connection": "revision_a"}
	require.Equal(t, []release.ManagedDataPin{{ConnectionID: "a_connection", RevisionID: "revision_a"}, {ConnectionID: "z_connection", RevisionID: "revision_z"}}, candidateManagedDataPins(pins))
	require.Equal(t, []string{"missing"}, missingCandidateManagedConnections([]string{"a_connection", "missing"}, pins))
}

func TestCandidateArtifactSetCarriesOneGenerationIdentity(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_1")
	require.NoError(t, err)
	set := release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{Identity: identity, ArtifactDigest: "sha256:artifact", DataRevision: "snapshot:1", DataMode: release.GenerationDataReuseSnapshot}}
	require.Equal(t, identity, set.Generation.Identity)
	require.Empty(t, set.Generation.AuthoredConnections)
}

func TestCandidateArtifactErrorClassificationIsStable(t *testing.T) {
	invalid := candidateArtifactInvalid(errors.New("invalid graph"))
	require.ErrorIs(t, invalid, release.ErrCandidateArtifactInvalid)
	unavailable := candidateArtifactUnavailable(errors.New("source unavailable"))
	require.ErrorIs(t, unavailable, release.ErrCandidateArtifactUnavailable)
}
