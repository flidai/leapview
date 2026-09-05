package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
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
	if err := configureAPIProtocol(&capabilityRoutes{}, &runtimeServices{}, platform, &httpPolicy{}, t.Context(), apiProtocolPersistence{
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
	if err := configureAPIProtocol(&capabilityRoutes{}, &runtimeServices{}, platform, &httpPolicy{}, t.Context(), apiProtocolPersistence{
		Idempotency: store, CursorSigning: cursorsigning.NewEphemeralInitializer(),
		BypassDurableIdempotency: map[string]struct{}{deploymentmodule.PlanProjectCandidateSynchronizationOperationID: {}},
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:leapview-showcase/candidate-sync/plan", strings.NewReader(`{"projectFile":"leapview.yaml","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifacts":[]}`))
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
	if err := configureAPIProtocol(&capabilityRoutes{}, &runtimeServices{}, platform, &httpPolicy{}, t.Context(), apiProtocolPersistence{
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
