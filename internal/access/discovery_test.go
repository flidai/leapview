package access

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type discoveryCandidate struct {
	ID      string
	Facet   string
	Suggest string
	Preview string
}

func TestAuthorizeAndPaginateFiltersBeforeTotalsAndDerivedProjections(t *testing.T) {
	candidates := []discoveryCandidate{
		{ID: "allowed-a", Facet: "sales", Suggest: "Sales", Preview: "safe-a"},
		{ID: "denied-secret", Facet: "finance", Suggest: "Secret", Preview: "secret-row"},
		{ID: "allowed-b", Facet: "sales", Suggest: "Orders", Preview: "safe-b"},
	}
	authorize := func(_ context.Context, candidate discoveryCandidate) (bool, error) {
		return !strings.HasPrefix(candidate.ID, "denied-"), nil
	}
	page, err := AuthorizeAndPaginate(t.Context(), candidates, DiscoveryRequest{
		Collection: "catalog.search", PrincipalID: "principal-a", SecurityContext: "auth-a", Snapshot: "snapshot-a",
		Query: "sales", Filter: "all", Limit: 1,
	}, authorize)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != "allowed-a" || page.NextCursor == "" {
		t.Fatalf("page = %#v, want one authorized item, total 2, and continuation", page)
	}
	for _, item := range page.Items {
		if item.Facet == "finance" || item.Suggest == "Secret" || item.Preview == "secret-row" {
			t.Fatalf("denied candidate leaked into page projection: %#v", item)
		}
	}

	// The values used for facets, autocomplete, and previews are intentionally
	// derived from the authorized page; the helper's Total is likewise computed
	// only after the authorizer has run.
	facets := map[string]bool{}
	suggestions := map[string]bool{}
	previews := map[string]bool{}
	for _, item := range page.Items {
		facets[item.Facet], suggestions[item.Suggest], previews[item.Preview] = true, true, true
	}
	if facets["finance"] || suggestions["Secret"] || previews["secret-row"] {
		t.Fatalf("denied candidate leaked into derived projections: facets=%v suggestions=%v previews=%v", facets, suggestions, previews)
	}
}

func TestAuthorizeAndPaginateReauthorizesContinuationAndBindsCursor(t *testing.T) {
	candidates := []string{"a", "b", "c"}
	denied := map[string]bool{}
	calls := 0
	authorize := func(_ context.Context, candidate string) (bool, error) {
		calls++
		return !denied[candidate], nil
	}
	request := DiscoveryRequest{
		Collection: "catalog.list", PrincipalID: "principal-a", SecurityContext: "auth-a", Snapshot: "snapshot-a",
		Filter: "kind=model", Limit: 1,
	}
	first, err := AuthorizeAndPaginate(t.Context(), candidates, request, authorize)
	if err != nil || first.NextCursor == "" || first.Items[0] != "a" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	denied["b"] = true
	secondRequest := request
	secondRequest.Cursor = first.NextCursor
	second, err := AuthorizeAndPaginate(t.Context(), candidates, secondRequest, authorize)
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 2 || len(second.Items) != 1 || second.Items[0] != "c" {
		t.Fatalf("second page = %#v, want c after reauthorization removed b", second)
	}
	if calls != len(candidates)*2 {
		t.Fatalf("authorizer calls = %d, want %d (all candidates on both pages)", calls, len(candidates)*2)
	}

	otherPrincipal := request
	otherPrincipal.PrincipalID = "principal-b"
	otherPrincipal.Cursor = first.NextCursor
	if _, err := AuthorizeAndPaginate(t.Context(), candidates, otherPrincipal, authorize); !errors.Is(err, ErrDiscoveryContextChanged) {
		t.Fatalf("cross-principal cursor error = %v, want context changed", err)
	}
	otherQuery := request
	otherQuery.Query = "different"
	otherQuery.Cursor = first.NextCursor
	if _, err := AuthorizeAndPaginate(t.Context(), candidates, otherQuery, authorize); !errors.Is(err, ErrInvalidDiscoveryCursor) {
		t.Fatalf("cross-query cursor error = %v, want invalid cursor", err)
	}
	if _, err := AuthorizeAndPaginate(t.Context(), candidates, DiscoveryRequest{
		Collection: "catalog.list", PrincipalID: "principal-a", SecurityContext: "auth-a", Snapshot: "snapshot-a", Filter: "kind=model", Limit: 1, Cursor: first.NextCursor + "x",
	}, authorize); !errors.Is(err, ErrInvalidDiscoveryCursor) {
		t.Fatalf("tampered cursor error = %v, want invalid cursor", err)
	}
}

func TestAuthorizeAndPaginateFailsClosedWithoutAuthorizer(t *testing.T) {
	_, err := AuthorizeAndPaginate(t.Context(), []string{"a"}, DiscoveryRequest{
		Collection: "catalog.list", PrincipalID: "principal-a", SecurityContext: "auth-a", Limit: 1,
	}, nil)
	if !errors.Is(err, ErrDiscoveryAuthorizationUnavailable) {
		t.Fatalf("nil authorizer error = %v, want unavailable", err)
	}
}
