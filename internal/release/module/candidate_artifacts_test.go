package module

import (
	"errors"
	"strings"
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

func TestCandidateManagedDataPinMapDropsRemovedBaseConnections(t *testing.T) {
	base := map[string]string{"orders": "revision_orders", "legacy": "revision_legacy"}

	pins := candidateManagedDataPinMap([]string{"orders"}, base)

	require.Equal(t, map[string]string{"orders": "revision_orders"}, pins)
}

func TestCandidateManagedDataPinMapDoesNotRetainSwitchedManagedConnection(t *testing.T) {
	base := map[string]string{"legacy": "revision_legacy"}

	pins := candidateManagedDataPinMap([]string{"orders"}, base)

	require.Empty(t, pins)
	require.Equal(t, []string{"orders"}, missingCandidateManagedConnections([]string{"orders"}, pins))
}

func TestCandidateSourcesDataRevisionIsPinOrderIndependent(t *testing.T) {
	first := candidateSourcesDataRevision("sha256:artifact", map[string]string{
		"z_connection": "revision_z",
		"a_connection": "revision_a",
	})
	second := candidateSourcesDataRevision("sha256:artifact", map[string]string{
		"a_connection": "revision_a",
		"z_connection": "revision_z",
	})
	if first != second {
		t.Fatalf("source data revision changed with pin order: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "sources:") {
		t.Fatalf("source data revision = %q, want sources: prefix", first)
	}
}

func TestCandidateSourcesDataRevisionChangesWhenManagedDataPinChanges(t *testing.T) {
	base := map[string]string{"orders": "revision_a"}
	changed := map[string]string{"orders": "revision_b"}
	if candidateSourcesDataRevision("sha256:artifact", base) == candidateSourcesDataRevision("sha256:artifact", changed) {
		t.Fatal("source data revision did not change when managed-data pin changed")
	}
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
