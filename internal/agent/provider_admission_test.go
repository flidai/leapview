package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/jobs"
)

type fakeProviderAdmission struct {
	acquired, released, waited atomic.Int64
	deny                       error
	mu                         sync.Mutex
	cancel                     context.CancelFunc
}

func (g *fakeProviderAdmission) Acquire(ctx context.Context) (context.Context, func(), error) {
	if g.deny != nil {
		return nil, nil, g.deny
	}
	g.acquired.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	g.mu.Lock()
	g.cancel = cancel
	g.mu.Unlock()
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); g.released.Add(1) }) }, nil
}
func (g *fakeProviderAdmission) Wait(ctx context.Context) (context.Context, func(), error) {
	g.waited.Add(1)
	return g.Acquire(ctx)
}
func (g *fakeProviderAdmission) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
}

func TestProviderAdmissionRejectsBeforeResolvingConfiguration(t *testing.T) {
	paused := errors.New("provider admission paused")
	gate := &fakeProviderAdmission{deny: paused}
	service := NewService(nil, Config{}, WithProviderAdmission(gate))
	// A missing configuration store would panic if Refresh runs before admission.
	service.configuration = &ConfigurationManager{service: service}
	if _, err := service.StartPrompt(t.Context(), PromptInput{}); !errors.Is(err, paused) {
		t.Fatalf("entry did not reject provider work before resolution: %v", err)
	}
}

func TestProviderAdmissionCancellationWaitsForActualProviderCleanup(t *testing.T) {
	gate := &fakeProviderAdmission{}
	entered, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	model := agentcore.ModelFunc(func(ctx context.Context, _ agentcore.ModelRequest, _ agentcore.ModelStream) (agentcore.ModelResponse, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-finish
		return agentcore.ModelResponse{}, ctx.Err()
	})
	store := newTestAgentStore()
	principal := createAgentAppPrincipal(t, t.Context(), store, "provider-drain@example.com")
	scope := Scope{ProjectID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "model"}, WithModel(model), WithProviderAdmission(gate))
	conversation, err := service.CreateConversation(t.Context(), scope, "Drain")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartPrompt(t.Context(), PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if gate.acquired.Load() != 1 || gate.released.Load() != 0 {
		t.Fatal("started provider lease not retained")
	}
	done := make(chan error, 1)
	go func() { _, err := started.Complete(t.Context(), nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	gate.stop()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive cancellation")
	}
	if gate.released.Load() != 0 {
		t.Fatal("cancellation prematurely acknowledged provider drain")
	}
	close(finish)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("provider cleanup did not finish")
	}
	if gate.released.Load() != 1 {
		t.Fatal("completed cleanup did not release provider lease")
	}
}

func TestDurableProviderPreparationReleasesAndResumeWaitsForAdmission(t *testing.T) {
	gate := &fakeProviderAdmission{}
	base := newTestAgentStore()
	store := &workflowAgentStore{testAgentStore: base}
	principal := createAgentAppPrincipal(t, t.Context(), base, "provider-queue@example.com")
	scope := Scope{ProjectID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "model"}, WithModel(newRecordingAgentModel()), WithProviderAdmission(gate))
	service.SetPromptWorkflow(func(PromptInput, string, PromptDispatch) jobs.WorkflowIntent { return jobs.WorkflowIntent{} })
	conversation, err := service.CreateConversation(t.Context(), scope, "Queue")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartDurablePrompt(t.Context(), PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "later"}, PromptDispatch{})
	if err != nil {
		t.Fatal(err)
	}
	if !started.DurablyQueued() || gate.acquired.Load() != 1 || gate.released.Load() != 1 {
		t.Fatal("queued request retained abandoned provider lease")
	}
	resumed, err := service.ResumePrompt(t.Context(), scope, conversation.ID, started.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.waited.Load() != 1 || gate.acquired.Load() != 2 || gate.released.Load() != 1 {
		t.Fatal("background resume did not wait/acquire atomically")
	}
	if err := resumed.Abort(t.Context(), errors.New("cleanup")); err != nil {
		t.Fatal(err)
	}
	if gate.released.Load() != 2 {
		t.Fatal("abort did not release resumed lease")
	}
}

func TestProviderAdmissionQueuedDirectCompletionReacquiresLease(t *testing.T) {
	gate := &fakeProviderAdmission{}
	base := newTestAgentStore()
	store := &workflowAgentStore{testAgentStore: base}
	principal := createAgentAppPrincipal(t, t.Context(), base, "direct-provider@example.com")
	scope := Scope{ProjectID: "test", PrincipalID: principal.ID}
	model := agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		if gate.acquired.Load()-gate.released.Load() != 1 {
			t.Error("provider ran outside admission")
		}
		return agentcore.ModelResponse{Content: "done", FinishReason: agentcore.FinishReasonStop}, nil
	})
	service := NewService(store, Config{APIKey: "key", Model: "model"}, WithModel(model), WithProviderAdmission(gate))
	service.SetPromptWorkflow(func(PromptInput, string, PromptDispatch) jobs.WorkflowIntent { return jobs.WorkflowIntent{} })
	conversation, err := service.CreateConversation(t.Context(), scope, "Direct")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartDurablePrompt(t.Context(), PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "later"}, PromptDispatch{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := started.Complete(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if gate.waited.Load() != 1 || gate.acquired.Load() != 2 || gate.released.Load() != 2 {
		t.Fatal("direct queued completion skipped admission or leaked lease")
	}
}

func TestProviderAdmissionQueuedRuntimeCannotReuseRetiredCredential(t *testing.T) {
	gate := &fakeProviderAdmission{}
	base := newTestAgentStore()
	store := &workflowAgentStore{testAgentStore: base}
	principal := createAgentAppPrincipal(t, t.Context(), base, "retired-provider@example.com")
	scope := Scope{ProjectID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{}, WithProviderAdmission(gate))
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	configStore := &configurationMemory{rows: []ConfigurationRevision{{Revision: 1, Enabled: true, Config: Config{Model: "model"}, CredentialVersionID: "old-version"}}}
	credentials := newConfigurationLifecycleStub(configStore)
	credentials.versions["old-version"] = "old-key"
	manager, err := NewConfigurationManager(configStore, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	service.SetConfigurationManager(manager)
	service.SetPromptWorkflow(func(PromptInput, string, PromptDispatch) jobs.WorkflowIntent { return jobs.WorkflowIntent{} })
	conversation, err := service.CreateConversation(t.Context(), scope, "Queued")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartDurablePrompt(t.Context(), PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "later"}, PromptDispatch{})
	if err != nil {
		t.Fatal(err)
	}
	delete(credentials.versions, "old-version")
	if _, err := service.ResumePrompt(t.Context(), scope, conversation.ID, started.RunID, ""); err == nil {
		t.Fatal("queued cached model bypassed retired-credential resolution")
	}
	if gate.acquired.Load() != gate.released.Load() {
		t.Fatal("failed historical resolution leaked provider admission")
	}
}

func TestProviderAdmissionTitleAndPreparationFailureRelease(t *testing.T) {
	gate := &fakeProviderAdmission{}
	service := NewService(nil, Config{}, WithProviderAdmission(gate))
	if _, err := service.StartPrompt(t.Context(), PromptInput{}); err == nil {
		t.Fatal("invalid preparation succeeded")
	}
	if gate.acquired.Load() != 1 || gate.released.Load() != 1 {
		t.Fatal("preparation failure leaked lease")
	}
	paused := errors.New("temporarily paused")
	gate.deny = paused
	service.configuration = &ConfigurationManager{service: service}
	if _, err := service.GenerateConversationTitle(t.Context(), Scope{}, ""); !errors.Is(err, paused) {
		t.Fatalf("title resolved credential without admission: %v", err)
	}
	if gate.waited.Load() != 1 {
		t.Fatal("background title generation did not wait for admission")
	}
}
