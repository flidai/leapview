package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/go-chi/chi/v5"
)

type savedAuthorizationReadRepository struct {
	savedCompositionRepository
	lifecycle     saved.Lifecycle
	revision      saved.Revision
	revisionReads int
}

func (r *savedAuthorizationReadRepository) GetLifecycle(_ context.Context, request saved.ReadInput) (saved.Lifecycle, error) {
	if request.ProjectID != r.lifecycle.ProjectID || request.ID != r.lifecycle.ID {
		return saved.Lifecycle{}, saved.ErrNotFound
	}
	return r.lifecycle, nil
}
func (r *savedAuthorizationReadRepository) GetRevision(context.Context, saved.RevisionReadInput) (saved.Revision, error) {
	r.revisionReads++
	return r.revision, nil
}

type savedAuthorizationProvider struct{ lease *savedAdapterLease }

func (p savedAuthorizationProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	return p.lease, nil
}

// Exercise the generated transport and real lifecycle service together. The
// transport's authentication-only envelope must never expose a revision before
// the exact domain and semantic authorization checks have succeeded.
func TestSavedExplorationTransportRequiresDomainAndTypedSemanticRead(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	payload, err := saved.NewExplorationSpecPayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := saved.NewRevision("revision-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, actor                          string
		visibility                           saved.Visibility
		modelRead, tokenDenied, wrongProject bool
		want                                 int
	}{
		{name: "owner", actor: "owner", visibility: saved.VisibilityPrivate, modelRead: true, want: http.StatusOK},
		{name: "anonymous", visibility: saved.VisibilityPrivate, modelRead: true, want: http.StatusUnauthorized},
		{name: "private non-owner", actor: "reader", visibility: saved.VisibilityPrivate, modelRead: true, want: http.StatusNotFound},
		{name: "organization reader", actor: "reader", visibility: saved.VisibilityOrganization, modelRead: true, want: http.StatusOK},
		{name: "organization without semantic read", actor: "reader", visibility: saved.VisibilityOrganization, want: http.StatusNotFound},
		{name: "owner without semantic read", actor: "owner", visibility: saved.VisibilityPrivate, want: http.StatusNotFound},
		{name: "owner token ceiling", actor: "owner", visibility: saved.VisibilityPrivate, modelRead: true, tokenDenied: true, want: http.StatusNotFound},
		{name: "wrong bound project", actor: "owner", visibility: saved.VisibilityPrivate, modelRead: true, wrongProject: true, want: http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := savedAdapterLifecycle(identity, savedAdapterProject, "exploration-1", "owner", test.visibility, saved.StatusActive, "semantic:sales")
			lifecycle.CurrentRevision = revision.Metadata
			lifecycle.CreatedAt, lifecycle.UpdatedAt = now, now
			repository := &savedAuthorizationReadRepository{lifecycle: lifecycle, revision: revision}
			var grants []accesssnapshot.Grant
			if test.modelRead && test.actor != "" {
				grants = append(grants, mustSavedAdapterGrant(t, graph, "read", mustSavedAdapterSubject(t, access.SubjectKindPrincipal, test.actor), access.CapabilityResourceRead))
			}
			snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, grants)
			authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
			if err != nil {
				t.Fatal(err)
			}
			service, err := savedapplication.NewService(savedapplication.Options{Repository: repository, Authorizer: authorizer, Runtime: savedAuthorizationProvider{lease: &savedAdapterLease{runtime: savedAdapterRuntime{identity: identity}, identity: identity, snapshot: snapshot}}, Now: func() time.Time { return now }, NewRevisionID: func() (saved.RevisionID, error) { return "revision-unused", nil }})
			if err != nil {
				t.Fatal(err)
			}
			config := analyticsmodule.AnalyticsAPIGenConfig{SavedExplorations: analyticsmodule.SavedExplorationAPIGenConfig{Service: service, CurrentPrincipal: func(r *http.Request) (string, bool) {
				principal, ok := accessmodule.PrincipalFromContext(r.Context())
				return principal.ID, ok
			}}}
			router := chi.NewRouter()
			router.Get("/api/v1/projects/{project}/saved-explorations/{exploration}", func(w http.ResponseWriter, r *http.Request) {
				if !analyticsmodule.DispatchAPIGenOperation(config, "getSavedExploration", nil, w, r) {
					t.Fatal("generated route missing")
				}
			})
			project := savedAdapterProject.String()
			if test.wrongProject {
				project = "project:other"
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+project+"/saved-explorations/exploration-1", nil)
			if test.actor != "" {
				request = request.WithContext(savedAdapterContext(test.actor))
			}
			if test.tokenDenied {
				request = request.WithContext(accessmodule.WithAPICredential(request.Context(), access.APICredential{Principal: access.Principal{ID: test.actor}, Token: access.APIToken{ID: "token", PrincipalID: test.actor, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{}}}))
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.want, recorder.Body.String())
			}
			if test.want != http.StatusOK && repository.revisionReads != 0 {
				t.Fatalf("denied request read %d revisions", repository.revisionReads)
			}
		})
	}
}
