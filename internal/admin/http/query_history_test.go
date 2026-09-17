package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBindQueryHistoryFiltersPreservesSelectedProjects(t *testing.T) {
	filters, err := bindQueryHistoryFilters(uisignals.AdminQueryHistoryFilters{}, projectgraph.ResourceID("project:active"))
	if err != nil {
		t.Fatal(err)
	}
	if filters.Projects != nil && len(*filters.Projects) != 0 {
		t.Fatalf("empty filters acquired an implicit project: %#v", filters.Projects)
	}

	selected := uisignals.AdminQueryHistoryFilters{Projects: uisignals.OptionalSlice([]string{"project:stale", "project:active", "project:other"})}
	cleared, err := bindQueryHistoryFilters(uisignals.AdminQueryHistoryFilters{}, projectgraph.ResourceID("project:active"))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Projects != nil && len(*cleared.Projects) != 0 {
		t.Fatalf("clear-all filters retained stale project: %#v", cleared.Projects)
	}
	bound, err := bindQueryHistoryFilters(selected, projectgraph.ResourceID("project:active"))
	if err != nil {
		t.Fatalf("selected project binding failed: %v", err)
	}
	if got := *bound.Projects; len(got) != 3 || got[0] != "project:stale" || got[1] != "project:active" || got[2] != "project:other" {
		t.Fatalf("selected projects were not preserved: %#v", got)
	}
	bound, err = bindQueryHistoryFilters(selected, "")
	if err != nil {
		t.Fatalf("selected project binding without active project failed: %v", err)
	}
	if got := *bound.Projects; len(got) != 3 || got[0] != "project:stale" || got[1] != "project:active" || got[2] != "project:other" {
		t.Fatalf("selected projects were not preserved without an active project: %#v", got)
	}
	if _, err := bindQueryHistoryFilters(uisignals.AdminQueryHistoryFilters{Projects: uisignals.OptionalSlice([]string{"not a project id"})}, projectgraph.ResourceID("project:active")); !errors.Is(err, errQueryHistoryProjectScope) {
		t.Fatalf("invalid project selection error = %v, want %v", err, errQueryHistoryProjectScope)
	}
}

func TestQueryHistoryFilterMenusUseGlobalOptions(t *testing.T) {
	repo := &globalQueryHistoryReader{}
	r := httptest.NewRequest(http.MethodGet, "/admin/queries", nil)
	menus := (ReadModel{}).queryHistoryFilterMenus(r, repo, projectgraph.ResourceID("project:active"), uisignals.AdminQueryHistoryFilters{
		Projects: uisignals.OptionalSlice([]string{"project:stale"}),
	}, "", "")
	if repo.globalCalls != 5 {
		t.Fatalf("global filter option calls = %d, want 5", repo.globalCalls)
	}
	if repo.scopedCalls != 0 {
		t.Fatalf("scoped filter option calls = %d, want 0", repo.scopedCalls)
	}
	var projectMenu uisignals.FilterMenuSignal
	for _, menu := range menus {
		if menu.ID == "project" {
			projectMenu = menu
			break
		}
	}
	if projectMenu.Options == nil {
		t.Fatal("project menu has no options")
	}
	values := make(map[string]bool, len(*projectMenu.Options))
	for _, option := range *projectMenu.Options {
		values[option.Value] = true
	}
	if !values["project:other"] || !values["project:stale"] {
		t.Fatalf("global and selected project options = %#v", values)
	}
}

type globalQueryHistoryReader struct {
	globalCalls int
	scopedCalls int
}

func (r *globalQueryHistoryReader) GetQueryEvent(context.Context, projectgraph.ResourceID, string) (queryaudit.Event, error) {
	return queryaudit.Event{}, errors.New("not implemented")
}

func (r *globalQueryHistoryReader) ListQueryEvents(context.Context, queryaudit.Filter) ([]queryaudit.Event, error) {
	return nil, nil
}

func (r *globalQueryHistoryReader) ListQueryEventFilterOptions(context.Context, projectgraph.ResourceID, string, string, int) ([]queryaudit.FilterOption, error) {
	r.scopedCalls++
	return nil, nil
}

func (r *globalQueryHistoryReader) ListQueryEventFilterOptionsGlobal(_ context.Context, field, _ string, _ int) ([]queryaudit.FilterOption, error) {
	r.globalCalls++
	switch field {
	case "project":
		return []queryaudit.FilterOption{{Value: "project:other", Count: 4}}, nil
	case "surface":
		return []queryaudit.FilterOption{{Value: "explore", Count: 4}}, nil
	default:
		return nil, nil
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
