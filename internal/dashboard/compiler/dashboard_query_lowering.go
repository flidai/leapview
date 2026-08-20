package compiler

// This file is the canonical Dashboard document query seam. It deliberately
// accepts the generated document DTOs and lowers them directly into the
// existing semantic planner and immutable visualization QueryBinding. It does
// not translate through the legacy dashboard/authoring query structs.

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

const canonicalQueryDefaultLimit int64 = 1000

var canonicalResultNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DashboardQueryResultField is one ordered field in a compiled result frame.
// Source is a semantic member for aggregate/pivot queries and a qualified
// physical field for records queries; Name is the only downstream identity.
type DashboardQueryResultField struct {
	Source string
	Name   string
	Grain  string
}

// LoweredDashboardQuery contains both the governed planner request and the
// immutable query binding consumed by Visual IR/runtime compilation. The
// result fields are ordered exactly as authored and are the sole valid names
// for presentation, calculation, interaction, accessibility, and export
// references.
type LoweredDashboardQuery struct {
	Type        string
	Request     semanticquery.Request
	RowRequest  *semanticquery.RowRequest
	RawRequest  *semanticquery.RawValueRequest
	Plan        semanticquery.Plan
	Binding     visualizationdefinition.QueryBinding
	ResultFrame []DashboardQueryResultField
}

// DashboardResultReferences groups the result-frame names used by downstream
// document concerns. Keeping these categories explicit makes a compiler call
// site prove that it never falls back to a semantic member or physical field
// after query lowering.
type DashboardResultReferences struct {
	Presentation  []string
	Calculations  []string
	Interactions  []string
	Accessibility []string
	Export        []string
}

// ValidateDownstreamReferences validates presentation, calculation,
// interaction, accessibility, and export bindings against one immutable
// result frame. Empty categories are valid; an authored non-empty reference
// must resolve to a compiled result name.
func (query LoweredDashboardQuery) ValidateDownstreamReferences(references DashboardResultReferences) error {
	categories := []struct {
		name   string
		values []string
	}{
		{name: "presentation", values: references.Presentation},
		{name: "calculations", values: references.Calculations},
		{name: "interactions", values: references.Interactions},
		{name: "accessibility", values: references.Accessibility},
		{name: "export", values: references.Export},
	}
	for _, category := range categories {
		if err := ValidateDashboardResultReferences(query, category.values); err != nil {
			return fmt.Errorf("%s: %w", category.name, err)
		}
	}
	return nil
}

// HasResultName reports whether a downstream binding addresses a compiled
// result-frame field. Callers should use this for every presentation,
// calculation, interaction, accessibility, and export reference rather than
// resolving semantic members again.
func (query LoweredDashboardQuery) HasResultName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, field := range query.ResultFrame {
		if field.Name == name {
			return true
		}
	}
	return false
}

// ValidateResultReference rejects a source-member reference at the compiled
// result boundary. It intentionally knows only result names, not semantic or
// physical field vocabularies.
func (query LoweredDashboardQuery) ValidateResultReference(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("compiled result field is required")
	}
	if !query.HasResultName(name) {
		return fmt.Errorf("reference %q is not a compiled result field", name)
	}
	return nil
}

// ValidateDashboardResultReferences validates an ordered set of downstream
// result-frame references with one diagnostic per invalid position.
func ValidateDashboardResultReferences(query LoweredDashboardQuery, names []string) error {
	for index, name := range names {
		if err := query.ValidateResultReference(name); err != nil {
			return fmt.Errorf("result reference %d: %w", index, err)
		}
	}
	return nil
}

// LowerDashboardQuery lowers an explicit canonical aggregate, records, pivot,
// histogram, or distribution query. Statistical query semantics are selected
// only by their tagged generated DTO, never by a visual type.
func LowerDashboardQuery(query document.DashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	if model == nil {
		return LoweredDashboardQuery{}, fmt.Errorf("semantic model is required")
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = strings.TrimSpace(model.Name)
	}
	if modelID == "" {
		return LoweredDashboardQuery{}, fmt.Errorf("semantic model ID is required")
	}

	variant, err := query.Type()
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	switch variant {
	case "aggregate":
		value, ok := query.Value.(*document.AggregateDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("aggregate query variant is required")
		}
		return lowerCanonicalAggregate(*value, model, modelID)
	case "records":
		value, ok := query.Value.(*document.RecordsDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("records query variant is required")
		}
		return lowerCanonicalRecords(*value, model, modelID)
	case "pivot":
		value, ok := query.Value.(*document.PivotDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("pivot query variant is required")
		}
		return lowerCanonicalPivot(*value, model, modelID)
	case "histogram":
		value, ok := query.Value.(*document.HistogramDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("histogram query variant is required")
		}
		return lowerCanonicalHistogram(*value, model, modelID)
	case "distribution":
		value, ok := query.Value.(*document.DistributionDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("distribution query variant is required")
		}
		return lowerCanonicalDistribution(*value, model, modelID)
	default:
		return LoweredDashboardQuery{}, fmt.Errorf("unsupported dashboard query type %q", variant)
	}
}

func lowerCanonicalHistogram(query document.HistogramDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	name, alias, err := canonicalMetric(query.Field)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram metric: %w", err)
	}
	if name == "pending_metric" {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram requires a metric")
	}
	if query.Bins <= 0 || query.Bins > 100000 {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram bins must be between 1 and 100000")
	}
	if query.NullPolicy != document.DashboardHistogramNullPolicyOmit && query.NullPolicy != document.DashboardHistogramNullPolicyInclude {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram null policy must be omit or include")
	}
	if query.Approximation != document.DashboardHistogramApproximationExact && query.Approximation != document.DashboardHistogramApproximationApproximate {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram approximation must be exact or approximate")
	}
	var domain *semanticquery.HistogramDomain
	if query.Domain != nil {
		if query.Domain.Minimum == nil || query.Domain.Maximum == nil || !finiteDashboardFloat(pointerValue(query.Domain.Minimum)) || !finiteDashboardFloat(pointerValue(query.Domain.Maximum)) || *query.Domain.Minimum >= *query.Domain.Maximum {
			return LoweredDashboardQuery{}, fmt.Errorf("histogram domain requires finite minimum less than maximum")
		}
		domain = &semanticquery.HistogramDomain{Minimum: *query.Domain.Minimum, Maximum: *query.Domain.Maximum}
	}
	raw := semanticquery.RawValueRequest{Metric: semanticquery.Field{Field: name, Alias: alias}}
	plan, err := planCanonicalHistogram(raw, model, int(query.Bins), semanticquery.HistogramOptions{Domain: domain, NullPolicy: string(query.NullPolicy), Approximation: string(query.Approximation)})
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	raw.Dataset = singleDataset(plan.Datasets)
	resultFrame := histogramResultFrame()
	binding := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultHistogramBins, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: singleDataset(plan.Datasets), Limit: 1, Histogram: &visualizationdefinition.HistogramQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: name, Alias: alias}, Bins: int64(query.Bins), Domain: histogramBindingDomain(domain), NullPolicy: string(query.NullPolicy), Approximation: string(query.Approximation)}}}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "histogram", Request: semanticquery.Request{Dataset: singleDataset(plan.Datasets), Metrics: []semanticquery.Field{{Field: name, Alias: alias}}}, RawRequest: &raw, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func lowerCanonicalDistribution(query document.DistributionDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	name, alias, err := canonicalMetric(query.Field)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution metric: %w", err)
	}
	if name == "pending_metric" {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution requires a metric")
	}
	if len(query.Quantiles) == 0 {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution requires at least one quantile")
	}
	previous := 0.0
	for index, quantile := range query.Quantiles {
		if !finiteDashboardFloat(quantile) || quantile <= 0 || quantile >= 1 || (index > 0 && quantile <= previous) {
			return LoweredDashboardQuery{}, fmt.Errorf("distribution quantiles must be finite, strictly increasing, and between 0 and 1")
		}
		previous = quantile
	}
	if query.Outliers != document.DashboardDistributionOutlierPolicyOmit && query.Outliers != document.DashboardDistributionOutlierPolicyInclude {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution outliers must be omit or include")
	}
	if query.Approximation != document.DashboardHistogramApproximationExact && query.Approximation != document.DashboardHistogramApproximationApproximate {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution approximation must be exact or approximate")
	}
	var groupRequest []semanticquery.Field
	var groupFields []DashboardQueryResultField
	if query.Group != nil {
		groupRequest, groupFields, err = canonicalDimensions([]document.DashboardDimensionSelection{*query.Group}, model)
		if err != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("distribution group: %w", err)
		}
	}
	limit, err := canonicalLimit(query.Limit, canonicalQueryDefaultLimit)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution limit: %w", err)
	}
	var whiskers *semanticquery.DistributionWhiskers
	if query.Whiskers != nil {
		if !finiteDashboardFloat(query.Whiskers.Lower) || !finiteDashboardFloat(query.Whiskers.Upper) || query.Whiskers.Lower <= 0 || query.Whiskers.Upper >= 1 || query.Whiskers.Lower >= query.Whiskers.Upper {
			return LoweredDashboardQuery{}, fmt.Errorf("distribution whiskers require finite probabilities 0 < lower < upper < 1")
		}
		whiskers = &semanticquery.DistributionWhiskers{Lower: query.Whiskers.Lower, Upper: query.Whiskers.Upper}
	}
	raw := semanticquery.RawValueRequest{Dimensions: groupRequest, Metric: semanticquery.Field{Field: name, Alias: alias}}
	plan, err := planCanonicalDistribution(raw, model, nil, int(limit), semanticquery.DistributionOptions{Quantiles: append([]float64(nil), query.Quantiles...), Whiskers: whiskers, Outliers: string(query.Outliers), Approximation: string(query.Approximation)})
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	raw.Dataset = singleDataset(plan.Datasets)
	resultFrame := make([]DashboardQueryResultField, len(plan.Columns))
	for index, column := range plan.Columns {
		resultFrame[index] = DashboardQueryResultField{Source: column, Name: column}
	}
	binding := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultDistribution, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: singleDataset(plan.Datasets), Dimensions: fieldsToBindings(groupFields), Limit: limit, Distribution: &visualizationdefinition.DistributionQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: name, Alias: alias}, Quantiles: append([]float64(nil), query.Quantiles...), Whiskers: distributionBindingWhiskers(whiskers), Outliers: string(query.Outliers), Approximation: string(query.Approximation)}}}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "distribution", Request: semanticquery.Request{Dataset: singleDataset(plan.Datasets), Dimensions: groupRequest, Metrics: []semanticquery.Field{{Field: name, Alias: alias}}, Limit: int(limit)}, RawRequest: &raw, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func planCanonicalHistogram(request semanticquery.RawValueRequest, model *semanticmodel.Model, bins int, options semanticquery.HistogramOptions) (semanticquery.Plan, error) {
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("compile semantic planner: %w", err)
	}
	plan, err := planner.PlanHistogram(request, bins, options)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("plan histogram query: %w", err)
	}
	return plan, nil
}

func planCanonicalDistribution(request semanticquery.RawValueRequest, model *semanticmodel.Model, sorts []semanticquery.Sort, limit int, options semanticquery.DistributionOptions) (semanticquery.Plan, error) {
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("compile semantic planner: %w", err)
	}
	plan, err := planner.PlanDistribution(request, sorts, limit, options)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("plan distribution query: %w", err)
	}
	return plan, nil
}

func histogramResultFrame() []DashboardQueryResultField {
	return []DashboardQueryResultField{{Source: "bucket", Name: "bucket"}, {Source: "count", Name: "count"}, {Source: "start", Name: "start"}, {Source: "end", Name: "end"}}
}

func histogramBindingDomain(domain *semanticquery.HistogramDomain) *visualizationdefinition.HistogramDomain {
	if domain == nil {
		return nil
	}
	return &visualizationdefinition.HistogramDomain{Minimum: domain.Minimum, Maximum: domain.Maximum}
}

func distributionBindingWhiskers(whiskers *semanticquery.DistributionWhiskers) *visualizationdefinition.DistributionWhiskers {
	if whiskers == nil {
		return nil
	}
	return &visualizationdefinition.DistributionWhiskers{Lower: whiskers.Lower, Upper: whiskers.Upper}
}

func finiteDashboardFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func pointerValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func lowerCanonicalAggregate(query document.AggregateDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	dimensions, fields, err := canonicalDimensions(query.Dimensions, model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("aggregate dimensions: %w", err)
	}
	metrics, metricFields, err := canonicalMetrics(query.Metrics, model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("aggregate metrics: %w", err)
	}
	resultFrame, err := uniqueResultFrame(append(fields, metricFields...))
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("aggregate result frame: %w", err)
	}
	sorts, err := canonicalSorts(query.Sort, resultFrame)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("aggregate sort: %w", err)
	}
	limit, err := canonicalLimit(query.Limit, canonicalQueryDefaultLimit)
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	request := semanticquery.Request{Dimensions: dimensions, Metrics: metrics, Sort: sorts, Limit: int(limit)}
	plan, err := planCanonicalAggregate(request, model)
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	tableID := singleDataset(plan.Datasets)
	resultShape := visualizationdefinition.ResultCategoryMultiMeasure
	if len(fields) == 0 && len(metricFields) == 1 {
		resultShape = visualizationdefinition.ResultScalar
	} else if len(fields) == 1 && len(metricFields) == 1 {
		resultShape = visualizationdefinition.ResultCategoryValue
	}
	binding := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: resultShape,
		ModelID: modelID, DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: tableID, Dimensions: fieldsToBindings(fields), Metrics: fieldsToBindings(metricFields), Sort: sortsToBindings(sorts), Limit: limit},
	}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("aggregate query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "aggregate", Request: request, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func lowerCanonicalRecords(query document.RecordsDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	dataset := strings.TrimSpace(query.Dataset)
	if dataset == "" {
		return LoweredDashboardQuery{}, fmt.Errorf("records query dataset is required")
	}
	if _, ok := model.Tables[dataset]; !ok {
		return LoweredDashboardQuery{}, fmt.Errorf("records query references unknown dataset %q", dataset)
	}
	if len(query.Fields) == 0 {
		return LoweredDashboardQuery{}, fmt.Errorf("records query requires at least one field")
	}
	dimensions, fields, err := canonicalRecordFields(query.Fields, dataset, model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("records fields: %w", err)
	}
	resultFrame, err := uniqueResultFrame(fields)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("records result frame: %w", err)
	}
	sorts, err := canonicalSorts(query.Sort, resultFrame)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("records sort: %w", err)
	}
	limit, err := canonicalLimit(query.Limit, canonicalQueryDefaultLimit)
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	request := semanticquery.RowRequest{Dataset: dataset, Dimensions: dimensions, Sort: sorts, Limit: int(limit)}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("compile semantic planner: %w", err)
	}
	plan, err := planner.PlanRows(request)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("plan records query: %w", err)
	}
	binding := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow,
		ModelID: modelID, DatasetID: "primary",
		Detail: &visualizationdefinition.DetailQueryBinding{TableID: dataset, Fields: fieldsToBindings(fields), DefaultSort: sortsToBindings(sorts), Limit: limit},
	}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("records query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "records", Request: semanticquery.Request{Dataset: dataset}, RowRequest: &request, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func lowerCanonicalPivot(query document.PivotDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	if len(query.Rows) == 0 {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot query requires at least one row dimension")
	}
	if len(query.Columns) == 0 {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot query requires at least one column dimension")
	}
	if len(query.Metrics) == 0 {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot query requires at least one metric")
	}
	rows, rowFields, err := canonicalDimensions(query.Rows, model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot rows: %w", err)
	}
	columns, columnFields, err := canonicalDimensions(query.Columns, model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot columns: %w", err)
	}
	metrics, metricFields, err := canonicalMetrics(query.Metrics, model)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot metrics: %w", err)
	}
	allFields := append(append(rowFields, columnFields...), metricFields...)
	resultFrame, err := uniqueResultFrame(allFields)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot result frame: %w", err)
	}
	sorts, err := canonicalSorts(query.Sort, resultFrame)
	if err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot sort: %w", err)
	}
	limit := canonicalQueryDefaultLimit
	offset := int64(0)
	var totals *visualizationdefinition.PivotTotals
	if query.Totals != nil {
		// The binding is the immutable hand-off for pivot totals. The runtime
		// window/totals execution cutover is owned by LEA-426; retaining the
		// normalized values here prevents accepted authoring fields from being
		// silently discarded in the meantime.
		totals = &visualizationdefinition.PivotTotals{
			Rows:    query.Totals.Rows != nil && *query.Totals.Rows,
			Columns: query.Totals.Columns != nil && *query.Totals.Columns,
			Grand:   query.Totals.Grand != nil && *query.Totals.Grand,
		}
	}
	if query.Window != nil {
		if query.Window.Limit <= 0 {
			return LoweredDashboardQuery{}, fmt.Errorf("pivot window limit must be positive")
		}
		if query.Window.Offset != nil && *query.Window.Offset < 0 {
			return LoweredDashboardQuery{}, fmt.Errorf("pivot window offset must not be negative")
		}
		if query.Window.Offset != nil {
			offset = int64(*query.Window.Offset)
		}
		limit = int64(query.Window.Limit)
	}
	request := semanticquery.Request{Dimensions: append(rows, columns...), Metrics: metrics, Sort: sorts, Limit: int(limit), Offset: int(offset)}
	plan, err := planCanonicalAggregate(request, model)
	if err != nil {
		return LoweredDashboardQuery{}, err
	}
	binding := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryPivot, ResultShape: visualizationdefinition.ResultPivotWindow,
		ModelID: modelID, DatasetID: "primary",
		Pivot: &visualizationdefinition.PivotQueryBinding{TableID: singleDataset(plan.Datasets), Rows: fieldsToBindings(rowFields), Columns: fieldsToBindings(columnFields), Metrics: fieldsToBindings(metricFields), Sort: sortsToBindings(sorts), Offset: offset, Totals: totals, Limit: limit},
	}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "pivot", Request: request, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func canonicalDimensions(values []document.DashboardDimensionSelection, model *semanticmodel.Model) ([]semanticquery.Field, []DashboardQueryResultField, error) {
	requests := make([]semanticquery.Field, 0, len(values))
	fields := make([]DashboardQueryResultField, 0, len(values))
	for index, value := range values {
		name, alias, grain, err := canonicalDimension(value)
		if err != nil {
			return nil, nil, fmt.Errorf("dimension %d: %w", index, err)
		}
		if _, err := model.ResolveSemanticDimension(name); err != nil {
			if strings.Contains(name, ".") {
				return nil, nil, fmt.Errorf("dimension %q is a physical field; aggregate dimensions require semantic dimensions", name)
			}
			return nil, nil, err
		}
		requests = append(requests, semanticquery.Field{Field: name, Alias: alias, Grain: grain})
		fields = append(fields, DashboardQueryResultField{Source: name, Name: alias, Grain: grain})
	}
	return requests, fields, nil
}

func canonicalMetrics(values []document.DashboardMetricSelection, model *semanticmodel.Model) ([]semanticquery.Field, []DashboardQueryResultField, error) {
	requests := make([]semanticquery.Field, 0, len(values))
	fields := make([]DashboardQueryResultField, 0, len(values))
	for index, value := range values {
		name, alias, err := canonicalMetric(value)
		if err != nil {
			return nil, nil, fmt.Errorf("metric %d: %w", index, err)
		}
		if _, err := model.ResolveMetric(name); err != nil {
			return nil, nil, err
		}
		requests = append(requests, semanticquery.Field{Field: name, Alias: alias})
		fields = append(fields, DashboardQueryResultField{Source: name, Name: alias})
	}
	return requests, fields, nil
}

func canonicalRecordFields(values []document.DashboardRecordFieldSelection, dataset string, model *semanticmodel.Model) ([]semanticquery.Field, []DashboardQueryResultField, error) {
	requests := make([]semanticquery.Field, 0, len(values))
	fields := make([]DashboardQueryResultField, 0, len(values))
	for index, value := range values {
		name, alias, err := canonicalRecordField(value)
		if err != nil {
			return nil, nil, fmt.Errorf("field %d: %w", index, err)
		}
		if strings.Contains(name, ".") {
			return nil, nil, fmt.Errorf("field %q must be an unqualified root physical field", name)
		}
		qualified := dataset + "." + name
		if _, err := model.ResolveDimension(qualified); err != nil {
			return nil, nil, fmt.Errorf("field %q is not a safe physical field on dataset %q: %w", name, dataset, err)
		}
		requests = append(requests, semanticquery.Field{Field: qualified, Alias: alias})
		fields = append(fields, DashboardQueryResultField{Source: qualified, Name: alias})
	}
	return requests, fields, nil
}

func canonicalDimension(value document.DashboardDimensionSelection) (string, string, string, error) {
	if value.String != nil && value.Reference != nil {
		return "", "", "", fmt.Errorf("selection has multiple variants")
	}
	if value.String != nil {
		name := strings.TrimSpace(*value.String)
		if name == "" {
			return "", "", "", fmt.Errorf("dimension is required")
		}
		return name, canonicalMemberName(name), "", nil
	}
	if value.Reference == nil {
		return "", "", "", fmt.Errorf("dimension selection is required")
	}
	name := strings.TrimSpace(value.Reference.Dimension)
	if name == "" {
		return "", "", "", fmt.Errorf("dimension is required")
	}
	grain := ""
	if value.Reference.Grain != nil {
		grain = string(*value.Reference.Grain)
	}
	alias, err := canonicalAlias(value.Reference.Alias, name)
	if err != nil {
		return "", "", "", err
	}
	return name, alias, grain, nil
}

func canonicalMetric(value document.DashboardMetricSelection) (string, string, error) {
	if value.String != nil && value.Reference != nil {
		return "", "", fmt.Errorf("selection has multiple variants")
	}
	if value.String != nil {
		name := strings.TrimSpace(*value.String)
		if name == "" {
			return "", "", fmt.Errorf("metric is required")
		}
		return name, canonicalMemberName(name), nil
	}
	if value.Reference == nil {
		return "", "", fmt.Errorf("metric selection is required")
	}
	name := strings.TrimSpace(value.Reference.Metric)
	if name == "" {
		return "", "", fmt.Errorf("metric is required")
	}
	alias, err := canonicalAlias(value.Reference.Alias, name)
	return name, alias, err
}

func canonicalRecordField(value document.DashboardRecordFieldSelection) (string, string, error) {
	if value.String != nil && value.Reference != nil {
		return "", "", fmt.Errorf("selection has multiple variants")
	}
	if value.String != nil {
		name := strings.TrimSpace(*value.String)
		if name == "" {
			return "", "", fmt.Errorf("field is required")
		}
		return name, canonicalMemberName(name), nil
	}
	if value.Reference == nil {
		return "", "", fmt.Errorf("field selection is required")
	}
	name := strings.TrimSpace(value.Reference.Field)
	if name == "" {
		return "", "", fmt.Errorf("field is required")
	}
	alias, err := canonicalAlias(value.Reference.Alias, canonicalMemberName(name))
	return name, alias, err
}

func canonicalAlias(value *string, fallback string) (string, error) {
	alias := fallback
	if value != nil {
		alias = strings.TrimSpace(*value)
		if alias == "" {
			return "", fmt.Errorf("alias must not be empty")
		}
	}
	if !canonicalResultNamePattern.MatchString(alias) {
		return "", fmt.Errorf("result name %q is not a valid field identifier", alias)
	}
	return alias, nil
}

func canonicalMemberName(value string) string {
	parts := strings.Split(value, ".")
	return parts[len(parts)-1]
}

func uniqueResultFrame(fields []DashboardQueryResultField) ([]DashboardQueryResultField, error) {
	seen := make(map[string]int, len(fields))
	result := make([]DashboardQueryResultField, len(fields))
	for index, field := range fields {
		if !canonicalResultNamePattern.MatchString(field.Name) {
			return nil, fmt.Errorf("result name %q is not a valid field identifier", field.Name)
		}
		if previous, ok := seen[field.Name]; ok {
			return nil, fmt.Errorf("result name %q is duplicated by fields %d and %d; add distinct aliases", field.Name, previous, index)
		}
		seen[field.Name] = index
		result[index] = field
	}
	return result, nil
}

func canonicalSorts(values *[]document.DashboardSort, frame []DashboardQueryResultField) ([]semanticquery.Sort, error) {
	if values == nil {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(frame))
	for _, field := range frame {
		allowed[field.Name] = struct{}{}
	}
	sorts := make([]semanticquery.Sort, len(*values))
	for index, value := range *values {
		field := strings.TrimSpace(value.Field)
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("sort %d references unknown compiled result field %q", index, field)
		}
		direction := string(value.Direction)
		if direction != "asc" && direction != "desc" {
			return nil, fmt.Errorf("sort %d has unsupported direction %q", index, direction)
		}
		sorts[index] = semanticquery.Sort{Field: field, Direction: direction}
	}
	return sorts, nil
}

func canonicalLimit(value *int32, fallback int64) (int64, error) {
	if value == nil {
		return fallback, nil
	}
	if *value <= 0 {
		return 0, fmt.Errorf("query limit must be positive")
	}
	return int64(*value), nil
}

func planCanonicalAggregate(request semanticquery.Request, model *semanticmodel.Model) (semanticquery.Plan, error) {
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("compile semantic planner: %w", err)
	}
	plan, err := planner.Plan(request)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("plan aggregate query: %w", err)
	}
	return plan, nil
}

func fieldsToBindings(values []DashboardQueryResultField) []visualizationdefinition.FieldBinding {
	result := make([]visualizationdefinition.FieldBinding, len(values))
	for index, value := range values {
		result[index] = visualizationdefinition.FieldBinding{FieldID: value.Source, Alias: value.Name, Grain: value.Grain}
	}
	return result
}

func sortsToBindings(values []semanticquery.Sort) []visualizationdefinition.Sort {
	result := make([]visualizationdefinition.Sort, len(values))
	for index, value := range values {
		result[index] = visualizationdefinition.Sort{FieldID: value.Field, Direction: value.Direction}
	}
	return result
}

func singleDataset(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return ""
}
