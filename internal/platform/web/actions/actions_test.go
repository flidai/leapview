package actions

import (
	"strings"
	"testing"

	apigenui "github.com/Yacobolo/toolbelt/apigen/runtime/ui"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func TestRequestEscapesPathAndSignalPatterns(t *testing.T) {
	got := QueryPost(`/projects/it's\here`, "runtime", "filters.controls", "table[0]")
	want := `@post('/projects/it\'s\\here', {filterSignals: {include: /^(?:runtime|filters[.]controls|table\[0\])(?:[.]|$)/}, headers: window.LeapViewCommand.headers()})`
	if got != want {
		t.Fatalf("QueryPost() = %q, want %q", got, want)
	}
}

func TestRequestWithoutSignalFilter(t *testing.T) {
	if got, want := Get("/search"), `@get('/search', {headers: window.LeapViewCommand.headers()})`; got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}

func TestConcurrentEventPostKeepsIndependentWindowRequests(t *testing.T) {
	got := ConcurrentEventPost("/windows", "runtime", "window")
	want := `@post('/windows', {filterSignals: {include: /^(?:runtime|window)(?:[.]|$)/}, headers: window.LeapViewCommand.headers(), requestCancellation: 'disabled'})`
	if got != want {
		t.Fatalf("ConcurrentEventPost() = %q, want %q", got, want)
	}
}

func TestCommandRequestsCarryTypedGeneratedOperationIdentity(t *testing.T) {
	binding := apigenui.MustAction("widget.create", "createWidget")
	if got, want := CommandPost(binding, "/widgets", "widget"), `@post('/widgets', {filterSignals: {include: /^(?:widget)(?:[.]|$)/}, headers: window.LeapViewCommand.headers('createWidget')})`; got != want {
		t.Fatalf("CommandPost() = %q, want %q", got, want)
	}
	if got, want := CommandPatch(binding, "/widgets", `"revision-1"`, "widget"), `@patch('/widgets', {filterSignals: {include: /^(?:widget)(?:[.]|$)/}, headers: window.LeapViewCommand.headers('createWidget', '"revision-1"')})`; got != want {
		t.Fatalf("CommandPatch() = %q, want %q", got, want)
	}

	switchRequest := CommandPostSwitch("evt.detail.action", map[string]uicommand.Binding{
		"update": apigenui.MustAction("widget.update", "updateWidget"),
		"create": binding,
	}, "/widgets")
	wantSwitch := `@post('/widgets', {headers: window.LeapViewCommand.headers(({'create': 'createWidget', 'update': 'updateWidget'})[evt.detail.action])})`
	if switchRequest != wantSwitch {
		t.Fatalf("CommandPostSwitch() = %q, want %q", switchRequest, wantSwitch)
	}

	sequence := CommandPostSequence([]uicommand.Binding{binding, apigenui.MustAction("widget.run", "runWidget")}, "/widgets")
	wantSequence := `@post('/widgets', {headers: window.LeapViewCommand.headers(['createWidget', 'runWidget'])})`
	if sequence != wantSequence {
		t.Fatalf("CommandPostSequence() = %q, want %q", sequence, wantSequence)
	}

	conditional := CommandPostConditional("$widget.id", []uicommand.Binding{apigenui.MustAction("widget.run", "runWidget")}, []uicommand.Binding{binding, apigenui.MustAction("widget.run", "runWidget")}, "/widgets")
	wantConditional := `@post('/widgets', {headers: window.LeapViewCommand.headers(($widget.id ? ['runWidget'] : ['createWidget', 'runWidget']))})`
	if conditional != wantConditional {
		t.Fatalf("CommandPostConditional() = %q, want %q", conditional, wantConditional)
	}
}

func TestNonReplayableCommandUsesSinglePostAndDedicatedHeaders(t *testing.T) {
	binding := apigenui.MustNonReplayableAction("credential.create", "createCredential")
	got := CommandPost(binding, "/credentials", "credentialDraft")
	want := `@post('/credentials', {retry: 'never', retryMaxCount: 0, openWhenHidden: true, filterSignals: {include: /^(?:credentialDraft)(?:[.]|$)/}, headers: window.LeapViewCommand.nonReplayableHeaders('createCredential')})`
	if got != want {
		t.Fatalf("CommandPost() = %q, want %q", got, want)
	}
	if strings.Contains(got, "Idempotency-Key") || strings.Contains(got, "window.LeapViewCommand.headers") {
		t.Fatalf("CommandPost() exposed replayable command headers: %q", got)
	}
}

func TestNonReplayableCommandIsRejectedByOtherCommandBuilders(t *testing.T) {
	forbidden := apigenui.MustNonReplayableAction("credential.create", "createCredential")
	ordinary := apigenui.MustAction("credential.rotate", "rotateCredential")
	cases := []struct {
		name  string
		build func()
	}{
		{"patch", func() { CommandPatch(forbidden, "/credentials", `"revision"`) }},
		{"patch-with-revision", func() { CommandPatchWithRevision(forbidden, "/credentials", `signal.revision`) }},
		{"switch", func() {
			CommandPostSwitch("evt.detail.action", map[string]uicommand.Binding{"create": forbidden, "rotate": ordinary}, "/credentials")
		}},
		{"switch-with-revision", func() {
			CommandPostSwitchWithRevision("evt.detail.action", map[string]uicommand.Binding{"create": forbidden}, "/credentials", `signal.revision`)
		}},
		{"sequence", func() { CommandPostSequence([]uicommand.Binding{ordinary, forbidden}, "/credentials") }},
		{"conditional-true", func() {
			CommandPostConditional("signal.create", []uicommand.Binding{forbidden}, []uicommand.Binding{ordinary}, "/credentials")
		}},
		{"conditional-false", func() {
			CommandPostConditional("signal.create", []uicommand.Binding{ordinary}, []uicommand.Binding{forbidden}, "/credentials")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected non-replayable binding to be rejected")
				}
			}()
			testCase.build()
		})
	}
}

func TestGetScopesActiveSearchSignals(t *testing.T) {
	got := Get("/chats/references/search", "agentReferenceSearch", "agentContext")
	want := `@get('/chats/references/search', {filterSignals: {include: /^(?:agentReferenceSearch|agentContext)(?:[.]|$)/}, headers: window.LeapViewCommand.headers()})`
	if got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}
