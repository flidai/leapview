package compiler

// This file is the canonical Dashboard layout seam. It consumes generated
// Dashboard DTOs directly and emits the existing immutable dashboard Page and
// PageVisual structures. No legacy authoring Dashboard, Canvas, breakpoint, or
// viewport-width value participates in this path.

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

const (
	defaultLayoutColumns   = 12
	defaultLayoutRowHeight = 48
	defaultLayoutGap       = 16
	defaultLayoutPadding   = 16
)

// CompiledDashboardLayout is a compiler-only intermediate. Page grids,
// derived heights, and placements are persisted on Definition.Pages rather
// than duplicated in this value.
type CompiledDashboardLayout struct {
	Defaults   definition.LayoutDefaults
	Pages      []dashboard.Page
	NarrowView definition.NarrowViewPolicy
}

// CompileDashboardLayout compiles dashboard defaults and page overrides into
// deterministic immutable page state. The generated Dashboard DTO has no
// canvas dimensions or breakpoints, so those concerns cannot enter here.
func CompileDashboardLayout(spec document.DashboardSpec) (CompiledDashboardLayout, error) {
	defaults := definition.LayoutDefaults{Columns: defaultLayoutColumns, RowHeight: defaultLayoutRowHeight, Gap: defaultLayoutGap, Padding: defaultLayoutPadding}
	if spec.Layout != nil {
		defaults = definition.LayoutDefaults{Columns: int(spec.Layout.Columns), RowHeight: int(spec.Layout.RowHeight), Gap: int(spec.Layout.Gap), Padding: int(spec.Layout.Padding)}
	}
	if err := validateCanonicalLayoutDefaults("dashboard", defaults); err != nil {
		return CompiledDashboardLayout{}, err
	}
	compiled := CompiledDashboardLayout{Defaults: defaults, NarrowView: definition.NarrowViewPolicyStack, Pages: make([]dashboard.Page, 0, len(spec.Pages))}
	seenPages := make(map[string]struct{}, len(spec.Pages))
	for index, page := range spec.Pages {
		if page.ID == "" {
			return CompiledDashboardLayout{}, fmt.Errorf("page %d requires id", index)
		}
		if _, exists := seenPages[page.ID]; exists {
			return CompiledDashboardLayout{}, fmt.Errorf("duplicate page id %q", page.ID)
		}
		seenPages[page.ID] = struct{}{}
		compiledPage, err := CompileDashboardPageLayout(page, defaults)
		if err != nil {
			return CompiledDashboardLayout{}, fmt.Errorf("page %q: %w", page.ID, err)
		}
		compiled.Pages = append(compiled.Pages, compiledPage)
	}
	return compiled, nil
}

// AttachDashboardLayout compiles canonical layout state and attaches it to a
// copy of an immutable definition. Existing Definition.Pages are replaced by
// the compiler-produced responsive pages; no second pages/components tree is
// persisted.
func AttachDashboardLayout(compiled definition.Definition, spec document.DashboardSpec) (definition.Definition, error) {
	layout, err := CompileDashboardLayout(spec)
	if err != nil {
		return definition.Definition{}, err
	}
	compiled.Layout = &definition.Layout{Defaults: layout.Defaults, NarrowView: layout.NarrowView}
	compiled.Pages = append([]dashboard.Page(nil), layout.Pages...)
	return compiled, nil
}

// CompileDashboardPageLayout compiles one generated page using inherited
// defaults and its optional documented override. Components remain in authored
// order on dashboard.Page.Visuals; overlap and out-of-grid values are rejected.
func CompileDashboardPageLayout(page document.DashboardPage, defaults definition.LayoutDefaults) (dashboard.Page, error) {
	if page.ID == "" {
		return dashboard.Page{}, fmt.Errorf("page id is required")
	}
	if err := validateCanonicalLayoutDefaults("inherited layout", defaults); err != nil {
		return dashboard.Page{}, err
	}
	resolved := defaults
	if page.Layout != nil {
		if page.Layout.Columns != nil {
			resolved.Columns = int(*page.Layout.Columns)
		}
		if page.Layout.RowHeight != nil {
			resolved.RowHeight = int(*page.Layout.RowHeight)
		}
		if page.Layout.Gap != nil {
			resolved.Gap = int(*page.Layout.Gap)
		}
		if page.Layout.Padding != nil {
			resolved.Padding = int(*page.Layout.Padding)
		}
	}
	if err := validateCanonicalLayoutDefaults("page layout", resolved); err != nil {
		return dashboard.Page{}, err
	}
	compiled := dashboard.Page{
		ID: page.ID, Title: page.Title,
		Grid:             dashboard.PageGrid{Columns: resolved.Columns, RowHeight: resolved.RowHeight, Gap: resolved.Gap, Padding: resolved.Padding},
		ResponsiveLayout: &dashboard.PageResponsiveLayout{NarrowView: string(definition.NarrowViewPolicyStack)},
		Visuals:          make([]dashboard.PageVisual, 0, len(page.Components)),
	}
	if page.Description != nil {
		compiled.Description = *page.Description
	}
	seenComponents := make(map[string]struct{}, len(page.Components))
	occupiedRows := 0
	for index, component := range page.Components {
		base, err := component.Base()
		if err != nil {
			return dashboard.Page{}, fmt.Errorf("component %d: %w", index, err)
		}
		if base.ID == "" {
			return dashboard.Page{}, fmt.Errorf("component %d requires id", index)
		}
		if _, exists := seenComponents[base.ID]; exists {
			return dashboard.Page{}, fmt.Errorf("duplicate component %q", base.ID)
		}
		seenComponents[base.ID] = struct{}{}
		placement := dashboard.PagePlacement{Col: int(base.Placement.Column), Row: int(base.Placement.Row), ColSpan: int(base.Placement.ColumnSpan), RowSpan: int(base.Placement.RowSpan)}
		if err := validateCanonicalPlacement(placement, resolved.Columns); err != nil {
			return dashboard.Page{}, fmt.Errorf("component %q: %w", base.ID, err)
		}
		for _, previous := range compiled.Visuals {
			if placementsOverlap(previous.Placement, placement) {
				return dashboard.Page{}, fmt.Errorf("components %q and %q overlap", previous.ID, base.ID)
			}
		}
		visual := dashboard.PageVisual{ID: base.ID, Placement: placement}
		switch value := component.Value.(type) {
		case *document.VisualDashboardPageComponent:
			visual.Kind, visual.Visual = "visual", value.Visual
		case *document.FilterDashboardPageComponent:
			visual.Kind = "slicer"
			visual.Binding = dashboardfilterBindingRef(value.Filter)
		case *document.HeaderDashboardPageComponent:
			visual.Kind = "header"
			if value.Title != nil {
				visual.Title = *value.Title
			}
			if value.Description != nil {
				visual.Description = *value.Description
			}
		default:
			return dashboard.Page{}, fmt.Errorf("component %q has unsupported type %T", base.ID, component.Value)
		}
		compiled.Visuals = append(compiled.Visuals, visual)
		occupied := placement.Row + placement.RowSpan - 1
		if occupied > occupiedRows {
			occupiedRows = occupied
		}
	}
	if err := validateCanonicalRowContinuity(compiled.Visuals); err != nil {
		return dashboard.Page{}, err
	}
	compiled.ResponsiveLayout.OccupiedRows = occupiedRows
	if occupiedRows == 0 {
		compiled.Height = resolved.Padding * 2
	} else {
		compiled.Height = resolved.Padding*2 + occupiedRows*resolved.RowHeight + (occupiedRows-1)*resolved.Gap
	}
	return compiled, nil
}

func validateCanonicalRowContinuity(visuals []dashboard.PageVisual) error {
	if len(visuals) == 0 {
		return nil
	}
	type rowInterval struct {
		start int
		end   int
	}
	intervals := make([]rowInterval, 0, len(visuals))
	for _, visual := range visuals {
		intervals = append(intervals, rowInterval{
			start: visual.Placement.Row,
			end:   visual.Placement.Row + visual.Placement.RowSpan - 1,
		})
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start == intervals[j].start {
			return intervals[i].end < intervals[j].end
		}
		return intervals[i].start < intervals[j].start
	})
	coveredThrough := 0
	for _, interval := range intervals {
		if interval.start > coveredThrough+1 {
			emptyStart, emptyEnd := coveredThrough+1, interval.start-1
			if emptyStart == emptyEnd {
				return fmt.Errorf("grid row %d is empty before component row %d; placements must not leave empty rows", emptyStart, interval.start)
			}
			return fmt.Errorf("grid rows %d..%d are empty before component row %d; placements must not leave empty rows", emptyStart, emptyEnd, interval.start)
		}
		if interval.end > coveredThrough {
			coveredThrough = interval.end
		}
	}
	return nil
}

// CompileCanonicalDashboardPageLayout accepts generated defaults for callers
// compiling a page in isolation.
func CompileCanonicalDashboardPageLayout(page document.DashboardPage, defaults document.DashboardLayoutDefaults) (dashboard.Page, error) {
	return CompileDashboardPageLayout(page, definition.LayoutDefaults{Columns: int(defaults.Columns), RowHeight: int(defaults.RowHeight), Gap: int(defaults.Gap), Padding: int(defaults.Padding)})
}

func validateCanonicalLayoutDefaults(scope string, value definition.LayoutDefaults) error {
	if value.Columns <= 0 {
		return fmt.Errorf("%s columns must be greater than zero", scope)
	}
	if value.RowHeight <= 0 {
		return fmt.Errorf("%s rowHeight must be greater than zero", scope)
	}
	if value.Gap < 0 {
		return fmt.Errorf("%s gap must be non-negative", scope)
	}
	if value.Padding < 0 {
		return fmt.Errorf("%s padding must be non-negative", scope)
	}
	return nil
}

func validateCanonicalPlacement(value dashboard.PagePlacement, columns int) error {
	if value.Col <= 0 || value.Row <= 0 {
		return fmt.Errorf("placement column and row must be greater than zero")
	}
	if value.ColSpan <= 0 || value.RowSpan <= 0 {
		return fmt.Errorf("placement spans must be greater than zero")
	}
	if value.Col > columns || value.ColSpan > columns-value.Col+1 {
		return fmt.Errorf("placement columns %d..%d exceed grid of %d columns", value.Col, value.Col+value.ColSpan-1, columns)
	}
	return nil
}

func placementsOverlap(left, right dashboard.PagePlacement) bool {
	leftRight := left.Col + left.ColSpan - 1
	leftBottom := left.Row + left.RowSpan - 1
	rightRight := right.Col + right.ColSpan - 1
	rightBottom := right.Row + right.RowSpan - 1
	return left.Col <= rightRight && right.Col <= leftRight && left.Row <= rightBottom && right.Row <= leftBottom
}

// dashboardfilterBindingRef creates the narrow page-component binding used by
// the runtime projection. Canonical filter compilation resolves the complete
// filter declaration and scope before this page projection is served.
func dashboardfilterBindingRef(filterID string) dashboardfilter.BindingRef {
	return dashboardfilter.BindingRef{Scope: dashboardfilter.ScopeReport, ID: filterID}
}

// ValidateCanonicalDashboardCompatibility validates closed visual/query/
// presentation combinations and page references. It remains separate from
// layout compilation so model resolution and query lowering can compose these
// checks without introducing a second authoring representation.
func ValidateCanonicalDashboardCompatibility(spec document.DashboardSpec) error {
	for visualID, visual := range spec.Visuals {
		if visualID == "" {
			return fmt.Errorf("dashboard visual id is required")
		}
		queryType, err := visual.Query.Type()
		if err != nil {
			return fmt.Errorf("visual %q query: %w", visualID, err)
		}
		presentationType, err := visual.Presentation.Type()
		if err != nil {
			return fmt.Errorf("visual %q presentation: %w", visualID, err)
		}
		if expected := canonicalPresentationType(visual.Type); expected == "" {
			return fmt.Errorf("visual %q has unsupported type %q", visualID, visual.Type)
		} else if expected != presentationType {
			return fmt.Errorf("visual %q type %q requires %s presentation, got %s", visualID, visual.Type, expected, presentationType)
		}
		if !canonicalQueryCompatible(visual.Type, queryType) {
			return fmt.Errorf("visual %q type %q is incompatible with %s query", visualID, visual.Type, queryType)
		}
	}
	return validateCanonicalPageReferences(spec)
}

func canonicalPresentationType(visualType document.DashboardVisualType) string {
	switch visualType {
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn, document.DashboardVisualTypeCandlestick, document.DashboardVisualTypeBoxplot, document.DashboardVisualTypeCombo, document.DashboardVisualTypeHeatmap, document.DashboardVisualTypeWaterfall, document.DashboardVisualTypeHistogram:
		return "cartesian"
	case document.DashboardVisualTypeScatter:
		return "point"
	case document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeFunnel:
		return "proportional"
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeSankey, document.DashboardVisualTypeGraph, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		return "hierarchy"
	case document.DashboardVisualTypeGauge, document.DashboardVisualTypeRadar:
		return "polar"
	case document.DashboardVisualTypeMap:
		return "geographic"
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return "table"
	case document.DashboardVisualTypeKpi:
		return "kpi"
	default:
		return ""
	}
}

func canonicalQueryCompatible(visualType document.DashboardVisualType, queryType string) bool {
	switch visualType {
	case document.DashboardVisualTypeTable:
		return queryType == "records"
	case document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return queryType == "pivot"
	case document.DashboardVisualTypeHistogram:
		return queryType == "histogram"
	case document.DashboardVisualTypeBoxplot:
		return queryType == "distribution"
	case document.DashboardVisualTypeMap:
		return queryType == "records" || queryType == "aggregate"
	default:
		return queryType == "aggregate"
	}
}

func validateCanonicalPageReferences(spec document.DashboardSpec) error {
	filters := make(map[string]struct{}, len(spec.Filters))
	for _, filter := range spec.Filters {
		if filter.ID == "" {
			return fmt.Errorf("dashboard filter id is required")
		}
		if _, exists := filters[filter.ID]; exists {
			return fmt.Errorf("duplicate dashboard filter %q", filter.ID)
		}
		filters[filter.ID] = struct{}{}
	}
	for _, page := range spec.Pages {
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil {
				return fmt.Errorf("page %q component: %w", page.ID, err)
			}
			switch value := component.Value.(type) {
			case *document.VisualDashboardPageComponent:
				if _, ok := spec.Visuals[value.Visual]; !ok {
					return fmt.Errorf("page %q component %q references unknown visual %q", page.ID, base.ID, value.Visual)
				}
			case *document.FilterDashboardPageComponent:
				if _, ok := filters[value.Filter]; !ok {
					return fmt.Errorf("page %q component %q references unknown filter %q", page.ID, base.ID, value.Filter)
				}
			}
		}
	}
	return nil
}
