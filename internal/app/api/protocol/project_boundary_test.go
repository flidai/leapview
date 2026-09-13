package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestIdempotencyReplaysCompatibleLegacyScopeAfterV2Rollout(t *testing.T) {
	store := idempotency.NewMemoryStore()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:server/releases", strings.NewReader(`{"name":"release"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "legacy-key")
	body := []byte(`{"name":"release"}`)
	digest := apiRequestDigest(request, body)
	credential := sha256.Sum256([]byte("credential"))
	legacyScope := "principal:" + hex.EncodeToString(credential[:]) + ":" + request.Method + ":" + request.URL.EscapedPath() + ":legacy-key"
	record, execute, err := store.Claim(t.Context(), legacyScope, digest, "legacy-owner", time.Minute, IdempotencyLifetime)
	if err != nil || !execute {
		t.Fatalf("seed legacy record: execute=%t record=%#v err=%v", execute, record, err)
	}
	if err := store.Complete(t.Context(), legacyScope, digest, "legacy-owner", record.LeaseGeneration, http.StatusCreated, http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"id":"release:legacy"}`)); err != nil {
		t.Fatal(err)
	}

	p, err := Build(t.Context(), Config{
		Store: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true },
		PrincipalID: func(*http.Request) (string, bool) { return "principal", true },
		AuthoritativeScope: func(*http.Request) (AuthoritativeScope, error) {
			return AuthoritativeScope{TargetID: "target:prod", ProjectID: "project:server", Environment: "prod", GenerationID: "generation:active"}, nil
		},
		ReplayAuthorize: func(*http.Request) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	w := httptest.NewRecorder()
	p.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, request)
	if w.Code != http.StatusCreated || w.Header().Get("Idempotency-Replayed") != "true" || called || w.Body.String() != `{"id":"release:legacy"}` {
		t.Fatalf("legacy retry status=%d replay=%q called=%t body=%s", w.Code, w.Header().Get("Idempotency-Replayed"), called, w.Body.String())
	}
}

func TestIdempotencyRejectsAmbiguousLegacyProjectScope(t *testing.T) {
	store := idempotency.NewMemoryStore()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:route/releases", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "legacy-key")
	digest := apiRequestDigest(request, []byte(`{}`))
	credential := sha256.Sum256([]byte("credential"))
	legacyScope := "principal:" + hex.EncodeToString(credential[:]) + ":" + request.Method + ":" + request.URL.EscapedPath() + ":legacy-key"
	record, execute, err := store.Claim(t.Context(), legacyScope, digest, "legacy-owner", time.Minute, IdempotencyLifetime)
	if err != nil || !execute {
		t.Fatalf("seed legacy record: execute=%t record=%#v err=%v", execute, record, err)
	}
	if err := store.Complete(t.Context(), legacyScope, digest, "legacy-owner", record.LeaseGeneration, http.StatusCreated, nil, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	p, err := Build(t.Context(), Config{
		Store: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true },
		PrincipalID: func(*http.Request) (string, bool) { return "principal", true },
		AuthoritativeScope: func(*http.Request) (AuthoritativeScope, error) {
			return AuthoritativeScope{TargetID: "target:prod", ProjectID: "project:other", Environment: "prod", GenerationID: "generation:active"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	w := httptest.NewRecorder()
	p.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, request)
	if w.Code != http.StatusServiceUnavailable || called || !strings.Contains(w.Body.String(), "IDEMPOTENCY_SCOPE_UNAVAILABLE") {
		t.Fatalf("ambiguous legacy retry status=%d called=%t body=%s", w.Code, called, w.Body.String())
	}
}
