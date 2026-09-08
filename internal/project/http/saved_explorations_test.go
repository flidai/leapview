package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
)

type savedExplorationBrowserServiceStub struct {
	createErr                                              error
	listErr                                                error
	items                                                  []saved.Lifecycle
	reopen                                                 saved.ReopenResult
	updateErr, duplicateErr, archiveErr                    error
	updateResult, duplicateResult, archiveResult           saved.MutationResult
	createCalls, updateCalls, duplicateCalls, archiveCalls *int
	listRequests                                           *[]saved.ListRequest
	authorizeReplayCalls                                   *int
	authorizeReplay                                        bool
}

func (s savedExplorationBrowserServiceStub) AuthorizeMutationReplay(context.Context, savedapplication.MutationReplayAuthorizationRequest) (bool, error) {
	if s.authorizeReplayCalls != nil {
		(*s.authorizeReplayCalls)++
	}
	return s.authorizeReplay, nil
}

func (s savedExplorationBrowserServiceStub) Create(context.Context, saved.CreateRequest) (saved.MutationResult, error) {
	if s.createCalls != nil {
		(*s.createCalls)++
	}
	return saved.MutationResult{}, s.createErr
}

func (s savedExplorationBrowserServiceStub) UpdateVersion(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error) {
	if s.updateCalls != nil {
		(*s.updateCalls)++
	}
	if s.updateErr != nil {
		return saved.MutationResult{}, s.updateErr
	}
	if s.updateResult.Lifecycle.ID == "" && !s.updateResult.Replayed {
		return saved.MutationResult{}, errors.New("unexpected update")
	}
	return s.updateResult, nil
}

func (s savedExplorationBrowserServiceStub) Duplicate(context.Context, saved.DuplicateRequest) (saved.MutationResult, error) {
	if s.duplicateCalls != nil {
		(*s.duplicateCalls)++
	}
	if s.duplicateErr != nil {
		return saved.MutationResult{}, s.duplicateErr
	}
	if s.duplicateResult.Lifecycle.ID == "" && !s.duplicateResult.Replayed {
		return saved.MutationResult{}, errors.New("unexpected duplicate")
	}
	return s.duplicateResult, nil
}

func (s savedExplorationBrowserServiceStub) List(_ context.Context, request saved.ListRequest) ([]saved.Lifecycle, error) {
	if s.listRequests != nil {
		*s.listRequests = append(*s.listRequests, request)
	}
	return s.items, s.listErr
}

func (s savedExplorationBrowserServiceStub) Archive(context.Context, saved.ArchiveRequest) (saved.MutationResult, error) {
	if s.archiveCalls != nil {
		(*s.archiveCalls)++
	}
	if s.archiveErr != nil {
		return saved.MutationResult{}, s.archiveErr
	}
	if s.archiveResult.Lifecycle.ID == "" && !s.archiveResult.Replayed {
		return saved.MutationResult{}, errors.New("unexpected archive")
	}
	return s.archiveResult, nil
}

func (s savedExplorationBrowserServiceStub) Reopen(context.Context, saved.ReopenRequest) (saved.ReopenResult, error) {
	return s.reopen, nil
}

func TestSavedExplorationCommandRequiresGeneratedExecutorCompletion(t *testing.T) {
	executed := false
	h := &BrowserHandler{
		SavedExplorations: savedExplorationBrowserServiceStub{createErr: errors.New("injected create failure")},
		SavedExplorationCommands: SavedExplorationCommandBindings{
			Create: analyticsgen.GenUIActionCreateSavedExploration(),
		},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
		BeginSavedExplorationCommand: func(ctx context.Context, invocation SavedExplorationCommandInvocation) (context.Context, error) {
			if invocation.IdempotencyKey != "ui:saved-create-1" || invocation.Revision != (saved.RevisionToken{}) {
				t.Fatalf("invocation = %#v", invocation)
			}
			return ctx, nil
		},
		ExecuteSavedExplorationCommand: func(ctx context.Context, _ SavedExplorationCommandInvocation, transaction func(context.Context) error) error {
			executed = true
			return transaction(ctx)
		},
	}
	body := `{"savedExplorations":{"command":{"action":"create","title":"Orders","slug":"orders","visibility":"private","spec":{"schemaVersion":1,"modelId":"model:orders","datasetId":"orders","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100}}}}`
	request := httptest.NewRequest(http.MethodPost, "/explore/saved/command", strings.NewReader(body))
	request.Header.Set("X-Request-ID", "saved-create-1")
	request.Header.Set("X-LeapView-Operation-ID", analyticsgen.GenUIActionCreateSavedExploration().OperationID())
	recorder := httptest.NewRecorder()
	h.SavedExplorationCommand(recorder, request)
	if !executed {
		t.Fatal("saved command did not run the generated executor callback")
	}
	if strings.Contains(recorder.Body.String(), "Saved exploration saved.") {
		t.Fatalf("failed transaction emitted success: %q", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Saved exploration operation is forbidden.") && !strings.Contains(recorder.Body.String(), "The saved exploration command is invalid.") {
		t.Fatalf("failure response did not fail closed: %q", recorder.Body.String())
	}
}

func TestSavedExplorationCommandExecutorMustInvokeTransaction(t *testing.T) {
	h := &BrowserHandler{
		SavedExplorations: savedExplorationBrowserServiceStub{createErr: nil},
		SavedExplorationCommands: SavedExplorationCommandBindings{
			Create: analyticsgen.GenUIActionCreateSavedExploration(),
		},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
		BeginSavedExplorationCommand: func(ctx context.Context, _ SavedExplorationCommandInvocation) (context.Context, error) {
			return ctx, nil
		},
		ExecuteSavedExplorationCommand: func(context.Context, SavedExplorationCommandInvocation, func(context.Context) error) error {
			return nil
		},
	}
	body := `{"savedExplorations":{"command":{"action":"create","title":"Orders","slug":"orders","visibility":"private","spec":{"schemaVersion":1,"modelId":"model:orders","datasetId":"orders","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100}}}}`
	request := httptest.NewRequest(http.MethodPost, "/explore/saved/command", strings.NewReader(body))
	request.Header.Set("X-Request-ID", "saved-create-no-callback")
	request.Header.Set("X-LeapView-Operation-ID", analyticsgen.GenUIActionCreateSavedExploration().OperationID())
	recorder := httptest.NewRecorder()
	h.SavedExplorationCommand(recorder, request)
	if strings.Contains(recorder.Body.String(), "Saved exploration saved.") {
		t.Fatalf("executor that skipped transaction emitted success: %q", recorder.Body.String())
	}
}

func TestSavedBrowserFallbackSlugRejectsSourcePunctuation(t *testing.T) {
	got := savedBrowserUniqueSlug("Copy of exploration:abc:def", "exploration-abc123")
	if got != "copy-of-exploration-abc-def-abc123" {
		t.Fatalf("fallback slug = %q", got)
	}
	if err := (saved.ExplorationID("exploration:abc:def")).Validate(); err != nil {
		t.Fatalf("fixture source ID became invalid: %v", err)
	}
}

func TestSavedBrowserCommandSlugPreservesExplicitValuesAndBoundsFallback(t *testing.T) {
	valid := "custom-slug"
	if got := savedBrowserCommandSlug(&valid, "ignored", "exploration-id"); got != valid {
		t.Fatalf("explicit valid slug = %q, want %q", got, valid)
	}
	empty := ""
	if got := savedBrowserCommandSlug(&empty, "Orders", "exploration-id"); got != empty {
		t.Fatalf("explicit empty slug = %q, want empty value", got)
	}
	malformed := "Bad Slug/with punctuation"
	if got := savedBrowserCommandSlug(&malformed, "Orders", "exploration-id"); got != malformed {
		t.Fatalf("explicit malformed slug = %q, want unchanged %q", got, malformed)
	}
	generated := savedBrowserCommandSlug(nil, "Orders", "exploration-id")
	if generated != "orders-id" {
		t.Fatalf("omitted slug = %q, want bounded unique fallback", generated)
	}
	longTitle := strings.Repeat("A", saved.MaxTitleLength)
	generated = savedBrowserCommandSlug(nil, longTitle, "exploration-0123456789abcdef0123456789abcdef")
	if len(generated) == 0 || len(generated) > saved.MaxSlugLength {
		t.Fatalf("max-title fallback length = %d, want 1-%d", len(generated), saved.MaxSlugLength)
	}
}

func TestSavedExplorationBrowserStateRetainsListAndSelection(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	lifecycle := saved.Lifecycle{
		ProjectID: "project:test", ID: "exploration:orders", OwnerPrincipalID: "principal:test",
		Title: "Orders", Slug: "orders", Visibility: saved.VisibilityPrivate, Status: saved.StatusActive,
		SemanticModelID: "model:orders", CreatedAt: now, UpdatedAt: now,
		CurrentRevision: saved.RevisionMetadata{ID: "revision:orders", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)},
	}
	h := &BrowserHandler{
		SavedExplorations: savedExplorationBrowserServiceStub{items: []saved.Lifecycle{lifecycle}},
		ResolveProjectID:  func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:       func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	state := h.savedExplorationStateForBrowser(httptest.NewRequest(http.MethodGet, "/explore?saved=exploration:orders", nil), "exploration:orders", false)
	if !state.State.Enabled || len(state.State.List.Items) != 1 || state.State.List.SelectedID == nil || *state.State.List.SelectedID != lifecycle.ID.String() || state.State.Current == nil {
		t.Fatalf("saved browser state = %#v, want retained list/current selection", state.State)
	}
}

func TestSavedExplorationBrowserStateFailsClosedOnListError(t *testing.T) {
	h := &BrowserHandler{
		SavedExplorations: savedExplorationBrowserServiceStub{listErr: errors.New("database unavailable")},
		ResolveProjectID:  func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:       func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	state := h.savedExplorationStateForBrowser(httptest.NewRequest(http.MethodGet, "/explore", nil), "", false)
	if state.State.Save.State != "error" || state.State.Save.Message == nil || *state.State.Save.Message != "Saved explorations are temporarily unavailable." || state.State.List.Items == nil {
		t.Fatalf("list error state = %#v, want safe explicit error", state.State)
	}
}

func TestSavedExplorationBrowserStateFailsClosedOnProjectResolutionError(t *testing.T) {
	h := &BrowserHandler{
		SavedExplorations: savedExplorationBrowserServiceStub{},
		ResolveProjectID:  func(context.Context) (projectgraph.ResourceID, error) { return "", errors.New("project unavailable") },
		CurrentUser:       func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	state := h.savedExplorationStateForBrowser(httptest.NewRequest(http.MethodGet, "/explore", nil), "", false)
	if state.State.Save.State != "error" || state.State.Save.Message == nil || *state.State.Save.Message != "Saved explorations are temporarily unavailable." {
		t.Fatalf("project resolution error state = %#v, want safe explicit error", state.State)
	}
}

func TestSavedCommandTargetUsesExplicitCanonicalFields(t *testing.T) {
	h := &BrowserHandler{}
	if _, err := h.savedCommandTarget(projectsignals.SavedExplorationCommandSignal{Action: "update"}); !errors.Is(err, saved.ErrInvalid) {
		t.Fatalf("missing update target error = %v", err)
	}
	target, err := h.savedCommandTarget(projectsignals.SavedExplorationCommandSignal{Action: "duplicate", SourceExplorationID: projectsignals.Optional("exploration:abc:def")})
	if err != nil {
		t.Fatal(err)
	}
	if target != "exploration:abc:def" {
		t.Fatalf("duplicate target = %q", target)
	}
}

func TestSavedExplorationBrowserIfMatchGuardsMutation(t *testing.T) {
	token := saved.RevisionToken{RevisionID: "revision:orders", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	mismatch := token
	mismatch.Number++
	for _, test := range []struct {
		action string
	}{
		{action: "update"}, {action: "duplicate"}, {action: "archive"},
	} {
		t.Run(test.action, func(t *testing.T) {
			for _, variant := range []struct {
				name       string
				header     string
				wantBegin  int
				wantMutate int
			}{
				{name: "missing", wantBegin: 0, wantMutate: 0},
				{name: "mismatched", header: savedRevisionJSON(t, mismatch), wantBegin: 0, wantMutate: 0},
				{name: "matching", header: savedRevisionJSON(t, token), wantBegin: 1, wantMutate: 1},
			} {
				t.Run(variant.name, func(t *testing.T) {
					var beginCalls, executeCalls, mutationCalls int
					service := &savedExplorationBrowserServiceStub{
						updateErr: errors.New("update reached"), duplicateErr: errors.New("duplicate reached"), archiveErr: errors.New("archive reached"),
						updateCalls: &mutationCalls, duplicateCalls: &mutationCalls, archiveCalls: &mutationCalls,
					}
					h := savedMutationBrowserHandler(test.action, service, &beginCalls, &executeCalls)
					request := savedMutationBrowserRequest(t, test.action, token, variant.header)
					response := httptest.NewRecorder()
					h.SavedExplorationCommand(response, request)
					if beginCalls != variant.wantBegin || executeCalls != variant.wantBegin || mutationCalls != variant.wantMutate {
						t.Fatalf("calls begin=%d execute=%d mutate=%d, want %d/%d/%d; body=%q", beginCalls, executeCalls, mutationCalls, variant.wantBegin, variant.wantBegin, variant.wantMutate, response.Body.String())
					}
				})
			}
		})
	}
}

func TestSavedExplorationBrowserIfMatchGuardsMutationReplay(t *testing.T) {
	token := saved.RevisionToken{RevisionID: "revision:orders", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	mismatch := token
	mismatch.ContentHash = "sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		action string
	}{
		{action: "update"}, {action: "duplicate"}, {action: "archive"}, {action: "create"},
	} {
		t.Run(test.action, func(t *testing.T) {
			for _, variant := range []struct {
				name       string
				header     string
				wantReplay bool
				callReplay bool
				wantMutate int
			}{
				{name: "missing ledger", header: savedRevisionJSON(t, token), wantReplay: false, callReplay: true, wantMutate: 0},
				{name: "mismatched", header: savedRevisionJSON(t, mismatch), wantReplay: false, wantMutate: 0},
				{name: "matching", header: savedRevisionJSON(t, token), wantReplay: true, callReplay: true, wantMutate: 0},
			} {
				t.Run(variant.name, func(t *testing.T) {
					var mutationCalls, authorizeReplayCalls int
					service := &savedExplorationBrowserServiceStub{
						createCalls: &mutationCalls,
						updateCalls: &mutationCalls, duplicateCalls: &mutationCalls, archiveCalls: &mutationCalls,
						authorizeReplayCalls: &authorizeReplayCalls, authorizeReplay: variant.wantReplay,
					}
					h := savedMutationBrowserHandler(test.action, service, nil, nil)
					request := savedMutationBrowserRequest(t, test.action, token, variant.header)
					wantAuthorizeReplayCalls := 0
					if variant.callReplay || test.action == "create" {
						wantAuthorizeReplayCalls = 1
					}
					if got := h.AuthorizeCreatorMutationReplay(request); got != variant.wantReplay || mutationCalls != 0 || authorizeReplayCalls != wantAuthorizeReplayCalls {
						t.Fatalf("replay authorized=%t mutate=%d authorizeReplay=%d, want %t/%d/%d", got, mutationCalls, authorizeReplayCalls, variant.wantReplay, 0, wantAuthorizeReplayCalls)
					}
				})
			}
		})
	}
}

func TestSavedExplorationArchiveResponseKeepsArchivedMetadata(t *testing.T) {
	projectID := projectgraph.ResourceID("project:test")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	identity, err := projectgraph.NewServingIdentity(projectID, "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	metadata := saved.RevisionMetadata{ID: "revision:orders", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64), CreatedAt: now, CreatedBy: "principal:test", ServingIdentity: identity}
	archivedAt := now.Add(time.Minute)
	lifecycle := saved.Lifecycle{
		ProjectID: projectID, ID: "exploration:orders", OwnerPrincipalID: "principal:test", Title: "Orders", Slug: "orders",
		Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Status: saved.StatusArchived,
		CreatedAt: now, UpdatedAt: archivedAt, ArchivedAt: &archivedAt, CurrentRevision: metadata,
	}
	fingerprint, err := saved.CanonicalFingerprint(struct{ ID string }{ID: lifecycle.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := saved.NewMutationEvidence("principal:test", saved.MutationActionArchive, "ui:archive-1", fingerprint, "archive-1", "archive-1", archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	result := saved.MutationResult{Lifecycle: lifecycle, AppliedRevision: metadata.Token(), ConcurrencyRevision: metadata.Token(), Evidence: evidence}
	if err := result.Validate(); err != nil {
		t.Fatalf("archive result fixture: %v", err)
	}
	var listRequests []saved.ListRequest
	var archiveCalls int
	service := &savedExplorationBrowserServiceStub{items: []saved.Lifecycle{lifecycle}, archiveResult: result, archiveCalls: &archiveCalls, listRequests: &listRequests}
	var beginCalls, executeCalls int
	h := savedMutationBrowserHandler("archive", service, &beginCalls, &executeCalls)
	request := savedMutationBrowserRequest(t, "archive", metadata.Token(), savedRevisionJSON(t, metadata.Token()))
	response := httptest.NewRecorder()
	h.SavedExplorationCommand(response, request)
	if archiveCalls != 1 || beginCalls != 1 || executeCalls != 1 {
		t.Fatalf("archive calls archive=%d begin=%d execute=%d", archiveCalls, beginCalls, executeCalls)
	}
	if len(listRequests) != 1 || !listRequests[0].IncludeArchived {
		t.Fatalf("archive list requests = %#v, want one explicit archived-inclusive read", listRequests)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"includeArchived":true`) || !strings.Contains(body, `"selectedId":"exploration:orders"`) || !strings.Contains(body, `"status":"archived"`) {
		t.Fatalf("archive response omitted archived metadata: %q", body)
	}
	if strings.Contains(body, `"spec"`) {
		t.Fatalf("archive response trusted or exposed a browser spec: %q", body)
	}
}

func savedRevisionJSON(t *testing.T, token saved.RevisionToken) string {
	t.Helper()
	encoded, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func savedMutationBrowserRequest(t *testing.T, action string, token saved.RevisionToken, ifMatch string) *http.Request {
	t.Helper()
	revision := savedRevisionJSON(t, token)
	var body string
	switch action {
	case "create":
		body = `{"savedExplorations":{"command":{"action":"create","title":"Orders","visibility":"private","spec":{"schemaVersion":1,"modelId":"model:orders","datasetId":"orders","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100}}}}`
	case "update":
		body = `{"savedExplorations":{"command":{"action":"update","explorationId":"exploration:orders","title":"Orders","slug":"orders","visibility":"private","spec":{"schemaVersion":1,"modelId":"model:orders","datasetId":"orders","dimensions":[],"metrics":[],"filters":[],"sort":[],"limit":100},"expectedRevision":` + revision + `}}}`
	case "duplicate":
		body = `{"savedExplorations":{"command":{"action":"duplicate","sourceExplorationId":"exploration:orders","title":"Copy","slug":"copy","visibility":"private","expectedSourceRevision":` + revision + `}}}`
	case "archive":
		body = `{"savedExplorations":{"command":{"action":"archive","explorationId":"exploration:orders","expectedRevision":` + revision + `}}}`
	default:
		t.Fatalf("unsupported saved mutation action %q", action)
	}
	request := httptest.NewRequest(http.MethodPost, "/explore/saved/command", strings.NewReader(body))
	request.Header.Set("X-Request-ID", "saved-"+action+"-1")
	switch action {
	case "create":
		request.Header.Set("X-LeapView-Operation-ID", analyticsgen.GenUIActionCreateSavedExploration().OperationID())
	case "update":
		request.Header.Set("X-LeapView-Operation-ID", analyticsgen.GenUIActionUpdateSavedExploration().OperationID())
	case "duplicate":
		request.Header.Set("X-LeapView-Operation-ID", analyticsgen.GenUIActionDuplicateSavedExploration().OperationID())
	case "archive":
		request.Header.Set("X-LeapView-Operation-ID", analyticsgen.GenUIActionArchiveSavedExploration().OperationID())
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	return request
}

func savedMutationBrowserHandler(action string, service SavedExplorationService, beginCalls, executeCalls *int) *BrowserHandler {
	h := &BrowserHandler{
		SavedExplorations: service, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser: func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
		BeginSavedExplorationCommand: func(ctx context.Context, _ SavedExplorationCommandInvocation) (context.Context, error) {
			if beginCalls != nil {
				(*beginCalls)++
			}
			return ctx, nil
		},
		ExecuteSavedExplorationCommand: func(ctx context.Context, _ SavedExplorationCommandInvocation, transaction func(context.Context) error) error {
			if executeCalls != nil {
				(*executeCalls)++
			}
			return transaction(ctx)
		},
	}
	switch action {
	case "create":
		h.SavedExplorationCommands.Create = analyticsgen.GenUIActionCreateSavedExploration()
	case "update":
		h.SavedExplorationCommands.Update = analyticsgen.GenUIActionUpdateSavedExploration()
	case "duplicate":
		h.SavedExplorationCommands.Duplicate = analyticsgen.GenUIActionDuplicateSavedExploration()
	case "archive":
		h.SavedExplorationCommands.Archive = analyticsgen.GenUIActionArchiveSavedExploration()
	}
	return h
}

func TestSavedExplorationReopenLeavesIncompatibleWorkingCopiesUnexecuted(t *testing.T) {
	const projectID = projectgraph.ResourceID("project:test")
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status", Type: "string", Datatype: semanticmodel.DataTypeString}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		// This field was removed from the active model. Reopen must still return
		// the detached authored copy so the user can repair it before saving.
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.removed"}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	token := saved.RevisionMetadata{ID: "revision:orders", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64), CreatedAt: now, CreatedBy: "principal:test"}
	makeLifecycle := func(id saved.ExplorationID, status saved.Status) saved.Lifecycle {
		lifecycle := saved.Lifecycle{ProjectID: projectID, ID: id, OwnerPrincipalID: "principal:test", Title: "Orders", Slug: "orders", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Status: status, CreatedAt: now, UpdatedAt: now, CurrentRevision: token}
		if status == saved.StatusArchived {
			archived := now.Add(time.Minute)
			lifecycle.ArchivedAt = &archived
		}
		return lifecycle
	}
	query := &browserDataQueryStub{}
	service := savedExplorationBrowserServiceStub{items: []saved.Lifecycle{makeLifecycle("exploration:active", saved.StatusActive)}, reopen: saved.ReopenResult{Lifecycle: makeLifecycle("exploration:active", saved.StatusActive), Spec: spec}}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: "source:orders", ProjectID: projectID, ServingStateID: "state", Type: "source", Key: "orders", Title: "Orders source", PayloadJSON: `{}`},
			{ID: "model:orders", ProjectID: projectID, ServingStateID: "state", Type: "model", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: "semantic:sales", ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{ID: string(projectID), Models: map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]}, SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}, NameIndex: projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}},
		QueryExecutor:           query, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		CurrentUser:       func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test", DevBypass: true}, true },
		SavedExplorations: &service,
	}
	reopenRequest := func(id string) *http.Request {
		route := chi.NewRouteContext()
		route.URLParams.Add("exploration", id)
		request := httptest.NewRequest(http.MethodGet, "/explore/saved/"+id, nil)
		return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	}
	activeResponse := httptest.NewRecorder()
	h.SavedExplorationReopen(activeResponse, reopenRequest("exploration:active"))
	if activeResponse.Code != http.StatusOK || query.calls != 0 {
		// The removed field is rejected by the non-strict execution signal
		// boundary before a governed query is sent, while the handoff remains
		// successful and editable.
		t.Fatalf("active reopen status=%d query calls=%d body=%q, want detached repair handoff without execution", activeResponse.Code, query.calls, activeResponse.Body.String())
	}
	for _, want := range []string{"orders.removed", `"revisionId":"revision:orders"`, `"number":1`, `"contentHash":"sha256:`} {
		if !strings.Contains(activeResponse.Body.String(), want) {
			t.Fatalf("active reopen body=%q, want incompatible authored field and exact CAS token (%s)", activeResponse.Body.String(), want)
		}
	}

	query.calls = 0
	service.reopen = saved.ReopenResult{Lifecycle: makeLifecycle("exploration:active", saved.StatusActive), Spec: exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}}
	compatibleResponse := httptest.NewRecorder()
	h.SavedExplorationReopen(compatibleResponse, reopenRequest("exploration:active"))
	if compatibleResponse.Code != http.StatusOK || query.calls != 1 {
		t.Fatalf("compatible active reopen status=%d query calls=%d body=%q, want one governed execution", compatibleResponse.Code, query.calls, compatibleResponse.Body.String())
	}

	query.calls = 0
	archived := makeLifecycle("exploration:archived", saved.StatusArchived)
	service.items = []saved.Lifecycle{archived}
	service.reopen = saved.ReopenResult{Lifecycle: archived, Spec: spec}
	archivedResponse := httptest.NewRecorder()
	h.SavedExplorationReopen(archivedResponse, reopenRequest("exploration:archived"))
	if archivedResponse.Code != http.StatusOK || query.calls != 0 {
		t.Fatalf("archived reopen status=%d query calls=%d body=%q, want no execution", archivedResponse.Code, query.calls, archivedResponse.Body.String())
	}
}
