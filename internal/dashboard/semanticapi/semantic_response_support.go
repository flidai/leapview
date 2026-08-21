package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/api"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/workload"
)

func (h Handler) enrichSemanticQueryResponse(
	r *nethttp.Request,
	metrics Metrics,
	modelID string,
	dimensions, metricFields []reportdef.QueryField,
	timeRef *reportdef.QueryTime,
	response *api.SemanticQueryResponse,
) {
	if response == nil {
		return
	}
	projectID, err := h.projectIDForRequest(r.Context())
	if err != nil {
		return
	}
	if model := semanticModelForID(metrics, modelID); model != nil {
		if compiled := compiledSemanticModel(metrics, modelID); compiled != nil {
			response.Columns = semanticQueryColumnsCompiled(modelID, model, compiled, response.Columns, dimensions, metricFields, timeRef)
		}
	}
	if h.QueryFreshness != nil {
		if freshness, ok := h.QueryFreshness(r.Context(), projectID.String(), modelID, response.ServingSnapshot); ok {
			response.Freshness = &freshness
		}
	}
}

func semanticQueryColumnsCompiled(
	modelID string,
	model *semanticmodel.Model,
	compiled *semanticquery.CompiledModel,
	columns []api.QueryColumn,
	dimensions, metrics []reportdef.QueryField,
	timeRef *reportdef.QueryTime,
) []api.QueryColumn {
	if compiled == nil {
		return append([]api.QueryColumn(nil), columns...)
	}
	semantic := make(map[string]api.QueryColumn, len(dimensions)+len(metrics)+1)
	for _, field := range dimensions {
		semantic[semanticOutputName(field.Field, field.Alias)] = semanticDimensionColumn(modelID, model, compiled, field)
	}
	if timeRef != nil && timeRef.Field != "" {
		field := reportdef.QueryField{Field: timeRef.Field, Alias: timeRef.Alias}
		semantic[semanticOutputName(field.Field, field.Alias)] = semanticDimensionColumn(modelID, model, compiled, field)
	}
	for _, field := range metrics {
		semantic[semanticOutputName(field.Field, field.Alias)] = semanticMetricColumn(modelID, model, compiled, field)
	}
	out := make([]api.QueryColumn, len(columns))
	for index, column := range columns {
		if descriptor, ok := semantic[column.Name]; ok {
			descriptor.Name = column.Name
			out[index] = descriptor
			continue
		}
		out[index] = column
	}
	return out
}

func semanticDimensionColumn(modelID string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, field reportdef.QueryField) api.QueryColumn {
	if compiled == nil {
		return api.QueryColumn{Name: semanticOutputName(field.Field, field.Alias), Type: "string", Nullable: true}
	}
	if model != nil {
		if dimension, ok := model.Dimensions[field.Field]; ok {
			dataType := semanticColumnType(dimension.Type)
			return api.QueryColumn{
				Name: semanticOutputName(field.Field, field.Alias), Type: dataType,
				Nullable: semanticDimensionNullable(compiled, dimension),
				FieldRef: &api.QueryFieldRef{Type: "field", ID: modelID + "." + field.Field},
				Label:    semanticLabel(dimension.Label, field.Field), Kind: "dimension",
			}
		}
	}
	if compiled != nil {
		if dimension, err := compiled.ResolveDimension(field.Field); err == nil {
			dataType := semanticColumnType(dimension.Type)
			return api.QueryColumn{
				Name: semanticOutputName(field.Field, field.Alias), Type: dataType,
				Nullable: physicalDimensionNullable(compiledSemanticTable(compiled, dimension.Table), dimension.Name),
				FieldRef: &api.QueryFieldRef{Type: "field", ID: modelID + "." + field.Field},
				Label:    semanticLabel(dimension.Label, dimension.Name), Kind: "dimension",
			}
		}
	}
	return api.QueryColumn{Name: semanticOutputName(field.Field, field.Alias), Type: "string", Nullable: true}
}

func semanticMetricColumn(modelID string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, field reportdef.QueryField) api.QueryColumn {
	if model != nil {
		if metric, ok := model.Metrics[field.Field]; ok {
			return api.QueryColumn{
				Name: semanticOutputName(field.Field, field.Alias), Type: semanticMetricTypeCompiled(compiled, metric), Nullable: metric.Empty != "zero",
				FieldRef: &api.QueryFieldRef{Type: "metric", ID: modelID + "." + field.Field},
				Label:    semanticLabel(metric.Label, field.Field), Kind: "metric", Unit: metric.Unit, Format: metric.Format,
			}
		}
	}
	return api.QueryColumn{Name: semanticOutputName(field.Field, field.Alias), Type: "string", Nullable: true}
}

func semanticMetricTypeCompiled(compiled *semanticquery.CompiledModel, metric semanticmodel.Metric) string {
	if compiled == nil {
		return "decimal"
	}
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return "int64"
	}
	if metric.Aggregation == "avg" {
		return "decimal"
	}
	if metric.Input != nil && metric.Input.Field != "" {
		if dimension, err := compiled.ResolveDimension(metric.Input.Field); err == nil {
			return semanticColumnType(dimension.Type)
		}
	}
	return "decimal"
}

func semanticMetricTypeFromModel(model *semanticmodel.Model, metric semanticmodel.Metric) string {
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return "int64"
	}
	if metric.Input != nil && metric.Input.Field != "" && model != nil {
		if dimension, err := model.ResolveDimension(metric.Input.Field); err == nil {
			return dimension.Type
		}
	}
	return "decimal"
}

func semanticColumnType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == "boolean" || strings.Contains(value, "bool"):
		return "boolean"
	case value == "date":
		return "date"
	case strings.Contains(value, "timestamp") || strings.Contains(value, "datetime"):
		return "timestamp"
	case strings.Contains(value, "int"):
		return "int64"
	case value == "number" || strings.Contains(value, "decimal") || strings.Contains(value, "numeric") ||
		strings.Contains(value, "double") || strings.Contains(value, "float") || strings.Contains(value, "real"):
		return "decimal"
	case value == "json":
		return "json"
	default:
		return "string"
	}
}

func semanticDimensionNullable(compiled *semanticquery.CompiledModel, dimension semanticmodel.SemanticDimension) bool {
	if compiled == nil {
		return true
	}
	for _, binding := range dimension.Bindings {
		physical, err := compiled.ResolveDimension(binding.Field)
		if err != nil || physicalDimensionNullable(compiledSemanticTable(compiled, physical.Table), physical.Name) {
			return true
		}
	}
	return len(dimension.Bindings) == 0
}

func compiledSemanticTable(compiled *semanticquery.CompiledModel, alias string) semanticmodel.Table {
	if compiled != nil {
		if dataset, ok := compiled.Dataset(alias); ok {
			return dataset.Table()
		}
	}
	return semanticmodel.Table{}
}

func physicalDimensionNullable(table semanticmodel.Table, field string) bool {
	for _, column := range table.Schema.Columns {
		if column.Name == field && column.Nullable != nil {
			return *column.Nullable
		}
	}
	return true
}

func semanticOutputName(field, alias string) string {
	if alias != "" {
		return alias
	}
	if index := strings.LastIndex(field, "."); index >= 0 {
		return field[index+1:]
	}
	return field
}

func semanticLabel(label, field string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	name := semanticOutputName(field, "")
	words := strings.Fields(strings.ReplaceAll(name, "_", " "))
	for index := range words {
		if words[index] != "" {
			words[index] = strings.ToUpper(words[index][:1]) + words[index][1:]
		}
	}
	return strings.Join(words, " ")
}

func queryIDForRequest(r *nethttp.Request) (string, error) {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value, nil
	}
	return "", errors.New("request ID is unavailable")
}

func servingSnapshotForRequest(r *nethttp.Request) (string, error) {
	if value := strings.TrimSpace(r.Header.Get("X-Serving-Snapshot")); value != "" {
		return value, nil
	}
	return "", errors.New("serving snapshot is unavailable")
}

func semanticExplainResponse(mode string, plan semanticquery.Plan, warnings []string) api.SemanticExplainResponse {
	if plan.Mode != "" {
		mode = plan.Mode
	}
	return api.SemanticExplainResponse{
		Mode:                 mode,
		Datasets:             append([]string{}, plan.Datasets...),
		StitchDimensions:     append([]string{}, plan.StitchDimensions...),
		PhysicalDependencies: append([]string{}, plan.PhysicalDependencies...),
		RelationshipPaths:    append([]string{}, plan.RelationshipPaths...),
		SQL:                  plan.SQL,
		Args:                 semanticExplainArgs(plan.Args),
		Columns:              append([]string{}, plan.Columns...),
		Warnings:             warnings,
		EffectiveOrdering:    semanticSortResponse(plan.EffectiveOrdering),
	}
}

func semanticSortResponse(sorts []semanticquery.Sort) []api.SemanticSort {
	out := make([]api.SemanticSort, 0, len(sorts))
	for _, item := range sorts {
		out = append(out, api.SemanticSort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func semanticExplainArgs(args []any) []map[string]any {
	out := make([]map[string]any, 0, len(args))
	for i, value := range args {
		out = append(out, map[string]any{"index": i + 1, "value": value})
	}
	return out
}

func semanticQueryWarnings(sorts []api.SemanticSort) []string {
	// The planner now supplies deterministic tie-breakers even when callers do
	// not provide an explicit sort, so an omitted sort is no longer a warning.
	return nil
}

func executeAggregateRows(ctx context.Context, metrics Metrics, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	query := aggregateDataQuery(modelID, request)
	query.ProjectID = metrics.Catalog().Project.ID
	result, err := metrics.ExecuteDataQuery(ctx, query)
	return queryRowsFromDataResult(result.Rows), err
}

func aggregateDataQuery(modelID string, request reportdef.AggregateQuery) dataquery.Query {
	return dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticAggregate,
		Target:  request.Dataset,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Time:    dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	}
}

func executePreviewRows(ctx context.Context, metrics Metrics, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	query := previewDataQuery(modelID, request)
	query.ProjectID = metrics.Catalog().Project.ID
	result, err := metrics.ExecuteDataQuery(ctx, query)
	return queryRowsFromDataResult(result.Rows), err
}

func previewDataQuery(modelID string, request reportdef.RowQuery) dataquery.Query {
	return dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticRows,
		Target:  request.Dataset,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	}
}

func statusForDataExecutionError(err error) int {
	if err == nil {
		return nethttp.StatusOK
	}
	if queryauthz.IsDenied(err) {
		// Query routes identify a concrete semantic model or dataset. Conceal
		// inaccessible IDs consistently with metadata handlers.
		return nethttp.StatusNotFound
	}
	if reason, ok := workload.ReasonOf(err); ok {
		if reason == workload.QueueTimeout {
			return nethttp.StatusGatewayTimeout
		}
		return nethttp.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nethttp.StatusGatewayTimeout
	}
	return nethttp.StatusBadRequest
}

func queryFieldsToDataFields(fields []reportdef.QueryField) []dataquery.Field {
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, dataquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func queryFiltersToDataFilters(filters []reportdef.QueryFilter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]dataquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, dataquery.FilterGroup{Filters: queryFiltersToDataFilters(group.Filters)})
		}
		out = append(out, dataquery.Filter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
		})
	}
	return out
}

func querySortToDataSort(sort []reportdef.QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, dataquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func queryRowsFromDataResult(rows []dataquery.Row) reportdef.QueryRows {
	out := make(reportdef.QueryRows, 0, len(rows))
	for _, row := range rows {
		converted := reportdef.QueryRow{}
		for key, value := range row {
			converted[key] = value
		}
		out = append(out, converted)
	}
	return out
}
