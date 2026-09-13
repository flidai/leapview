package protocol

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
)

func TestIdempotentCommandTargetsArePathBound(t *testing.T) {
	for operationID, contract := range apiaggregate.GetAPIGenOperationContracts() {
		if contract.Command == nil || contract.Command.Idempotency != "required" || contract.Command.Target == nil {
			continue
		}
		parameter := strings.TrimSpace(contract.Command.Target.Parameter)
		if parameter == "" || !strings.Contains(contract.Path, "{"+parameter+"}") {
			t.Errorf("idempotent command %s target %q is not path-bound in %q", operationID, parameter, contract.Path)
		}
	}
}

func TestProjectBoundaryIdempotencySeparatesLocatorsAndReauthorizes(t *testing.T) {
	allowed, calls := true, 0
	p, err := Build(t.Context(), Config{
		Store: idempotency.NewMemoryStore(), CursorSigning: cursorsigning.NewEphemeralInitializer(),
		AuthoritativeScope: testAuthoritativeScope,
		BearerToken:        func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true },
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

func TestIdempotencyScopeSeparatesEveryAuthoritativeRuntimeIdentity(t *testing.T) {
	current := AuthoritativeScope{TargetID: "target:prod", ProjectID: "project:one", Environment: "prod", GenerationID: "generation:one"}
	p, err := Build(t.Context(), Config{
		Store: idempotency.NewMemoryStore(), CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true },
		PrincipalID:        func(*http.Request) (string, bool) { return "principal", true },
		AuthoritativeScope: func(*http.Request) (AuthoritativeScope, error) { return current, nil },
		ReplayAuthorize:    func(*http.Request) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler := p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "%s/%s/%s/%s", current.TargetID, current.ProjectID, current.Environment, current.GenerationID)
	}))
	request := func(candidate string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:route/delivery/candidates/"+candidate+"/publish", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer credential")
		r.Header.Set("Idempotency-Key", "same-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	first := request("candidate-one")
	if replay := request("candidate-one"); replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != first.Body.String() {
		t.Fatalf("identical authoritative scope did not replay: %d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	for name, mutate := range map[string]func(*AuthoritativeScope){
		"target":      func(scope *AuthoritativeScope) { scope.TargetID = "target:other" },
		"Project":     func(scope *AuthoritativeScope) { scope.ProjectID = "project:other" },
		"environment": func(scope *AuthoritativeScope) { scope.Environment = "staging" },
		"generation":  func(scope *AuthoritativeScope) { scope.GenerationID = "generation:other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := current
			mutate(&changed)
			current = changed
			response := request("candidate-one")
			if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "" {
				t.Fatalf("changed %s scope collided: %d headers=%v body=%s", name, response.Code, response.Header(), response.Body.String())
			}
		})
	}
	if response := request("candidate-two"); response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("changed candidate scope collided: %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if calls != 6 {
		t.Fatalf("domain executions=%d, want one per authoritative scope plus candidate", calls)
	}
}

func TestIdempotencyFailsClosedWhenAuthoritativeScopeIsUnavailable(t *testing.T) {
	p, err := Build(t.Context(), Config{
		Store: idempotency.NewMemoryStore(), CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true },
		PrincipalID: func(*http.Request) (string, bool) { return "principal", true },
		AuthoritativeScope: func(*http.Request) (AuthoritativeScope, error) {
			return AuthoritativeScope{}, fmt.Errorf("authority unavailable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := p.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/groups", strings.NewReader(`{"name":"group"}`))
	r.Header.Set("Authorization", "Bearer credential")
	r.Header.Set("Idempotency-Key", "same-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "IDEMPOTENCY_SCOPE_UNAVAILABLE") || called {
		t.Fatalf("unavailable authoritative scope response=%d called=%t body=%s", w.Code, called, w.Body.String())
	}
}
