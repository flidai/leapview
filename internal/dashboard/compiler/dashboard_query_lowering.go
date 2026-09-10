package compiler

// This file is the canonical Dashboard document query seam. It deliberately
// accepts the generated document DTOs and lowers them directly into the
// existing semantic planner and immutable visualization QueryBinding. It does
// not translate through the legacy dashboard/authoring query structs.

import (
	"fmt"
	"math"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

const canonicalQueryDefaultLimit int64 = 1000

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
	Type string
	// Datasets is the semantic dataset scope used to build the binding. It is
	// retained on this transient lowering result for compiler validation; the
	// persisted QueryBinding carries only its runtime branch's table identity.
	Datasets    []string
	Request     semanticquery.Request
	RowRequest  *semanticquery.RowRequest
	RawRequest  *semanticquery.RawValueRequest
	Plan        semanticquery.Plan
	Binding     visualizationdefinition.QueryBinding
	ResultFrame []DashboardQueryResultField
}

func loweredDashboardQueryDatasets(query LoweredDashboardQuery) []string {
	if len(query.Datasets) > 0 {
		return append([]string(nil), query.Datasets...)
	}
	switch {
	case query.Binding.Aggregate != nil && query.Binding.Aggregate.TableID != "":
		return []string{query.Binding.Aggregate.TableID}
	case query.Binding.Detail != nil && query.Binding.Detail.TableID != "":
		return []string{query.Binding.Detail.TableID}
	case query.Binding.Matrix != nil && query.Binding.Matrix.TableID != "":
		return []string{query.Binding.Matrix.TableID}
	case query.Binding.Pivot != nil && query.Binding.Pivot.TableID != "":
		return []string{query.Binding.Pivot.TableID}
	case query.Binding.Spatial != nil && query.Binding.Spatial.TableID != "":
		return []string{query.Binding.Spatial.TableID}
	default:
		return nil
	}
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
	return lowerDashboardQuery(query, model, modelID, true)
}

// LowerDashboardQueryBinding validates and lowers an authored dashboard query
// into the semantic query binding used by a compiled definition. A dashboard
// definition persists semantic bindings and Visual IR; it does not persist a
// physical SQL plan. Keeping this path separate lets authored Models defer
// physical field datatypes until schema discovery while the strict
// LowerDashboardQuery path remains available for plan-producing callers.
func LowerDashboardQueryBinding(query document.DashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	return lowerDashboardQuery(query, model, modelID, false)
}

func lowerDashboardQuery(query document.DashboardQuery, model *semanticmodel.Model, modelID string, plan bool) (LoweredDashboardQuery, error) {
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
		return lowerCanonicalAggregateWithPlan(*value, model, modelID, plan)
	case "records":
		value, ok := query.Value.(*document.RecordsDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("records query variant is required")
		}
		return lowerCanonicalRecordsWithPlan(*value, model, modelID, plan)
	case "pivot":
		value, ok := query.Value.(*document.PivotDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("pivot query variant is required")
		}
		return lowerCanonicalPivotWithPlan(*value, model, modelID, plan)
	case "histogram":
		value, ok := query.Value.(*document.HistogramDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("histogram query variant is required")
		}
		return lowerCanonicalHistogramWithPlan(*value, model, modelID, plan)
	case "distribution":
		value, ok := query.Value.(*document.DistributionDashboardQuery)
		if !ok || value == nil {
			return LoweredDashboardQuery{}, fmt.Errorf("distribution query variant is required")
		}
		return lowerCanonicalDistributionWithPlan(*value, model, modelID, plan)
	default:
		return LoweredDashboardQuery{}, fmt.Errorf("unsupported dashboard query type %q", variant)
	}
}

func lowerCanonicalHistogram(query document.HistogramDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	return lowerCanonicalHistogramWithPlan(query, model, modelID, true)
}

func lowerCanonicalHistogramWithPlan(query document.HistogramDashboardQuery, model *semanticmodel.Model, modelID string, shouldPlan bool) (LoweredDashboardQuery, error) {
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
	plan := semanticquery.Plan{}
	dataset := ""
	datasets := []string{}
	if !shouldPlan {
		scope, validationErr := semanticquery.ValidateRawValueRequest(model, raw)
		if validationErr != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("validate histogram query: %w", validationErr)
		}
		datasets = append(datasets, scope.Datasets...)
		dataset = singleDataset(scope.Datasets)
	}
	if shouldPlan {
		plan, err = planCanonicalHistogram(raw, model, int(query.Bins), semanticquery.HistogramOptions{Domain: domain, NullPolicy: string(query.NullPolicy), Approximation: string(query.Approximation)})
		if err != nil {
			return LoweredDashboardQuery{}, err
		}
		datasets = append(datasets, plan.Datasets...)
		dataset = singleDataset(plan.Datasets)
	}
	resultFrame := histogramResultFrame()
	raw.Dataset = dataset
	binding := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultHistogramBins, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: dataset, Limit: 1, Histogram: &visualizationdefinition.HistogramQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: name, Alias: alias}, Bins: int64(query.Bins), Domain: histogramBindingDomain(domain), NullPolicy: string(query.NullPolicy), Approximation: string(query.Approximation)}}}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("histogram query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "histogram", Datasets: datasets, Request: semanticquery.Request{Dataset: dataset, Metrics: []semanticquery.Field{{Field: name, Alias: alias}}}, RawRequest: &raw, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func lowerCanonicalDistribution(query document.DistributionDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	return lowerCanonicalDistributionWithPlan(query, model, modelID, true)
}

func lowerCanonicalDistributionWithPlan(query document.DistributionDashboardQuery, model *semanticmodel.Model, modelID string, shouldPlan bool) (LoweredDashboardQuery, error) {
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
	if query.Outliers == document.DashboardDistributionOutlierPolicyOmit && whiskers == nil {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution outliers omit requires whiskers")
	}
	if query.Outliers == document.DashboardDistributionOutlierPolicyInclude && whiskers != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution whiskers require outliers omit")
	}
	raw := semanticquery.RawValueRequest{Dimensions: groupRequest, Metric: semanticquery.Field{Field: name, Alias: alias}}
	plan := semanticquery.Plan{}
	dataset := ""
	datasets := []string{}
	if !shouldPlan {
		scope, validationErr := semanticquery.ValidateRawValueRequest(model, raw)
		if validationErr != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("validate distribution query: %w", validationErr)
		}
		datasets = append(datasets, scope.Datasets...)
		dataset = singleDataset(scope.Datasets)
	}
	if shouldPlan {
		plan, err = planCanonicalDistribution(raw, model, nil, int(limit), semanticquery.DistributionOptions{Quantiles: append([]float64(nil), query.Quantiles...), Whiskers: whiskers, Outliers: string(query.Outliers), Approximation: string(query.Approximation)})
		if err != nil {
			return LoweredDashboardQuery{}, err
		}
		datasets = append(datasets, plan.Datasets...)
		dataset = singleDataset(plan.Datasets)
	}
	raw.Dataset = dataset
	resultFrame := distributionResultFrame(query.Quantiles)
	if shouldPlan {
		resultFrame = make([]DashboardQueryResultField, len(plan.Columns))
		for index, column := range plan.Columns {
			resultFrame[index] = DashboardQueryResultField{Source: column, Name: column}
		}
	}
	binding := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultDistribution, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: dataset, Dimensions: fieldsToBindings(groupFields), Limit: limit, Distribution: &visualizationdefinition.DistributionQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: name, Alias: alias}, Quantiles: append([]float64(nil), query.Quantiles...), Whiskers: distributionBindingWhiskers(whiskers), Outliers: string(query.Outliers), Approximation: string(query.Approximation)}}}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("distribution query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "distribution", Datasets: datasets, Request: semanticquery.Request{Dataset: dataset, Dimensions: groupRequest, Metrics: []semanticquery.Field{{Field: name, Alias: alias}}, Limit: int(limit)}, RawRequest: &raw, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
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

func distributionResultFrame(quantiles []float64) []DashboardQueryResultField {
	result := []DashboardQueryResultField{{Source: "label", Name: "label"}, {Source: "min", Name: "min"}}
	canonical := len(quantiles) == 3 && quantiles[0] == 0.25 && quantiles[1] == 0.5 && quantiles[2] == 0.75
	for index, quantile := range quantiles {
		name := fmt.Sprintf("q%d", index)
		switch {
		case canonical && quantile == 0.25:
			name = "q1"
		case canonical && quantile == 0.5:
			name = "median"
		case canonical && quantile == 0.75:
			name = "q3"
		}
		result = append(result, DashboardQueryResultField{Source: name, Name: name})
	}
	return append(result, DashboardQueryResultField{Source: "max", Name: "max"})
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
	return lowerCanonicalAggregateWithPlan(query, model, modelID, true)
}

func lowerCanonicalAggregateWithPlan(query document.AggregateDashboardQuery, model *semanticmodel.Model, modelID string, shouldPlan bool) (LoweredDashboardQuery, error) {
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
	plan := semanticquery.Plan{}
	dataset := ""
	datasets := []string{}
	if !shouldPlan {
		scope, validationErr := semanticquery.ValidateRequest(model, request)
		if validationErr != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("validate aggregate query: %w", validationErr)
		}
		datasets = append(datasets, scope.Datasets...)
		dataset = singleDataset(scope.Datasets)
	}
	if shouldPlan {
		plan, err = planCanonicalAggregate(request, model)
		if err != nil {
			return LoweredDashboardQuery{}, err
		}
		datasets = append(datasets, plan.Datasets...)
		dataset = singleDataset(plan.Datasets)
	}
	tableID := dataset
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
	return LoweredDashboardQuery{Type: "aggregate", Datasets: datasets, Request: request, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func lowerCanonicalRecords(query document.RecordsDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	return lowerCanonicalRecordsWithPlan(query, model, modelID, true)
}

func lowerCanonicalRecordsWithPlan(query document.RecordsDashboardQuery, model *semanticmodel.Model, modelID string, shouldPlan bool) (LoweredDashboardQuery, error) {
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
	validationModel := model
	var err error
	if !shouldPlan {
		validationModel, err = modelForAuthoringRecords(query.Fields, dataset, model)
		if err != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("records fields: %w", err)
		}
	}
	dimensions, fields, err := canonicalRecordFields(query.Fields, dataset, validationModel)
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
	plan := semanticquery.Plan{}
	datasets := []string{dataset}
	if !shouldPlan {
		if _, validationErr := semanticquery.ValidateRowRequest(validationModel, request); validationErr != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("validate records query: %w", validationErr)
		}
	}
	if shouldPlan {
		planner, err := semanticquery.NewCompiledPlanner(model)
		if err != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("compile semantic planner: %w", err)
		}
		plan, err = planner.PlanRows(request)
		if err != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("plan records query: %w", err)
		}
	}
	binding := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow,
		ModelID: modelID, DatasetID: "primary",
		Detail: &visualizationdefinition.DetailQueryBinding{TableID: dataset, Fields: fieldsToBindings(fields), DefaultSort: sortsToBindings(sorts), Limit: limit},
	}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("records query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "records", Datasets: datasets, Request: semanticquery.Request{Dataset: dataset}, RowRequest: &request, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
}

func lowerCanonicalPivot(query document.PivotDashboardQuery, model *semanticmodel.Model, modelID string) (LoweredDashboardQuery, error) {
	return lowerCanonicalPivotWithPlan(query, model, modelID, true)
}

func lowerCanonicalPivotWithPlan(query document.PivotDashboardQuery, model *semanticmodel.Model, modelID string, shouldPlan bool) (LoweredDashboardQuery, error) {
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
	plan := semanticquery.Plan{}
	dataset := ""
	datasets := []string{}
	if !shouldPlan {
		scope, validationErr := semanticquery.ValidateRequest(model, request)
		if validationErr != nil {
			return LoweredDashboardQuery{}, fmt.Errorf("validate pivot query: %w", validationErr)
		}
		datasets = append(datasets, scope.Datasets...)
		dataset = singleDataset(scope.Datasets)
	}
	if shouldPlan {
		plan, err = planCanonicalAggregate(request, model)
		if err != nil {
			return LoweredDashboardQuery{}, err
		}
		datasets = append(datasets, plan.Datasets...)
		dataset = singleDataset(plan.Datasets)
	}
	binding := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryPivot, ResultShape: visualizationdefinition.ResultPivotWindow,
		ModelID: modelID, DatasetID: "primary",
		Pivot: &visualizationdefinition.PivotQueryBinding{TableID: dataset, Rows: fieldsToBindings(rowFields), Columns: fieldsToBindings(columnFields), Metrics: fieldsToBindings(metricFields), Sort: sortsToBindings(sorts), Offset: offset, Totals: totals, Limit: limit},
	}
	if err := binding.Validate(); err != nil {
		return LoweredDashboardQuery{}, fmt.Errorf("pivot query binding: %w", err)
	}
	return LoweredDashboardQuery{Type: "pivot", Datasets: datasets, Request: request, Plan: plan, Binding: binding, ResultFrame: resultFrame}, nil
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

// modelForAuthoringRecords adds provisional dimensions for safe root fields
// used by a dashboard while a Model's output schema is still undiscovered.
// The copy is used only for request validation; discovery and activation keep
// the original model as the authority that decides whether those fields exist.
func modelForAuthoringRecords(values []document.DashboardRecordFieldSelection, dataset string, model *semanticmodel.Model) (*semanticmodel.Model, error) {
	table, ok := model.Tables[dataset]
	if !ok || table.AuthoredFields == nil || len(table.Schema.Columns) > 0 {
		return model, nil
	}
	deferred := make([]string, 0)
	for _, value := range values {
		name, _, err := canonicalRecordField(value)
		if err != nil || strings.Contains(name, ".") || !canonicalResultNamePattern.MatchString(name) {
			continue
		}
		qualified := dataset + "." + name
		if _, err := model.ResolveDimension(qualified); err == nil {
			continue
		}
		deferred = append(deferred, name)
	}
	if len(deferred) == 0 {
		return model, nil
	}
	clone := model.ExecutionSnapshot()
	if clone == nil {
		return nil, fmt.Errorf("semantic model snapshot is required")
	}
	clonedTable, ok := clone.Tables[dataset]
	if !ok {
		return nil, fmt.Errorf("records query references unknown dataset %q", dataset)
	}
	if clonedTable.Dimensions == nil {
		clonedTable.Dimensions = map[string]semanticmodel.MetricDimension{}
	}
	if clonedTable.Columns == nil {
		clonedTable.Columns = map[string]semanticmodel.ModelColumn{}
	}
	for name, dimension := range clonedTable.Dimensions {
		if _, exists := clonedTable.Columns[name]; !exists {
			clonedTable.Columns[name] = semanticmodel.ModelColumn{
				Name: name, Field: dataset + "." + name, SourceField: name,
				Type: dimension.Type, Datatype: dimension.Datatype,
			}
		}
	}
	for _, name := range deferred {
		if _, exists := clonedTable.Dimensions[name]; !exists {
			clonedTable.Dimensions[name] = semanticmodel.MetricDimension{}
		}
		if _, exists := clonedTable.Columns[name]; !exists {
			clonedTable.Columns[name] = semanticmodel.ModelColumn{Name: name, Field: dataset + "." + name, SourceField: name}
		}
	}
	clone.Tables[dataset] = clonedTable
	return clone, nil
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
