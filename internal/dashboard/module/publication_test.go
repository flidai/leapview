package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type publicationRepositoryStub struct {
	row       publication.Publication
	mutations int
}

func (s *publicationRepositoryStub) Get(context.Context, projectgraph.ResourceID, string) (publication.Publication, error) {
	return s.row, nil
}
func (s *publicationRepositoryStub) GetByPublicID(context.Context, string) (publication.Publication, error) {
	return s.row, nil
}
func (s *publicationRepositoryStub) List(context.Context, projectgraph.ResourceID) ([]publication.Publication, error) {
	return []publication.Publication{s.row}, nil
}
func (s *publicationRepositoryStub) ListAll(context.Context) ([]publication.Publication, error) {
	return []publication.Publication{s.row}, nil
}
func (s *publicationRepositoryStub) ListEvents(context.Context, string) ([]publication.Event, error) {
	return nil, nil
}
func (s *publicationRepositoryStub) Suspend(context.Context, projectgraph.ResourceID, string, string, int64) (publication.Publication, error) {
	s.mutations++
	return publication.Publication{}, errors.New("unexpected mutation")
}
func (s *publicationRepositoryStub) Resume(context.Context, projectgraph.ResourceID, string, string, int64) (publication.Publication, error) {
	s.mutations++
	return publication.Publication{}, errors.New("unexpected mutation")
}
func (s *publicationRepositoryStub) Rotate(context.Context, projectgraph.ResourceID, string, string, int64) (publication.Publication, error) {
	s.mutations++
	return publication.Publication{}, errors.New("unexpected mutation")
}

func TestPublicationExecutionContextUsesPublicationPrincipal(t *testing.T) {
	row := publication.Publication{
		ProjectID: "project_1",
		Name:      "website-showcase",
		Dashboard: "visual-showcase",
	}

	metadata := dataquery.MetadataFromContext(PublicationExecutionContext(context.Background(), row, ""))
	want := "dashboard_publication:project_1.website-showcase"
	if metadata.PrincipalID != want {
		t.Fatalf("public principal id = %q, want %q", metadata.PrincipalID, want)
	}
	if metadata.Surface != dataquery.SurfacePublicDashboard || metadata.ObjectType != "dashboard_publication" || metadata.ObjectID != "website-showcase" {
		t.Fatalf("public metadata = %#v", metadata)
	}
}

func TestCanonicalPublicationResourceIDPreservesSemanticModelDependency(t *testing.T) {
	resource := canonicalPublicationResourceID("semantic-model:visuals")
	if err := resource.Validate(); err != nil {
		t.Fatalf("semantic model dependency is invalid: %v", err)
	}
	if resource.CanonicalID() != "semantic-model:visuals" || resource.Kind() != projectgraph.KindSemanticModel {
		t.Fatalf("semantic model dependency = %q/%q", resource.CanonicalID(), resource.Kind())
	}
}

func TestEmbedWithNoAllowedOriginsDeniesFraming(t *testing.T) {
	header := http.Header{}
	SetPublicDashboardSecurityHeaders(header, "embed", nil)
	if got := header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want omitted", got)
	}
	if got := header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	policy := header.Get("Content-Security-Policy")
	for _, want := range []string{
		"script-src 'self' 'unsafe-eval'",
		"script-src-attr 'none'",
		"style-src 'self'",
		"style-src-elem 'self' 'unsafe-inline'",
		"style-src-attr 'unsafe-inline'",
	} {
		if !strings.Contains(policy, want) {
			t.Fatalf("Content-Security-Policy missing %q: %q", want, policy)
		}
	}
}

func TestMutateDashboardPublicationRejectsStaleIfMatchBeforeMutation(t *testing.T) {
	repository := &publicationRepositoryStub{row: publication.Publication{ID: "018f4f2e-0000-7000-0000-000000000701", ProjectID: "sales", Name: "executive", Dashboard: "dashboard:executive", Revision: 2, Configured: true, ServingStateID: "generation-2"}}
	m := &Module{
		publications: repository, publicationService: publication.NewService(repository, nil), publicationAuditConfigured: true,
		currentActor: func(*http.Request) string { return "actor" },
		handler:      dashboardhttp.Handler{CurrentPrincipalID: func(*http.Request) string { return "actor" }, AuthorizeListResource: func(context.Context, string, access.ResourceRef, access.Capability) (bool, error) { return true, nil }},
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("If-Match", `"1"`)
	r.Header.Set("X-Request-ID", "stale-request")
	r.Header.Set("Idempotency-Key", "018f4f2e-0000-7000-0000-000000000702")
	invocation := publication.CommandInvocation{Surface: "api", RequestID: "stale-request", CorrelationID: "stale-request", IdempotencyKey: "018f4f2e-0000-7000-0000-000000000702", ExpectedRevision: 1}
	ctx, err := beginGeneratedPublicationInvocation(r.Context(), publication.ActionSuspend, "sales", invocation)
	if err != nil {
		t.Fatal(err)
	}
	r = r.WithContext(ctx)
	response := httptest.NewRecorder()
	m.SuspendDashboardPublication(response, r, "sales", "executive")
	if response.Code != http.StatusPreconditionFailed || !strings.Contains(response.Body.String(), "PUBLICATION_PRECONDITION_FAILED") {
		t.Fatalf("stale API status = %d, body=%s", response.Code, response.Body.String())
	}
	if repository.mutations != 0 {
		t.Fatalf("stale API invoked publication mutation %d times", repository.mutations)
	}
}

func TestMutateDashboardPublicationRejectsNonUUIDv7IdempotencyKey(t *testing.T) {
	repository := &publicationRepositoryStub{row: publication.Publication{ID: "018f4f2e-0000-7000-0000-000000000721", ProjectID: "sales", Name: "executive", Dashboard: "dashboard:executive", Revision: 1, Configured: true, ServingStateID: "generation-1"}}
	m := &Module{
		publications: repository, publicationService: publication.NewService(repository, nil), publicationAuditConfigured: true,
		currentActor: func(*http.Request) string { return "actor" },
		handler:      dashboardhttp.Handler{CurrentPrincipalID: func(*http.Request) string { return "actor" }, AuthorizeListResource: func(context.Context, string, access.ResourceRef, access.Capability) (bool, error) { return true, nil }},
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("If-Match", `"1"`)
	r.Header.Set("Idempotency-Key", "publication-command")
	response := httptest.NewRecorder()
	m.SuspendDashboardPublication(response, r, "sales", "executive")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("invalid idempotency status = %d, body=%s", response.Code, response.Body.String())
	}
	if repository.mutations != 0 {
		t.Fatalf("invalid idempotency key invoked publication mutation %d times", repository.mutations)
	}
}

func TestMutatePublicationWithInvocationRejectsStaleRevisionBeforeMutation(t *testing.T) {
	repository := &publicationRepositoryStub{row: publication.Publication{ID: "018f4f2e-0000-7000-0000-000000000711", ProjectID: "sales", Name: "executive", Revision: 2, Configured: true, ServingStateID: "generation-2"}}
	m := &Module{publications: repository, publicationService: publication.NewService(repository, nil), publicationAuditConfigured: true}
	invocation := publication.CommandInvocation{Surface: "ui", RequestID: "ui-stale-request", CorrelationID: "ui-stale-request", IdempotencyKey: "018f4f2e-0000-7000-0000-000000000712", ExpectedRevision: 1}
	if _, err := m.MutatePublicationWithInvocation(context.Background(), "sales", "executive", "actor", publication.ActionSuspend, invocation); err == nil {
		t.Fatal("stale UI invocation unexpectedly succeeded")
	} else if kind, ok := apigenfailure.KindOf(err); !ok || kind != "precondition" {
		t.Fatalf("stale UI invocation error kind = %q (ok=%v), want precondition: %v", kind, ok, err)
	}
	if repository.mutations != 0 {
		t.Fatalf("stale UI invocation called publication mutation %d times", repository.mutations)
	}
}
