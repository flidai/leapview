package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatDashboardDraftCreateUsesRequestScopedSlug(t *testing.T) {
	for _, tc := range []struct {
		name, embed, slug, requestID, wantSlug string
	}{
		{"chat draft", "chat", "", browserTestRequestID, "chat-" + browserTestRequestID},
		{"same title in another chat draft", "chat", "", "01890f3e-4c00-7000-8000-000000000002", "chat-01890f3e-4c00-7000-8000-000000000002"},
		{"explicit chat slug", "chat", "sales-custom", browserTestRequestID, "sales-custom"},
		{"catalog draft", "", "", browserTestRequestID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &builderAuthoringFake{createResult: browserDraftResult(t, "dashboard-created")}
			handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
			request := httptest.NewRequest(nethttp.MethodPost, "/dashboards/new", strings.NewReader("title=Sales&semanticModel=sales-model&embed="+tc.embed+"&slug="+tc.slug+"&idempotencyKey="+tc.requestID))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			handler.DashboardDraftCreate(recorder, request)
			if recorder.Code != nethttp.StatusSeeOther {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			// A chat's display title can repeat. Its draft's route identity must
			// distinguish separate requests while remaining stable for a retry.
			if fake.createRequest.Title != "Sales" || fake.createRequest.Slug != tc.wantSlug || fake.createRequest.IdempotencyKey != tc.requestID {
				t.Fatalf("create request = %#v, want slug %q and request ID %q", fake.createRequest, tc.wantSlug, tc.requestID)
			}
		})
	}
}
