package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/go-chi/chi/v5"
)

func TestChatDashboardDocumentPreservesAllCardsAndScopedFilters(t *testing.T) {
	var visual document.DashboardVisual
	if err := json.Unmarshal([]byte(`{"type":"bar","title":"Revenue by region","query":{"type":"aggregate","dimensions":["region"],"metrics":["revenue"],"limit":50},"presentation":{"type":"cartesian"},"dataBudget":{"maxRows":50,"requiredCompleteness":"partial"}}`), &visual); err != nil {
		t.Fatal(err)
	}
	var filters []document.DashboardFilter
	if err := json.Unmarshal([]byte(`[{"id":"country","label":"Country","dimension":"country","control":{"type":"text"},"default":{"type":"comparison","operator":"eq","value":{"type":"string","value":"France"}}}]`), &filters); err != nil {
		t.Fatal(err)
	}
	draft := agent.ChatDashboardDraft{Revision: "compose-1", Title: "Performance", SemanticModelID: "semantic-model:finance", Visuals: []agent.ChatDashboardDraftVisual{
		{ID: "revenue", ArtifactID: "artifact-revenue", Title: "Revenue by region", Visual: visual, Filters: filters},
		{ID: "comparison", ArtifactID: "artifact-comparison", Title: "Comparison", Visual: visual},
	}}
	value, err := chatDashboardDocument(draft, authoring.CommandID(chatVisualDashboardTestCommandID))
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Spec.Visuals) != 2 || len(value.Spec.Pages) != 1 || len(value.Spec.Pages[0].Components) != 2 {
		t.Fatalf("incomplete imported dashboard: %#v", value.Spec)
	}
	if value.Spec.SemanticModel != draft.SemanticModelID {
		t.Fatal("semantic model changed")
	}
	first := value.Spec.Pages[0].Components[0].Value.(*document.VisualDashboardPageComponent)
	second := value.Spec.Pages[0].Components[1].Value.(*document.VisualDashboardPageComponent)
	if first.Visual == second.Visual {
		t.Fatal("distinct cards were collapsed")
	}
	if !reflect.DeepEqual(value.Spec.Visuals[first.Visual], visual) || !reflect.DeepEqual(value.Spec.Visuals[second.Visual], visual) {
		t.Fatal("authored visual definition changed")
	}
	if len(value.Spec.Filters) != 1 || value.Spec.Filters[0].Targets == nil || !reflect.DeepEqual(*value.Spec.Filters[0].Targets, []string{first.Visual}) {
		t.Fatal("card filter leaked into another visual")
	}
	if !reflect.DeepEqual(value.Spec.Filters[0].Default, filters[0].Default) {
		t.Fatal("filter expression lost")
	}
	if len(draft.Visuals[0].Filters) != 1 || draft.Visuals[0].Filters[0].Targets != nil {
		t.Fatal("saved document mutated original conversation source")
	}
	if first.Placement.Row == second.Placement.Row && first.Placement.Column == second.Placement.Column {
		t.Fatal("saved cards overlap")
	}
	retried, err := chatDashboardDocument(draft, authoring.CommandID(chatVisualDashboardTestCommandID))
	if err != nil || !reflect.DeepEqual(value, retried) {
		t.Fatal("retry changed document identity")
	}
}

func TestChatDashboardDocumentRejectsEmptyDraft(t *testing.T) {
	if _, err := chatDashboardDocument(agent.ChatDashboardDraft{SemanticModelID: "semantic-model:finance"}, authoring.CommandID(chatVisualDashboardTestCommandID)); err == nil {
		t.Fatal("empty dashboard accepted")
	}
}

func TestImportedChatProportionalLegendMatchesPreview(t *testing.T) {
	for _, position := range []string{"", "top", "left", "right", "bottom", "none"} {
		t.Run(position, func(t *testing.T) {
			presentation := &document.ProportionalDashboardPresentation{Type: "proportional"}
			if position != "" {
				legend := document.DashboardLegendPosition(position)
				presentation.Legend = &legend
			}
			visual := document.DashboardVisual{Type: document.DashboardVisualTypeDonut, Presentation: document.DashboardPresentation{Value: presentation}}
			normalized := normalizeImportedChatLegend(visual)
			got := normalized.Presentation.Value.(*document.ProportionalDashboardPresentation)
			want := document.DashboardLegendPositionBottom
			if position == "none" {
				want = document.DashboardLegendPositionNone
			}
			if got.Legend == nil || *got.Legend != want {
				t.Fatalf("legend = %v, want %s", got.Legend, want)
			}
			if position == "" && presentation.Legend != nil || position != "" && string(*presentation.Legend) != position {
				t.Fatal("legend normalization mutated the conversation source")
			}
		})
	}
}

func TestChatDashboardSaveRejectsClientAuthoredRowsAndInvalidIdentity(t *testing.T) {
	fixture := newChatVisualDashboardHTTPFixture(t)
	handler := fixture.handler(fixture.ownerID, nil)
	router := chi.NewRouter()
	router.Post("/chats/{conversation}/dashboard", handler.SaveChatDashboardDraftUI)
	for _, body := range []string{
		`{"revision":"r1","title":"Dashboard","visuals":[{"rows":[[100]]}]}`,
		`{"revision":"","title":"Dashboard"}`,
		`{"revision":"r1","title":" "}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/chats/"+fixture.conversationID+"/dashboard", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid payload status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
