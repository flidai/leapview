package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

// Test application routes are project-scoped, so their fixtures need the same
// generation-bound runtime contract as production. Keep one host per test
// database and close it from testStore's cleanup hook.
var testRuntimeHosts sync.Map // map[*testControlStore]*runtimehostmodule.Module

func closeTestRuntimeHost(store *testControlStore) {
	if store == nil {
		return
	}
	if value, ok := testRuntimeHosts.LoadAndDelete(store); ok {
		_ = value.(*runtimehostmodule.Module).Close()
	}
}

// ensureTestRuntimeHost installs a real, minimal serving generation. The
// graph intentionally includes the resource kinds exercised by app tests;
// authorization is still evaluated by the immutable snapshot rather than a
// production bypass. Principals present when the server is assembled receive
// explicit test-only project roles (platform admins get project-admin and all
// other principals get project-viewer) while token capability allowlists still
// apply.
type testServingStateRepository interface {
	servingStateRepository
	Create(context.Context, servingstate.CreateInput) (servingstate.State, error)
	SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
	Activate(context.Context, projectgraph.ResourceID, servingstate.Environment, servingstate.ID, servingstate.ID) (servingstate.State, error)
}

func ensureTestRuntimeHost(ctx context.Context, store *testControlStore, states testServingStateRepository, projectID projectgraph.ResourceID, environment servingstate.Environment) (*runtimehostmodule.Module, error) {
	if store == nil || states == nil {
		return nil, errors.New("test runtime fixture requires store and serving states")
	}
	if projectID == "" {
		projectID = testProjectID
	}
	if environment == "" {
		environment = servingstate.DefaultEnvironment
	}
	if value, ok := testRuntimeHosts.Load(store); ok {
		host := value.(*runtimehostmodule.Module)
		if host.ProjectID() == projectID && host.Environment() == environment {
			return host, nil
		}
		return nil, fmt.Errorf("test runtime host already bound to %s/%s", host.ProjectID(), host.Environment())
	}

	graph, err := testRuntimeGraph()
	if err != nil {
		return nil, err
	}
	repository := store.fixture.Graph.Access
	principals, err := repository.ListPrincipals(ctx, access.PrincipalFilter{})
	if err != nil {
		return nil, fmt.Errorf("list test principals: %w", err)
	}
	platformAdmins := map[string]struct{}{}
	rows, err := store.fixture.RuntimePool.Query(ctx, `SELECT principal_id::text FROM access.platform_role_binding WHERE role = $1 AND revoked_at IS NULL`, string(access.PlatformRoleAdmin))
	if err != nil {
		return nil, fmt.Errorf("list test platform admins: %w", err)
	}
	for rows.Next() {
		var principalID string
		if err := rows.Scan(&principalID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan test platform admin: %w", err)
		}
		platformAdmins[principalID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate test platform admins: %w", err)
	}
	rows.Close()
	subjects := make([]testRuntimeSubject, 0, len(principals)+1)
	seen := map[string]struct{}{}
	addSubject := func(id string, role access.ProjectRole) error {
		if id == "" {
			return nil
		}
		if _, ok := seen[id]; ok {
			return nil
		}
		subject, subjectErr := access.NewSubjectRef(access.SubjectKindPrincipal, id)
		if subjectErr != nil {
			return subjectErr
		}
		seen[id] = struct{}{}
		subjects = append(subjects, testRuntimeSubject{subject: subject, role: role})
		return nil
	}
	if err := addSubject(accessmodule.DevelopmentPrincipalID, access.ProjectRoleAdmin); err != nil {
		return nil, err
	}
	for _, principal := range principals {
		role := access.ProjectRoleViewer
		if _, ok := platformAdmins[principal.ID]; ok {
			role = access.ProjectRoleAdmin
		}
		if err := addSubject(principal.ID, role); err != nil {
			return nil, fmt.Errorf("principal %q: %w", principal.ID, err)
		}
	}

	active, _, activeErr := states.ActiveArtifact(ctx, projectID, environment)
	if errors.Is(activeErr, servingstate.ErrNotFound) {
		created, createErr := states.Create(ctx, servingstate.CreateInput{
			ProjectID: projectID, Environment: environment, CreatedBy: "test-fixture", Source: servingstate.SourcePublish,
		})
		if createErr != nil {
			return nil, fmt.Errorf("create test serving state: %w", createErr)
		}
		manifest := "{}"
		digest := graph.Digest()
		artifact := servingstate.Artifact{
			ID: "artifact_" + string(created.ID), ServingStateID: created.ID,
			Digest: digest, Format: "test-fixture", Path: "test-fixture.tar.gz", ManifestJSON: manifest,
		}
		if _, err := states.SaveValidated(ctx, created.ID, servingstate.Validation{
			Digest: digest, ManifestJSON: manifest, ProjectID: projectID, ProjectDigest: graph.Digest(), Graph: graph,
		}, artifact); err != nil {
			return nil, fmt.Errorf("validate test serving state: %w", err)
		}
		if err := states.RecordDuckLakeSnapshot(ctx, created.ID, 1); err != nil {
			return nil, fmt.Errorf("record test serving snapshot: %w", err)
		}
		if _, err := states.Activate(ctx, projectID, environment, created.ID, ""); err != nil {
			return nil, fmt.Errorf("activate test serving state: %w", err)
		}
		active = created
	} else if activeErr != nil {
		return nil, fmt.Errorf("load test active serving state: %w", activeErr)
	} else if active.DuckLakeSnapshotID == 0 {
		if err := states.RecordDuckLakeSnapshot(ctx, active.ID, 1); err != nil {
			return nil, fmt.Errorf("repair test serving snapshot: %w", err)
		}
	}

	factory := testRuntimeFactory{graph: graph, subjects: subjects}
	host, err := runtimehostmodule.Build(ctx, runtimehostmodule.Config{
		States: states, ProjectID: projectID, Environment: environment,
		Factory: factory, Authorization: testRuntimeAuthorizationInstaller{},
	})
	if err != nil {
		return nil, fmt.Errorf("build test runtime host: %w", err)
	}
	actual, loaded := testRuntimeHosts.LoadOrStore(store, host)
	if loaded {
		_ = host.Close()
		return actual.(*runtimehostmodule.Module), nil
	}
	return host, nil
}

type memoryServingStateRepository struct {
	mu        sync.RWMutex
	states    map[servingstate.ID]servingstate.State
	artifacts map[servingstate.ID]servingstate.Artifact
	active    map[servingstate.ActiveScope]servingstate.ID
}

func newMemoryServingStateRepository() *memoryServingStateRepository {
	return &memoryServingStateRepository{
		states: make(map[servingstate.ID]servingstate.State), artifacts: make(map[servingstate.ID]servingstate.Artifact),
		active: make(map[servingstate.ActiveScope]servingstate.ID),
	}
}

func (r *memoryServingStateRepository) Create(_ context.Context, input servingstate.CreateInput) (servingstate.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := servingstate.State{ID: servingstate.ID(uuid.NewString()), ProjectID: input.ProjectID, Environment: servingstate.NormalizeEnvironment(input.Environment), Status: servingstate.StatusPending, Source: input.Source, CreatedBy: input.CreatedBy, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.states[state.ID] = state
	return state, nil
}

func (r *memoryServingStateRepository) SaveValidated(_ context.Context, id servingstate.ID, validation servingstate.Validation, artifact servingstate.Artifact) (servingstate.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[id]
	if !ok {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	state.Status, state.Digest, state.ManifestJSON = servingstate.StatusValidated, validation.Digest, validation.ManifestJSON
	state.ProjectID, state.ProjectDigest = validation.ProjectID, validation.ProjectDigest
	state.AccessPolicyJSON = "{}"
	state.DashboardPublicationsJSON, state.DashboardAppearancesJSON = validation.DashboardPublicationsJSON, validation.DashboardAppearancesJSON
	artifact.ServingStateID = id
	r.states[id], r.artifacts[id] = state, artifact
	return state, nil
}

func (r *memoryServingStateRepository) RecordDuckLakeSnapshot(_ context.Context, id servingstate.ID, snapshot int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[id]
	if !ok {
		return servingstate.ErrNotFound
	}
	state.DuckLakeSnapshotID = snapshot
	r.states[id] = state
	return nil
}

func (r *memoryServingStateRepository) Activate(_ context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment, id, expected servingstate.ID) (servingstate.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	scope := servingstate.ActiveScope{ProjectID: projectID, Environment: servingstate.NormalizeEnvironment(environment)}
	if r.active[scope] != expected {
		return servingstate.State{}, servingstate.ErrActivationConflict
	}
	state, ok := r.states[id]
	if !ok {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	if previous := r.active[scope]; previous != "" && previous != id {
		prior := r.states[previous]
		prior.Status, prior.SupersededAt = servingstate.StatusInactive, time.Now().UTC().Format(time.RFC3339Nano)
		r.states[previous] = prior
	}
	state.Status, state.ActivatedAt = servingstate.StatusActive, time.Now().UTC().Format(time.RFC3339Nano)
	r.states[id], r.active[scope] = state, id
	return state, nil
}

func (r *memoryServingStateRepository) ActiveArtifact(_ context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id := r.active[servingstate.ActiveScope{ProjectID: projectID, Environment: servingstate.NormalizeEnvironment(environment)}]
	if id == "" {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return r.states[id], r.artifacts[id], nil
}

func (r *memoryServingStateRepository) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.states[id]
	if !ok {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	return state, nil
}

func (r *memoryServingStateRepository) ArtifactByServingState(_ context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	artifact, ok := r.artifacts[id]
	if !ok {
		return servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return artifact, nil
}

func (r *memoryServingStateRepository) ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]servingstate.ActiveScope, 0, len(r.active))
	for scope := range r.active {
		out = append(out, scope)
	}
	return out, nil
}

func testRuntimeGraph() (projectgraph.ProjectGraph, error) {
	resources := []projectgraph.Resource{
		{ID: projectgraph.ResourceID("test"), Kind: projectgraph.KindSemanticModel, Name: "test"},
		{ID: projectgraph.ResourceID("executive-sales"), Kind: projectgraph.KindDashboard, Name: "executive_sales"},
		{ID: projectgraph.ResourceID("model.orders"), Kind: projectgraph.KindModel, Name: "orders"},
		{ID: projectgraph.ResourceID("connection:test"), Kind: projectgraph.KindConnection, Name: "test_connection"},
		{ID: projectgraph.ResourceID("source:test"), Kind: projectgraph.KindSource, Name: "test_source"},
		{ID: projectgraph.ResourceID("pipeline:visuals-refresh"), Kind: projectgraph.KindPipeline, Name: "visuals_refresh"},
	}
	return projectgraph.NewProjectGraph(resources, nil)
}

type testRuntimeFactory struct {
	graph    projectgraph.ProjectGraph
	subjects []testRuntimeSubject
}

type testRuntimeSubject struct {
	subject access.SubjectRef
	role    access.ProjectRole
}

func (f testRuntimeFactory) Prepare(_ context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(servingstate.NormalizeEnvironment(input.State.Environment)), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	model := testSemanticModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return nil, fmt.Errorf("compile test semantic model: %w", err)
	}
	bindings := make([]accesssnapshot.RoleBinding, 0, len(f.subjects))
	for _, subject := range f.subjects {
		sum := sha256.Sum256([]byte(subject.subject.ID + "\x00" + string(subject.role)))
		bindings = append(bindings, accesssnapshot.RoleBinding{
			ID: "test-binding-" + hex.EncodeToString(sum[:8]), Name: "test fixture project role", Subject: subject.subject,
			Role: subject.role, Capabilities: access.ProjectRoleCapabilities(subject.role),
		})
		permissionRoles := []access.PermissionRole{access.PermissionRoleViewer}
		if subject.role == access.ProjectRoleAdmin {
			// Project Admin deliberately does not imply data access in the typed
			// contract. The test development principal exercises both admin and
			// BI surfaces, so grant those independent presets explicitly.
			permissionRoles = []access.PermissionRole{access.PermissionRoleProjectAdmin, access.PermissionRoleExplorer}
		}
		for _, permissionRole := range permissionRoles {
			typed, typedErr := access.NewTypedRoleBinding(
				"test-typed-binding-"+hex.EncodeToString(sum[:8])+"-"+string(permissionRole),
				"test fixture typed project role",
				subject.subject,
				permissionRole,
				input.State.ProjectID,
			)
			if typedErr != nil {
				return nil, fmt.Errorf("build test typed role for %q: %w", subject.subject.ID, typedErr)
			}
			bindings = append(bindings, typed)
		}
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, f.graph, bindings, nil, nil)
	if err != nil {
		return nil, err
	}
	return testPreparedRuntime{
		authorization: authorization, snapshotID: input.State.DuckLakeSnapshotID,
		semanticModel: model, compiledModel: planner.CompiledModel(),
	}, nil
}

type testPreparedRuntime struct {
	authorization accesssnapshot.AuthorizationSnapshot
	snapshotID    int64
	semanticModel *semanticmodel.Model
	compiledModel *semanticquery.CompiledModel
}

func (r testPreparedRuntime) Close() error { return nil }
func (r testPreparedRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}
func (r testPreparedRuntime) DuckLakeSnapshotID() int64 { return r.snapshotID }

// ProjectManifest and CompiledSemanticModel make the app fixture expose the
// same activation-owned semantic metadata that the public catalog consumes in
// production. Keeping both facts on one prepared runtime prevents tests from
// accidentally making catalog visibility public without a compiled source.
func (r testPreparedRuntime) ProjectManifest() projectmanifest.ResourceManifest {
	if r.semanticModel == nil {
		return projectmanifest.ResourceManifest{}
	}
	return projectmanifest.ResourceManifest{SemanticModels: map[string]*semanticmodel.Model{
		"test": r.semanticModel.ExecutionSnapshot(),
	}}
}

func (r testPreparedRuntime) CompiledSemanticModel(modelID string) (*semanticquery.CompiledModel, bool) {
	if modelID != "test" || r.compiledModel == nil {
		return nil, false
	}
	return r.compiledModel, true
}

type testRuntimeAuthorizationInstaller struct{}

func (testRuntimeAuthorizationInstaller) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}
