package module

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type moduleJobFixture struct {
	store *platform.Store
	repo  *agentsqlite.Repository
	jobs  *jobsqlite.Repository
	mod   *Module
	owner access.Principal
}

func newModuleJobFixture(t *testing.T) moduleJobFixture {
	t.Helper()
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := projectsqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	owner, err := accesssqlite.NewRepository(store.SQLDB()).SetPrincipalRole(ctx, access.PrincipalRoleInput{WorkspaceID: "test", Email: "jobs@example.com", DisplayName: "Jobs", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := agentsqlite.NewRepositoryWithWorkflow(store.SQLDB(), queue, queue)
	service := agent.NewService(repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "done", FinishReason: agentcore.FinishReasonStop}, nil
	})))
	execution, err := loadRunExecutionContract()
	if err != nil {
		t.Fatal(err)
	}
	return moduleJobFixture{store: store, repo: repo, jobs: queue, mod: &Module{service: service, runWorkloadClass: jobs.WorkloadClassBackground, runExecution: execution}, owner: owner}
}

func (f moduleJobFixture) scope() agent.Scope {
	return agent.Scope{ProjectID: "test", PrincipalID: f.owner.ID}
}

func (f moduleJobFixture) run(t *testing.T, id, status string) (agent.Conversation, agent.Run) {
	t.Helper()
	ctx := context.Background()
	conv, err := f.repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: f.owner.ID, Title: id})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repo.CreateRun(ctx, agent.RunInput{PrincipalID: f.owner.ID, ConversationID: conv.ID, RunID: id, Status: agent.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if status != agent.RunStatusRunning {
		run, err = f.repo.FinishRun(ctx, agent.RunFinish{PrincipalID: f.owner.ID, ConversationID: conv.ID, RunID: id, Status: status, MetadataJSON: `{}`})
		if err != nil {
			t.Fatal(err)
		}
	}
	return conv, run
}

func (f moduleJobFixture) claim(t *testing.T, conv agent.Conversation, run agent.Run) jobs.Job {
	t.Helper()
	payload, _ := json.Marshal(RunJob{Scope: f.scope(), Conversation: conv.ID, Run: run.ID})
	job, err := f.jobs.Enqueue(context.Background(), jobs.EnqueueInput{ID: "agent:" + run.ID + ":run", Kind: f.mod.runExecution.JobKind, WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: f.owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: f.mod.runExecution.ResourceKind, ResourceID: run.ID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := f.jobs.ClaimByID(context.Background(), job.ID, jobs.WorkloadClassBackground, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	return claimed
}

func TestJobHandlersRedeliveryConvergesTerminalRuns(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		wantError bool
		wantJob   jobs.Status
	}{
		{name: "completed", status: agent.RunStatusCompleted, wantJob: jobs.StatusSucceeded},
		{name: "failed", status: agent.RunStatusFailed, wantError: true, wantJob: jobs.StatusFailed},
		{name: "canceled", status: agent.RunStatusCanceled, wantJob: jobs.StatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModuleJobFixture(t)
			conv, run := f.run(t, "run_"+tc.name, tc.status)
			job := f.claim(t, conv, run)
			h := f.mod.JobHandlers(f.jobs)[0]
			err := h.Handle(context.Background(), job)
			if (err != nil) != tc.wantError {
				t.Fatalf("handler error = %v, wantError=%v", err, tc.wantError)
			}
			if err == nil && tc.status != agent.RunStatusCanceled {
				if err := f.jobs.Complete(context.Background(), job.ID, job.Fence()); err != nil {
					t.Fatalf("complete: %v", err)
				}
			} else if err != nil {
				_ = f.jobs.Fail(context.Background(), job.ID, job.Fence(), []byte(`{"code":"ASYNC_JOB_FAILED"}`))
			}
			got, err := f.jobs.Get(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantJob {
				t.Fatalf("job status = %q, want %q", got.Status, tc.wantJob)
			}
			// A redelivery is idempotent and must not alter the terminal outcome.
			_ = h.Handle(context.Background(), job)
		})
	}
}

func TestJobHandlerResumeFailuresTerminalizeOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript string
		promptErr  bool
	}{
		{name: "malformed transcript", transcript: "{"},
		{name: "no user prompt", transcript: `[{"role":"assistant","content":"orphan"}]`},
		{name: "system prompt unavailable", transcript: `[{"role":"user","content":"hello"}]`, promptErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModuleJobFixture(t)
			conv, run := f.run(t, "run_resume_failure", agent.RunStatusRunning)
			if tc.transcript == "{" {
				if _, err := f.store.SQLDB().ExecContext(context.Background(), `UPDATE agent_conversations SET transcript_json = ? WHERE id = ?`, tc.transcript, conv.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.repo.UpdateConversationTranscript(context.Background(), f.owner.ID, conv.ID, tc.transcript); err != nil {
				t.Fatal(err)
			}
			if tc.promptErr {
				f.mod.service.SetSystemPromptProvider(func(context.Context) (string, error) { return "", errors.New("store secret leaked") })
			}
			job := f.claim(t, conv, run)
			h := f.mod.JobHandlers(f.jobs)[0]
			if err := h.Handle(context.Background(), job); err == nil {
				t.Fatal("resume failure unexpectedly succeeded")
			}
			gotRun, err := f.repo.GetRun(context.Background(), f.owner.ID, conv.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if gotRun.Status != agent.RunStatusFailed {
				t.Fatalf("run status = %q, want failed", gotRun.Status)
			}
			if gotRun.Error != "durable prompt resume failed" {
				t.Fatalf("run error = %q, want bounded generic error", gotRun.Error)
			}
			events, err := f.jobs.ListEvents(context.Background(), "agent_run", run.ID, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].EventType != "agent_run.failed" {
				t.Fatalf("failed events = %#v, want one keyed event", events)
			}
			// Replay after a process restart sees the durable failed run and does
			// not append a second event.
			_ = h.Handle(context.Background(), job)
			events, err = f.jobs.ListEvents(context.Background(), "agent_run", run.ID, 0, 20)
			if err != nil || len(events) != 1 {
				t.Fatalf("replay events = %#v err=%v", events, err)
			}
		})
	}
}

func TestJobHandlerCancellationLeavesClaimRecoverable(t *testing.T) {
	f := newModuleJobFixture(t)
	conv, run := f.run(t, "run_reclaim", agent.RunStatusRunning)
	if _, err := f.repo.UpdateConversationTranscript(context.Background(), f.owner.ID, conv.ID, `[{"role":"user","content":"retry"}]`); err != nil {
		t.Fatal(err)
	}
	job := f.claim(t, conv, run)
	h := f.mod.JobHandlers(f.jobs)[0]
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Handle(canceled, job); err == nil {
		t.Fatal("canceled handler unexpectedly succeeded")
	}
	got, err := f.jobs.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusRunning {
		t.Fatalf("job after cancellation = %q, want running", got.Status)
	}
	current, err := f.repo.GetRun(context.Background(), f.owner.ID, conv.ID, run.ID)
	if err != nil || current.Status != agent.RunStatusRunning {
		t.Fatalf("run after cancellation = %#v err=%v, want running", current, err)
	}
	// Once the lease expires a new worker can reclaim and finish exactly once.
	if _, err := f.store.SQLDB().ExecContext(context.Background(), `UPDATE api_async_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, ok, err := f.jobs.ClaimByID(context.Background(), job.ID, jobs.WorkloadClassBackground, "worker-b", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("reclaim = %#v ok=%v err=%v", reclaimed, ok, err)
	}
	if err := h.Handle(context.Background(), reclaimed); err != nil {
		t.Fatalf("reclaimed handler: %v", err)
	}
	if err := f.jobs.Complete(context.Background(), reclaimed.ID, reclaimed.Fence()); err != nil {
		t.Fatal(err)
	}
	current, err = f.repo.GetRun(context.Background(), f.owner.ID, conv.ID, run.ID)
	if err != nil || current.Status != agent.RunStatusCompleted {
		t.Fatalf("run after reclaim = %#v err=%v", current, err)
	}
}

func TestJobHandlerInvalidPersistedStatusFailsSafely(t *testing.T) {
	f := newModuleJobFixture(t)
	conv, run := f.run(t, "run_invalid_status", agent.RunStatusRunning)
	if _, err := f.store.SQLDB().ExecContext(context.Background(), `UPDATE agent_runs SET status = 'unexpected' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	job := f.claim(t, conv, run)
	h := f.mod.JobHandlers(f.jobs)[0]
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("invalid status unexpectedly succeeded")
	}
	got, err := f.repo.GetRun(context.Background(), f.owner.ID, conv.ID, run.ID)
	if err != nil || got.Status != "unexpected" {
		t.Fatalf("run after invalid status = %#v err=%v", got, err)
	}
	events, err := f.jobs.ListEvents(context.Background(), "agent_run", run.ID, 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after invalid status = %#v err=%v, want none", events, err)
	}
	if err := f.jobs.Fail(context.Background(), job.ID, job.Fence(), []byte(`{"code":"ASYNC_JOB_FAILED"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestJobHandlerMissingRunFailsWithoutDomainEvent(t *testing.T) {
	f := newModuleJobFixture(t)
	payload, _ := json.Marshal(RunJob{Scope: f.scope(), Conversation: "missing-conversation", Run: "missing-run"})
	job, err := f.jobs.Enqueue(context.Background(), jobs.EnqueueInput{ID: "agent:missing-run:run", Kind: f.mod.runExecution.JobKind, WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: f.owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: f.mod.runExecution.ResourceKind, ResourceID: "missing-run", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.jobs.ClaimByID(context.Background(), job.ID, jobs.WorkloadClassBackground, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%v err=%v", job, ok, err)
	}
	if err := f.mod.JobHandlers(f.jobs)[0].Handle(context.Background(), job); err == nil {
		t.Fatal("missing run unexpectedly succeeded")
	}
	events, err := f.jobs.ListEvents(context.Background(), "agent_run", "missing-run", 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("missing-run events = %#v err=%v", events, err)
	}
	if err := f.jobs.Fail(context.Background(), job.ID, job.Fence(), []byte(`{"code":"ASYNC_JOB_FAILED"}`)); err != nil {
		t.Fatal(err)
	}
}
