package authoring

import (
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func canonicalVisualSwitchBindings(query document.DashboardQuery) VisualTypeFieldBindings {
	bindings := VisualTypeFieldBindings{}
	appendDimension := func(selection document.DashboardDimensionSelection) {
		id, _ := canonicalDimensionSelection(selection)
		bindings.Dimensions = append(bindings.Dimensions, id)
	}
	appendMetric := func(selection document.DashboardMetricSelection) {
		id, _ := canonicalMetricSelection(selection)
		if id != "pending_metric" {
			bindings.Metrics = append(bindings.Metrics, id)
		}
	}
	switch value := query.Value.(type) {
	case *document.AggregateDashboardQuery:
		for _, selection := range value.Dimensions {
			appendDimension(selection)
		}
		for _, selection := range value.Metrics {
			appendMetric(selection)
		}
	case *document.PivotDashboardQuery:
		for _, selection := range value.Rows {
			appendDimension(selection)
		}
		for _, selection := range value.Columns {
			appendDimension(selection)
		}
		for _, selection := range value.Metrics {
			appendMetric(selection)
		}
	case *document.HistogramDashboardQuery:
		appendMetric(value.Field)
	case *document.DistributionDashboardQuery:
		appendMetric(value.Field)
		if value.Group != nil {
			appendDimension(*value.Group)
		}
	case *document.RecordsDashboardQuery:
		bindings.Dataset = value.Dataset
		for _, selection := range value.Fields {
			id, _ := canonicalRecordSelection(selection)
			bindings.Details = append(bindings.Details, id)
		}
	}
	return bindings
}

func canonicalVisualSwitchQuery(target document.DashboardQuery, visualType document.DashboardVisualType, bindings *VisualTypeFieldBindings) document.DashboardQuery {
	if bindings == nil {
		return target
	}
	dimensions := boundedVisualSwitchFields(bindings.Dimensions, visualType, FieldRoleDimension)
	metrics := boundedVisualSwitchFields(bindings.Metrics, visualType, FieldRoleMetric)
	details := boundedVisualSwitchFields(bindings.Details, visualType, FieldRoleDetail)
	switch query := target.Value.(type) {
	case *document.AggregateDashboardQuery:
		query.Dimensions = visualSwitchDimensionSelections(dimensions)
		query.Metrics = visualSwitchMetricSelections(metrics)
	case *document.RecordsDashboardQuery:
		if dataset := strings.TrimSpace(bindings.Dataset); dataset != "" {
			query.Dataset = dataset
		}
		query.Fields = visualSwitchRecordSelections(details)
	case *document.PivotDashboardQuery:
		query.Rows = nil
		query.Columns = nil
		if len(dimensions) > 0 {
			query.Rows = visualSwitchDimensionSelections(dimensions[:1])
		}
		if len(dimensions) > 1 {
			query.Columns = visualSwitchDimensionSelections(dimensions[1:2])
		}
		if len(dimensions) > 2 {
			query.Rows = append(query.Rows, visualSwitchDimensionSelections(dimensions[2:])...)
		}
		query.Metrics = visualSwitchMetricSelections(metrics)
	case *document.HistogramDashboardQuery:
		if len(metrics) > 0 {
			query.Field = visualSwitchMetricSelections(metrics[:1])[0]
		}
	case *document.DistributionDashboardQuery:
		if len(metrics) > 0 {
			query.Field = visualSwitchMetricSelections(metrics[:1])[0]
		}
	}
	return target
}

func boundedVisualSwitchFields(fields []string, visualType document.DashboardVisualType, role FieldRole) []string {
	maximum := int32(0)
	for _, limit := range CanonicalVisualRoleLimits(visualType) {
		if limit.Role == string(role) {
			maximum = limit.Maximum
			break
		}
	}
	result := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, value := range fields {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if maximum > 0 && int32(len(result)) >= maximum {
			break
		}
	}
	return result
}

func visualSwitchDimensionSelections(fields []string) []document.DashboardDimensionSelection {
	result := make([]document.DashboardDimensionSelection, 0, len(fields))
	for _, value := range fields {
		field := value
		result = append(result, document.DashboardDimensionSelection{String: &field})
	}
	return result
}

func visualSwitchMetricSelections(fields []string) []document.DashboardMetricSelection {
	result := make([]document.DashboardMetricSelection, 0, len(fields))
	for _, value := range fields {
		field := value
		result = append(result, document.DashboardMetricSelection{String: &field})
	}
	return result
}

func visualSwitchRecordSelections(fields []string) []document.DashboardRecordFieldSelection {
	result := make([]document.DashboardRecordFieldSelection, 0, len(fields))
	for _, value := range fields {
		field := value
		result = append(result, document.DashboardRecordFieldSelection{String: &field})
	}
	return result
}

func configureTargetPresentationBindings(visual *document.DashboardVisual) {
	if visual == nil || visual.Type != document.DashboardVisualTypeScatter {
		return
	}
	query, ok := visual.Query.Value.(*document.AggregateDashboardQuery)
	if !ok {
		return
	}
	presentation, ok := visual.Presentation.Value.(*document.PointDashboardPresentation)
	if !ok {
		return
	}
	if len(query.Dimensions) > 0 {
		_, alias := canonicalDimensionSelection(query.Dimensions[0])
		if alias != "" {
			presentation.Identity = []string{alias}
		}
	}
	if len(query.Metrics) > 0 {
		_, alias := canonicalMetricSelection(query.Metrics[0])
		if alias != "" {
			presentation.X = alias
		}
	}
	if len(query.Metrics) > 1 {
		_, alias := canonicalMetricSelection(query.Metrics[1])
		if alias != "" {
			presentation.Y = alias
		}
	}
}

func mergeCartesianPresentation(target, source *document.CartesianDashboardPresentation) {
	if target == nil || source == nil {
		return
	}
	// The Cartesian presentation is one generated union member. Copy the whole
	// member so newly-added compatible controls (tooltip, axes, references,
	// series intent, and legend metadata) survive a same-family mark switch.
	// Keep the target discriminator supplied by defaultCanonicalVisual.
	targetType := target.Type
	*target = *source
	target.Type = targetType
}
