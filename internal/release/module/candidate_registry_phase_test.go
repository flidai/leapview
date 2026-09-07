package module

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/extension"
	projectdomain "github.com/flidai/leapview/internal/project"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestProtectedCandidateRegistryIsQualifiedBeforeEveryArtifactPhaseBoundary(t *testing.T) {
	fixture := makeProtectedCandidatePhaseFixture(t)

	for _, phase := range []string{"inspect", "prepare", "hydrate"} {
		for _, registryState := range []string{"missing", "stale", "cross-scope"} {
			t.Run(phase+"/"+registryState, func(t *testing.T) {
				registry := &candidateRegistryFixture{value: fixture.registry}
				service := &candidateArtifactService{
					instanceID:       "instance:one",
					semanticRegistry: registry,
					states:           &candidatePhaseStates{},
					environment:      servingstate.DefaultEnvironment,
					artifacts:        &candidatePhaseArtifactStore{},
				}
				switch registryState {
				case "missing":
					service.semanticRegistry = nil
				case "stale":
					registry.change = true
				case "cross-scope":
					registry.value.Control.ProjectID = "project:other"
				}

				var err error
				switch phase {
				case "inspect":
					_, err = service.InspectCandidateArtifacts(t.Context(), fixture.request)
				case "prepare":
					_, err = service.Prepare(t.Context(), fixture.request)
				case "hydrate":
					_, err = service.HydrateCandidateArtifacts(t.Context(), fixture.request, fixture.inspected, release.CandidateArtifactIdentity{
						ServingArtifactID:     "artifact_candidate",
						ServingArtifactDigest: "sha256:" + strings.Repeat("b", 64),
						ServingStateID:        "state_candidate",
					})
				}

				require.ErrorIs(t, err, release.ErrCandidateArtifactInvalid)
				wantMessage := map[string]string{
					"missing":     "protected candidate semantic registry is unavailable",
					"stale":       "candidate semantic registry or control revision is stale",
					"cross-scope": "semantic registry instance/project mismatch",
				}[registryState]
				require.ErrorContains(t, err, wantMessage)
				wantReads := map[string]int{"missing": 0, "stale": 2, "cross-scope": 1}[registryState]
				require.Equal(t, wantReads, registry.reads)
				states := service.states.(*candidatePhaseStates)
				require.Equal(t, 0, states.createCalls)
				require.Equal(t, 0, states.byIDCalls)
				require.Equal(t, 0, states.artifactCalls)
				store := service.artifacts.(*candidatePhaseArtifactStore)
				require.Equal(t, 0, store.saveCalls)
			})
		}
	}
}

func TestProtectedCandidateRegistryValidEvidenceCrossesInspectBoundary(t *testing.T) {
	fixture := makeProtectedCandidatePhaseFixture(t)
	registry := &candidateRegistryFixture{value: fixture.registry}
	states := &candidatePhaseStates{}
	pins := &candidatePhasePins{}
	extensions := &candidatePhaseExtensions{}
	service := &candidateArtifactService{
		instanceID:           "instance:one",
		semanticRegistry:     registry,
		states:               states,
		pins:                 pins,
		environment:          servingstate.DefaultEnvironment,
		extensionPreparation: extensions,
	}

	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	require.NoError(t, err)
	require.Equal(t, 3, registry.reads, "qualifying, phase preflight, and phase-boundary revalidation reads")
	require.NotNil(t, inspected.Compiler.SemanticRegistry)
	require.Equal(t, 1, pins.resolveCalls)
	require.Equal(t, []string{"ducklake"}, extensions.requested)
	require.Equal(t, 0, states.createCalls)
}

type protectedCandidatePhaseFixture struct {
	request   release.CandidateArtifactRequest
	inspected release.CandidateArtifactSet
	registry  access.SemanticRegistryContext
}

func makeProtectedCandidatePhaseFixture(t *testing.T) protectedCandidatePhaseFixture {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  definition: {type: direct, source: orders}
  fields: {id: {datatype: Integer}, region: {datatype: String}}
  entities: {order: {type: primary, fields: [id]}}
  grain: {entity: order}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  accessGrants:
    canViewSales: {userAttribute: region, allowedValues: [us]}
  datasets:
    orders:
      model: orders_model
      requiredAccessGrants: [canViewSales]
      accessFilters: [{field: region, userAttribute: region}]
  dimensions:
    region:
      datatype: String
      bindings: {orders: {field: orders.region}}
  metrics:
    order_count:
      type: aggregate
      dataset: orders
      aggregation: count
      input: {field: orders.id}
      empty: zero
`,
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	project, err := projectcompiler.Compile(root)
	require.NoError(t, err)
	artifactPath := filepath.Join(t.TempDir(), "retained-project.json")
	require.NoError(t, os.WriteFile(artifactPath, project.Canonical(), 0o600))

	registry := access.SemanticRegistryContext{
		Control: access.AuthorizationControlRevision{InstanceID: "instance:one", ProjectID: "project:source-root", Revision: 1},
		Registry: access.SemanticAttributeRegistrySnapshot{
			State: access.SemanticAttributeRegistryState{
				Profile:  semanticvalue.Profile,
				Revision: 1,
				Digest:   "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
			Definitions: []access.SemanticAttributeDefinition{{
				ID: "definition:region", Name: "region", Type: semanticvalue.TypeString,
				Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
				DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
			}},
		},
	}
	request := release.CandidateArtifactRequest{
		CandidateID:    "candidate_1",
		Scope:          projectgraph.CandidateScope{ProjectID: project.ProjectID(), Environment: string(servingstate.DefaultEnvironment)},
		OwnerID:        "owner_1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		Source: projectdomain.CandidateSourceSnapshot{
			ProjectID:           project.ProjectID(),
			ArtifactDigest:      "sha256:" + strings.Repeat("a", 64),
			ProjectPath:         root,
			ProjectDigest:       project.Digest(),
			ProjectArtifactPath: artifactPath,
		},
	}
	return protectedCandidatePhaseFixture{
		request:  request,
		registry: registry,
		inspected: release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{
			Manifest:         project.Manifest(),
			SemanticRegistry: &registry,
		}},
	}
}

type candidatePhaseStates struct {
	createCalls   int
	byIDCalls     int
	artifactCalls int
}

func (s *candidatePhaseStates) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	s.byIDCalls++
	return servingstate.State{}, servingstate.ErrNotFound
}

func (s *candidatePhaseStates) MarkFailed(context.Context, servingstate.ID, error) error { return nil }

func (s *candidatePhaseStates) SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error) {
	return servingstate.State{}, nil
}

func (s *candidatePhaseStates) Create(context.Context, servingstate.CreateInput) (servingstate.State, error) {
	s.createCalls++
	return servingstate.State{}, nil
}

func (s *candidatePhaseStates) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	s.artifactCalls++
	return servingstate.Artifact{}, servingstate.ErrNotFound
}

func (s *candidatePhaseStates) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
}

func (s *candidatePhaseStates) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}

type candidatePhaseArtifactStore struct{ saveCalls int }

func (s *candidatePhaseArtifactStore) SaveUpload(context.Context, servingstate.ID, io.Reader) (int64, error) {
	s.saveCalls++
	return 0, nil
}

type candidatePhasePins struct{ resolveCalls int }

func (p *candidatePhasePins) ResolveCandidatePins(_ context.Context, _ projectgraph.ResourceID, connections []projectgraph.ResourceID, _ string) (map[projectgraph.ResourceID]string, error) {
	p.resolveCalls++
	resolved := make(map[projectgraph.ResourceID]string, len(connections))
	for _, connection := range connections {
		resolved[connection] = "revision:one"
	}
	return resolved, nil
}

func (*candidatePhasePins) ValidateServingStatePins(context.Context, projectgraph.ServingIdentity, map[projectgraph.ResourceID]string) error {
	return nil
}

type candidatePhaseExtensions struct{ requested []string }

func (e *candidatePhaseExtensions) PrepareExtensions(_ context.Context, names []string) ([]extension.Evidence, error) {
	e.requested = append([]string(nil), names...)
	result := make([]extension.Evidence, len(names))
	for index, name := range names {
		result[index] = extension.Evidence{
			Name: name, Identity: "identity:" + name, DuckDBVersion: "duckdb", ExtensionVersion: "version",
			GOOS: "linux", GOARCH: "amd64", Platform: "linux-amd64", SupportProfile: "stable",
			Digest: "sha256:" + strings.Repeat("c", 64), Origin: "test", Provenance: "test", Signature: "test",
		}
	}
	return result, nil
}
