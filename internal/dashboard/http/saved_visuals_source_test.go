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

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
)

type visualSourceStore struct {
	authoring.SavedVisualStore
	saved []authoring.SavedVisual
}

func (s *visualSourceStore) SaveVisual(_ context.Context, _, _ string, value authoring.SavedVisual) (authoring.SavedVisual, error) {
	s.saved = append(s.saved, value)
	value.ID = "saved-id"
	return value, nil
}
func (s *visualSourceStore) SavedVisuals(context.Context, string, string) ([]authoring.SavedVisual, error) {
	return s.saved, nil
}

func TestSaveGeneratedVisualUsesAuthorizedCurrentCanonicalSource(t *testing.T) {
	var doc document.DashboardDocument
	err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Dashboard","metadata":{"id":"dashboard:sales","name":"sales"},"spec":{"semanticModel":"sales","visuals":[{"id":"revenue","type":"kpi","title":"Revenue","query":{"type":"aggregate","dimensions":[],"metrics":["net_revenue"]},"presentation":{"type":"kpi"}}],"pages":[{"id":"overview","title":"Overview","components":[{"id":"placement","type":"visual","visual":"revenue","placement":{"column":1,"row":1,"columnSpan":3,"rowSpan":2}}]}],"filters":[{"id":"country","label":"Country","dimension":"country","targets":["revenue"],"control":{"type":"singleSelect"}},{"id":"unrelated","label":"Other","dimension":"segment","targets":["other"],"control":{"type":"singleSelect"}},{"id":"dependent","label":"Dependent","dimension":"country","control":{"type":"singleSelect","options":{"type":"distinct","dataset":"sales","dependsOn":["unrelated"]}}}]}}`), &doc)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(doc)
	for _, tc := range []struct {
		name, page, component, revision string
		denied                          bool
		wantSave                        bool
	}{
		{"valid", "overview", "placement", "revision", false, true},
		{"wrong page", "other", "placement", "revision", false, false},
		{"definition is not placement", "overview", "revenue", "revision", false, false},
		{"stale", "overview", "placement", "old", false, false},
		{"unauthorized", "overview", "placement", "revision", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &visualSourceStore{}
			result := browserDraftResult(t, "dashboard-owned")
			fake := &builderAuthoringFake{draftRead: application.DraftRead{Lifecycle: result.Lifecycle, Revision: authoring.Revision{ID: "revision", Document: doc}}}
			if tc.denied {
				fake.err = access.ErrForbidden
			}
			handler := Handler{Authoring: fake, SavedVisuals: store, ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "owner" }}
			form := url.Values{"dashboardId": {"dashboard-owned"}, "revisionId": {tc.revision}, "pageId": {tc.page}, "componentId": {tc.component}, "title": {"Revenue"}, "sourceKey": {"chat/revenue"}, "definition": {`{"semanticModelId":"injected"}`}}
			request := httptest.NewRequest(http.MethodPost, "/visuals/saved", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			handler.SaveVisual(recorder, request)
			if (len(store.saved) == 1) != tc.wantSave {
				t.Fatalf("saved=%d status=%d body=%s", len(store.saved), recorder.Code, recorder.Body.String())
			}
			if fake.draftRequest.ActorID != "owner" || fake.draftRequest.DashboardID != "dashboard-owned" {
				t.Fatalf("authorization request=%+v", fake.draftRequest)
			}
			if tc.wantSave {
				if recorder.Code != http.StatusSeeOther {
					t.Fatalf("status=%d", recorder.Code)
				}
				var copied chatDraftVisual
				if err := json.Unmarshal([]byte(store.saved[0].DefinitionJSON), &copied); err != nil {
					t.Fatal(err)
				}
				if copied.SemanticModelID != "sales" || len(copied.Filters) != 1 || copied.Filters[0].ID != "saved_filter_0" || copied.Visual.Title == nil || *copied.Visual.Title != "Revenue" {
					t.Fatalf("copied source=%+v", copied)
				}
			}
			after, _ := json.Marshal(doc)
			if string(before) != string(after) {
				t.Fatal("source document mutated")
			}
		})
	}
	_, err = dashboardVisualForLibrary(doc, "overview", "unknown")
	if !errors.Is(err, authoring.ErrNotFound) {
		t.Fatalf("missing component error=%v", err)
	}
}

func TestGeneratedVisualCopyKeepsScopedCascadesWithoutDuplicatingReportControls(t *testing.T) {
	var filters []document.DashboardFilter
	if err := json.Unmarshal([]byte(`[
 {"id":"report","label":"Country","dimension":"country","control":{"type":"singleSelect"}},
 {"id":"child","label":"City","dimension":"city","targets":["revenue"],"control":{"type":"singleSelect","options":{"type":"distinct","dataset":"sales","dependsOn":["report"]}}},
 {"id":"unrelated","label":"Other","dimension":"segment","control":{"type":"singleSelect"}}
 ]`), &filters); err != nil {
		t.Fatal(err)
	}
	copied, err := application.CopyChatVisualFilters(filters, "revenue")
	if err != nil || len(copied) != 2 {
		t.Fatalf("scoped cascade=%+v err=%v", copied, err)
	}
	reportOnly, err := application.CopyChatVisualFilters(filters, "other-visual")
	if err != nil || len(reportOnly) != 0 {
		t.Fatalf("report controls duplicated=%+v err=%v", reportOnly, err)
	}
	if filters[0].Targets != nil || filters[2].Targets != nil {
		t.Fatal("source report scope mutated")
	}
}
