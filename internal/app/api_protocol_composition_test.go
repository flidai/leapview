package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

func TestAPIProtocolPersistenceRequiresCompleteExplicitAuthorities(t *testing.T) {
	_, _, err := (apiProtocolPersistence{Idempotency: idempotency.NewMemoryStore()}).authorities()
	if err == nil || !strings.Contains(err.Error(), "both idempotency and cursor-signing") {
		t.Fatalf("partial explicit protocol authorities error = %v", err)
	}
	_, _, err = (apiProtocolPersistence{RequireExplicit: true}).authorities()
	if err == nil || !strings.Contains(err.Error(), "requires explicit durable authorities") {
		t.Fatalf("missing production protocol authorities error = %v", err)
	}
	store, cursor, err := (apiProtocolPersistence{
		Idempotency: idempotency.NewMemoryStore(), CursorSigning: cursorsigning.NewEphemeralInitializer(), RequireExplicit: true,
	}).authorities()
	if err != nil || store == nil || cursor == nil {
		t.Fatalf("complete explicit protocol authorities = (%T, %T, %v)", store, cursor, err)
	}
}

func TestAuthoritativeAPIIdempotencyScopeUsesServerIdentity(t *testing.T) {
	resolveProjectID := func(context.Context) (projectgraph.ResourceID, error) {
		return "project:server", nil
	}
	config := runtimeAssemblyInputs{
		InstanceID: "target:prod", DefaultEnvironment: "prod",
		ServingSnapshotResolver: func(context.Context) (string, error) { return "generation:active", nil },
	}
	projectRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:client/releases", nil)
	scope, err := authoritativeAPIIdempotencyScope(projectRequest, resolveProjectID, config)
	if err != nil {
		t.Fatal(err)
	}
	if scope.TargetID != "target:prod" || scope.ProjectID != "project:server" || scope.Environment != "prod" || scope.GenerationID != "generation:active" {
		t.Fatalf("Project command scope = %#v", scope)
	}

	platformRequest := httptest.NewRequest(http.MethodPost, "/api/v1/groups", nil)
	scope, err = authoritativeAPIIdempotencyScope(platformRequest, resolveProjectID, config)
	if err != nil {
		t.Fatal(err)
	}
	if scope.TargetID != "target:prod" || scope.Environment != "prod" || scope.ProjectID != "" || scope.GenerationID != "" {
		t.Fatalf("platform command inherited Project runtime scope = %#v", scope)
	}

	browserRequest := httptest.NewRequest(http.MethodPost, "/admin/publications/command", nil)
	scope, err = authoritativeAPIIdempotencyScope(browserRequest, resolveProjectID, config)
	if err != nil || scope.ProjectID != "project:server" || scope.GenerationID != "generation:active" {
		t.Fatalf("browser command scope = %#v, %v", scope, err)
	}
}

func TestAuthoritativeAPIIdempotencyScopeAllowsOnlyExplicitNoGenerationState(t *testing.T) {
	resolveProjectID := func(context.Context) (projectgraph.ResourceID, error) {
		return "project:server", nil
	}
	config := runtimeAssemblyInputs{
		InstanceID: "target:prod", DefaultEnvironment: "prod",
		ServingSnapshotResolver: func(context.Context) (string, error) { return "", servingstate.ErrNotFound },
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:server/releases", nil)
	scope, err := authoritativeAPIIdempotencyScope(request, resolveProjectID, config)
	if err != nil || scope.ProjectID != "project:server" || scope.GenerationID != "" {
		t.Fatalf("unactivated Project scope = %#v, %v", scope, err)
	}
	config.ServingSnapshotResolver = func(context.Context) (string, error) { return "", errors.New("storage unavailable") }
	if _, err := authoritativeAPIIdempotencyScope(request, resolveProjectID, config); err == nil {
		t.Fatal("Project command accepted an unavailable generation authority")
	}
}

func TestConfigureAPIProtocolUsesDurableClaimScopeBeforeFirstActivation(t *testing.T) {
	store := &compositionCountingIdempotencyStore{}
	platform := &platformServices{}
	runtime := &runtimeServices{projectIDResolver: func(context.Context) (projectgraph.ResourceID, error) {
		return "", servingstate.ErrNotFound
	}}
	config := runtimeAssemblyInputs{
		InstanceID: "target:test", DefaultEnvironment: "test",
		IdempotencyProjectIDResolver: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:claimed", nil
		},
		ServingSnapshotResolver: func(context.Context) (string, error) {
			return "", servingstate.ErrNotFound
		},
	}
	if err := configureAPIProtocol(&capabilityRoutes{}, runtime, platform, &httpPolicy{}, config, t.Context(), apiProtocolPersistence{
		Idempotency: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:claimed/releases", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "release-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	platform.apiProtocol.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || store.claims.Load() != 1 {
		t.Fatalf("pre-activation claimed Project response = %d claims=%d body=%s", recorder.Code, store.claims.Load(), recorder.Body.String())
	}
}

type compositionCountingIdempotencyStore struct {
	claims   atomic.Int32
	reclaims atomic.Int32
}

func (s *compositionCountingIdempotencyStore) Claim(_ context.Context, _ string, digest, owner string, lease, _ time.Duration) (idempotency.Record, bool, error) {
	s.claims.Add(1)
	return idempotency.Record{State: "pending", Digest: digest, Owner: owner, LeaseGeneration: 1, LeaseExpires: time.Now().Add(lease)}, true, nil
}
func (s *compositionCountingIdempotencyStore) ClaimReclaimable(_ context.Context, _ string, digest, owner string, lease, _ time.Duration) (idempotency.Record, bool, error) {
	s.reclaims.Add(1)
	return idempotency.Record{State: "pending", Digest: digest, Owner: owner, LeaseGeneration: 1, LeaseExpires: time.Now().Add(lease)}, true, nil
}
func (*compositionCountingIdempotencyStore) Load(context.Context, string) (idempotency.Record, error) {
	return idempotency.Record{}, nil
}
func (*compositionCountingIdempotencyStore) Renew(context.Context, string, string, string, int64, time.Duration) (time.Time, error) {
	return time.Now().Add(time.Minute), nil
}
func (*compositionCountingIdempotencyStore) Complete(context.Context, string, string, string, int64, int, http.Header, []byte) error {
	return nil
}
func (*compositionCountingIdempotencyStore) MarkIndeterminate(context.Context, string, string, string, int64) error {
	return nil
}

func TestConfigureAPIProtocolBypassesOnlyConfiguredCommandDurability(t *testing.T) {
	store := &compositionCountingIdempotencyStore{}
	platform := &platformServices{}
	if err := configureAPIProtocol(&capabilityRoutes{}, testAPIProtocolRuntime(), platform, &httpPolicy{}, runtimeAssemblyInputs{InstanceID: "target:test", DefaultEnvironment: "test"}, t.Context(), apiProtocolPersistence{
		Idempotency: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BypassDurableIdempotency: map[string]struct{}{"createRefreshRun": {}},
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project/refresh-runs", strings.NewReader(`{"pipelineId":"pipeline:sales"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "refresh-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	called := false
	platform.apiProtocol.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || !called {
		t.Fatalf("configured bypass response = %d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
	if store.claims.Load() != 0 {
		t.Fatalf("configured bypass claimed durable idempotency %d times", store.claims.Load())
	}
}

func TestConfigureAPIProtocolBypassesCandidateSourcePlanDurability(t *testing.T) {
	store := &compositionCountingIdempotencyStore{}
	platform := &platformServices{}
	if err := configureAPIProtocol(&capabilityRoutes{}, testAPIProtocolRuntime(), platform, &httpPolicy{}, runtimeAssemblyInputs{InstanceID: "target:test", DefaultEnvironment: "test"}, t.Context(), apiProtocolPersistence{
		Idempotency: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BypassDurableIdempotency: map[string]struct{}{deploymentmodule.PlanProjectCandidateSynchronizationOperationID: {}},
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:leapview-showcase/candidate-sync/plan", strings.NewReader(`{"artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifacts":[]}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "plan-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	platform.apiProtocol.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("candidate source plan bypass response = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.claims.Load() != 0 {
		t.Fatalf("candidate source plan bypass claimed durable idempotency %d times", store.claims.Load())
	}
}

func TestConfigureAPIProtocolReclaimsOnlyConfiguredCommand(t *testing.T) {
	store := &compositionCountingIdempotencyStore{}
	platform := &platformServices{}
	if err := configureAPIProtocol(&capabilityRoutes{}, testAPIProtocolRuntime(), platform, &httpPolicy{}, runtimeAssemblyInputs{InstanceID: "target:test", DefaultEnvironment: "test"}, t.Context(), apiProtocolPersistence{
		Idempotency: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
		ReclaimExpiredIdempotency: map[string]struct{}{"retainProjectCandidateSource": {}},
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project/candidate-sync/source", strings.NewReader(`{"candidateKey":"candidate","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifacts":[]}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "source-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	platform.apiProtocol.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("configured reclaim response = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.reclaims.Load() != 1 || store.claims.Load() != 0 {
		t.Fatalf("configured reclaim calls = %d ordinary claims = %d", store.reclaims.Load(), store.claims.Load())
	}
}

func testAPIProtocolRuntime() *runtimeServices {
	return &runtimeServices{projectIDResolver: func(context.Context) (projectgraph.ResourceID, error) { return "project", nil }}
}
