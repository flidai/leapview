package authoring

import (
	"encoding/json"
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
		query.Rows = []document.DashboardDimensionSelection{}
		query.Columns = []document.DashboardDimensionSelection{}
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
	found := false
	for _, limit := range CanonicalVisualRoleLimits(visualType) {
		if limit.Role == string(role) {
			maximum = limit.Maximum
			found = true
			break
		}
	}
	if !found {
		return nil
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

func mergeCompatiblePresentation(target *document.DashboardVisual, source document.DashboardPresentation) error {
	raw, err := presentationObject(target.Presentation)
	if err != nil {
		return err
	}
	prior, err := presentationObject(source)
	if err != nil {
		return err
	}
	// Sharing a generated presentation family does not make every option
	// compatible (for example, even rose:false is invalid on a Funnel).
	// Keep family-wide fields outside the bounded applicability registry,
	// and retain target defaults when the source did not author a value.
	for field, value := range prior {
		known := false
		for _, visual := range canonicalVisualCatalog {
			if document.SupportsPresentationField(visual.Type, field) {
				known = true
				break
			}
		}
		if !known || document.SupportsPresentationField(target.Type, field) {
			raw[field] = value
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, &target.Presentation); err != nil {
		return err
	}
	base, err := target.Presentation.Base()
	if err != nil {
		return err
	}
	base.Type, err = target.Presentation.Type()
	return err
}
