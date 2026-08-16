package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

type canonicalRefreshHarness struct {
	*harness
	projectID     projectgraph.ResourceID
	environment   servingstate.Environment
	identity      projectgraph.ServingIdentity
	pipelineID    projectgraph.ResourceID
	semanticModel projectgraph.ResourceID
	modelIDs      []projectgraph.ResourceID
	states        *servingstatemodule.Module
}

func newCanonicalRefreshHarness(t *testing.T) *canonicalRefreshHarness {
	t.Helper()
	ctx := context.Background()
	projectPath := canonicalProjectPath(t)
	project, err := projectcompiler.Compile(projectPath)
	if err != nil {
		t.Fatalf("compile canonical project: %v", err)
	}
	plan, err := projectcompiler.PlanProject(projectPath)
	if err != nil {
		t.Fatalf("plan canonical project: %v", err)
	}
	var bundle bytes.Buffer
	_, bundleDigest, err := projectbundle.PackCompiledProject(project, plan, &bundle)
	if err != nil {
		t.Fatalf("pack canonical project: %v", err)
	}
	artifactPath := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(artifactPath, bundle.Bytes(), 0o600); err != nil {
		t.Fatalf("write canonical project artifact: %v", err)
	}
	manifestJSON, err := json.Marshal(project.Manifest())
	if err != nil {
		t.Fatalf("encode canonical project manifest: %v", err)
	}

	store := testStore(t)
	states, err := servingstatemodule.Build(ctx, servingstatemodule.Config{Database: store.SQLDB()})
	if err != nil {
		t.Fatalf("build serving-state repository: %v", err)
	}
	environment := servingstate.DefaultEnvironment
	created, err := states.Create(ctx, servingstate.CreateInput{
		ProjectID: project.ProjectID(), Environment: environment, CreatedBy: "integration", Source: servingstate.SourcePublish,
	})
	if err != nil {
		t.Fatalf("create canonical serving state: %v", err)
	}
	artifact := servingstate.Artifact{
		ID: "artifact_" + string(created.ID), ServingStateID: created.ID,
		Digest: bundleDigest, Format: projectbundle.BundleFormat, Path: artifactPath,
		ManifestJSON: string(manifestJSON), SizeBytes: int64(bundle.Len()),
	}
	validation := servingstate.Validation{
		Digest: bundleDigest, ManifestJSON: string(manifestJSON), ProjectID: project.ProjectID(),
		ProjectDigest: project.Digest(), Graph: project.Graph(),
	}
	if _, err := states.SaveValidated(ctx, created.ID, validation, artifact); err != nil {
		t.Fatalf("validate canonical serving state: %v", err)
	}
	if err := states.RecordDuckLakeSnapshot(ctx, created.ID, 1); err != nil {
		t.Fatalf("record canonical serving snapshot: %v", err)
	}
	active, err := states.Activate(ctx, project.ProjectID(), environment, created.ID, "")
	if err != nil {
		t.Fatalf("activate canonical serving state: %v", err)
	}
	identity, err := projectgraph.NewServingIdentity(project.ProjectID(), string(environment), string(active.ID))
	if err != nil {
		t.Fatalf("canonical serving identity: %v", err)
	}
	runtimeHost, err := runtimehostmodule.Build(ctx, runtimehostmodule.Config{
		States: states, ProjectID: project.ProjectID(), Environment: environment,
		Factory: canonicalRefreshRuntimeFactory{graph: project.Graph()}, Authorization: canonicalRefreshAuthorizationInstaller{},
	})
	if err != nil {
		t.Fatalf("build canonical runtime host: %v", err)
	}
	t.Cleanup(func() { _ = runtimeHost.Close() })
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		RuntimeHost: runtimeHost, ProjectID: project.ProjectID(), DefaultEnvironment: string(environment), Reloader: runtimeHost,
		RefreshMaterializer: canonicalRefreshMaterializer{}, EnableRefreshDispatcher: true,
	}))
	if err := server.routes.refreshModule.Start(ctx); err != nil {
		t.Fatalf("start refresh module: %v", err)
	}
	t.Cleanup(func() { _ = server.routes.refreshModule.Stop(ctx) })
	t.Cleanup(func() { _ = store.Close() })
	pipelines := project.RefreshPipelines()
	pipeline, ok := pipelines["pipeline:visuals-refresh"]
	if !ok {
		t.Fatalf("canonical project missing pipeline:visuals-refresh (pipelines=%v)", keys(pipelines))
	}
	pipelineID := pipeline.ID
	semanticModel := pipeline.SemanticModelID
	semanticModels := project.Models()
	semanticModelDef, ok := semanticModels[semanticModel.String()]
	if !ok || semanticModelDef == nil {
		t.Fatalf("canonical semantic model %s missing from project", semanticModel)
	}
	modelIDs := make([]projectgraph.ResourceID, 0, len(semanticModelDef.Tables))
	for tableID := range semanticModelDef.Tables {
		id, parseErr := projectgraph.NewResourceID(tableID)
		if parseErr != nil {
			t.Fatalf("canonical model table ID %q: %v", tableID, parseErr)
		}
		modelIDs = append(modelIDs, id)
	}
	h := &harness{handler: server.Routes(), store: store}
	h.server = httptest.NewServer(h.handler)
	t.Cleanup(h.server.Close)
	return &canonicalRefreshHarness{harness: h, projectID: project.ProjectID(), environment: environment, identity: identity, pipelineID: pipelineID, semanticModel: semanticModel, modelIDs: modelIDs, states: states}
}

type canonicalRefreshRuntimeFactory struct{ graph projectgraph.ProjectGraph }

func (f canonicalRefreshRuntimeFactory) Prepare(_ context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(servingstate.NormalizeEnvironment(input.State.Environment)), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, f.graph, nil, nil)
	if err != nil {
		return nil, err
	}
	return &canonicalRefreshRuntime{authorization: authorization, snapshotID: input.State.DuckLakeSnapshotID}, nil
}

type canonicalRefreshRuntime struct {
	authorization accesssnapshot.AuthorizationSnapshot
	snapshotID    int64
}

func (r *canonicalRefreshRuntime) Close() error                 { return nil }
func (r *canonicalRefreshRuntime) Verify(context.Context) error { return nil }
func (r *canonicalRefreshRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}
func (r *canonicalRefreshRuntime) DuckLakeSnapshotID() int64 { return r.snapshotID }

type canonicalRefreshAuthorizationInstaller struct{}

func (canonicalRefreshAuthorizationInstaller) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}

type canonicalRefreshMaterializer struct{}

func (canonicalRefreshMaterializer) Materialize(_ context.Context, input refreshrun.MaterializeInput) (int64, error) {
	if input.Active.DuckLakeSnapshotID <= 0 {
		return 0, fmt.Errorf("canonical refresh fixture active snapshot is missing")
	}
	return input.Active.DuckLakeSnapshotID + 1, nil
}

func canonicalProjectPath(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve test working directory: %v", err)
	}
	for dir := workingDir; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "dashboards", "leapview.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	t.Fatalf("canonical project dashboards/leapview.yaml not found from %s", workingDir)
	return ""
}

func keys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
