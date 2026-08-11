package module

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workspace"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
)

func TestCandidateArtifactsRefreshThenReuseTargetSnapshot(t *testing.T) {
	projectPath := targetBoundCandidateProject(t)
	snapshot, err := (projectdevloop.FilesystemBuilder{
		ProjectPath: projectPath,
	}).Build(t.Context())
	require.NoError(t, err)
	states := newCandidateArtifactStateRepository()
	workspaces := &candidateArtifactWorkspaceRepository{}
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	module, err := Build(t.Context(), Config{
		Database:          store.SQLDB(),
		States:            states,
		Workspaces:        workspaces,
		ArtifactDirectory: t.TempDir(),
		Environment:       servingstate.DefaultEnvironment,
	})
	require.NoError(t, err)
	source := releaseCandidateSource(t, snapshot, projectPath)
	if err := os.RemoveAll(filepath.Dir(projectPath)); err != nil {
		t.Fatal(err)
	}
	first, err := module.PrepareCandidateArtifacts(t.Context(), release.CandidateArtifactRequest{
		CandidateID: "candidate_1", ProjectID: snapshot.ProjectID,
		OwnerID: "principal_1", Environment: "dev", ArtifactDigest: snapshot.Digest,
		Source: source,
	})
	require.NoError(t, err)
	if first.AuthorizationFingerprint == "" || len(first.Workspaces) == 0 {
		t.Fatalf("first artifact set = %#v", first)
	}
	if first.Artifact.SourceDigest != snapshot.Digest ||
		first.Artifact.ProjectDigest != source.ProjectDigest ||
		first.Artifact.CompilerVersion == "" ||
		first.Artifact.SchemaVersion < 1 ||
		len(first.Artifact.Workspaces) != len(first.Workspaces) {
		t.Fatalf("immutable project artifact provenance = %#v", first.Artifact)
	}
	for _, prepared := range first.Workspaces {
		if prepared.DataMode != "refresh_sources" || len(prepared.Connections) == 0 {
			t.Fatalf("initial workspace must refresh through target bindings: %#v", prepared)
		}
		state := states.states[servingstate.ID(prepared.ServingStateID)]
		state.DuckLakeSnapshotID = 42
		state.Status = servingstate.StatusActive
		states.states[state.ID] = state
		states.active[activeCandidateArtifactKey{
			workspace: state.WorkspaceID, environment: state.Environment,
		}] = state.ID
	}

	second, err := module.PrepareCandidateArtifacts(t.Context(), release.CandidateArtifactRequest{
		CandidateID: "candidate_2", ProjectID: snapshot.ProjectID,
		OwnerID: "principal_1", Environment: "dev", ArtifactDigest: snapshot.Digest,
		Source: source,
	})
	require.NoError(t, err)
	if second.AuthorizationFingerprint == "" || len(second.Workspaces) != len(first.Workspaces) {
		t.Fatalf("second artifact set = %#v", second)
	}
	if diff := cmp.Diff(first.Artifact, second.Artifact); diff != "" {
		t.Fatalf(
			"target snapshot reuse changed project artifact provenance (-first +second):\n%s",
			diff,
		)
	}
	for _, prepared := range second.Workspaces {
		if prepared.DataMode != "reuse_snapshot" ||
			prepared.DataRevision != "snapshot:42" ||
			len(prepared.Connections) == 0 {
			t.Fatalf("unchanged workspace did not reuse target snapshot: %#v", prepared)
		}
		if diff := cmp.Diff(first.Workspaces[0].Connections, prepared.Connections); diff != "" {
			t.Fatalf("snapshot reuse dropped target connection requirements (-want +got):\n%s", diff)
		}
		if state := states.states[servingstate.ID(prepared.ServingStateID)]; state.DuckLakeSnapshotID != 42 {
			t.Fatalf("candidate serving state snapshot = %d, want 42", state.DuckLakeSnapshotID)
		}
	}
	if len(workspaces.ensured) != len(first.Workspaces)+len(second.Workspaces) {
		t.Fatalf("ensured workspaces = %v", workspaces.ensured)
	}
}

func TestCandidateArtifactsAllowInitialAuthoredHTTPRefresh(t *testing.T) {
	projectPath := authoredCandidateProject(t)
	snapshot, err := (projectdevloop.FilesystemBuilder{
		ProjectPath: projectPath,
	}).Build(t.Context())
	require.NoError(t, err)
	states := newCandidateArtifactStateRepository()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), States: states,
		Workspaces:        &candidateArtifactWorkspaceRepository{},
		ArtifactDirectory: t.TempDir(), Environment: servingstate.DefaultEnvironment,
	})
	require.NoError(t, err)
	source := releaseCandidateSource(t, snapshot, projectPath)

	prepared, err := module.PrepareCandidateArtifacts(t.Context(), release.CandidateArtifactRequest{
		CandidateID: "candidate_public", ProjectID: snapshot.ProjectID,
		OwnerID: "principal_1", Environment: "dev", ArtifactDigest: snapshot.Digest,
		Source: source,
	})
	require.NoError(t, err)
	require.NotEmpty(t, prepared.Workspaces)
	for _, candidateWorkspace := range prepared.Workspaces {
		require.Equal(t, "refresh_sources", candidateWorkspace.DataMode)
		require.Empty(t, candidateWorkspace.Connections)
		require.Empty(t, candidateWorkspace.ManagedDataPins)
		require.Equal(t, []release.CandidateAuthoredConnection{{
			LogicalConnectionID: "olist", ConnectorKind: "http",
		}}, candidateWorkspace.AuthoredConnections)
	}
}

func TestCandidateRestrictionsSelectOnlyOwnerAndUniversalPolicies(t *testing.T) {
	policy := `{
		"groups":{"authors":{"id":"authors","name":"Authors","members":[{"principalId":"author_1"}]}},
		"dataPolicies":{
			"all":{"id":"all","object":{"type":"workspace"},"policyType":"row_filter","expressionJson":"{\"field\":\"orders.region\",\"operator\":\"equals\",\"values\":[\"EU\"]}"},
			"owner":{"id":"owner","object":{"type":"semantic_model","id":"sales"},"subject":{"kind":"principal","principalId":"author_1"},"policyType":"column_mask","expressionJson":"{\"field\":\"orders.email\",\"mask\":\"redact\"}"},
			"group":{"id":"group","object":{"type":"table","id":"orders"},"subject":{"kind":"group","group":"authors"},"policyType":"row_filter","expressionJson":"{\"field\":\"orders.team\",\"operator\":\"equals\",\"values\":[\"A\"]}"},
			"foreign":{"id":"foreign","object":{"type":"workspace"},"subject":{"kind":"principal","principalId":"author_2"},"policyType":"row_filter","expressionJson":"{\"allowAll\":true}"}
		}
	}`
	restrictions, err := candidateRestrictions(policy, "sales", "author_1")
	require.NoError(t, err)
	if len(restrictions) != 3 {
		t.Fatalf("candidate restrictions = %#v", restrictions)
	}
	if restrictions[0].ObjectID != "workspace:sales" ||
		restrictions[1].ObjectID != "table:sales:orders" ||
		restrictions[2].ObjectID != "semantic_model:sales:sales" {
		t.Fatalf("candidate restriction scopes = %#v", restrictions)
	}
}

func TestCandidateConnectionRequirementsFollowConnectorActivationModes(t *testing.T) {
	project, err := projectartifact.NewProject("demo", map[string]projectartifact.WorkspaceInput{
		"sales": {
			Metadata: workspace.Workspace{ID: "sales"},
			Manifest: &projectmanifest.Workspace{Models: map[string]*semanticmodel.Model{
				"sales": {Connections: map[string]semanticmodel.Connection{
					"managed_data": {Kind: "managed"},
					"public_http":  {Kind: "http"},
					"warehouse":    {Kind: "quack"},
				}},
			}},
		},
	})
	require.NoError(t, err)
	compiled, ok := project.Workspace("sales")
	require.True(t, ok)

	requirements, managed, authored, err := candidateConnectionRequirements(compiled)
	require.NoError(t, err)
	require.Equal(t, []string{"managed_data"}, managed)
	require.Equal(t, []release.CandidateAuthoredConnection{{
		LogicalConnectionID: "public_http", ConnectorKind: "http",
	}}, authored)
	require.Equal(t, []release.CandidateConnectionRequirement{{
		LogicalConnectionID: "warehouse", ConnectorKind: "quack",
	}}, requirements)
}

func targetBoundCandidateProject(t *testing.T) string {
	return candidateProjectWithConnection(t, "postgres")
}

func authoredCandidateProject(t *testing.T) string {
	return candidateProjectWithConnection(t, "http")
}

func candidateProjectWithConnection(t *testing.T, connectorKind string) string {
	t.Helper()
	sourceProject := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	sourceProject, err := filepath.Abs(sourceProject)
	require.NoError(t, err)
	sourceRoot := filepath.Dir(sourceProject)
	destinationRoot := t.TempDir()
	files, err := projectcompiler.SourceFiles(sourceProject)
	require.NoError(t, err)
	for _, source := range files {
		relative, err := filepath.Rel(sourceRoot, source)
		require.NoError(t, err)
		content, err := os.ReadFile(source)
		require.NoError(t, err)
		if filepath.ToSlash(relative) == "connections/olist.yaml" {
			replacement := "kind: " + connectorKind
			if connectorKind == "http" {
				replacement += "\n  scope: https://example.test/olist/"
			}
			content = []byte(strings.Replace(string(content), "kind: managed", replacement, 1))
		}
		if connectorKind == "postgres" && strings.HasPrefix(filepath.ToSlash(relative), "sources/") {
			content = []byte(strings.Replace(
				string(content),
				"  path:",
				"  object:",
				1,
			))
		}
		target := filepath.Join(destinationRoot, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(destinationRoot, "leapview.yaml")
}

func releaseCandidateSource(
	t *testing.T,
	snapshot projectdevloop.Snapshot,
	projectPath string,
) project.CandidateSourceSnapshot {
	t.Helper()
	compiled, err := projectcompiler.CompileProjectArtifact(projectPath)
	require.NoError(t, err)
	artifactPath := filepath.Join(t.TempDir(), "project.artifact.json")
	if err := os.WriteFile(artifactPath, compiled.Canonical(), 0o600); err != nil {
		t.Fatal(err)
	}
	return project.CandidateSourceSnapshot{
		ProjectID: snapshot.ProjectID, ArtifactDigest: snapshot.Digest,
		ProjectDigest: compiled.Digest(), ProjectArtifactPath: artifactPath,
	}
}

type activeCandidateArtifactKey struct {
	workspace   servingstate.WorkspaceID
	environment servingstate.Environment
}

type candidateArtifactStateRepository struct {
	next      int
	states    map[servingstate.ID]servingstate.State
	artifacts map[servingstate.ID]servingstate.Artifact
	active    map[activeCandidateArtifactKey]servingstate.ID
}

func newCandidateArtifactStateRepository() *candidateArtifactStateRepository {
	return &candidateArtifactStateRepository{
		states:    make(map[servingstate.ID]servingstate.State),
		artifacts: make(map[servingstate.ID]servingstate.Artifact),
		active:    make(map[activeCandidateArtifactKey]servingstate.ID),
	}
}

func (repository *candidateArtifactStateRepository) Create(
	_ context.Context,
	input servingstate.CreateInput,
) (servingstate.State, error) {
	repository.next++
	state := servingstate.State{
		ID:          servingstate.ID("candidate_state_" + strconv.Itoa(repository.next)),
		WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID,
		Environment: input.Environment, Source: input.Source,
		Status: servingstate.StatusPending, CreatedBy: input.CreatedBy,
	}
	repository.states[state.ID] = state
	return state, nil
}

func (repository *candidateArtifactStateRepository) ByID(
	_ context.Context,
	id servingstate.ID,
) (servingstate.State, error) {
	state, ok := repository.states[id]
	if !ok {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	return state, nil
}

func (repository *candidateArtifactStateRepository) MarkFailed(
	_ context.Context,
	id servingstate.ID,
	cause error,
) error {
	state := repository.states[id]
	state.Status = servingstate.StatusFailed
	state.Error = cause.Error()
	repository.states[id] = state
	return nil
}

func (repository *candidateArtifactStateRepository) SaveValidated(
	_ context.Context,
	id servingstate.ID,
	validation servingstate.Validation,
	artifact servingstate.Artifact,
) (servingstate.State, error) {
	state := repository.states[id]
	state.Status = servingstate.StatusValidated
	state.ProjectDigest = validation.ProjectDigest
	state.ProjectWorkspaces = append([]string(nil), validation.ProjectWorkspaces...)
	state.AccessPolicyJSON = "{}"
	state.Digest = validation.Digest
	repository.states[id] = state
	repository.artifacts[id] = artifact
	return state, nil
}

func (repository *candidateArtifactStateRepository) ActiveArtifact(
	_ context.Context,
	workspaceID servingstate.WorkspaceID,
	environment servingstate.Environment,
) (servingstate.State, servingstate.Artifact, error) {
	id, ok := repository.active[activeCandidateArtifactKey{
		workspace: workspaceID, environment: environment,
	}]
	if !ok {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return repository.states[id], repository.artifacts[id], nil
}

func (repository *candidateArtifactStateRepository) RecordDuckLakeSnapshot(
	_ context.Context,
	id servingstate.ID,
	snapshotID int64,
) error {
	state := repository.states[id]
	state.DuckLakeSnapshotID = snapshotID
	repository.states[id] = state
	return nil
}

type candidateArtifactWorkspaceRepository struct {
	ensured []workspace.WorkspaceID
}

func (repository *candidateArtifactWorkspaceRepository) Ensure(
	_ context.Context,
	input workspace.EnsureInput,
) error {
	repository.ensured = append(repository.ensured, input.ID)
	return nil
}
