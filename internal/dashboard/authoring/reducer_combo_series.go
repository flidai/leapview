package authoring

import "github.com/flidai/leapview/internal/dashboard/document"

// syncCanonicalComboSeries keeps the presentation's explicit series list in
// the same order and cardinality as the compiled metric selections. Combo
// series carry authored mark/axis choices, so existing entries are reused by
// metric alias and only newly-added metrics receive a primary-axis default.
func syncCanonicalComboSeries(visual *document.DashboardVisual) {
	if visual == nil || visual.Type != document.DashboardVisualTypeCombo {
		return
	}
	query, ok := visual.Query.Value.(*document.AggregateDashboardQuery)
	if !ok {
		return
	}
	presentation, ok := visual.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok {
		return
	}
	// A nil series list means the visual intentionally uses the renderer's
	// automatic combo defaults; do not turn that into an explicit all-line map.
	if presentation.Series == nil {
		return
	}
	if len(query.Metrics) == 0 {
		presentation.Series = nil
		return
	}

	existing := make(map[string]document.DashboardComboSeries, len(query.Metrics))
	for _, series := range *presentation.Series {
		existing[series.Field] = series
	}

	defaultMark := document.DashboardComboSeriesMarkLine
	hasBar, hasColumn := false, false
	for _, series := range existing {
		hasBar = hasBar || series.Mark == document.DashboardComboSeriesMarkBar
		hasColumn = hasColumn || series.Mark == document.DashboardComboSeriesMarkColumn
	}
	if hasBar && !hasColumn {
		defaultMark = document.DashboardComboSeriesMarkBar
	}
	defaultAxis := document.DashboardComboSeriesAxisPrimary

	series := make([]document.DashboardComboSeries, 0, len(query.Metrics))
	for _, metric := range query.Metrics {
		_, alias := canonicalMetricSelection(metric)
		if current, found := existing[alias]; found {
			current.Field = alias
			series = append(series, current)
			continue
		}
		series = append(series, document.DashboardComboSeries{Field: alias, Mark: defaultMark, Axis: defaultAxis})
	}
	presentation.Series = &series
}

func rewriteCanonicalComboSeriesAlias(visual *document.DashboardVisual, previousAlias, currentAlias string) {
	if visual == nil || visual.Type != document.DashboardVisualTypeCombo || previousAlias == "" || currentAlias == "" || previousAlias == currentAlias {
		return
	}
	presentation, ok := visual.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation.Series == nil {
		return
	}
	for index := range *presentation.Series {
		if (*presentation.Series)[index].Field == previousAlias {
			(*presentation.Series)[index].Field = currentAlias
		}
	}
}
