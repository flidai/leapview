package http

import (
	"slices"

	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// Resolve placement and page bindings before copying the visual's independent
// controls. A definition ID alone cannot identify a qualified canvas consumer.
func placedVisualFilters(doc document.DashboardDocument, page document.DashboardPage, componentID, visualID string) ([]document.DashboardFilter, error) {
	filters := append([]document.DashboardFilter(nil), doc.Spec.Filters...)
	pageScoped := map[string]bool{}
	for _, candidate := range doc.Spec.Pages {
		if candidate.FilterBindings != nil {
			for _, binding := range *candidate.FilterBindings {
				pageScoped[binding.Filter] = true
			}
		}
	}
	for index := range filters {
		filter := &filters[index]
		if pageScoped[filter.ID] {
			// A page binding replaces the report template, including on pages
			// where that filter has no consumer.
			targets := []string{}
			filter.Targets = &targets
			if page.FilterBindings != nil {
				for _, binding := range *page.FilterBindings {
					if binding.Filter != filter.ID {
						continue
					}
					if binding.Targets == nil || slices.Contains(*binding.Targets, componentID) {
						targets = []string{visualID}
					}
					if binding.Default != nil {
						filter.Default = binding.Default
					}
					if binding.Required != nil {
						filter.Required = binding.Required
					}
					if binding.ReaderEditable != nil {
						filter.ReaderEditable = binding.ReaderEditable
					}
					if binding.URLParameter != nil {
						filter.URLParameter = binding.URLParameter
					}
				}
			}
		} else if filter.Targets != nil && slices.Contains(*filter.Targets, page.ID+"/"+componentID) {
			targets := []string{visualID}
			filter.Targets = &targets
		}
	}
	return application.CopyChatVisualFilters(filters, visualID)
}
