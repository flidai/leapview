package protocol

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
)

func TestProjectBoundaryIdempotencySeparatesLocatorsAndReauthorizes(t *testing.T) {
	allowed, calls := true, 0
	p, err := Build(t.Context(), Config{
		Store: idempotency.NewMemoryStore(), CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true },
		PrincipalID:     func(*http.Request) (string, bool) { return "principal", true },
		ReplayAuthorize: func(*http.Request) bool { return allowed },
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	request := func(project, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+project+"/releases", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "same-key")
		r.Header.Set("Authorization", "Bearer credential")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, project := range []string{"project_a", "project_b", "project_a"} {
		response := request(project, `{}`)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "/"+project+"/") {
			t.Fatalf("cross-Project replay: %d %s", response.Code, response.Body)
		}
	}
	if calls != 2 {
		t.Fatalf("executions=%d, want one per locator", calls)
	}
	if response := request("project_a", `{"changed":true}`); response.Code != http.StatusConflict {
		t.Fatal("changed request replayed")
	}
	allowed = false
	if response := request("project_a", `{}`); response.Code != http.StatusForbidden {
		t.Fatal("revoked request replayed")
	}
	if calls != 2 {
		t.Fatal("rejected replay executed domain work")
	}
}
