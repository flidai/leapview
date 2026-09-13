package authoring

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func setCanonicalFilterScope(value *document.DashboardDocument, patch SetFilterScopePayload) error {
	filterIndex := -1
	for index := range value.Spec.Filters {
		if value.Spec.Filters[index].ID == patch.FilterID {
			filterIndex = index
			break
		}
	}
	if filterIndex < 0 {
		return fmt.Errorf("%w: filter %q", ErrNotFound, patch.FilterID)
	}
	if patch.Scope == "report" {
		filter := &value.Spec.Filters[filterIndex]
		if len(patch.Targets) == 0 {
			filter.Targets = nil
		} else {
			targets := append([]string(nil), patch.Targets...)
			filter.Targets = &targets
		}
		// Restoring report scope removes every explicit page binding while
		// preserving slicer components, which now resolve to the report binding.
		for pageIndex := range value.Spec.Pages {
			page := &value.Spec.Pages[pageIndex]
			if page.FilterBindings == nil {
				continue
			}
			retained := (*page.FilterBindings)[:0]
			for _, binding := range *page.FilterBindings {
				if binding.Filter != patch.FilterID {
					retained = append(retained, binding)
				}
			}
			if len(retained) == 0 {
				page.FilterBindings = nil
			} else {
				copied := append([]document.DashboardPageFilterBinding(nil), retained...)
				page.FilterBindings = &copied
			}
		}
		return nil
	}
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	if len(patch.Targets) > 0 {
		if err := ensureCanonicalPageTargetsHaveIndependentVisuals(value, pageIndex, patch.Targets); err != nil {
			return err
		}
	}

	// A page binding is local to exactly one page. A report-scoped filter can
	// have slicer presentations on every page, but moving it to one page must
	// not leave those other presentations pointing at a binding that cannot be
	// resolved there. Keep the selected page's canvas references and clear the
	// out-of-scope components and bindings as one atomic transition.
	for index := range value.Spec.Pages {
		page := &value.Spec.Pages[index]
		if index != pageIndex {
			removeCanonicalFilterBinding(page, patch.FilterID)
			removeCanonicalFilterComponents(page, patch.FilterID)
			continue
		}
		bindings := make([]document.DashboardPageFilterBinding, 0, lenCanonicalPageBindings(page))
		found := false
		if page.FilterBindings != nil {
			for _, binding := range *page.FilterBindings {
				if binding.Filter == patch.FilterID {
					if found {
						continue
					}
					binding.Targets = optionalCanonicalTargets(patch.Targets)
					bindings = append(bindings, binding)
					found = true
					continue
				}
				bindings = append(bindings, binding)
			}
		}
		if !found {
			bindings = append(bindings, document.DashboardPageFilterBinding{ID: nextCanonicalPageFilterBindingID(*page, patch.FilterID), Filter: patch.FilterID, Targets: optionalCanonicalTargets(patch.Targets)})
		}
		page.FilterBindings = &bindings
	}
	value.Spec.Filters[filterIndex].Targets = nil
	return nil
}

func lenCanonicalPageBindings(page *document.DashboardPage) int {
	if page == nil || page.FilterBindings == nil {
		return 1
	}
	return len(*page.FilterBindings) + 1
}

func removeCanonicalFilterBinding(page *document.DashboardPage, filterID string) {
	if page == nil || page.FilterBindings == nil {
		return
	}
	retained := (*page.FilterBindings)[:0]
	for _, binding := range *page.FilterBindings {
		if binding.Filter != filterID {
			retained = append(retained, binding)
		}
	}
	if len(retained) == 0 {
		page.FilterBindings = nil
		return
	}
	copied := append([]document.DashboardPageFilterBinding(nil), retained...)
	page.FilterBindings = &copied
}

func removeCanonicalFilterComponents(page *document.DashboardPage, filterID string) {
	if page == nil {
		return
	}
	retained := page.Components[:0]
	for _, component := range page.Components {
		filter, ok := component.Value.(*document.FilterDashboardPageComponent)
		if ok && filter.Filter == filterID {
			continue
		}
		retained = append(retained, component)
	}
	page.Components = retained
}

func optionalCanonicalTargets(targets []string) *[]string {
	if len(targets) == 0 {
		return nil
	}
	copied := append([]string(nil), targets...)
	return &copied
}
