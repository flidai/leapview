package module

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type moduleJobFixture struct {
	pool  *pgxpool.Pool
	repo  *agentpostgres.Repository
	jobs  *moduleJobStore
	mod   *Module
	owner access.Principal
}

func newModuleJobFixture(t *testing.T) moduleJobFixture {
	t.Helper()
	ctx := context.Background()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema, agentpostgres.ApplySchema)
	accessRepository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("agent-module-test-key", 2))})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := accessRepository.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "jobs@example.com", DisplayName: "Jobs"})
	if err != nil {
		t.Fatal(err)
	}
	queue := newModuleJobStore()
	repo, err := agentpostgres.NewWithOptions(pool, agentpostgres.Options{Workflow: queue, Jobs: queue})
	if err != nil {
		t.Fatal(err)
	}
	service := agent.NewService(repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "done", FinishReason: agentcore.FinishReasonStop}, nil
	})))
	execution, err := loadRunExecutionContract()
	if err != nil {
		t.Fatal(err)
	}
	return moduleJobFixture{pool: pool, repo: repo, jobs: queue, mod: &Module{service: service, runWorkloadClass: jobplatform.WorkloadClassBackground, runExecution: execution}, owner: owner}
}

func (f moduleJobFixture) scope() agent.Scope {
	return agent.Scope{ProjectID: "project:test", PrincipalID: f.owner.ID}
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
	job, err := f.jobs.Enqueue(context.Background(), jobs.EnqueueInput{ID: "agent:" + run.ID + ":run", Kind: f.mod.runExecution.JobKind, WorkloadClass: jobplatform.WorkloadClassBackground, PrincipalID: f.owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: f.mod.runExecution.ResourceKind, ResourceID: run.ID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := f.jobs.ClaimByID(context.Background(), job.ID, jobplatform.WorkloadClassBackground, "worker", time.Minute)
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
		{name: "no user prompt", transcript: `[{"role":"assistant","content":"orphan"}]`},
		{name: "system prompt unavailable", transcript: `[{"role":"user","content":"hello"}]`, promptErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModuleJobFixture(t)
			conv, run := f.run(t, "run_resume_failure", agent.RunStatusRunning)
			if _, err := f.repo.UpdateConversationTranscript(context.Background(), f.owner.ID, conv.ID, tc.transcript, conv.TranscriptRevision); err != nil {
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
	if _, err := f.repo.UpdateConversationTranscript(context.Background(), f.owner.ID, conv.ID, `[{"role":"user","content":"retry"}]`, conv.TranscriptRevision); err != nil {
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
	f.jobs.expire(job.ID)
	reclaimed, ok, err := f.jobs.ClaimByID(context.Background(), job.ID, jobplatform.WorkloadClassBackground, "worker-b", 2*time.Second)
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

func TestPostgresRejectsInvalidPersistedRunStatus(t *testing.T) {
	f := newModuleJobFixture(t)
	conv, run := f.run(t, "run_invalid_status", agent.RunStatusRunning)
	if _, err := f.pool.Exec(context.Background(), `UPDATE agent.runs SET status = 'unexpected' WHERE id = $1`, run.ID); err == nil {
		t.Fatal("PostgreSQL accepted invalid persisted run status")
	}
	got, err := f.repo.GetRun(context.Background(), f.owner.ID, conv.ID, run.ID)
	if err != nil || got.Status != agent.RunStatusRunning {
		t.Fatalf("run after rejected invalid status = %#v err=%v", got, err)
	}
	events, err := f.jobs.ListEvents(context.Background(), "agent_run", run.ID, 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after invalid status = %#v err=%v, want none", events, err)
	}
}

func TestJobHandlerMissingRunFailsWithoutDomainEvent(t *testing.T) {
	f := newModuleJobFixture(t)
	payload, _ := json.Marshal(RunJob{Scope: f.scope(), Conversation: "missing-conversation", Run: "missing-run"})
	job, err := f.jobs.Enqueue(context.Background(), jobs.EnqueueInput{ID: "agent:missing-run:run", Kind: f.mod.runExecution.JobKind, WorkloadClass: jobplatform.WorkloadClassBackground, PrincipalID: f.owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: f.mod.runExecution.ResourceKind, ResourceID: "missing-run", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.jobs.ClaimByID(context.Background(), job.ID, jobplatform.WorkloadClassBackground, "worker", time.Minute)
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

func TestDashboardRunGeneratesConversationTitle(t *testing.T) {
	for _, tc := range []struct {
		name, title, status, want string
	}{
		{"dashboard", agent.ConversationDefaultTitle, agent.RunStatusRunning, "Revenue variance explained"},
		{"completed redelivery", agent.ConversationDefaultTitle, agent.RunStatusCompleted, "Revenue variance explained"},
		{"manual title", "My finance notes", agent.RunStatusRunning, "My finance notes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModuleJobFixture(t)
			ctx := context.Background()
			f.mod.service = agent.NewService(f.repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(_ context.Context, req agentcore.ModelRequest, _ agentcore.ModelStream) (agentcore.ModelResponse, error) {
				if req.Purpose == "title_generation" {
					return agentcore.ModelResponse{Content: "Revenue variance explained", FinishReason: agentcore.FinishReasonStop}, nil
				}
				return agentcore.ModelResponse{Content: "Revenue is below budget.", FinishReason: agentcore.FinishReasonStop}, nil
			})))
			conv, err := f.repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: f.owner.ID, Title: tc.title})
			if err != nil {
				t.Fatal(err)
			}
			conv, err = f.repo.UpdateConversationTranscript(ctx, f.owner.ID, conv.ID, `[{"id":"prompt-core","role":"user","content":"Explain revenue variance"}]`, conv.TranscriptRevision)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repo.AppendMessage(ctx, agent.MessageInput{PrincipalID: f.owner.ID, ConversationID: conv.ID, Role: agent.MessageRoleUser, ContentText: "Explain revenue variance", ContentJSON: `{"message_id":"prompt-core"}`})
			if err != nil {
				t.Fatal(err)
			}
			run, err := f.repo.CreateRun(ctx, agent.RunInput{PrincipalID: f.owner.ID, ConversationID: conv.ID, RunID: "run-title", Status: agent.RunStatusRunning})
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == agent.RunStatusCompleted {
				run, err = f.repo.FinishRun(ctx, agent.RunFinish{PrincipalID: f.owner.ID, ConversationID: conv.ID, RunID: run.ID, Status: tc.status, MetadataJSON: `{}`})
				if err != nil {
					t.Fatal(err)
				}
			}
			job := f.claim(t, conv, run) // Dashboard/API runs have no full-chat client ID.
			if err := f.mod.JobHandlers(f.jobs)[0].Handle(ctx, job); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				got, err := f.repo.GetConversation(ctx, f.owner.ID, conv.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Title == tc.want && !f.mod.isChatTitlePending(conv.ID) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("title = %q, want %q", got.Title, tc.want)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

type moduleJobStore struct {
	mu        sync.Mutex
	jobs      map[string]jobs.Job
	events    []jobs.Event
	eventKeys map[string]struct{}
}

func newModuleJobStore() *moduleJobStore {
	return &moduleJobStore{jobs: make(map[string]jobs.Job), eventKeys: make(map[string]struct{})}
}

func (s *moduleJobStore) Enqueue(_ context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueue(input)
}

func (s *moduleJobStore) enqueue(input jobs.EnqueueInput) (jobs.Job, error) {
	if current, ok := s.jobs[input.ID]; ok {
		return cloneModuleJob(current), nil
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(input.Payload))
	job := jobs.Job{ID: input.ID, Kind: input.Kind, WorkloadClass: input.WorkloadClass, PrincipalID: input.PrincipalID, PartitionKey: input.PartitionKey, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, RequestDigest: digest, GroupIDs: append([]string(nil), input.GroupIDs...), EstimatedMemoryBytes: input.EstimatedMemoryBytes, Payload: append([]byte(nil), input.Payload...), Status: jobs.StatusQueued, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.jobs[job.ID] = job
	return cloneModuleJob(job), nil
}

func (s *moduleJobStore) Get(_ context.Context, id string) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(id)
}

func (s *moduleJobStore) GetTx(_ context.Context, _ jobspostgres.Tx, id string) (jobs.Job, error) {
	return s.Get(context.Background(), id)
}

func (s *moduleJobStore) get(id string) (jobs.Job, error) {
	job, ok := s.jobs[id]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return cloneModuleJob(job), nil
}

func (s *moduleJobStore) Cancel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel(id)
}

func (s *moduleJobStore) CancelTx(_ context.Context, _ jobspostgres.Tx, id string) error {
	return s.Cancel(context.Background(), id)
}

func (s *moduleJobStore) cancel(id string) error {
	job, ok := s.jobs[id]
	if !ok {
		return jobs.ErrNotFound
	}
	job.Status, job.FinishedAt = jobs.StatusCancelled, time.Now().UTC().Format(time.RFC3339Nano)
	s.jobs[id] = job
	return nil
}

func (s *moduleJobStore) RecordWorkflow(_ context.Context, _ jobspostgres.Tx, intent jobs.WorkflowIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if intent.Event.Key != "" {
		if _, exists := s.eventKeys[intent.Event.Key]; !exists {
			s.eventKeys[intent.Event.Key] = struct{}{}
			s.appendEvent(intent.Event.ResourceKind, intent.Event.ResourceID, intent.Event.EventType, intent.Event.Data)
		}
	}
	if intent.Job.ID != "" {
		_, err := s.enqueue(intent.Job)
		return err
	}
	return nil
}

func (s *moduleJobStore) CancelClaimed(_ context.Context, id string, fence jobs.Fence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return jobs.ErrNotFound
	}
	if job.Status != jobs.StatusRunning || job.Fence() != fence {
		return jobs.ErrConflict
	}
	job.Status = jobs.StatusCancelled
	job.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.jobs[id] = job
	return nil
}

func (s *moduleJobStore) AppendEvent(_ context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendEvent(kind, id, event, data), nil
}

func (s *moduleJobStore) appendEvent(kind, id, event string, data []byte) jobs.Event {
	row := jobs.Event{ID: int64(len(s.events) + 1), ResourceKind: kind, ResourceID: id, EventType: event, Data: append([]byte(nil), data...), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.events = append(s.events, row)
	return row
}

func (s *moduleJobStore) ListEvents(_ context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]jobs.Event, 0, limit)
	for _, row := range s.events {
		if row.ResourceKind == kind && row.ResourceID == id && row.ID > after && len(out) < limit {
			row.Data = append([]byte(nil), row.Data...)
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *moduleJobStore) ClaimByID(_ context.Context, id, workloadClass, owner string, lease time.Duration) (jobs.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return jobs.Job{}, false, jobs.ErrNotFound
	}
	now := time.Now().UTC()
	expires, _ := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if job.WorkloadClass != workloadClass || (job.Status == jobs.StatusRunning && expires.After(now)) || (job.Status != jobs.StatusQueued && job.Status != jobs.StatusRunning) {
		return cloneModuleJob(job), false, nil
	}
	job.Status, job.LeaseOwner, job.LeaseGeneration = jobs.StatusRunning, owner, job.LeaseGeneration+1
	job.LeaseExpiresAt, job.StartedAt = now.Add(lease).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)
	job.Attempts++
	s.jobs[id] = job
	return cloneModuleJob(job), true, nil
}

func (s *moduleJobStore) Complete(_ context.Context, id string, fence jobs.Fence) error {
	return s.finish(id, fence, jobs.StatusSucceeded, nil)
}

func (s *moduleJobStore) Fail(_ context.Context, id string, fence jobs.Fence, problem []byte) error {
	return s.finish(id, fence, jobs.StatusFailed, problem)
}

func (s *moduleJobStore) finish(id string, fence jobs.Fence, status jobs.Status, problem []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return jobs.ErrNotFound
	}
	if job.Fence() != fence || job.Status != jobs.StatusRunning {
		return jobs.ErrConflict
	}
	job.Status, job.FinishedAt, job.ErrorJSON = status, time.Now().UTC().Format(time.RFC3339Nano), string(problem)
	s.jobs[id] = job
	return nil
}

func (s *moduleJobStore) expire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	job.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	s.jobs[id] = job
}

func cloneModuleJob(job jobs.Job) jobs.Job {
	job.GroupIDs = append([]string(nil), job.GroupIDs...)
	job.Payload = append([]byte(nil), job.Payload...)
	sort.Strings(job.GroupIDs)
	return job
}
