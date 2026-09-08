package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

// Test application routes are project-scoped, so their fixtures need the same
// generation-bound runtime contract as production. Keep one host per test
// database and close it from testStore's cleanup hook.
var testRuntimeHosts sync.Map // map[*sql.DB]*runtimehostmodule.Module

func closeTestRuntimeHost(database *sql.DB) {
	if database == nil {
		return
	}
	if value, ok := testRuntimeHosts.LoadAndDelete(database); ok {
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
func ensureTestRuntimeHost(ctx context.Context, store *platform.Store, states *servingstatemodule.Module, projectID projectgraph.ResourceID, environment servingstate.Environment) (*runtimehostmodule.Module, error) {
	if store == nil || states == nil {
		return nil, errors.New("test runtime fixture requires store and serving states")
	}
	if projectID == "" {
		projectID = testProjectID
	}
	if environment == "" {
		environment = servingstate.DefaultEnvironment
	}
	if value, ok := testRuntimeHosts.Load(store.SQLDB()); ok {
		host := value.(*runtimehostmodule.Module)
		if host.ProjectID() == projectID && host.Environment() == environment {
			return host, nil
		}
		return nil, fmt.Errorf("test runtime host already bound to %s/%s", host.ProjectID(), host.Environment())
	}

	graph, err := testRuntimeGraph(projectID)
	if err != nil {
		return nil, err
	}
	repository := accesssqlite.NewRepository(store.SQLDB())
	principals, err := repository.ListPrincipals(ctx, access.PrincipalFilter{})
	if err != nil {
		return nil, fmt.Errorf("list test principals: %w", err)
	}
	platformAdmins := map[string]struct{}{}
	rows, err := store.SQLDB().QueryContext(ctx, `SELECT principal_id FROM platform_role_bindings WHERE role = ?`, string(access.PlatformRoleAdmin))
	if err != nil {
		return nil, fmt.Errorf("list test platform admins: %w", err)
	}
	for rows.Next() {
		var principalID string
		if err := rows.Scan(&principalID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan test platform admin: %w", err)
		}
		platformAdmins[principalID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate test platform admins: %w", err)
	}
	_ = rows.Close()
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
	if err := addSubject("dev", access.ProjectRoleAdmin); err != nil {
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
	actual, loaded := testRuntimeHosts.LoadOrStore(store.SQLDB(), host)
	if loaded {
		_ = host.Close()
		return actual.(*runtimehostmodule.Module), nil
	}
	return host, nil
}

func testRuntimeGraph(projectID projectgraph.ResourceID) (projectgraph.ProjectGraph, error) {
	resources := []projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "project"},
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
	bindings := make([]accesssnapshot.RoleBinding, 0, len(f.subjects))
	for _, subject := range f.subjects {
		sum := sha256.Sum256([]byte(subject.subject.ID + "\x00" + string(subject.role)))
		bindings = append(bindings, accesssnapshot.RoleBinding{
			ID: "test-binding-" + hex.EncodeToString(sum[:8]), Name: "test fixture project role", Subject: subject.subject,
			Role: subject.role, Capabilities: access.ProjectRoleCapabilities(subject.role),
		})
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, f.graph, bindings, nil, nil)
	if err != nil {
		return nil, err
	}
	return testPreparedRuntime{authorization: authorization, identity: identity, snapshotID: input.State.DuckLakeSnapshotID}, nil
}

type testPreparedRuntime struct {
	authorization accesssnapshot.AuthorizationSnapshot
	identity      projectgraph.ServingIdentity
	snapshotID    int64
}

func (r testPreparedRuntime) Close() error { return nil }
func (r testPreparedRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}
func (r testPreparedRuntime) DuckLakeSnapshotID() int64              { return r.snapshotID }
func (r testPreparedRuntime) Identity() projectgraph.ServingIdentity { return r.identity }

// SemanticModelProjection keeps the assembled app fixture on the same
// runtime-owned model seam used by saved-exploration mutations. The
// projection is detached per call, so a test cannot accidentally mutate the
// immutable fixture generation through an API request.
func (r testPreparedRuntime) SemanticModelProjection(modelID projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if modelID != "test" {
		return nil, false
	}
	return testSemanticModel(), true
}

type testRuntimeAuthorizationInstaller struct{}

func (testRuntimeAuthorizationInstaller) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}
