package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type testLease struct {
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l testLease) Release()                               {}
func (l testLease) Identity() projectgraph.ServingIdentity { return l.snapshot.Identity() }
func (l testLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

type testLeases struct{ lease Lease }

func (p testLeases) Acquire(context.Context) (Lease, error) { return p.lease, nil }

type testSubjects struct {
	byPrincipal map[string][]access.SubjectRef
}

func (s testSubjects) AuthorizationSubjects(_ context.Context, principalID string) ([]access.SubjectRef, error) {
	return append([]access.SubjectRef(nil), s.byPrincipal[principalID]...), nil
}

func catalogFixture(t *testing.T, grants []accesssnapshot.Grant) (*Service, projectgraph.ProjectGraph, access.SubjectRef, access.SubjectRef) {
	t.Helper()
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders", Metadata: projectgraph.Metadata{DisplayName: "Orders", Domain: "commerce"}},
		{ID: "dashboard_sales", Kind: projectgraph.KindDashboard, Name: "sales", Metadata: projectgraph.Metadata{DisplayName: "Sales dashboard", Domain: "commerce"}},
		{ID: "source_finance", Kind: projectgraph.KindSource, Name: "finance", Metadata: projectgraph.Metadata{Domain: "finance"}},
	}, []projectgraph.Edge{{From: "dashboard_sales", To: "model_orders"}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	group, _ := access.NewSubjectRef(access.SubjectKindGroup, "group_analytics")
	service, err := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{
		principal.ID: {principal, group},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return service, project, principal, group
}

func grant(t *testing.T, project projectgraph.ProjectGraph, id string, subject access.SubjectRef, resource projectgraph.ResourceID, kind projectgraph.Kind) accesssnapshot.Grant {
	t.Helper()
	ref, err := access.NewResourceRef(resource, kind)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := access.NewCanonicalGrant(project, subject, ref, access.CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	return accesssnapshot.Grant{ID: id, Canonical: canonical}
}

func TestDevelopmentBypassReturnsExactActiveGraphWithEmptyGrants(t *testing.T) {
	service, _, _, _ := catalogFixture(t, nil)
	ctx := context.Background()

	search, err := service.Search(ctx, SearchRequest{PrincipalID: "dev", DevAuthBypass: true, Query: "orders", Limit: 20})
	if err != nil || len(search.Items) != 1 || search.Items[0].Ref.ID != "model_orders" {
		t.Fatalf("development search = %#v, %v", search, err)
	}
	listed, err := service.List(ctx, ListRequest{PrincipalID: "dev", DevAuthBypass: true, Limit: 20})
	if err != nil || len(listed.Items) != 4 {
		t.Fatalf("development list = %#v, %v", listed, err)
	}
	resolved, err := service.Resolve(ctx, "dev", Ref{ID: "model_orders", Kind: projectgraph.KindModel}, access.CapabilityResourceRead, true)
	if err != nil || resolved.Ref.ID != "model_orders" {
		t.Fatalf("development resolve = %#v, %v", resolved, err)
	}
}

func TestSearchUsesDirectAndGroupGrantsAndDoesNotEnumerateDeniedResources(t *testing.T) {
	// Build the graph first so grants can be bound to its exact IDs.
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "model_secret", Kind: projectgraph.KindModel, Name: "secret"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	group, _ := access.NewSubjectRef(access.SubjectKindGroup, "group_analytics")
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant(t, project, "grant_group", group, "model_orders", projectgraph.KindModel)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal, group}}})
	page, err := service.Search(context.Background(), SearchRequest{PrincipalID: principal.ID, Query: "orders", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Ref.ID != "model_orders" {
		t.Fatalf("authorized search = %#v, want only group-granted model", page.Items)
	}
	if _, err := service.Resolve(context.Background(), principal.ID, Ref{ID: "model_secret", Kind: projectgraph.KindModel}, access.CapabilityResourceRead, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unauthorized resolve error = %v, want non-enumerating not found", err)
	}
}

func TestResolveRejectsUnknownAndWrongKindIDs(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant(t, project, "grant_direct", principal, "model_orders", projectgraph.KindModel)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal}}})
	for _, ref := range []Ref{{ID: "missing", Kind: projectgraph.KindModel}, {ID: "model_orders", Kind: projectgraph.KindSource}} {
		if _, err := service.Resolve(context.Background(), principal.ID, ref, access.CapabilityResourceRead, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve(%#v) error = %v, want not found", ref, err)
		}
	}
}

func TestResolveProjectRequiresProjectAdminCapability(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	ref, err := access.NewResourceRef(project.ProjectID(), projectgraph.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := access.NewCanonicalGrant(project, principal, ref, access.CapabilityProjectAdmin)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{{ID: "grant_admin", Canonical: canonical}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal}}})
	if _, err := service.Resolve(context.Background(), principal.ID, Ref{ID: project.ProjectID(), Kind: projectgraph.KindProject}, access.CapabilityProjectAdmin, false); err != nil {
		t.Fatalf("project admin resolve = %v", err)
	}
	if _, err := service.Resolve(context.Background(), principal.ID, Ref{ID: project.ProjectID(), Kind: projectgraph.KindProject}, access.CapabilityResourceRead, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project read resolve = %v, want not found", err)
	}
}

func TestDomainFilterDoesNotChangeResourceID(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders", Metadata: projectgraph.Metadata{Domain: "commerce"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant(t, project, "grant_direct", principal, "model_orders", projectgraph.KindModel)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal}}})
	first, err := service.Search(context.Background(), SearchRequest{PrincipalID: principal.ID, Query: "orders", Domain: "commerce"})
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("domain search = %#v, %v", first, err)
	}
	if first.Items[0].Ref.ID != "model_orders" {
		t.Fatalf("domain search ID = %q", first.Items[0].Ref.ID)
	}
	second, err := service.Search(context.Background(), SearchRequest{PrincipalID: principal.ID, Query: "orders", Domain: "other"})
	if err != nil || len(second.Items) != 0 {
		t.Fatalf("other domain search = %#v, %v", second, err)
	}
}

func TestSearchMatchesStableResourceID(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "unrelated"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant(t, project, "grant_direct", principal, "model_orders", projectgraph.KindModel)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal}}})
	page, err := service.Search(context.Background(), SearchRequest{PrincipalID: principal.ID, Query: "MODEL_ORDERS"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Ref.ID != "model_orders" {
		t.Fatalf("ID search = %#v, want model_orders", page.Items)
	}
}

func TestRootListScansDisconnectedResourcesWithoutProjectEdges(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "source_warehouse", Kind: projectgraph.KindSource, Name: "warehouse"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{
		grant(t, project, "grant_model", principal, "model_orders", projectgraph.KindModel),
		grant(t, project, "grant_source", principal, "source_warehouse", projectgraph.KindSource),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal}}})
	page, err := service.List(context.Background(), ListRequest{PrincipalID: principal.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("root list = %#v, want disconnected model and source", page.Items)
	}
}

func TestListCursorIsBoundToParent(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_a", Kind: projectgraph.KindModel, Name: "a"},
		{ID: "model_b", Kind: projectgraph.KindModel, Name: "b"},
		{ID: "source_a", Kind: projectgraph.KindSource, Name: "source_a"},
		{ID: "source_a2", Kind: projectgraph.KindSource, Name: "source_a2"},
		{ID: "source_b", Kind: projectgraph.KindSource, Name: "source_b"},
		{ID: "source_b2", Kind: projectgraph.KindSource, Name: "source_b2"},
	}, []projectgraph.Edge{{From: "model_a", To: "source_a"}, {From: "model_a", To: "source_a2"}, {From: "model_b", To: "source_b"}, {From: "model_b", To: "source_b2"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_1")
	identity, _ := projectgraph.NewServingIdentity(project.ProjectID(), "development", "generation_1")
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{
		grant(t, project, "grant_model_a", principal, "model_a", projectgraph.KindModel),
		grant(t, project, "grant_model_b", principal, "model_b", projectgraph.KindModel),
		grant(t, project, "grant_a", principal, "source_a", projectgraph.KindSource),
		grant(t, project, "grant_a2", principal, "source_a2", projectgraph.KindSource),
		grant(t, project, "grant_b", principal, "source_b", projectgraph.KindSource),
		grant(t, project, "grant_b2", principal, "source_b2", projectgraph.KindSource),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(testLeases{lease: testLease{snapshot: snapshot}}, testSubjects{byPrincipal: map[string][]access.SubjectRef{principal.ID: {principal}}})
	first, err := service.List(context.Background(), ListRequest{PrincipalID: principal.ID, Parent: &Ref{ID: "model_a", Kind: projectgraph.KindModel}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" {
		t.Fatal("expected a cursor for the first parent")
	}
	_, err = service.List(context.Background(), ListRequest{PrincipalID: principal.ID, Parent: &Ref{ID: "model_b", Kind: projectgraph.KindModel}, Limit: 1, Cursor: first.NextCursor})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cross-parent cursor error = %v, want invalid cursor", err)
	}
}

func TestSearchRejectsOversizedQuery(t *testing.T) {
	service, _, principal, _ := catalogFixture(t, nil)
	_, err := service.Search(context.Background(), SearchRequest{PrincipalID: principal.ID, Query: strings.Repeat("x", MaxQueryLength+1)})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized query error = %v, want invalid request", err)
	}
}

func TestListRejectsNegativeLimit(t *testing.T) {
	service, _, principal, _ := catalogFixture(t, nil)
	_, err := service.List(context.Background(), ListRequest{PrincipalID: principal.ID, Limit: -1})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("negative limit error = %v, want invalid request", err)
	}
}

func TestListRejectsOversizedCursor(t *testing.T) {
	service, _, principal, _ := catalogFixture(t, nil)
	_, err := service.List(context.Background(), ListRequest{PrincipalID: principal.ID, Cursor: strings.Repeat("x", MaxCursorLength+1)})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("oversized cursor error = %v, want invalid cursor", err)
	}
}

func TestSnapshotDigestFailsClosed(t *testing.T) {
	_, err := snapshotDigest(accesssnapshot.AuthorizationSnapshot{})
	if !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("invalid snapshot digest error = %v, want snapshot changed", err)
	}
}
