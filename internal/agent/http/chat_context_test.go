package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestChatReferenceSearchUsesGlobalScopeAndEchoesRequestIdentity(t *testing.T) {
	results := make([]ui.AgentReferenceSignal, 30)
	for index := range results {
		results[index] = ui.AgentReferenceSignal{
			Reference: ui.AgentReferenceKeySignal{ProjectID: "project_demo", Type: "model", ID: "model-" + string(rune('a'+index))},
			Name:      "Field", Project: ui.AgentReferenceProjectSignal{ID: "project_demo", Name: "Demo"},
			Locations: []ui.AgentReferenceLocationSignal{}, Context: []string{},
		}
	}
	searchedContext := agent.TurnContext{Surface: "invalid"}
	searchedLimit := 0
	handler := NewHandler(Options{
		SearchReferences: func(_ *http.Request, context agent.TurnContext, _ string, limit int) ([]ui.AgentReferenceSignal, error) {
			searchedContext = context
			searchedLimit = limit
			return results, nil
		},
	})
	signals, err := json.Marshal(map[string]any{
		"agentReferenceSearch": map[string]any{
			"query": "field", "requestId": 7,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/?datastar="+url.QueryEscape(string(signals)), nil)
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()

	handler.ChatReferenceSearch(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if searchedLimit != maxChatReferenceSearchResults {
		t.Fatalf("searched limit = %d, want %d", searchedLimit, maxChatReferenceSearchResults)
	}
	if got := strings.Count(response.Body.String(), `"type":"model"`); got != 24 {
		t.Fatalf("result count = %d, want 24:\n%s", got, response.Body.String())
	}
	for _, want := range []string{`"query":"field"`, `"requestId":7`, `"projectId":"project_demo"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("search response missing %s:\n%s", want, response.Body.String())
		}
	}
	for _, deadField := range []string{`"dashboardId":"executive-sales"`, `"pageId":"overview"`} {
		if strings.Contains(response.Body.String(), deadField) {
			t.Fatalf("search response echoed redundant context %s:\n%s", deadField, response.Body.String())
		}
	}
}

func TestChatSignalPatchKeepsEmbeddedArtifactsSeparateFromDashboardVisuals(t *testing.T) {
	state := ui.ChatViewState{
		Agent:   ui.ChatSignal{Conversations: []ui.ChatConversationSummary{}, Transcript: []ui.ChatTranscriptItemSignal{}},
		Visuals: map[string]visualizationir.VisualizationEnvelope{},
	}
	embedded := chatSignalPatch(state, true)
	if _, ok := embedded["agentVisuals"]; !ok {
		t.Fatalf("embedded patch = %#v, want agentVisuals", embedded)
	}
	if _, ok := embedded["visuals"]; ok {
		t.Fatalf("embedded patch = %#v, must not replace dashboard visuals", embedded)
	}
	standalone := chatSignalPatch(state, false)
	if _, ok := standalone["visuals"]; !ok {
		t.Fatalf("standalone patch = %#v, want visuals", standalone)
	}
}
