package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

func TestServeCandidatePreviewDelegatesOpaqueScopeToDeploymentModule(t *testing.T) {
	handler := &candidatePreviewHandlerStub{}
	request := httptest.NewRequest(http.MethodGet, "/candidates/cand_opaque", nil)
	response := httptest.NewRecorder()

	serveCandidatePreview(handler, "cand_opaque", "principal_1", nil, response, request)

	if !handler.called || handler.candidateID != "cand_opaque" || handler.principalID != "principal_1" {
		t.Fatalf("candidate preview delegation = %#v", handler)
	}
}

func TestServeCandidatePreviewRejectsIncompleteScope(t *testing.T) {
	handler := &candidatePreviewHandlerStub{}
	request := httptest.NewRequest(http.MethodGet, "/candidates/", nil)
	response := httptest.NewRecorder()

	serveCandidatePreview(handler, "", "principal_1", nil, response, request)

	if response.Code != http.StatusNotFound || handler.called {
		t.Fatalf("incomplete preview status=%d delegated=%t", response.Code, handler.called)
	}
}

func TestServeCandidateReviewRejectsIncompleteScope(t *testing.T) {
	handler := &candidateReviewHandlerStub{}
	request := httptest.NewRequest(http.MethodGet, "/candidates/", nil)
	response := httptest.NewRecorder()
	serveCandidateReview(handler, "", "", nil, response, request)
	if response.Code != http.StatusNotFound || handler.called {
		t.Fatalf("incomplete review status=%d delegated=%t", response.Code, handler.called)
	}
}

func TestResolveCandidateRuntimeKeepsLiveRuntime(t *testing.T) {
	view := runtimehostmodule.OwnedCandidateView{CandidateID: "candidate"}
	host := &candidateRuntimeHostStub{views: []candidateRuntimeHostResult{{view: view}}}
	prep := &candidateNativeRuntimePreparerStub{}

	got, err := resolveCandidateRuntimeWith(host, prep, context.Background(), "candidate", "owner")
	if err != nil || got.CandidateID != view.CandidateID {
		t.Fatalf("view=%#v err=%v", got, err)
	}
	if host.calls != 1 || prep.calls != 0 {
		t.Fatalf("live runtime calls: resolve=%d ensure=%d, want 1/0", host.calls, prep.calls)
	}
}

func TestResolveCandidateRuntimePreparesMissingOrExpiredRuntimeOnce(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "missing", err: runtimehostmodule.ErrCandidateRuntimeNotFound},
		{name: "expired", err: runtimehostmodule.ErrCandidateRuntimeExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := runtimehostmodule.OwnedCandidateView{CandidateID: "candidate"}
			host := &candidateRuntimeHostStub{views: []candidateRuntimeHostResult{{err: test.err}, {view: view}}}
			prep := &candidateNativeRuntimePreparerStub{}
			ctx := context.WithValue(context.Background(), candidateRouteContextKey("request"), "value")

			got, err := resolveCandidateRuntimeWith(host, prep, ctx, "candidate", "owner")
			if err != nil || got.CandidateID != view.CandidateID {
				t.Fatalf("view=%#v err=%v", got, err)
			}
			if host.calls != 2 || prep.calls != 1 {
				t.Fatalf("runtime calls: resolve=%d ensure=%d, want 2/1", host.calls, prep.calls)
			}
			if prep.ctx == nil || prep.ctx.Value(candidateRouteContextKey("request")) != "value" {
				t.Fatalf("ensure context did not preserve request context")
			}
		})
	}
}

func TestResolveCandidateRuntimeDoesNotPrepareOtherErrors(t *testing.T) {
	other := errors.New("runtime registry unavailable")
	host := &candidateRuntimeHostStub{views: []candidateRuntimeHostResult{{err: other}}}
	prep := &candidateNativeRuntimePreparerStub{}

	_, err := resolveCandidateRuntimeWith(host, prep, context.Background(), "candidate", "owner")
	if !errors.Is(err, other) {
		t.Fatalf("err=%v, want %v", err, other)
	}
	if host.calls != 1 || prep.calls != 0 {
		t.Fatalf("other error calls: resolve=%d ensure=%d, want 1/0", host.calls, prep.calls)
	}
}

type candidateRouteContextKey string

type candidateRuntimeHostResult struct {
	view runtimehostmodule.OwnedCandidateView
	err  error
}

type candidateRuntimeHostStub struct {
	views []candidateRuntimeHostResult
	calls int
}

func (stub *candidateRuntimeHostStub) ResolveOwnedCandidate(string, string) (runtimehostmodule.OwnedCandidateView, error) {
	index := stub.calls
	stub.calls++
	if index >= len(stub.views) {
		return runtimehostmodule.OwnedCandidateView{}, runtimehostmodule.ErrCandidateRuntimeNotFound
	}
	return stub.views[index].view, stub.views[index].err
}

type candidateNativeRuntimePreparerStub struct {
	calls int
	ctx   context.Context
}

func (stub *candidateNativeRuntimePreparerStub) EnsureNativeCandidateRuntime(ctx context.Context, _, _ string) error {
	stub.calls++
	stub.ctx = ctx
	return nil
}

type candidatePreviewHandlerStub struct {
	called                   bool
	candidateID, principalID string
}

type candidateReviewHandlerStub struct{ called bool }

func (stub *candidateReviewHandlerStub) ServeCandidateReview(
	_ http.ResponseWriter,
	_ *http.Request,
	_ string,
	_ projectgraph.ResourceID,
	_ webpage.Provider,
) {
	stub.called = true
}

func (stub *candidatePreviewHandlerStub) ServeCandidatePreview(
	_ http.ResponseWriter,
	_ *http.Request,
	candidateID, principalID string,
	_ webpage.Provider,
) {
	stub.called = true
	stub.candidateID = candidateID
	stub.principalID = principalID
}
