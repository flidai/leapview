package release

import (
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestProvenanceCanonicalizesGenerationEvidence(t *testing.T) {
	provenance, err := NewProvenance(testGenerationInput(t, GenerationDataRefreshSources))
	require.NoError(t, err)
	require.NoError(t, provenance.Validate())
	require.Equal(t, provenance.Artifact.ContentDigest, provenance.Artifact.ContentDigest)
}

func TestProvenanceDetectsArtifactAndPlanTampering(t *testing.T) {
	provenance, err := NewProvenance(testGenerationInput(t, GenerationDataRefreshSources))
	require.NoError(t, err)
	for name, mutate := range map[string]func(*Provenance){
		"artifact": func(value *Provenance) { value.Artifact.ContentDigest = testDigest("tampered") },
		"plan":     func(value *Provenance) { value.Plan.ManagedDataPins[0].RevisionID = "revision_other" },
		"binding":  func(value *Provenance) { value.Plan.Bindings[0].ValidatedVersion = "provider_other" },
	} {
		t.Run(name, func(t *testing.T) {
			tampered := provenance
			mutate(&tampered)
			require.Error(t, tampered.Validate())
		})
	}
}

func TestProvenanceSnapshotReuseRejectsAuthoredRefreshEvidence(t *testing.T) {
	input := testGenerationInput(t, GenerationDataReuseSnapshot)
	input.Plan.AuthoredConnections = []AuthoredConnectionEvidence{{ConnectionID: "connection_2", ConnectorKind: "http"}}
	_, err := NewProvenance(input)
	require.Error(t, err)
}

func TestProvenanceRefreshAcceptsManagedAndAuthoredEvidence(t *testing.T) {
	input := testGenerationInput(t, GenerationDataRefreshSources)
	input.Plan.AuthoredConnections = []AuthoredConnectionEvidence{{ConnectionID: "connection_2", ConnectorKind: "http"}}
	provenance, err := NewProvenance(input)
	require.NoError(t, err)
	require.Len(t, provenance.Plan.AuthoredConnections, 1)
}

func TestProvenanceInitialGenerationAllowsMissingBaseIdentity(t *testing.T) {
	provenance, err := NewProvenance(testGenerationInput(t, GenerationDataRefreshSources))
	require.NoError(t, err)
	require.Nil(t, provenance.Plan.BaseIdentity)
}

func TestProvenanceRejectsMismatchedBaseIdentityScope(t *testing.T) {
	input := testGenerationInput(t, GenerationDataRefreshSources)
	base, _ := projectgraph.NewServingIdentity("other_project", "prod", "generation_1")
	input.Plan.BaseIdentity = &base
	_, err := NewProvenance(input)
	require.Error(t, err)
}

func TestProvenanceRejectsDuplicateConnectionsAndWhitespaceAliases(t *testing.T) {
	input := testGenerationInput(t, GenerationDataRefreshSources)
	input.Plan.ManagedDataPins = append(input.Plan.ManagedDataPins, input.Plan.ManagedDataPins[0])
	_, err := NewProvenance(input)
	require.Error(t, err)
	input = testGenerationInput(t, GenerationDataRefreshSources)
	input.Plan.TargetID = " target_1"
	_, err = NewProvenance(input)
	require.Error(t, err)
}

func TestProvenanceRejectsSourceRevisionWhitespaceAliases(t *testing.T) {
	for _, mutate := range []func(*SourceRevisionProvenance){
		func(value *SourceRevisionProvenance) { value.Revision = " revision_1" },
		func(value *SourceRevisionProvenance) { value.Repository = "https://example.com/repository " },
		func(value *SourceRevisionProvenance) { value.Ref = " refs/heads/main" },
		func(value *SourceRevisionProvenance) { value.ChangeID = "change_1 " },
	} {
		input := testGenerationInput(t, GenerationDataRefreshSources)
		input.SourceRevision = &SourceRevisionProvenance{Revision: "revision_1", Repository: "https://example.com/repository", Ref: "refs/heads/main", ChangeID: "change_1"}
		mutate(input.SourceRevision)
		_, err := NewProvenance(input)
		require.Error(t, err)
	}
}

func testGenerationInput(t *testing.T, mode GenerationDataMode) ProvenanceInput {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_2")
	require.NoError(t, err)
	return ProvenanceInput{
		Artifact:  ProjectArtifactProvenance{SourceDigest: testDigest("a"), ProjectDigest: testDigest("b"), ContentDigest: testDigest("c"), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		Candidate: CandidateProvenance{ID: "candidate_1", Revision: 1, OwnerID: "principal_1"},
		Plan:      GenerationPlanProvenance{Identity: identity, TargetID: "target_1", RuntimeVersion: "runtime:v1", PolicyDigest: testDigest("d"), DataRevision: "sources:1", DataMode: mode, ManagedDataPins: []ManagedDataPin{{ConnectionID: "connection_1", RevisionID: "revision_1"}}, Bindings: []BindingEvidence{{BindingID: "binding_1", ConnectionID: "connection_1", ConnectorKind: "postgres", Revision: 1, ValidatedVersion: "provider:v1", EndpointConfigHash: testDigest("e")}}, AuthoredConnections: nil},
	}
}

func testDigest(value string) string { return "sha256:" + strings.Repeat(value, 64)[:64] }
