package report

import (
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
)

type Metrics interface {
	DefaultFilters(dashboardID string) dashboard.Filters
	Resolver() dashboardresolver.Resolver
}

func ActivePage(pages []dashboard.Page, pageID string) (dashboard.Page, bool) {
	if len(pages) == 0 {
		return DefaultPage(), true
	}
	if pageID != "" {
		for _, page := range pages {
			if page.ID == pageID {
				return page.WithDefaults(), true
			}
		}
		return dashboard.Page{}, false
	}
	return pages[0].WithDefaults(), true
}

func ActivePageOrDefault(pages []dashboard.Page, pageID string) (dashboard.Page, bool) {
	if len(pages) == 0 {
		return dashboard.Page{}, false
	}
	if pageID != "" {
		for _, page := range pages {
			if page.ID == pageID {
				return page.WithDefaults(), true
			}
		}
	}
	return pages[0].WithDefaults(), true
}

func DefaultPage() dashboard.Page {
	return dashboard.Page{
		ID:     "overview",
		Title:  "Overview",
		Canvas: dashboard.PageCanvas{Width: 1366, Height: 940},
		Grid:   dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16},
	}
}

func DefaultFilters(metrics Metrics, dashboardID, pageID string) dashboard.Filters {
	resolved, err := resolve(metrics, dashboardID)
	if err != nil {
		return dashboard.Filters{}.WithDefaults()
	}
	report := resolved.Definition
	page, ok := report.PageOrDefault(pageID)
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return report.DefaultFiltersForPage(page.ID)
}

func NormalizeFilters(metrics Metrics, dashboardID, pageID string, filters dashboard.Filters) dashboard.Filters {
	resolved, err := resolve(metrics, dashboardID)
	if err == nil {
		report := resolved.Definition
		page, ok := report.PageOrDefault(pageID)
		if !ok {
			return dashboard.Filters{}.WithDefaults()
		}
		return report.NormalizeFiltersForPage(page.ID, filters)
	}
	defaults := dashboard.Filters{}.WithDefaults()
	filters = filters.WithDefaults()
	defaults.Selections = append([]dashboard.InteractionSelection{}, filters.Selections...)
	defaults.SpatialSelections = append([]dashboard.SpatialInteractionSelection{}, filters.SpatialSelections...)
	if filters.CompiledState != nil {
		state := dashboardfilter.CloneState(*filters.CompiledState)
		defaults.CompiledState = &state
	}
	defaults.ServingStateID = filters.ServingStateID
	defaults.InteractionRevision = filters.InteractionRevision
	if filters.DataRevisions != nil {
		defaults.DataRevisions = make(map[string]int64, len(filters.DataRevisions))
		for visualID, revision := range filters.DataRevisions {
			defaults.DataRevisions[visualID] = revision
		}
	}
	return defaults.WithDefaults()
}

func resolve(metrics Metrics, dashboardID string) (dashboardresolver.Resolved, error) {
	if metrics == nil || metrics.Resolver() == nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return metrics.Resolver().Resolve(dashboardID)
}
