package actions

import (
	"testing"

	apigenui "github.com/Yacobolo/toolbelt/apigen/runtime/ui"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func TestRequestEscapesPathAndSignalPatterns(t *testing.T) {
	got := QueryPost(`/workspaces/it's\here`, "runtime", "filters.controls", "table[0]")
	want := `@post('/workspaces/it\'s\\here', {filterSignals: {include: /^(?:runtime|filters[.]controls|table\[0\])(?:[.]|$)/}, headers: window.LeapViewCommand.headers()})`
	if got != want {
		t.Fatalf("QueryPost() = %q, want %q", got, want)
	}
}

func TestRequestWithoutSignalFilter(t *testing.T) {
	if got, want := Get("/search"), `@get('/search', {headers: window.LeapViewCommand.headers()})`; got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
	if got, want := UncontractedMutationPatch("/api/config"), `@patch('/api/config', {headers: window.LeapViewCommand.headers()})`; got != want {
		t.Fatalf("UncontractedMutationPatch() = %q, want %q", got, want)
	}
}

func TestCommandRequestsCarryTypedGeneratedOperationIdentity(t *testing.T) {
	binding := apigenui.MustAction("widget.create", "createWidget")
	if got, want := CommandPost(binding, "/widgets", "widget"), `@post('/widgets', {filterSignals: {include: /^(?:widget)(?:[.]|$)/}, headers: window.LeapViewCommand.headers('createWidget')})`; got != want {
		t.Fatalf("CommandPost() = %q, want %q", got, want)
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

func TestGetScopesActiveSearchSignals(t *testing.T) {
	got := Get("/chats/references/search", "agentReferenceSearch", "agentContext")
	want := `@get('/chats/references/search', {filterSignals: {include: /^(?:agentReferenceSearch|agentContext)(?:[.]|$)/}, headers: window.LeapViewCommand.headers()})`
	if got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}
