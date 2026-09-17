package http

import (
	"testing"

	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBindQueryHistoryFiltersDoesNotReintroduceActiveProject(t *testing.T) {
	filters, err := bindQueryHistoryFilters(uisignals.AdminQueryHistoryFilters{}, projectgraph.ResourceID("project:active"))
	if err != nil {
		t.Fatal(err)
	}
	if filters.Projects != nil && len(*filters.Projects) != 0 {
		t.Fatalf("empty filters acquired an implicit project: %#v", filters.Projects)
	}

	stale := uisignals.AdminQueryHistoryFilters{Projects: uisignals.OptionalSlice([]string{"project:stale"})}
	cleared, err := bindQueryHistoryFilters(uisignals.AdminQueryHistoryFilters{}, projectgraph.ResourceID("project:active"))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Projects != nil && len(*cleared.Projects) != 0 {
		t.Fatalf("clear-all filters retained stale project: %#v", cleared.Projects)
	}
	bound, err := bindQueryHistoryFilters(stale, projectgraph.ResourceID("project:active"))
	if err != nil {
		t.Fatalf("stale project binding failed: %v", err)
	}
	if bound.Projects != nil && len(*bound.Projects) != 0 {
		t.Fatalf("stale project trapped history in a scoped result: %#v", bound.Projects)
	}
	bound, err = bindQueryHistoryFilters(stale, "")
	if err != nil {
		t.Fatalf("stale project binding without active project failed: %v", err)
	}
	if bound.Projects != nil && len(*bound.Projects) != 0 {
		t.Fatalf("stale project survived without an active project: %#v", bound.Projects)
	}
}

func TestNormalizeQueryHistoryCommandAcceptsClearAll(t *testing.T) {
	command := normalizeQueryHistoryCommand(uisignals.AdminQueryHistoryCommand{Action: "clear_all", PageToken: uisignals.Optional("stale-cursor")})
	if command.Action != "clear_all" {
		t.Fatalf("action = %q, want clear_all", command.Action)
	}
	if command.PageToken == nil || *command.PageToken != "stale-cursor" {
		t.Fatalf("normalization unexpectedly discarded page token before command handling: %#v", command.PageToken)
	}
}

func TestLooksLikeQueryEventID(t *testing.T) {
	if !looksLikeQueryEventID("0198f2c0-7c7a-7f00-8a11-000000000203") {
		t.Fatal("UUID event ID was not recognized")
	}
	if looksLikeQueryEventID("dashboard revenue") {
		t.Fatal("statement text was recognized as an event ID")
	}
}
