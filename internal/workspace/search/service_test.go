package search

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
)

type testCursorSigner struct{}

func (testCursorSigner) Sign(prefix string, payload []byte) string {
	return cursorsigning.Sign(prefix, payload)
}

func (testCursorSigner) Verify(prefix, token string) ([]byte, error) {
	return cursorsigning.Verify(prefix, token)
}

type fakeRepository struct {
	snapshot   string
	candidates []Candidate
}

func (r *fakeRepository) Snapshot(context.Context, RepositoryQuery) (string, error) {
	return r.snapshot, nil
}

func (r *fakeRepository) Candidates(_ context.Context, query RepositoryQuery, offset, limit int) ([]Candidate, bool, error) {
	if offset >= len(r.candidates) {
		return []Candidate{}, false, nil
	}
	end := offset + limit
	if end > len(r.candidates) {
		end = len(r.candidates)
	}
	return append([]Candidate(nil), r.candidates[offset:end]...), end < len(r.candidates), nil
}

func (r *fakeRepository) Resolve(_ context.Context, _ string, references []Reference) ([]Candidate, error) {
	out := make([]Candidate, 0, len(references))
	for _, reference := range references {
		for _, candidate := range r.candidates {
			if candidate.Result.Reference == reference {
				out = append(out, candidate)
			}
		}
	}
	return out, nil
}

type fakeAuthorizer struct{ denied map[string]struct{} }

func (a fakeAuthorizer) CanView(_ context.Context, _ Subject, object access.ObjectRef) (bool, error) {
	_, denied := a.denied[object.CanonicalID()]
	return !denied, nil
}

func TestServiceSearchScansPastDeniedCandidates(t *testing.T) {
	repository := &fakeRepository{snapshot: "snapshot-1", candidates: []Candidate{
		searchCandidate("denied", "Denied"),
		searchCandidate("orders", "Orders"),
	}}
	service := NewService(repository, fakeAuthorizer{denied: map[string]struct{}{
		access.WorkspaceObject("denied").CanonicalID(): {},
	}}, testCursorSigner{})

	page, err := service.Search(context.Background(), Subject{ID: "principal-1"}, Query{Text: "orders", Environment: "dev", Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Reference.WorkspaceID != "orders" {
		t.Fatalf("search items = %#v, want authorized orders result", page.Items)
	}
}

func TestNormalizeQueryTreatsObjectTypeNamesAsImplicitFilters(t *testing.T) {
	tests := map[string]struct {
		query     string
		wantText  string
		wantTypes []Type
	}{
		"singular":         {query: "dashboard", wantTypes: []Type{TypeDashboard}},
		"plural":           {query: "dashboards", wantTypes: []Type{TypeDashboard}},
		"with search text": {query: "executive dashboard", wantText: "executive", wantTypes: []Type{TypeDashboard}},
		"multiword type":   {query: "revenue semantic models", wantText: "revenue", wantTypes: []Type{TypeSemanticModel}},
		"multiple types":   {query: "visuals dashboards", wantTypes: []Type{TypeDashboard, TypeVisual}},
		"type prefix":      {query: "dashboar", wantTypes: []Type{TypeDashboard}},
		"short prefix":     {query: "dash", wantTypes: []Type{TypeDashboard}},
		"text and prefix":  {query: "state dashboar", wantText: "state", wantTypes: []Type{TypeDashboard}},
		"multiword prefix": {query: "semantic mod", wantTypes: []Type{TypeSemanticModel}},
		"ambiguous prefix": {query: "semantic", wantTypes: []Type{TypeSemanticModel, TypeSemanticTable}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeQuery(Subject{}, Query{Text: test.query, Environment: "dev"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != test.wantText {
				t.Fatalf("normalized text = %q, want %q", got.Text, test.wantText)
			}
			if len(got.Types) != len(test.wantTypes) {
				t.Fatalf("normalized types = %#v, want %#v", got.Types, test.wantTypes)
			}
			for index := range test.wantTypes {
				if got.Types[index] != test.wantTypes[index] {
					t.Fatalf("normalized types = %#v, want %#v", got.Types, test.wantTypes)
				}
			}
		})
	}
}

func TestNormalizeQueryKeepsTextualTypeTermsWhenExplicitFiltersArePresent(t *testing.T) {
	got, err := normalizeQuery(Subject{}, Query{Text: "dashboard", Types: []Type{TypeMeasure}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "dashboard" || len(got.Types) != 1 || got.Types[0] != TypeMeasure {
		t.Fatalf("normalized query = %#v", got)
	}
}

func TestNormalizeQueryRemovesRedundantTypeTermsMatchingExplicitFilters(t *testing.T) {
	got, err := normalizeQuery(Subject{}, Query{Text: "executive dashboard", Types: []Type{TypeDashboard}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "executive" || !slices.Equal(got.Types, []Type{TypeDashboard}) {
		t.Fatalf("normalized query = %#v, want executive dashboard search", got)
	}
}

func TestNormalizeQueryConstrainsResultsToAllowedTypes(t *testing.T) {
	tests := map[string]struct {
		query     Query
		wantText  string
		wantTypes []Type
		noTypes   bool
	}{
		"untyped query uses allowlist": {
			query:    Query{Text: "revenue", AllowedTypes: []Type{TypeVisual, TypeMeasure}},
			wantText: "revenue", wantTypes: []Type{TypeMeasure, TypeVisual},
		},
		"implicit type intersects allowlist": {
			query:     Query{Text: "dashboar", AllowedTypes: []Type{TypeVisual, TypeDashboard}},
			wantTypes: []Type{TypeDashboard},
		},
		"disallowed implicit type returns no candidates": {
			query:   Query{Text: "source", AllowedTypes: []Type{TypeVisual, TypeDashboard}},
			noTypes: true,
		},
		"explicit public filter keeps textual type semantics": {
			query:    Query{Text: "dashboard", Types: []Type{TypeMeasure}, AllowedTypes: []Type{TypeVisual, TypeMeasure}},
			wantText: "dashboard", wantTypes: []Type{TypeMeasure},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeQuery(Subject{}, test.query)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != test.wantText || got.NoTypes != test.noTypes || !slices.Equal(got.Types, test.wantTypes) {
				t.Fatalf("normalized query = %#v, want text=%q types=%#v noTypes=%v", got, test.wantText, test.wantTypes, test.noTypes)
			}
		})
	}
}

func TestServiceSearchDoesNotExposeCursorForDeniedRemainder(t *testing.T) {
	allowed := searchCandidate("sales", "Sales")
	denied := searchCandidate("secret", "Secret")
	service := NewService(&fakeRepository{snapshot: "snapshot", candidates: []Candidate{allowed, denied}}, fakeAuthorizer{denied: map[string]struct{}{
		denied.Object.CanonicalID(): {},
	}}, testCursorSigner{})
	page, err := service.Search(context.Background(), Subject{ID: "principal"}, Query{Environment: "dev", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("page = %#v, denied candidates must not create pagination metadata", page)
	}
}

func TestServiceCursorIsBoundToCallerAndQuery(t *testing.T) {
	repository := &fakeRepository{snapshot: "snapshot", candidates: []Candidate{searchCandidate("one", "One"), searchCandidate("two", "Two")}}
	service := NewService(repository, fakeAuthorizer{}, testCursorSigner{})
	first, err := service.Search(context.Background(), Subject{ID: "principal-1"}, Query{Text: "o", Environment: "dev", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		subject Subject
		query   Query
	}{
		"subject": {Subject{ID: "principal-2"}, Query{Text: "o", Environment: "dev", Limit: 1, Cursor: first.NextCursor}},
		"query":   {Subject{ID: "principal-1"}, Query{Text: "two", Environment: "dev", Limit: 1, Cursor: first.NextCursor}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Search(context.Background(), test.subject, test.query)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestServiceCursorRejectsChangedSearchSnapshot(t *testing.T) {
	repository := &fakeRepository{snapshot: "snapshot-1", candidates: []Candidate{
		searchCandidate("sales", "Sales"),
		searchCandidate("operations", "Operations"),
	}}
	service := NewService(repository, fakeAuthorizer{}, testCursorSigner{})
	first, err := service.Search(context.Background(), Subject{ID: "principal-1"}, Query{Environment: "dev", Limit: 1})
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("first search cursor is empty")
	}

	repository.snapshot = "snapshot-2"
	_, err = service.Search(context.Background(), Subject{ID: "principal-1"}, Query{Environment: "dev", Limit: 1, Cursor: first.NextCursor})
	if !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("continued search error = %v, want ErrSnapshotChanged", err)
	}
}

func TestServiceResolveReauthorizesCanonicalReferences(t *testing.T) {
	denied := searchCandidate("denied", "Denied")
	allowed := searchCandidate("sales", "Sales")
	service := NewService(&fakeRepository{snapshot: "snapshot-1", candidates: []Candidate{denied, allowed}}, fakeAuthorizer{denied: map[string]struct{}{
		denied.Object.CanonicalID(): {},
	}}, testCursorSigner{})

	results, err := service.Resolve(context.Background(), Subject{ID: "principal-1"}, "dev", []Reference{denied.Result.Reference, allowed.Result.Reference})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(results) != 1 || results[0].Reference != allowed.Result.Reference {
		t.Fatalf("resolved results = %#v, want only allowed reference", results)
	}
}

func TestServiceSearchRemovesUnauthorizedLocations(t *testing.T) {
	workspace := access.WorkspaceObject("sales")
	allowedDashboard := access.ItemObjectWithParent(access.SecurableDashboard, "sales", "allowed", workspace)
	deniedDashboard := access.ItemObjectWithParent(access.SecurableDashboard, "sales", "denied", workspace)
	candidate := Candidate{
		Result: Result{
			Reference: Reference{WorkspaceID: "sales", Type: TypeVisual, ID: "orders"},
			Name:      "Orders", Workspace: Workspace{ID: "sales", Name: "Sales"},
			Href: "/denied", Locations: []Location{{DashboardID: "denied", Href: "/denied"}, {DashboardID: "allowed", Href: "/allowed"}},
		},
		Object: workspace, LocationObjects: []access.ObjectRef{deniedDashboard, allowedDashboard}, RequireLocation: true,
	}
	service := NewService(&fakeRepository{snapshot: "snapshot", candidates: []Candidate{candidate}}, fakeAuthorizer{denied: map[string]struct{}{
		deniedDashboard.CanonicalID(): {},
	}}, testCursorSigner{})

	page, err := service.Search(context.Background(), Subject{ID: "principal"}, Query{Environment: "dev", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || len(page.Items[0].Locations) != 1 || page.Items[0].Locations[0].DashboardID != "allowed" || page.Items[0].Href != "/allowed" {
		t.Fatalf("authorized result = %#v", page.Items)
	}
}

func searchCandidate(workspaceID, name string) Candidate {
	return Candidate{
		Result: Result{
			Reference: Reference{WorkspaceID: workspaceID, Type: TypeWorkspace, ID: workspaceID},
			Name:      name,
			Workspace: Workspace{ID: workspaceID, Name: name},
			Href:      "/workspaces/" + workspaceID,
		},
		Object: access.WorkspaceObject(workspaceID),
	}
}
