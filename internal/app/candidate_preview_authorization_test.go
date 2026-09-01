package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

func TestProtectCandidateProjectResourcesAllowsFreshTargetPreview(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	runtime := &candidatePreviewRuntimeFake{
		project:   projectID,
		activeErr: servingstate.ErrNotFound,
	}
	accessValue := candidatePreviewAccessFake{
		principal: accessmodule.Principal{ID: "owner"},
		ok:        true,
		admin:     true,
	}
	called := false
	protected := protectCandidateProjectResources(
		accessValue,
		runtime,
		access.CapabilityProjectAdmin,
		activeProjectResource,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}),
	)

	request := httptest.NewRequest(http.MethodGet, "/candidates/candidate_1", nil)
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("fresh candidate preview status=%d called=%t, want 204/true", response.Code, called)
	}
	if runtime.acquireCalls != 0 {
		t.Fatalf("fresh candidate preview acquired active runtime %d times", runtime.acquireCalls)
	}
}

func TestProtectCandidateProjectResourcesRequiresAuthenticationOnFreshTarget(t *testing.T) {
	runtime := &candidatePreviewRuntimeFake{
		project:   projectgraph.ResourceID("project_demo"),
		activeErr: servingstate.ErrNotFound,
	}
	protected := protectCandidateProjectResources(
		candidatePreviewAccessFake{},
		runtime,
		access.CapabilityProjectAdmin,
		activeProjectResource,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("unauthenticated candidate preview reached handler")
		}),
	)

	response := httptest.NewRecorder()
	protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/candidates/candidate_1", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("fresh candidate preview unauthenticated status=%d, want 401", response.Code)
	}
}

func TestProtectCandidateProjectResourcesDeniesFreshTargetNonAdmin(t *testing.T) {
	runtime := &candidatePreviewRuntimeFake{
		project:   projectgraph.ResourceID("project_demo"),
		activeErr: servingstate.ErrNotFound,
	}
	accessValue := candidatePreviewAccessFake{
		principal: accessmodule.Principal{ID: "viewer"},
		ok:        true,
		admin:     false,
	}
	protected := protectCandidateProjectResources(
		accessValue,
		runtime,
		access.CapabilityProjectAdmin,
		activeProjectResource,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("fresh non-admin candidate preview reached handler")
		}),
	)

	response := httptest.NewRecorder()
	protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/candidates/candidate_1", nil))

	if response.Code != http.StatusForbidden {
		t.Fatalf("fresh non-admin candidate preview status=%d, want 403", response.Code)
	}
}

func TestProtectCandidateProjectResourcesFailsClosedWhenFreshAdminLookupUnavailable(t *testing.T) {
	runtime := &candidatePreviewRuntimeFake{
		project:   projectgraph.ResourceID("project_demo"),
		activeErr: servingstate.ErrNotFound,
	}
	accessValue := candidatePreviewAccessFake{
		principal: accessmodule.Principal{ID: "owner"},
		ok:        true,
		adminErr:  errors.New("platform admin store unavailable"),
	}
	protected := protectCandidateProjectResources(
		accessValue,
		runtime,
		access.CapabilityProjectAdmin,
		activeProjectResource,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("fresh candidate preview reached handler after admin lookup failure")
		}),
	)

	response := httptest.NewRecorder()
	protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/candidates/candidate_1", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh admin lookup failure status=%d, want 503", response.Code)
	}
}

func TestProtectCandidateProjectResourcesPreservesActiveProjectDenial(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	snapshot := tusSnapshot(t, "viewer", "connection_sales", false)
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &candidatePreviewRuntimeFake{
		project: projectID,
		state:   servingstate.State{ID: servingstate.ID(identity.GenerationID)},
		lease: &tusLease{
			identity: identity,
			snapshot: snapshot,
		},
	}
	accessValue := candidatePreviewAccessFake{
		principal: accessmodule.Principal{ID: "viewer"},
		ok:        true,
	}
	protected := protectCandidateProjectResources(
		accessValue,
		runtime,
		access.CapabilityProjectAdmin,
		activeProjectResource,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("active unauthorized candidate preview reached handler")
		}),
	)

	response := httptest.NewRecorder()
	protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/candidates/candidate_1", nil))

	if response.Code != http.StatusForbidden {
		t.Fatalf("active unauthorized candidate preview status=%d, want 403", response.Code)
	}
	if runtime.acquireCalls != 1 {
		t.Fatalf("active candidate preview acquired runtime %d times, want 1", runtime.acquireCalls)
	}
}

func TestCandidatePreviewServingGenerationActiveFailsClosedOnLookupError(t *testing.T) {
	runtime := &candidatePreviewRuntimeFake{
		project:   projectgraph.ResourceID("project_demo"),
		activeErr: errors.New("serving state store unavailable"),
	}
	if _, err := candidatePreviewServingGenerationActive(context.Background(), runtime); err == nil {
		t.Fatal("active serving-state lookup unexpectedly succeeded")
	}
}

type candidatePreviewRuntimeFake struct {
	project      projectgraph.ResourceID
	state        servingstate.State
	activeErr    error
	lease        runtimehost.Lease
	acquireCalls int
}

func (f *candidatePreviewRuntimeFake) ProjectID() projectgraph.ResourceID { return f.project }

func (f *candidatePreviewRuntimeFake) ActiveArtifact(context.Context) (servingstate.State, servingstate.Artifact, error) {
	if f.activeErr != nil {
		return servingstate.State{}, servingstate.Artifact{}, f.activeErr
	}
	return f.state, servingstate.Artifact{}, nil
}

func (f *candidatePreviewRuntimeFake) Acquire(context.Context) (runtimehost.Lease, error) {
	f.acquireCalls++
	if f.lease == nil {
		return nil, errors.New("candidate preview runtime lease unavailable")
	}
	return f.lease, nil
}

var _ canonicalRuntimeHost = (*candidatePreviewRuntimeFake)(nil)
var _ candidatePreviewServingStateReader = (*candidatePreviewRuntimeFake)(nil)

type candidatePreviewAccessFake struct {
	principal accessmodule.Principal
	ok        bool
	admin     bool
	adminErr  error
}

func (a candidatePreviewAccessFake) Authenticate(next http.Handler) http.Handler { return next }

func (a candidatePreviewAccessFake) CurrentPrincipal(*http.Request) (accessmodule.Principal, bool) {
	return a.principal, a.ok
}

func (a candidatePreviewAccessFake) AuthorizationSubjects(context.Context, string) ([]access.SubjectRef, error) {
	if !a.ok {
		return nil, nil
	}
	return []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: a.principal.ID}}, nil
}

func (a candidatePreviewAccessFake) IsPlatformAdmin(context.Context, string) (bool, error) {
	return a.admin, a.adminErr
}

var _ canonicalAccessModule = candidatePreviewAccessFake{}
