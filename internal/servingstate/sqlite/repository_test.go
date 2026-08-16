package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestRepositoryCreateRejectsMalformedProjectIdentity(t *testing.T) {
	_, repo := openRepo(t)
	if _, err := repo.Create(t.Context(), servingstate.CreateInput{}); err == nil {
		t.Fatal("Create() accepted empty project identity")
	}
	for _, projectID := range []projectgraph.ResourceID{"project/id", " project", "project "} {
		if _, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: servingstate.DefaultEnvironment}); err == nil {
			t.Fatalf("Create() accepted malformed project identity %q", projectID)
		}
	}
}

func TestRepositoryRejectsMalformedPersistedServingIdentity(t *testing.T) {
	store, repo := openRepo(t)
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: "project", Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE serving_states SET project_id = 'not a project id' WHERE id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ByID(t.Context(), created.ID); err == nil {
		t.Fatal("ByID accepted malformed persisted project identity")
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE serving_states SET project_id = 'project', environment = 'prod/env' WHERE id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ByID(t.Context(), created.ID); err == nil {
		t.Fatal("ByID accepted malformed persisted environment")
	}
}

func TestRepositoryRejectsIdentityAliasesAtValidationAndActivationBoundaries(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	created := createValidated(t, repo, projectID, servingstate.DefaultEnvironment)

	validation := validValidation(projectID)
	validation.ProjectID = projectgraph.ResourceID(" project")
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, validArtifact(created.ID)); err == nil {
		t.Fatal("SaveValidated accepted project identity alias")
	}
	if _, err := repo.Activate(t.Context(), projectgraph.ResourceID(" project"), servingstate.DefaultEnvironment, created.ID, ""); err == nil {
		t.Fatal("Activate accepted project identity alias")
	}
	if _, err := repo.Activate(t.Context(), projectID, servingstate.Environment("dev "), created.ID, ""); err == nil {
		t.Fatal("Activate accepted environment alias")
	}
	if _, _, err := repo.ActiveArtifact(t.Context(), projectgraph.ResourceID(" project"), servingstate.DefaultEnvironment); err == nil {
		t.Fatal("ActiveArtifact accepted project identity alias")
	}
}

func TestRepositoryRejectsMalformedEnvironmentsForScopedOperations(t *testing.T) {
	_, repo := openRepo(t)
	for _, environment := range []servingstate.Environment{"prod ", "prod/env", "-prod"} {
		if _, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectgraph.ResourceID("project"), Environment: environment}); err == nil {
			t.Fatalf("Create accepted malformed environment %q", environment)
		}
	}
	for _, environment := range []string{"", "prod ", "prod/env"} {
		if _, err := repo.ReferencedDuckLakeSnapshots(t.Context(), environment); err == nil {
			t.Fatalf("ReferencedDuckLakeSnapshots accepted malformed environment %q", environment)
		}
		if err := repo.ReconcileRetention(t.Context(), environment, time.Now()); err == nil {
			t.Fatalf("ReconcileRetention accepted malformed environment %q", environment)
		}
	}
}

func TestRepositoryRejectsInvalidArtifactDigestAndSize(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	invalidDigest := validValidation(projectID)
	invalidDigest.Digest = "digest"
	if _, err := repo.SaveValidated(t.Context(), created.ID, invalidDigest, validArtifact(created.ID)); err == nil {
		t.Fatal("SaveValidated accepted invalid artifact digest")
	}
	negativeSize := validArtifact(created.ID)
	negativeSize.SizeBytes = -1
	if _, err := repo.SaveValidated(t.Context(), created.ID, validValidation(projectID), negativeSize); err == nil {
		t.Fatal("SaveValidated accepted negative artifact size")
	}
}

func TestRepositorySaveValidatedBindsProjectGraphAndArtifact(t *testing.T) {
	store, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	validation := validValidation(projectID)
	artifact := validArtifact(created.ID)
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, artifact); err != nil {
		t.Fatalf("SaveValidated() = %v", err)
	}
	state, err := repo.ByID(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectID != projectID || state.Environment != servingstate.DefaultEnvironment || state.Status != servingstate.StatusValidated {
		t.Fatalf("state = %#v", state)
	}
	for _, table := range []string{"serving_states", "serving_state_artifacts", "assets", "asset_edges", "query_snapshot_leases"} {
		var workspaceColumns int
		query := `SELECT count(*) FROM pragma_table_info('` + table + `') WHERE name IN ('workspace_id', 'workspace_title', 'workspace_scope')`
		if err := store.SQLDB().QueryRowContext(t.Context(), query).Scan(&workspaceColumns); err != nil {
			t.Fatal(err)
		}
		if workspaceColumns != 0 {
			t.Fatalf("%s retained workspace identity", table)
		}
	}
	var oldPointerTable int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'workspace_active_serving_states'`).Scan(&oldPointerTable); err != nil {
		t.Fatal(err)
	}
	if oldPointerTable != 0 {
		t.Fatal("legacy workspace active pointer table retained")
	}

	badArtifact := validArtifact(created.ID)
	badArtifact.Digest = "different"
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, badArtifact); err == nil {
		t.Fatal("SaveValidated() accepted artifact digest mismatch")
	}
}

func TestRepositorySaveValidatedIsIdempotentAndImmutable(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	validation := validValidation(projectID)
	artifact := validArtifact(created.ID)
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, artifact); err != nil {
		t.Fatal(err)
	}
	before, err := repo.ByID(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var assetsBefore int
	if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM assets WHERE serving_state_id = ?`, created.ID).Scan(&assetsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, artifact); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	mutated := validation
	mutated.Digest = "sha256:" + strings.Repeat("b", 64)
	mutated.ProjectDigest = "sha256:" + strings.Repeat("c", 64)
	mutatedArtifact := artifact
	mutatedArtifact.Digest = mutated.Digest
	if _, err := repo.SaveValidated(t.Context(), created.ID, mutated, mutatedArtifact); err == nil {
		t.Fatal("SaveValidated accepted immutable candidate mutation")
	}
	mutatedArtifact = artifact
	mutatedArtifact.ID = "different-artifact"
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, mutatedArtifact); err == nil {
		t.Fatal("SaveValidated accepted immutable artifact identity mutation")
	}
	after, err := repo.ByID(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Digest != before.Digest || after.ProjectDigest != before.ProjectDigest || after.Status != before.Status {
		t.Fatalf("immutable state changed: before=%#v after=%#v", before, after)
	}
	var assetsAfter int
	if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM assets WHERE serving_state_id = ?`, created.ID).Scan(&assetsAfter); err != nil {
		t.Fatal(err)
	}
	if assetsAfter != assetsBefore {
		t.Fatalf("asset count after rejected mutation = %d, want %d", assetsAfter, assetsBefore)
	}
}

func TestRepositorySaveValidatedRollsBackInvalidGraphAndArtifact(t *testing.T) {
	store, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	invalid := validValidation(projectID)
	invalid.Graph = projectgraph.ProjectGraph{}
	if _, err := repo.SaveValidated(t.Context(), created.ID, invalid, validArtifact(created.ID)); err == nil {
		t.Fatal("SaveValidated accepted zero graph")
	}
	assertPendingWithoutArtifacts(t, store, repo, created.ID)

	first := createValidated(t, repo, projectID, "prod")
	firstArtifact, err := repo.ArtifactByServingState(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	duplicateArtifact := validArtifact(second.ID)
	duplicateArtifact.ID = firstArtifact.ID
	if _, err := repo.SaveValidated(t.Context(), second.ID, validValidation(projectID), duplicateArtifact); err == nil {
		t.Fatal("SaveValidated accepted duplicate artifact primary key")
	}
	assertPendingWithoutArtifacts(t, store, repo, second.ID)
}

func TestRepositorySaveValidatedRejectsProjectGraphAndArtifactMismatches(t *testing.T) {
	store, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	wrongProject := validValidation(projectgraph.ResourceID("other"))
	if _, err := repo.SaveValidated(t.Context(), created.ID, wrongProject, validArtifact(created.ID)); err == nil {
		t.Fatal("SaveValidated accepted validation project mismatch")
	}
	graphMismatch := validValidation(projectID)
	graphMismatch.Graph = graphForProject(projectgraph.ResourceID("other"))
	if _, err := repo.SaveValidated(t.Context(), created.ID, graphMismatch, validArtifact(created.ID)); err == nil {
		t.Fatal("SaveValidated accepted graph root mismatch")
	}
	badDigest := validValidation(projectID)
	badArtifact := validArtifact(created.ID)
	badArtifact.Digest = "different"
	if _, err := repo.SaveValidated(t.Context(), created.ID, badDigest, badArtifact); err == nil {
		t.Fatal("SaveValidated accepted artifact digest mismatch")
	}
	badManifest := validArtifact(created.ID)
	badManifest.ManifestJSON = `{"manifest":"different"}`
	if _, err := repo.SaveValidated(t.Context(), created.ID, validValidation(projectID), badManifest); err == nil {
		t.Fatal("SaveValidated accepted artifact manifest mismatch")
	}
	wrongArtifactState := validArtifact(created.ID)
	wrongArtifactState.ServingStateID = "other-state"
	if _, err := repo.SaveValidated(t.Context(), created.ID, validValidation(projectID), wrongArtifactState); err == nil {
		t.Fatal("SaveValidated accepted artifact serving-state mismatch")
	}
	assertPendingWithoutArtifacts(t, store, repo, created.ID)
}

func TestRepositorySaveValidatedRejectsGraphProjectMismatch(t *testing.T) {
	_, repo := openRepo(t)
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectgraph.ResourceID("project"), Environment: servingstate.DefaultEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	validation := validValidation(projectgraph.ResourceID("other"))
	if _, err := repo.SaveValidated(t.Context(), created.ID, validation, validArtifact(created.ID)); err == nil {
		t.Fatal("SaveValidated() accepted graph project mismatch")
	}
}

func TestRepositoryActivationCompareAndSwapDrainsPreviousGeneration(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	first := createValidated(t, repo, projectID, "dev")
	second := createValidated(t, repo, projectID, "dev")
	if _, err := repo.Activate(t.Context(), projectID, "dev", first.ID, ""); err != nil {
		t.Fatalf("activate first: %v", err)
	}
	if _, err := repo.Activate(t.Context(), projectID, "dev", second.ID, "stale"); !errors.Is(err, servingstate.ErrActivationConflict) {
		t.Fatalf("stale activation error = %v, want ErrActivationConflict", err)
	}
	active, _, err := repo.ActiveArtifact(t.Context(), projectID, "dev")
	if err != nil || active.ID != first.ID {
		t.Fatalf("active after stale activation = %s, %v", active.ID, err)
	}
	if _, err := repo.Activate(t.Context(), projectID, "dev", second.ID, first.ID); err != nil {
		t.Fatalf("activate second: %v", err)
	}
	old, err := repo.ByID(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != servingstate.StatusDraining {
		t.Fatalf("old generation status = %q, want draining", old.Status)
	}
	if old.SupersededAt == "" {
		t.Fatal("old generation superseded_at is empty")
	}
}

func TestRepositoryTracksActiveGenerationsPerEnvironment(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	dev := createValidated(t, repo, projectID, "dev")
	prod := createValidated(t, repo, projectID, "prod")
	if _, err := repo.Activate(t.Context(), projectID, "dev", dev.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Activate(t.Context(), projectID, "prod", prod.ID, ""); err != nil {
		t.Fatal(err)
	}
	activeDev, _, err := repo.ActiveArtifact(t.Context(), projectID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	activeProd, _, err := repo.ActiveArtifact(t.Context(), projectID, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if activeDev.ID != dev.ID || activeProd.ID != prod.ID {
		t.Fatalf("active generations dev=%s prod=%s, want %s/%s", activeDev.ID, activeProd.ID, dev.ID, prod.ID)
	}
	scopes, err := repo.ListActiveScopes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("active scopes = %#v, want two environments", scopes)
	}
}

func TestRepositoryConcurrentActivationRejectsStaleCAS(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	first := createValidated(t, repo, projectID, "dev")
	second := createValidated(t, repo, projectID, "dev")
	third := createValidated(t, repo, projectID, "dev")
	if _, err := repo.Activate(t.Context(), projectID, "dev", first.ID, ""); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type activationResult struct {
		id  servingstate.ID
		err error
	}
	results := make(chan activationResult, 2)
	var wg sync.WaitGroup
	for _, candidate := range []servingstate.ID{second.ID, third.ID} {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Activate(context.Background(), projectID, "dev", candidate, first.ID)
			results <- activationResult{id: candidate, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded, conflicts int
	var winner servingstate.ID
	for result := range results {
		if result.err == nil {
			succeeded++
			winner = result.id
		} else if errors.Is(result.err, servingstate.ErrActivationConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent activation %s error = %v", result.id, result.err)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatalf("concurrent activation results succeeded=%d conflicts=%d, want 1/1", succeeded, conflicts)
	}
	active, _, err := repo.ActiveArtifact(t.Context(), projectID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != winner {
		t.Fatalf("active winner = %s, want %s", active.ID, winner)
	}
}

func TestRepositoryReconcileRetentionOnlyDeletesTargetEnvironment(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	devFirst := createValidated(t, repo, projectID, "dev")
	devSecond := createValidated(t, repo, projectID, "dev")
	if _, err := repo.Activate(t.Context(), projectID, "dev", devFirst.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Activate(t.Context(), projectID, "dev", devSecond.ID, devFirst.ID); err != nil {
		t.Fatal(err)
	}
	prod := createValidated(t, repo, projectID, "prod")
	if _, err := repo.db.ExecContext(t.Context(), `UPDATE serving_states SET status = 'draining' WHERE id = ?`, prod.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReconcileRetention(t.Context(), "dev", time.Now()); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, repo, devFirst.ID, servingstate.StatusDeleted)
	assertStatus(t, repo, devSecond.ID, servingstate.StatusActive)
	assertStatus(t, repo, prod.ID, servingstate.StatusDraining)
}

func TestRepositoryReferencesSnapshotsAndLeasesByEnvironment(t *testing.T) {
	_, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	dev := createValidated(t, repo, projectID, "dev")
	prod := createValidated(t, repo, projectID, "prod")
	if err := repo.RecordDuckLakeSnapshot(t.Context(), dev.ID, 7); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordDuckLakeSnapshot(t.Context(), prod.ID, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Activate(t.Context(), projectID, "dev", dev.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Activate(t.Context(), projectID, "prod", prod.ID, ""); err != nil {
		t.Fatal(err)
	}
	refs, err := repo.ReferencedDuckLakeSnapshots(t.Context(), "dev")
	if err != nil || len(refs) != 1 || refs[0] != 7 {
		t.Fatalf("dev references = %#v, %v", refs, err)
	}
	foreign, err := repo.ForeignEnvironmentDuckLakeSnapshots(t.Context(), "dev")
	if err != nil || len(foreign) != 1 || foreign[0] != 11 {
		t.Fatalf("dev foreign references = %#v, %v", foreign, err)
	}
	devLease, err := repo.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: dev.ID, DuckLakeSnapshotID: 7, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	prodLease, err := repo.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: prod.ID, DuckLakeSnapshotID: 11, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := repo.LeasedDuckLakeSnapshots(t.Context(), "dev")
	if err != nil || len(leased) != 1 || leased[0] != 7 {
		t.Fatalf("dev leased snapshots = %#v, %v", leased, err)
	}
	if err := repo.ReleaseQuerySnapshotLease(t.Context(), devLease); err != nil {
		t.Fatal(err)
	}
	if err := repo.ExtendQuerySnapshotLease(t.Context(), devLease, time.Now().Add(time.Hour)); !errors.Is(err, servingstate.ErrSnapshotLeaseLost) {
		t.Fatalf("extend released lease = %v, want ErrSnapshotLeaseLost", err)
	}
	leased, err = repo.LeasedDuckLakeSnapshots(t.Context(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 0 {
		t.Fatalf("dev leased snapshots after release = %#v, want empty", leased)
	}
	if err := repo.ReleaseQuerySnapshotLease(t.Context(), prodLease); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPointerForeignGenerationRejectedAndActiveUnique(t *testing.T) {
	store, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	first := createValidated(t, repo, projectID, "dev")
	second := createValidated(t, repo, projectID, "dev")
	if _, err := repo.Activate(t.Context(), projectID, "dev", first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO project_active_serving_states(project_id, environment, generation_id) VALUES ('other','dev',?)`, first.ID); err == nil {
		t.Fatal("pointer accepted generation from another project")
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE serving_states SET status = 'active' WHERE id = ?`, second.ID); err == nil {
		t.Fatal("serving_states allowed two active generations for one project/environment")
	}
}

func TestRepositoryLeaseAndPointerCascadeWithGenerationDeletion(t *testing.T) {
	store, repo := openRepo(t)
	projectID := projectgraph.ResourceID("project")
	state := createValidated(t, repo, projectID, "dev")
	if _, err := repo.Activate(t.Context(), projectID, "dev", state.ID, ""); err != nil {
		t.Fatal(err)
	}
	leaseID, err := repo.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: state.ID, DuckLakeSnapshotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `DELETE FROM serving_states WHERE id = ?`, state.ID); err != nil {
		t.Fatal(err)
	}
	var pointerCount, leaseCount int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT count(*) FROM project_active_serving_states`).Scan(&pointerCount); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT count(*) FROM query_snapshot_leases WHERE id = ?`, leaseID).Scan(&leaseCount); err != nil {
		t.Fatal(err)
	}
	if pointerCount != 0 || leaseCount != 0 {
		t.Fatalf("cascade counts pointer=%d lease=%d", pointerCount, leaseCount)
	}
}

func openRepo(t *testing.T) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewRepository(store.SQLDB())
}

func assertPendingWithoutArtifacts(t *testing.T, store *platform.Store, repo *Repository, id servingstate.ID) {
	t.Helper()
	state, err := repo.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != servingstate.StatusPending {
		t.Fatalf("state %s status = %q, want pending", id, state.Status)
	}
	if _, err := repo.ArtifactByServingState(t.Context(), id); !errors.Is(err, servingstate.ErrNotFound) {
		t.Fatalf("state %s artifact error = %v, want ErrNotFound", id, err)
	}
	var assets int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT count(*) FROM assets WHERE serving_state_id = ?`, id).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if assets != 0 {
		t.Fatalf("state %s assets = %d, want zero after rollback", id, assets)
	}
}

func assertStatus(t *testing.T, repo *Repository, id servingstate.ID, want servingstate.Status) {
	t.Helper()
	state, err := repo.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != want {
		t.Fatalf("state %s status = %q, want %q", id, state.Status, want)
	}
}

func createValidated(t *testing.T, repo *Repository, projectID projectgraph.ResourceID, environment servingstate.Environment) servingstate.State {
	t.Helper()
	created, err := repo.Create(t.Context(), servingstate.CreateInput{ProjectID: projectID, Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveValidated(t.Context(), created.ID, validValidation(projectID), validArtifact(created.ID)); err != nil {
		t.Fatal(err)
	}
	return created
}

func validValidation(projectID projectgraph.ResourceID) servingstate.Validation {
	return servingstate.Validation{
		Digest:        "sha256:" + strings.Repeat("d", 64),
		ManifestJSON:  "{}",
		ProjectID:     projectID,
		ProjectDigest: "sha256:" + strings.Repeat("a", 64),
		Graph:         graphForProject(projectID),
	}
}

func graphForProject(projectID projectgraph.ResourceID) projectgraph.ProjectGraph {
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "project"},
		{ID: projectgraph.ResourceID("dashboard"), Kind: projectgraph.KindDashboard, Name: "dashboard"},
	}, []projectgraph.Edge{{From: projectID, To: "dashboard", Relation: "contains"}})
	if err != nil {
		panic(err)
	}
	return graphValue
}

func validArtifact(id servingstate.ID) servingstate.Artifact {
	return servingstate.Artifact{ID: "artifact_" + string(id), ServingStateID: id, Digest: "sha256:" + strings.Repeat("d", 64), Format: "tar.gz", Path: "artifact.tar.gz", ManifestJSON: "{}"}
}
