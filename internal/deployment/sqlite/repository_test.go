package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
)

func TestCreateDeploymentRoundTripsAndRejectsConflictingReplay(t *testing.T) {
	store, repository := openDeploymentRepository(t)
	insertDeploymentGeneration(t, store, "project", "prod", "generation_new", "validated", deploymentDigest("a"))
	input := deploymentCreateInput(t, "deployment_1", "project", "prod", "generation_new", "")

	created, err := repository.CreateDeployment(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.ServingIdentity != input.ServingIdentity || created.ArtifactDigest != input.ArtifactDigest || created.Status != deployment.StatusPending {
		t.Fatalf("created deployment = %#v", created)
	}
	replayed, err := repository.CreateDeployment(t.Context(), input)
	if err != nil || replayed != created {
		t.Fatalf("idempotent replay = %#v, err=%v", replayed, err)
	}
	conflict := input
	conflict.RequestDigest = deploymentDigest("c")
	if _, err := repository.CreateDeployment(t.Context(), conflict); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrConflict", err)
	}
}

func TestCreateDeploymentRequiresExactActivatableGeneration(t *testing.T) {
	store, repository := openDeploymentRepository(t)
	insertDeploymentGeneration(t, store, "other", "prod", "generation_other", "validated", deploymentDigest("a"))
	input := deploymentCreateInput(t, "deployment_wrong_project", "project", "prod", "generation_other", "")
	if _, err := repository.CreateDeployment(t.Context(), input); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("cross-project generation error = %v, want ErrConflict", err)
	}
	insertDeploymentGeneration(t, store, "project", "prod", "generation_failed", "failed", deploymentDigest("a"))
	input = deploymentCreateInput(t, "deployment_failed_generation", "project", "prod", "generation_failed", "")
	if _, err := repository.CreateDeployment(t.Context(), input); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("failed generation error = %v, want ErrConflict", err)
	}
}

func TestCreateDeploymentRollsBackWithReleaseOrWorkflowFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		hooks ActivationHooks
		input func(*testing.T) deployment.CreateInput
	}{
		{
			name: "release linkage",
			hooks: ActivationHooks{LinkRelease: func(context.Context, transaction.Transaction, deployment.CreateInput) error {
				return errors.New("link failure")
			}},
			input: func(t *testing.T) deployment.CreateInput {
				input := deploymentCreateInput(t, "deployment_release", "project", "prod", "generation_new", "")
				input.ReleaseID = "release_1"
				return input
			},
		},
		{
			name: "workflow",
			hooks: ActivationHooks{RecordWorkflow: jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
				return errors.New("workflow failure")
			})},
			input: func(t *testing.T) deployment.CreateInput {
				input := deploymentCreateInput(t, "deployment_workflow", "project", "prod", "generation_new", "")
				input.Workflow = jobs.WorkflowIntent{Job: jobs.EnqueueInput{ID: "deployment:activate"}}
				return input
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := openDeploymentRepository(t)
			insertDeploymentGeneration(t, store, "project", "prod", "generation_new", "validated", deploymentDigest("a"))
			repository := NewRepositoryWithHooks(store.SQLDB(), test.hooks)
			input := test.input(t)
			if _, err := repository.CreateDeployment(t.Context(), input); err == nil {
				t.Fatal("CreateDeployment unexpectedly succeeded")
			}
			if _, err := repository.DeploymentByID(t.Context(), input.ID); !errors.Is(err, deployment.ErrNotFound) {
				t.Fatalf("rolled-back deployment error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestActivateDeploymentAtomicallySwapsOneProjectGeneration(t *testing.T) {
	store, repository := openDeploymentRepository(t)
	insertDeploymentGeneration(t, store, "project", "prod", "generation_old", "active", deploymentDigest("b"))
	insertDeploymentGeneration(t, store, "project", "prod", "generation_new", "validated", deploymentDigest("a"))
	setActiveDeploymentGeneration(t, store, "project", "prod", "generation_old")
	created, err := repository.CreateDeployment(t.Context(), deploymentCreateInput(t, "deployment_1", "project", "prod", "generation_new", "generation_old"))
	if err != nil {
		t.Fatal(err)
	}
	activation := deployment.ActivationInput{
		DeploymentID: created.ID, ServingIdentity: created.ServingIdentity, ArtifactDigest: created.ArtifactDigest,
		PriorGenerationID: created.PriorGenerationID, ActivationPrincipal: "principal_1", VerificationDigest: deploymentDigest("d"),
	}
	active, err := repository.ActivateDeployment(t.Context(), activation)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != deployment.StatusActive || active.ActivationPrincipal != "principal_1" || active.VerificationDigest != deploymentDigest("d") {
		t.Fatalf("active deployment = %#v", active)
	}
	assertDeploymentGeneration(t, store, "project", "prod", "generation_new", "generation_old")
	replayed, err := repository.ActivateDeployment(t.Context(), activation)
	if err != nil || replayed.ID != active.ID || replayed.Status != deployment.StatusActive {
		t.Fatalf("activation replay = %#v, err=%v", replayed, err)
	}
}

func TestActivateDeploymentCASConflictRollsBackStateChanges(t *testing.T) {
	store, repository := openDeploymentRepository(t)
	insertDeploymentGeneration(t, store, "project", "prod", "generation_old", "active", deploymentDigest("b"))
	insertDeploymentGeneration(t, store, "project", "prod", "generation_new", "validated", deploymentDigest("a"))
	insertDeploymentGeneration(t, store, "project", "prod", "generation_intruder", "inactive", deploymentDigest("e"))
	setActiveDeploymentGeneration(t, store, "project", "prod", "generation_old")
	created, err := repository.CreateDeployment(t.Context(), deploymentCreateInput(t, "deployment_1", "project", "prod", "generation_new", "generation_old"))
	if err != nil {
		t.Fatal(err)
	}
	setActiveDeploymentGeneration(t, store, "project", "prod", "generation_intruder")
	_, err = repository.ActivateDeployment(t.Context(), deployment.ActivationInput{
		DeploymentID: created.ID, ServingIdentity: created.ServingIdentity, ArtifactDigest: created.ArtifactDigest,
		PriorGenerationID: created.PriorGenerationID, ActivationPrincipal: "principal_1", VerificationDigest: deploymentDigest("d"),
	})
	if !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("ActivateDeployment error = %v, want ErrConflict", err)
	}
	var candidateStatus, priorStatus string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM serving_states WHERE id='generation_new'`).Scan(&candidateStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM serving_states WHERE id='generation_old'`).Scan(&priorStatus); err != nil {
		t.Fatal(err)
	}
	if candidateStatus != "validated" || priorStatus != "active" {
		t.Fatalf("CAS failure mutated states: candidate=%q prior=%q", candidateStatus, priorStatus)
	}
}

func TestDeploymentReadsRejectMalformedPersistedServingIdentity(t *testing.T) {
	store, repository := openDeploymentRepository(t)
	insertDeploymentGeneration(t, store, "project", "prod", "generation_new", "validated", deploymentDigest("a"))
	created, err := repository.CreateDeployment(t.Context(), deploymentCreateInput(t, "deployment_1", "project", "prod", "generation_new", ""))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.SQLDB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), `UPDATE project_deployments SET environment='prod/env' WHERE id=?`, created.ID); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DeploymentByID(t.Context(), created.ID); err == nil {
		t.Fatal("DeploymentByID accepted malformed persisted identity")
	}
}

func openDeploymentRepository(t *testing.T) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
}

func testRepository(t *testing.T) (context.Context, *sql.DB, *Repository) {
	t.Helper()
	store, repository := openDeploymentRepository(t)
	return context.Background(), store.SQLDB(), repository
}

func deploymentCreateInput(t *testing.T, id, projectID, environment, generationID, priorGenerationID string) deployment.CreateInput {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), environment, generationID)
	if err != nil {
		t.Fatal(err)
	}
	return deployment.CreateInput{
		ID: id, ServingIdentity: identity, ArtifactDigest: deploymentDigest("a"), PriorGenerationID: priorGenerationID,
		RequestDigest: deploymentDigest("f"), CreatedBy: "principal_1",
	}
}

func insertDeploymentGeneration(t *testing.T, store *platform.Store, projectID, environment, generationID, status, digest string) {
	t.Helper()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id,project_id,environment,status,source,digest) VALUES (?,?,?,?,?,?)`, generationID, projectID, environment, status, "publish", digest); err != nil {
		t.Fatal(err)
	}
}

func setActiveDeploymentGeneration(t *testing.T, store *platform.Store, projectID, environment, generationID string) {
	t.Helper()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO project_active_serving_states(project_id,environment,generation_id) VALUES(?,?,?) ON CONFLICT(project_id,environment) DO UPDATE SET generation_id=excluded.generation_id`, projectID, environment, generationID); err != nil {
		t.Fatal(err)
	}
}

func assertDeploymentGeneration(t *testing.T, store *platform.Store, projectID, environment, activeID, drainingID string) {
	t.Helper()
	var gotActive, activeStatus, drainingStatus string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT generation_id FROM project_active_serving_states WHERE project_id=? AND environment=?`, projectID, environment).Scan(&gotActive); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM serving_states WHERE id=?`, activeID).Scan(&activeStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM serving_states WHERE id=?`, drainingID).Scan(&drainingStatus); err != nil {
		t.Fatal(err)
	}
	if gotActive != activeID || activeStatus != "active" || drainingStatus != "draining" {
		t.Fatalf("generation cutover = active pointer %q, active status %q, prior status %q", gotActive, activeStatus, drainingStatus)
	}
}

func deploymentDigest(hexDigit string) string {
	return "sha256:" + strings.Repeat(hexDigit, 64)
}
