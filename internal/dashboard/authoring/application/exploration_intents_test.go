package application_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func TestExecuteIntentAppendExplorationIsCompilerAdmittedAndIdempotent(t *testing.T) {
	repository, revision := newAppendApplicationRepository(t)
	identity, err := graph.NewServingIdentity("project:test", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	compiler := &appendApplicationCompiler{identity: identity}
	// The authored resource ID is a canonical graph key while Name is a
	// human-facing model name. They are intentionally different in real
	// projects; admission must use the lease/projection key, not Name.
	runtime := &appendApplicationRuntime{identity: identity, model: &semanticmodel.Model{Name: "Sales"}}
	app, lease := newAppendApplication(t, repository, compiler, runtime, identity)
	command := appendIntentCommand(revision.Token())

	first, err := app.ExecuteIntent(t.Context(), application.IntentRequest{ProjectID: "project:test", ActorID: "actor", Command: command})
	if err != nil {
		t.Fatalf("append exploration: %v", err)
	}
	if first.Revision.Number != 2 || repository.appendCalls != 1 {
		t.Fatalf("first result = %#v, append calls = %d", first, repository.appendCalls)
	}
	if compiler.calls != 1 || repository.lookupCalls != 1 || runtime.acquireCalls != 1 || lease.releases != 1 {
		t.Fatalf("admission calls compiler=%d lookup=%d acquire=%d release=%d", compiler.calls, repository.lookupCalls, runtime.acquireCalls, lease.releases)
	}
	if len(compiler.candidate.Spec.Visuals) != 2 || len(compiler.candidate.Spec.Pages[0].Components) != 2 || len(compiler.candidate.Spec.Filters) != 1 {
		t.Fatalf("compiler candidate did not contain atomic append: %#v", compiler.candidate)
	}

	// The retry deliberately carries the stale expected revision. Durable
	// command lookup must replay before active-runtime admission or reduction.
	replay, err := app.ExecuteIntent(t.Context(), application.IntentRequest{ProjectID: "project:test", ActorID: "actor", Command: command})
	if err != nil {
		t.Fatalf("append replay: %v", err)
	}
	if replay.Revision != first.Revision || repository.appendCalls != 1 || compiler.calls != 1 || runtime.acquireCalls != 1 {
		t.Fatalf("replay = %#v, append=%d compiler=%d acquire=%d", replay, repository.appendCalls, compiler.calls, runtime.acquireCalls)
	}
}

func TestExplorationTargetsDoNotAcquireBuilderRuntimePerDashboard(t *testing.T) {
	repository, _ := newAppendApplicationRepository(t)
	identity, err := graph.NewServingIdentity("project:test", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	compiler := &appendApplicationCompiler{identity: identity}
	runtime := &appendApplicationRuntime{identity: identity, model: &semanticmodel.Model{Name: "model"}}
	app, lease := newAppendApplication(t, repository, compiler, runtime, identity)
	targets, err := app.ExplorationTargets(t.Context(), application.ExplorationTargetsRequest{ProjectID: "project:test", ActorID: "actor", SourceModelID: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != "dashboard:test" || targets[0].SemanticModel != "model" || len(targets[0].Pages) != 0 || targets[0].RevisionToken != "" {
		t.Fatalf("targets = %#v", targets)
	}
	if repository.getRevisionCalls != 0 || runtime.acquireCalls != 0 || lease.releases != 0 {
		t.Fatalf("target listing read mutable dashboard state: revisions=%d acquire=%d release=%d", repository.getRevisionCalls, runtime.acquireCalls, lease.releases)
	}
}

func TestExplorationTargetsBoundOnlyAuthorizedTargets(t *testing.T) {
	repository, _ := newAppendApplicationRepository(t)
	repository.lifecycles = make([]authoring.DashboardLifecycle, 0, maxPrivateExplorationTargets+1)
	for i := 0; i < maxPrivateExplorationTargets; i++ {
		private := repository.lifecycle
		private.ID = authoring.DashboardID(fmt.Sprintf("dashboard:private-%03d", i))
		repository.lifecycles = append(repository.lifecycles, private)
	}
	repository.lifecycles = append(repository.lifecycles, repository.lifecycle)
	identity, err := graph.NewServingIdentity("project:test", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	compiler := &appendApplicationCompiler{identity: identity}
	runtime := &appendApplicationRuntime{identity: identity, model: &semanticmodel.Model{Name: "Sales"}}
	app, lease := newAppendApplication(t, repository, compiler, runtime, identity, &selectiveTargetAuthorizer{})
	targets, err := app.ExplorationTargets(t.Context(), application.ExplorationTargetsRequest{ProjectID: "project:test", ActorID: "actor", SourceModelID: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != "dashboard:test" {
		t.Fatalf("authorized targets = %#v", targets)
	}
	if repository.getRevisionCalls != 0 || runtime.acquireCalls != 0 || lease.releases != 0 {
		t.Fatalf("target listing read mutable dashboard state: revisions=%d acquire=%d release=%d", repository.getRevisionCalls, runtime.acquireCalls, lease.releases)
	}
}

func TestAppendExplorationRejectsRuntimeIdentityMismatch(t *testing.T) {
	repository, revision := newAppendApplicationRepository(t)
	identity, err := graph.NewServingIdentity("project:test", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	compiler := &appendApplicationCompiler{identity: identity}
	runtime := &appendApplicationRuntime{identity: identity, model: &semanticmodel.Model{Name: "Sales"}}
	app, lease := newAppendApplication(t, repository, compiler, runtime, identity)
	runtime.identity.GenerationID = "generation-other"
	token, err := explorationTargetTokenForTest(revision.Token())
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.AppendExploration(t.Context(), application.ExplorationAppendRequest{
		ProjectID: "project:test", ActorID: "actor", DashboardID: "dashboard:test", PageID: "overview",
		RevisionToken: token, RequestID: "runtime-identity-mismatch", PlacementChoice: "half",
		Spec: explorationSpecForTest(),
	})
	if err == nil || repository.appendCalls != 0 || lease.releases != 1 {
		t.Fatalf("runtime identity mismatch err=%v append=%d releases=%d", err, repository.appendCalls, lease.releases)
	}
}

func explorationTargetTokenForTest(revision authoring.RevisionToken) (string, error) {
	raw, err := json.Marshal(struct {
		DraftID  authoring.DraftID       `json:"draftId"`
		Revision authoring.RevisionToken `json:"revision"`
	}{DraftID: "draft-1", Revision: revision})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func explorationSpecForTest() exploration.ExplorationSpec {
	return exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "model", Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10}
}

const maxPrivateExplorationTargets = 129

type selectiveTargetAuthorizer struct{}

func (*selectiveTargetAuthorizer) Authorize(_ context.Context, request service.AuthorizationRequest) error {
	if strings.HasPrefix(request.DashboardID.String(), "dashboard:private-") {
		return access.ErrForbidden
	}
	return nil
}

func TestExecuteIntentAppendExplorationAuthorizationPrecedesAdmission(t *testing.T) {
	repository, revision := newAppendApplicationRepository(t)
	identity, err := graph.NewServingIdentity("project:test", "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	compiler := &appendApplicationCompiler{identity: identity}
	runtime := &appendApplicationRuntime{identity: identity, model: &semanticmodel.Model{Name: "model"}}
	denied := errors.New("target edit denied")
	app, lease := newAppendApplication(t, repository, compiler, runtime, identity, &applicationAuthorizer{err: denied})

	if _, err := app.ExecuteIntent(t.Context(), application.IntentRequest{ProjectID: "project:test", ActorID: "actor", Command: appendIntentCommand(revision.Token())}); !errors.Is(err, denied) {
		t.Fatalf("authorization error = %v, want %v", err, denied)
	}
	if repository.appendCalls != 0 || compiler.calls != 0 || runtime.acquireCalls != 0 || lease.releases != 0 {
		t.Fatalf("denied target reached admission: append=%d compiler=%d acquire=%d release=%d", repository.appendCalls, compiler.calls, runtime.acquireCalls, lease.releases)
	}
}

func TestExecuteIntentAppendExplorationRejectsBeforePersistence(t *testing.T) {
	tests := map[string]func(*appendApplicationRuntime, *appendApplicationCompiler){
		"semantic model unavailable": func(runtime *appendApplicationRuntime, _ *appendApplicationCompiler) {
			runtime.model = nil
		},
		"serving identity mismatch": func(runtime *appendApplicationRuntime, _ *appendApplicationCompiler) {
			runtime.identity.GenerationID = "generation-other"
		},
		"compiled identity mismatch": func(_ *appendApplicationRuntime, compiler *appendApplicationCompiler) {
			compiler.identity.GenerationID = "generation-other"
		},
		"compiler rejection": func(_ *appendApplicationRuntime, compiler *appendApplicationCompiler) {
			compiler.err = errors.New("candidate is not compilable")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			repository, revision := newAppendApplicationRepository(t)
			identity, err := graph.NewServingIdentity("project:test", "production", "generation-1")
			if err != nil {
				t.Fatal(err)
			}
			compiler := &appendApplicationCompiler{identity: identity}
			runtime := &appendApplicationRuntime{identity: identity, model: &semanticmodel.Model{Name: "model"}}
			app, lease := newAppendApplication(t, repository, compiler, runtime, identity)
			mutate(runtime, compiler)
			if _, err := app.ExecuteIntent(t.Context(), application.IntentRequest{ProjectID: "project:test", ActorID: "actor", Command: appendIntentCommand(revision.Token())}); err == nil {
				t.Fatal("append unexpectedly succeeded")
			}
			if repository.appendCalls != 0 {
				t.Fatalf("failed admission persisted %d revisions", repository.appendCalls)
			}
			if name == "compiler rejection" || name == "compiled identity mismatch" {
				if compiler.calls != 1 {
					t.Fatalf("compiler admission calls = %d, want 1", compiler.calls)
				}
			} else if compiler.calls != 0 {
				t.Fatalf("pre-compile admission called compiler %d times", compiler.calls)
			}
			if lease.releases != 1 {
				t.Fatalf("lease releases = %d, want 1", lease.releases)
			}
		})
	}
}

type appendApplicationCompiler struct {
	identity  graph.ServingIdentity
	err       error
	calls     int
	candidate document.DashboardDocument
}

func (c *appendApplicationCompiler) Compile(_ context.Context, _ graph.ResourceID, _ graph.ResourceID, authored document.DashboardDocument) (service.Compilation, error) {
	c.calls++
	c.candidate = authored
	if c.err != nil {
		return service.Compilation{}, c.err
	}
	return service.Compilation{SemanticIdentity: c.identity}, nil
}

type appendApplicationRuntime struct {
	identity     graph.ServingIdentity
	model        *semanticmodel.Model
	acquireCalls int
}

func (r *appendApplicationRuntime) Close() error { return nil }

func (r *appendApplicationRuntime) Identity() graph.ServingIdentity { return r.identity }

func (r *appendApplicationRuntime) SemanticModelProjection(id graph.ResourceID) (*semanticmodel.Model, bool) {
	if r.model == nil || id != "model" {
		return nil, false
	}
	return r.model, true
}

type appendApplicationLease struct {
	runtime  projectruntime.Runtime
	identity graph.ServingIdentity
	releases int
}

func (l *appendApplicationLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *appendApplicationLease) Identity() graph.ServingIdentity { return l.identity }
func (l *appendApplicationLease) Release()                        { l.releases++ }

func newAppendApplication(t *testing.T, repository *appendApplicationRepository, compiler *appendApplicationCompiler, runtime *appendApplicationRuntime, identity graph.ServingIdentity, authorizers ...service.Authorizer) (*application.Application, *appendApplicationLease) {
	t.Helper()
	var authorizer service.Authorizer = &applicationAuthorizer{}
	if len(authorizers) > 1 {
		t.Fatal("at most one authorizer is supported by this test helper")
	}
	if len(authorizers) == 1 {
		authorizer = authorizers[0]
	}
	serviceValue, err := service.NewService(service.Options{
		Repository: repository, Authorizer: authorizer, Compiler: compiler,
		Now:            func() time.Time { return time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) { return "unused-dashboard", nil },
		NewDraftID:     func() (authoring.DraftID, error) { return "unused-draft", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return "rev-2", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := &appendApplicationLease{runtime: runtime, identity: identity}
	runtime.acquireCalls = 0
	runtimeProvider := func(context.Context) (projectruntime.Lease, error) {
		runtime.acquireCalls++
		return lease, nil
	}
	app, err := application.New(application.Options{Authoring: serviceValue, Repository: repository, Authorizer: authorizer, Compiler: compiler, AcquireRuntime: runtimeProvider})
	if err != nil {
		t.Fatal(err)
	}
	return app, lease
}

type appendApplicationRepository struct {
	*applicationRepository
	lifecycle        authoring.DashboardLifecycle
	lifecycles       []authoring.DashboardLifecycle
	revisions        map[authoring.RevisionID]authoring.Revision
	commandID        authoring.CommandID
	fingerprint      string
	result           authoring.RevisionToken
	appendCalls      int
	lookupCalls      int
	getRevisionCalls int
}

func newAppendApplicationRepository(t *testing.T) (*appendApplicationRepository, authoring.Revision) {
	t.Helper()
	base := &applicationRepository{}
	value := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:test", Name: "test"},
		Spec: document.DashboardSpec{
			SemanticModel: "model", Filters: []document.DashboardFilter{},
			Visuals: map[string]document.DashboardVisual{"base": appendApplicationVisual("bar")},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{
				DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "base-component", Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 12, RowSpan: 4}},
				Type:                       "visual", Visual: "base",
			}}}}},
		},
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor", ConversationID: "conversation", ToolCallID: "tool"}
	revision, err := authoring.NewRevision("rev-1", "dashboard:test", 1, time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC), value, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project:test", ID: "dashboard:test", OwnerPrincipalID: "owner", Slug: "test", Title: "Test", SemanticModel: "model", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft-1", DashboardID: "dashboard:test", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &appendApplicationRepository{applicationRepository: base, lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{revision.ID: revision}}, revision
}

func (r *appendApplicationRepository) Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}

func (r *appendApplicationRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	if r.lifecycles != nil {
		return r.lifecycles, nil
	}
	return []authoring.DashboardLifecycle{r.lifecycle}, nil
}

func (r *appendApplicationRepository) GetRevision(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.getRevisionCalls++
	value, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return value, nil
}

func (r *appendApplicationRepository) LookupCommandResult(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	r.lookupCalls++
	if r.commandID == "" {
		return authoring.CommandResult{}, false, nil
	}
	if r.commandID != evidence.ID || r.fingerprint != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	return authoring.CommandResult{Revision: r.result}, true, nil
}

func (r *appendApplicationRepository) AppendDraft(_ context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	if r.lifecycle.Draft == nil || r.lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.Revision{}, authoring.ErrStaleRevision
	}
	r.appendCalls++
	r.lifecycle = input.Next
	r.revisions[input.Revision.ID] = input.Revision
	r.commandID = input.Evidence.ID
	r.fingerprint = input.Evidence.Fingerprint
	r.result = input.Revision.Token()
	return input.Revision, nil
}

func appendIntentCommand(revision authoring.RevisionToken) authoring.Command {
	targets := []string{"exploration_visual"}
	return authoring.Command{
		ID: "append-exploration", DashboardID: "dashboard:test", DraftID: "draft-1", ExpectedRevision: revision,
		Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor", ConversationID: "conversation", ToolCallID: "tool"},
		AppendExplorationVisual: &authoring.AppendExplorationVisualPayload{
			PageID: "overview", VisualID: "exploration_visual", ComponentID: "exploration_component", Placement: document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 8, RowSpan: 5}, SemanticModel: "model",
			Visual: appendApplicationVisual("line"), Filters: []document.DashboardFilter{{
				ID: "exploration_filter", Label: "Status", Dimension: "status",
				Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "text"}, Type: "text"}},
				Default: &document.DashboardFilterExpression{Value: &document.UnfilteredDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "unfiltered"}, Type: "unfiltered"}}, Targets: &targets,
			}},
		},
	}
}

func appendApplicationVisual(kind string) document.DashboardVisual {
	title := "Explore"
	return document.DashboardVisual{
		Type: document.DashboardVisualType(kind), Title: &title,
		Query:        document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}},
	}
}
