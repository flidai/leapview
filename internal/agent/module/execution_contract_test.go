package module

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
)

func TestCreateAgentRunGeneratedExecutionContractMatchesJobRegistration(t *testing.T) {
	execution, err := loadRunExecutionContract()
	if err != nil {
		t.Fatal(err)
	}
	module := &Module{runExecution: execution}
	if err := validateRunJobHandlers(execution, module.JobHandlers(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAgentRunGeneratedExecutionContractIsPersistedAtomically(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "agent-execution.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal, err := accesssqlite.NewRepository(store.SQLDB()).UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "agent-contract", Kind: access.PrincipalKindUser, Email: "contract@example.com", DisplayName: "Contract proof",
	})
	if err != nil {
		t.Fatal(err)
	}
	jobStore := jobsqlite.NewRepository(store.SQLDB())
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), ProjectID: projectgraph.ResourceID("project:contract"),
		Jobs:  jobStore,
		Model: ModelConfig{APIKey: "test", Model: "test"},
		RecordAudit: func(context.Context, access.AuditEventInput) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := agent.Scope{ProjectID: "project:contract", PrincipalID: principal.ID}
	conversation, err := module.service.CreateConversation(t.Context(), scope, "Contract proof")
	if err != nil {
		t.Fatal(err)
	}
	started, err := module.service.StartDurablePrompt(t.Context(), agent.PromptInput{
		Scope: scope, ConversationID: conversation.ID, Input: "prove the workflow",
	}, agent.PromptDispatch{})
	if err != nil {
		t.Fatal(err)
	}
	if !started.DurablyQueued() {
		t.Fatal("agent run was not atomically queued")
	}
	execution := module.runExecution
	job, err := jobStore.Get(t.Context(), "agent:"+started.RunID+":run")
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != execution.JobKind || job.ResourceKind != execution.ResourceKind || job.ResourceID != started.RunID {
		t.Fatalf("persisted job = %#v, execution = %#v", job, execution)
	}
	events, err := jobStore.ListEvents(t.Context(), execution.ResourceKind, started.RunID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != execution.InitialEvent {
		t.Fatalf("initial events = %#v, execution = %#v", events, execution)
	}
}

func TestCreateAgentRunRejectsJobHandlerRegistrationDrift(t *testing.T) {
	execution, err := loadRunExecutionContract()
	if err != nil {
		t.Fatal(err)
	}
	err = validateRunJobHandlers(execution, []jobs.Handler{jobs.HandlerFunc{JobKind: "agent.wrong"}})
	if err == nil {
		t.Fatal("job handler drift was accepted")
	}
}
