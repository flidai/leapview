package application

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// missingVisualFields completes only unfinished charts. Candidates favor fields
// already used on this page, then the stable governed catalog. Existing queries,
// aliases, filters and presentation are retained. Every completed candidate must
// pass the real preview compiler, including the dashboard's filter contract.
func missingVisualFields(doc document.DashboardDocument, pageID string, model *semanticmodel.Model) ([]authoring.AssignFieldPayload, error) {
	var page *document.DashboardPage
	for i := range doc.Spec.Pages {
		if doc.Spec.Pages[i].ID == pageID {
			page = &doc.Spec.Pages[i]
			break
		}
	}
	if page == nil {
		return nil, fmt.Errorf("%w: page %q", authoring.ErrNotFound, pageID)
	}
	candidates := map[authoring.FieldRole][]string{}
	add := func(role authoring.FieldRole, id string) {
		if !slices.Contains(candidates[role], id) {
			candidates[role] = append(candidates[role], id)
		}
	}
	for _, component := range page.Components {
		placed, ok := component.Value.(*document.VisualDashboardPageComponent)
		if !ok {
			continue
		}
		bindings := resolveVisualTypeFieldBindings(model, doc.Spec.Visuals[placed.Visual])
		for _, id := range bindings.Metrics {
			add(authoring.FieldRoleMetric, id)
		}
		for _, id := range bindings.Dimensions {
			add(authoring.FieldRoleDimension, id)
		}
		if bindings.Dataset != "" && bindings.Dataset != "pending_dataset" {
			for _, id := range bindings.Details {
				add(authoring.FieldRoleDetail, bindings.Dataset+"."+id)
			}
		}
	}
	metricIDs := make([]string, 0, len(model.Metrics))
	for id := range model.Metrics {
		metricIDs = append(metricIDs, id)
	}
	sort.Strings(metricIDs)
	dimensionIDs := make([]string, 0, len(model.Dimensions))
	for id := range model.Dimensions {
		dimensionIDs = append(dimensionIDs, id)
	}
	sort.Strings(dimensionIDs)
	for _, id := range metricIDs {
		add(authoring.FieldRoleMetric, id)
	}
	for _, id := range dimensionIDs {
		add(authoring.FieldRoleDimension, id)
	}
	tableIDs := make([]string, 0, len(model.Tables))
	for id := range model.Tables {
		tableIDs = append(tableIDs, id)
	}
	sort.Strings(tableIDs)
	for _, tableID := range tableIDs {
		fields := make([]string, 0, len(model.Tables[tableID].Dimensions))
		for id := range model.Tables[tableID].Dimensions {
			fields = append(fields, id)
		}
		sort.Strings(fields)
		for _, id := range fields {
			add(authoring.FieldRoleDetail, tableID+"."+id)
		}
	}
	working := doc
	completedVisuals := map[string]bool{}
	if initial, err := dashboardcompiler.CompileDocumentBuilderPreview(doc, map[string]*semanticmodel.Model{doc.Spec.SemanticModel: model}); err == nil {
		for id := range initial.Definition.Visualizations {
			completedVisuals[id] = true
		}
	}
	var additions []authoring.AssignFieldPayload
	for _, component := range page.Components {
		placed, ok := component.Value.(*document.VisualDashboardPageComponent)
		if !ok {
			continue
		}
		base, err := component.Base()
		if err != nil {
			return nil, err
		}
		visual := working.Spec.Visuals[placed.Visual]
		if completedVisuals[placed.Visual] {
			continue
		}
		counts := visualFieldCounts(visual)
		var missing []authoring.FieldRole
		limits := authoring.CanonicalVisualRoleLimits(visual.Type)
		// Cartesian renderers need an X/category binding as well as a measure.
		// The catalog permits zero dimensions for editing intermediate drafts;
		// use renderable minima when completing an unfinished chart.
		switch visual.Type {
		case document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn, document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeCombo:
			limits = []authoring.VisualRoleLimit{{Role: "metric", Minimum: 1}, {Role: "dimension", Minimum: 1}}
		}
		if visual.Type == document.DashboardVisualTypeTable {
			limits = []authoring.VisualRoleLimit{{Role: "detail", Minimum: 1}}
		}
		// Measures establish the dataset before choosing dimensions, including ratio
		// and calculated measures which have no single physical dataset owner.
		for _, role := range []authoring.FieldRole{authoring.FieldRoleMetric, authoring.FieldRoleDimension, authoring.FieldRoleDetail} {
			for _, limit := range limits {
				if limit.Role == string(role) {
					for n := counts[role]; n < int(limit.Minimum); n++ {
						missing = append(missing, role)
					}
				}
			}
		}
		if len(missing) == 0 {
			continue
		}
		attempts := 0
		var search func(document.DashboardDocument, int, []authoring.AssignFieldPayload) (document.DashboardDocument, []authoring.AssignFieldPayload, bool)
		search = func(current document.DashboardDocument, index int, fields []authoring.AssignFieldPayload) (document.DashboardDocument, []authoring.AssignFieldPayload, bool) {
			if index == len(missing) {
				return current, fields, previewableVisual(current, placed.Visual, model)
			}
			role := missing[index]
			bindings := resolveVisualTypeFieldBindings(model, current.Spec.Visuals[placed.Visual])
			roleCandidates := candidates[role]
			if role == authoring.FieldRoleDimension && (visual.Type == document.DashboardVisualTypeFunnel || visual.Type == document.DashboardVisualTypePie || visual.Type == document.DashboardVisualTypeDonut) {
				roleCandidates = append([]string(nil), roleCandidates...)
				// Categories provide a more readable initial breakdown than dates
				// in part-to-whole charts; preserve contextual order within each group.
				sort.SliceStable(roleCandidates, func(i, j int) bool {
					return !temporalVisualDimension(roleCandidates[i], model.Dimensions[roleCandidates[i]]) && temporalVisualDimension(roleCandidates[j], model.Dimensions[roleCandidates[j]])
				})
			}
			for _, id := range roleCandidates {
				if attempts >= 256 {
					break
				}
				attempts++
				if role == authoring.FieldRoleMetric && slices.Contains(bindings.Metrics, id) || role == authoring.FieldRoleDimension && slices.Contains(bindings.Dimensions, id) {
					continue
				}
				assignment := authoring.AssignFieldPayload{PageID: pageID, VisualID: base.ID, FieldID: id, Role: role}
				if validateGovernedField(model, id, role) != nil {
					continue
				}
				assignment.ResolvedTable = resolvedTableForField(model, assignment)
				candidate, err := authoring.WithAssignedVisualFields(current, []authoring.AssignFieldPayload{assignment})
				if err != nil {
					continue
				}
				// Lowering checks governed datasets, joins, dimensions and metric types
				// before exploring further fields; renderer shape is checked at the leaf.
				partialQuery := candidate.Spec.Visuals[placed.Visual].Query
				if pivot, ok := partialQuery.Value.(*document.PivotDashboardQuery); ok {
					// A partial pivot is not yet renderable; validate its semantic
					// selections before both axes have been populated.
					partialQuery = document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Metrics: pivot.Metrics, Dimensions: append(append([]document.DashboardDimensionSelection{}, pivot.Rows...), pivot.Columns...)}}
				}
				if _, err := dashboardcompiler.LowerDashboardQueryBinding(partialQuery, model, doc.Spec.SemanticModel); err != nil {
					continue
				}
				if completed, resolved, ok := search(candidate, index+1, append(append([]authoring.AssignFieldPayload{}, fields...), assignment)); ok {
					return completed, resolved, true
				}
			}
			return current, nil, false
		}
		if completed, fields, ok := search(working, 0, nil); ok {
			working = completed
			completedVisuals[placed.Visual] = true
			additions = append(additions, fields...)
		}
	}
	return additions, nil
}

func previewableVisual(doc document.DashboardDocument, id string, model *semanticmodel.Model) bool {
	visual, ok := doc.Spec.Visuals[id]
	if !ok || dashboardcompiler.ValidateBuilderVisual(id, visual, doc.Spec.SemanticModel, model) != nil {
		return false
	}
	// Only successful candidates need the cross-chart filter compatibility check.
	_, err := dashboardcompiler.CompileCanonicalDashboardBuilderFilters(doc, model)
	return err == nil
}

func visualFieldCounts(visual document.DashboardVisual) map[authoring.FieldRole]int {
	counts := map[authoring.FieldRole]int{}
	switch query := visual.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		counts[authoring.FieldRoleDimension] = len(query.Dimensions)
		counts[authoring.FieldRoleMetric] = len(query.Metrics)
	case *document.PivotDashboardQuery:
		counts[authoring.FieldRoleDimension] = len(query.Rows) + len(query.Columns)
		counts[authoring.FieldRoleMetric] = len(query.Metrics)
	case *document.RecordsDashboardQuery:
		counts[authoring.FieldRoleDetail] = len(query.Fields)
	case *document.HistogramDashboardQuery:
		if id := visualSwitchMetricID(query.Field); id != "" && id != "pending_metric" {
			counts[authoring.FieldRoleMetric] = 1
		}
	case *document.DistributionDashboardQuery:
		if id := visualSwitchMetricID(query.Field); id != "" && id != "pending_metric" {
			counts[authoring.FieldRoleMetric] = 1
		}
		if query.Group != nil {
			counts[authoring.FieldRoleDimension] = 1
		}
	}
	return counts
}

func temporalVisualDimension(id string, dimension semanticmodel.SemanticDimension) bool {
	switch dimension.Datatype {
	case semanticmodel.DataTypeDate, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return true
	}
	if dimension.Type == "timestamp" || dimension.Type == "date" {
		return true
	}
	// Authored month/week labels can deliberately be strings. Name tokens are
	// a preference only; these fields remain eligible when no category fits.
	for _, token := range strings.FieldsFunc(strings.ToLower(id), func(r rune) bool { return r == '_' || r == '-' || r == ' ' }) {
		switch token {
		case "date", "day", "week", "month", "quarter", "year", "time":
			return true
		}
	}
	return false
}
