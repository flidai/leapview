package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBuilderResumeCompletesInterruptedBootstrap(t *testing.T) {
	for _, mode := range []string{"loading", "missing preview", "missing loading"} {
		t.Run(mode, func(t *testing.T) {
			h, fake, runtime, signals := builderResumeFixture(t)
			// A previous loading patch already installed the current revision,
			// but left the prior successful runtime identity in the browser.
			prior := fake.builder
			prior.Revision.ID = "revision-1"
			runtime.ServingStateID = uisignals.Optional(builderServingStateIDForGeneration(prior, "generation-2"))
			signals["runtime"] = runtime
			loading := builderLoadingPatch(fake.builder)["builder"].(uisignals.DashboardBuilderSignal)
			encoded, err := json.Marshal(loading)
			if err != nil {
				t.Fatal(err)
			}
			var mounted map[string]any
			if err := json.Unmarshal(encoded, &mounted); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing preview":
				delete(mounted, "preview")
			case "missing loading":
				delete(mounted["preview"].(map[string]any), "loading")
			}
			signals["builder"] = mounted
			recorder := runBuilderResume(t, h, signals)
			assertBuilderResumeBootstrap(t, fake, recorder)
		})
	}
}

func TestBuilderResumeRefreshesChangedGenerationWithoutFilterSession(t *testing.T) {
	for _, mode := range []string{"filters", "no store", "missing filters", "zero revision", "missing serving identity"} {
		t.Run(mode, func(t *testing.T) {
			h, fake, runtime, signals := builderResumeFixture(t)
			runtime.ServingStateID = uisignals.Optional(builderServingStateIDForGeneration(fake.builder, "generation-1"))
			switch mode {
			case "no store":
				h.SessionStore = nil
			case "missing filters":
				delete(signals, "builderFilterState")
			case "zero revision":
				signals["builderFilterState"] = dashboardfilter.State{}
			case "missing serving identity":
				runtime.ServingStateID = nil
			}
			signals["runtime"] = runtime
			recorder := runBuilderResume(t, h, signals)
			assertBuilderResumeBootstrap(t, fake, recorder)
		})
	}
}

func TestBuilderResumeDoesNotBootstrapAfterCompileFailure(t *testing.T) {
	for _, failure := range []struct {
		name   string
		err    error
		status int
	}{
		{"authorization", access.ErrForbidden, http.StatusForbidden},
		{"revision race", authoring.ErrStaleRevision, http.StatusConflict},
		{"unavailable", errors.New("compile unavailable"), http.StatusInternalServerError},
	} {
		t.Run(failure.name, func(t *testing.T) {
			h, fake, _, signals := builderResumeFixture(t)
			// These cases must still authorize/compile before deciding there is
			// no filter session to recover.
			h.SessionStore = nil
			delete(signals, "builderFilterState")
			fake.compileErr = failure.err
			recorder := runBuilderResume(t, h, signals)
			if recorder.Code != failure.status || fake.previewCalls != 0 {
				t.Fatalf("status=%d previews=%d body=%s", recorder.Code, fake.previewCalls, recorder.Body.String())
			}
		})
	}
}

func TestBuilderResumeRetainsCurrentGenerationFilters(t *testing.T) {
	h, fake, runtime, signals := builderResumeFixture(t)
	definition := fake.compilation.Definition
	definition.FilterDefinitions = map[string]dashboardfilter.Definition{
		"status": {Label: "Status", Field: "status", ValueKind: dashboardfilter.ValueString,
			Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionComparison, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorEquals}}}},
	}
	definition.FilterBindings = map[string]dashboardfilter.Binding{
		"status": {Key: "status", ID: "status", Filter: "status", Scope: dashboardfilter.ScopeReport, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}, Selection: dashboardfilter.SelectionPolicy{Mode: dashboardfilter.SelectionSingle}},
	}
	fake.compilation.Definition = definition
	machine := dashboardfilter.NewMachine(definition.FilterApplication.WithDefaults().Mode, definition.FilterBindingSpecs())
	expression := dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals, Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "open"}}
	selected, err := machine.Execute(dashboardfilter.Command{Kind: dashboardfilter.CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "select-open", BindingKey: "status", Operation: dashboardfilter.MutationSet, Expression: &expression})
	if err != nil {
		t.Fatal(err)
	}
	signals["builderFilterState"] = selected.State
	recorder := runBuilderResume(t, h, signals)
	patches := ssetest.PatchSignals(t, recorder.Body.String())
	if recorder.Code != http.StatusOK || fake.previewCalls != 0 || len(patches) != 1 || patches[0]["pageStreamRecovery"] != false {
		t.Fatalf("current generation was rebootstrapped: status=%d previews=%d body=%s", recorder.Code, fake.previewCalls, recorder.Body.String())
	}
	key := dashboardsession.Key{ProjectID: "sales", DashboardID: "revenue", PrincipalOrClient: "principal-1:client_1", ServingStateID: *runtime.ServingStateID, StreamInstanceID: "stream_1"}
	record, err := h.SessionStore.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	state := record.State.Filters.State
	if state.Revision != selected.State.Revision || state.AppliedControls["status"].Expression.Value == nil || state.AppliedControls["status"].Expression.Value.Value != "open" {
		t.Fatalf("recovered filter state = %#v", state)
	}
}

func builderResumeFixture(t *testing.T) (Handler, *builderAuthoringFake, uisignals.RouteRuntimeSignal, map[string]any) {
	t.Helper()
	definition, err := dashboarddefinition.New("revenue", "Revenue", "", "sales", []dashboard.Page{{ID: "details"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder := uisignals.DashboardBuilderSignal{
		ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-7",
		Revision:       uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: "sha256:" + strings.Repeat("a", 64)},
		SelectedPageID: uisignals.Optional("details"), SelectedVisualID: uisignals.Optional("chart-1"),
		Pages:        []uisignals.DashboardBuilderPageSignal{{ID: "details"}},
		Capabilities: uisignals.DashboardBuilderCapabilitiesSignal{CanEdit: true},
	}
	builder.Preview.Active = true
	evidence := preview.SemanticServingStateEvidence{Identity: projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation-2"}}
	fake := &builderAuthoringFake{
		builder:     builder,
		compilation: preview.Compilation{Definition: definition, SemanticEvidence: evidence},
		preview:     preview.Preview{Definition: definition, SemanticEvidence: evidence},
	}
	h := Handler{Authoring: fake, ProjectID: "sales", SessionStore: dashboardsession.NewMemoryStore(), CurrentPrincipalID: func(*http.Request) string { return "principal-1" }}
	runtime := uisignals.RouteRuntimeSignal{
		ClientID: uisignals.Optional("client_1"), StreamInstanceID: uisignals.Optional("stream_1"),
		ServingStateID: uisignals.Optional(builderServingStateIDForGeneration(builder, "generation-2")),
	}
	return h, fake, runtime, map[string]any{
		"builder": builder, "runtime": runtime, "builderFilterState": definition.DefaultFilterState(),
		"agent": map[string]any{"activeConversationId": "conversation-1"}, "agentVisuals": map[string]any{"retained": map[string]any{"title": "Keep me"}},
	}
}

type builderResumeRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (r *builderResumeRecorder) Flush() {
	r.ResponseRecorder.Flush()
	// End the idle stream after a bootstrap or successful resume, without
	// interrupting its preceding loading patch.
	if strings.Contains(r.Body.String(), `"runtime":`) || strings.Contains(r.Body.String(), `"pageStreamRecovery":`) {
		r.cancel()
	}
}

func runBuilderResume(t *testing.T, h Handler, signals map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(signals)
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"dashboard": {"revenue"}, "draft": {"draft-7"}, "route": {"dashboard_builder"}, "streamInstance": {"stream_1"}, "page": {"initial-page"}, "visual": {"initial-visual"}, "datastar": {string(encoded)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/updates?"+query.Encode(), nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: webtransport.ClientIDCookieName, Value: "client_1"})
	recorder := &builderResumeRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	h.DashboardBuilderUpdates(recorder, request)
	return recorder.ResponseRecorder
}

func assertBuilderResumeBootstrap(t *testing.T, fake *builderAuthoringFake, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusOK || fake.previewCalls != 1 {
		t.Fatalf("status=%d previews=%d body=%s", recorder.Code, fake.previewCalls, recorder.Body.String())
	}
	patches := ssetest.PatchSignals(t, recorder.Body.String())
	if len(patches) != 2 {
		t.Fatalf("patches=%d, want loading then bootstrap: %s", len(patches), recorder.Body.String())
	}
	bootstrap := patches[1]
	builder := bootstrap["builder"].(map[string]any)
	if builder["preview"].(map[string]any)["loading"] != false || builder["capabilities"].(map[string]any)["canEdit"] != true {
		t.Fatalf("bootstrap did not restore editing: %#v", builder)
	}
	if fake.builderReq.SelectedPageID != "details" || fake.builderReq.SelectedVisualID != "chart-1" {
		t.Fatalf("lost mounted selection: %#v", fake.builderReq)
	}
	if bootstrap["runtime"].(map[string]any)["servingStateId"] != builderServingStateIDForGeneration(fake.builder, "generation-2") {
		t.Fatalf("bootstrap retained stale runtime: %#v", bootstrap["runtime"])
	}
	for _, patch := range patches {
		for _, signal := range []string{"agent", "agentVisuals"} {
			if _, present := patch[signal]; present {
				t.Fatalf("bootstrap replaced retained %s", signal)
			}
		}
	}
}
