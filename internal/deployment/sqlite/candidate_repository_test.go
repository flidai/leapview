package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestCandidateRepositoryPersistsResumeAndOptimisticReplacementAcrossRestart(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	if _, err := db.ExecContext(ctx, `INSERT INTO project_deployments (id, project_id, environment, request_digest, status, activated_at) VALUES ('deployment_current', 'finance', 'prod', 'sha256:current', 'active', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	firstDigest := "sha256:" + strings.Repeat("a", 64)
	secondDigest := "sha256:" + strings.Repeat("b", 64)
	candidate := candidateRecord(t, now, "cand_1", "finance", "principal_1", firstDigest)

	created, resumed, err := repository.StartCandidate(ctx, candidate, 4)
	if err != nil || resumed {
		t.Fatalf("StartCandidate() = %#v, resumed=%v, err=%v", created, resumed, err)
	}
	restarted := NewRepositoryWithHooks(db, ActivationHooks{})
	if _, err := db.ExecContext(ctx, `UPDATE project_deployments SET status = 'superseded' WHERE id = 'deployment_current'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_deployments (id, project_id, environment, request_digest, status, activated_at) VALUES ('deployment_advanced_after_candidate_started', 'finance', 'prod', 'sha256:advanced', 'active', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	replayRequest := candidateRecord(t, now.Add(time.Minute), "cand_other", "finance", "principal_1", firstDigest)
	replayRequest.BaseGeneration = "deployment_advanced_after_candidate_started"
	replayed, resumed, err := restarted.StartCandidate(ctx, replayRequest, 4)
	if err != nil || resumed || replayed.ID == created.ID || replayed.BaseGeneration != replayRequest.BaseGeneration {
		t.Fatalf("stale StartCandidate() = %#v, resumed=%v, err=%v", replayed, resumed, err)
	}
	conflicting := candidateRecord(t, now, "cand_conflict", "finance", "principal_1", secondDigest)
	conflicting.BaseGeneration = "deployment_current"
	if _, _, err := restarted.StartCandidate(ctx, conflicting, 4); !errors.Is(err, deployment.ErrCandidateConflict) {
		t.Fatalf("conflicting StartCandidate() error = %v", err)
	}

	next, err := replayed.ReplaceArtifact(firstDigest, secondDigest, now.Add(time.Minute), now.Add(2*time.Hour))
	require.NoError(t, err)
	saved, err := restarted.SaveCandidate(ctx, next, replayed.Revision)
	if err != nil || saved.ArtifactDigest != secondDigest {
		t.Fatalf("SaveCandidate() = %#v, %v", saved, err)
	}
	if _, err := restarted.SaveCandidate(ctx, next, replayed.Revision); !errors.Is(err, deployment.ErrCandidateConflict) {
		t.Fatalf("stale SaveCandidate() error = %v", err)
	}
}

func TestCandidateRepositoryIsolatesActiveSessionsByCandidateKey(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	first := candidateRecord(t, now, "cand_1", "finance", "principal_1", digest)
	first.Key = "github:pull/41"
	second := candidateRecord(t, now, "cand_2", "finance", "principal_1", digest)
	second.Key = "github:pull/42"
	if _, _, err := repository.StartCandidate(ctx, first, 4); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.StartCandidate(ctx, second, 4); err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.ActiveCandidate(
		ctx,
		"lvinst_prod",
		"finance",
		"principal_1",
		"github:pull/42",
	)
	if err != nil || resolved.ID != second.ID {
		t.Fatalf("ActiveCandidate() = %#v, %v", resolved, err)
	}
}

func TestCandidateRepositoryConcurrentStartsFenceAgainstCutover(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	if _, err := db.ExecContext(ctx, `INSERT INTO project_deployments (id, project_id, environment, request_digest, status, activated_at) VALUES ('deployment_before', 'finance', 'prod', 'sha256:before', 'active', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	var group sync.WaitGroup
	results := make(chan deployment.Candidate, 8)
	errorsCh := make(chan error, 8)
	for index := range 8 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			candidate := candidateRecord(t, now.Add(time.Duration(index)*time.Millisecond), fmt.Sprintf("concurrent_%d", index), "finance", "principal_1", digest)
			stored, _, err := repository.StartCandidate(ctx, candidate, 20)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- stored
		}(index)
	}
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	for candidate := range results {
		if candidate.BaseGeneration != "deployment_before" {
			t.Fatalf("concurrent candidate base=%q", candidate.BaseGeneration)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE project_deployments SET status = 'superseded' WHERE id = 'deployment_before'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_deployments (id, project_id, environment, request_digest, status, activated_at) VALUES ('deployment_after', 'finance', 'prod', 'sha256:after', 'active', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	replaced := candidateRecord(t, now.Add(time.Hour), "after-cutover", "finance", "principal_1", digest)
	stored, resumed, err := repository.StartCandidate(ctx, replaced, 20)
	if err != nil || resumed || stored.BaseGeneration != "deployment_after" {
		t.Fatalf("post-cutover candidate=%#v resumed=%v err=%v", stored, resumed, err)
	}
}

func TestCandidateRepositoryWriteFenceBlocksCutoverUntilCandidateCommit(t *testing.T) {
	ctx, db, repository := testRepository(t)
	db.SetMaxOpenConns(2)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	if _, err := db.ExecContext(ctx, `INSERT INTO project_deployments (id, project_id, environment, request_digest, status, activated_at) VALUES ('deployment_before', 'finance', 'prod', 'sha256:before', 'active', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	candidate := candidateRecord(t, now, "fenced-candidate", "finance", "principal_1", "sha256:"+strings.Repeat("a", 64))
	baseRead := make(chan struct{})
	release := make(chan struct{})
	repository.candidateBaseReadHook = func() {
		close(baseRead)
		<-release
	}

	activationConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer activationConn.Close()
	candidateDone := make(chan deployment.Candidate, 1)
	candidateErr := make(chan error, 1)
	go func() {
		stored, _, startErr := repository.StartCandidate(ctx, candidate, 4)
		candidateDone <- stored
		candidateErr <- startErr
	}()
	select {
	case <-baseRead:
	case <-time.After(time.Second):
		t.Fatal("candidate did not reach fenced base read")
	}
	activationDone := make(chan error, 1)
	go func() {
		tx, beginErr := activationConn.BeginTx(ctx, nil)
		if beginErr != nil {
			activationDone <- beginErr
			return
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE project_deployments SET status = 'superseded' WHERE id = 'deployment_before'`); updateErr != nil {
			_ = tx.Rollback()
			activationDone <- updateErr
			return
		}
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO project_deployments (id, project_id, environment, request_digest, status, activated_at) VALUES ('deployment_after', 'finance', 'prod', 'sha256:after', 'active', CURRENT_TIMESTAMP)`); insertErr != nil {
			_ = tx.Rollback()
			activationDone <- insertErr
			return
		}
		activationDone <- tx.Commit()
	}()
	select {
	case err := <-activationDone:
		t.Fatalf("activation committed before candidate commit, err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	stored := <-candidateDone
	if err := <-candidateErr; err != nil {
		t.Fatalf("candidate start: %v", err)
	}
	if stored.BaseGeneration != "deployment_before" {
		t.Fatalf("candidate base=%q, want deployment_before", stored.BaseGeneration)
	}
	if err := <-activationDone; err != nil {
		t.Fatalf("activation after candidate commit: %v", err)
	}
	active, err := repository.ActiveCandidateBaseGeneration(ctx, "finance", "prod")
	if err != nil || active != "deployment_after" {
		t.Fatalf("active generation=%q err=%v", active, err)
	}
}

func TestCandidateRepositoryEnforcesQuotaAndExpiresOnlyMatchingTarget(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	first := candidateRecord(t, now, "cand_1", "finance", "principal_1", "sha256:"+strings.Repeat("a", 64))
	first.ExpiresAt = now.Add(time.Minute)
	if _, _, err := repository.StartCandidate(ctx, first, 1); err != nil {
		t.Fatal(err)
	}
	second := candidateRecord(t, now, "cand_2", "marketing", "principal_1", "sha256:"+strings.Repeat("b", 64))
	if _, _, err := repository.StartCandidate(ctx, second, 1); !errors.Is(err, deployment.ErrCandidateQuota) {
		t.Fatalf("quota error = %v", err)
	}

	foreign := candidateRecord(t, now, "cand_foreign", "marketing", "principal_1", "sha256:"+strings.Repeat("c", 64))
	foreign.TargetID = "lvinst_other"
	foreign.ExpiresAt = now.Add(time.Minute)
	if _, _, err := repository.StartCandidate(ctx, foreign, 2); err != nil {
		t.Fatal(err)
	}
	count, err := repository.ExpireCandidates(ctx, "lvinst_prod", now.Add(2*time.Minute))
	if err != nil || count != 1 {
		t.Fatalf("ExpireCandidates() = %d, %v", count, err)
	}
	expired, err := repository.CandidateByID(ctx, first.ID)
	if err != nil || expired.Status != deployment.CandidateExpired {
		t.Fatalf("expired candidate = %#v, %v", expired, err)
	}
	unchanged, err := repository.CandidateByID(ctx, foreign.ID)
	if err != nil || unchanged.Status != deployment.CandidatePreparing {
		t.Fatalf("foreign candidate = %#v, %v", unchanged, err)
	}
}

func TestCandidateRepositoryNeverChangesActiveServingState(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	insertWorkspaceCandidate(t, ctx, db, "sales", "sales_old", "sales_new", "prod")
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	candidate := candidateRecord(t, now, "cand_1", "finance", "principal_1", "sha256:"+strings.Repeat("a", 64))
	if _, _, err := repository.StartCandidate(ctx, candidate, 4); err != nil {
		t.Fatal(err)
	}
	cancelled, err := candidate.Cancel(now.Add(time.Minute))
	require.NoError(t, err)
	if _, err := repository.SaveCandidate(ctx, cancelled, candidate.Revision); err != nil {
		t.Fatal(err)
	}
	assertActiveState(t, ctx, db, "sales", "prod", "sales_old")
}

func TestCandidateRepositoryRejectsReadyCandidateWithoutReleaseProvenance(t *testing.T) {
	ctx, db, repository := testRepository(t)
	insertCandidatePrincipal(t, ctx, db, "principal_1")
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	candidate := candidateRecord(
		t,
		now,
		"cand_1",
		"finance",
		"principal_1",
		"sha256:"+strings.Repeat("a", 64),
	)
	if _, _, err := repository.StartCandidate(ctx, candidate, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(
		ctx,
		`UPDATE project_candidates
		 SET status = 'ready', ready_at = ?, revision = revision + 1
		 WHERE id = ?`,
		now.Add(time.Minute).Format(time.RFC3339Nano),
		candidate.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CandidateByID(
		ctx,
		candidate.ID,
	); !errors.Is(err, deployment.ErrCandidateInvalid) {
		t.Fatalf("CandidateByID() error = %v, want ErrCandidateInvalid", err)
	}
}

func candidateRecord(t *testing.T, now time.Time, id, project, owner, artifactDigest string) deployment.Candidate {
	t.Helper()
	candidate, err := deployment.NewCandidate(deployment.CandidateStartInput{
		ID: id, TargetID: "lvinst_prod", OwnerID: owner,
		Scope:          deployment.CandidateScope{ProjectID: projectgraph.ResourceID(project), Environment: "prod", BaseGenerationID: "deployment_7"},
		ArtifactDigest: artifactDigest, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	require.NoError(t, err)
	return candidate
}

func insertCandidatePrincipal(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, id, id+"@example.test", id); err != nil {
		t.Fatal(err)
	}
}
