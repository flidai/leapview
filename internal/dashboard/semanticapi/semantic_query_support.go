package http

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/api"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func semanticAggregateRequest(datasetID string, input api.SemanticQueryRequest, includeExtraRow bool, cursorScope ...string) (reportdef.AggregateQuery, int, error) {
	limit, offset, err := semanticLimitAndOffset(input.Limit, input.PageToken, cursorScope...)
	if err != nil {
		return reportdef.AggregateQuery{}, 0, err
	}
	requestLimit := limit
	if includeExtraRow {
		requestLimit++
	}
	request := reportdef.AggregateQuery{
		Dataset:    datasetID,
		Dimensions: semanticQueryFields(input.Dimensions),
		Metrics:    semanticQueryFields(input.Metrics),
		Filters:    semanticFilters(input.Filters),
		Sort:       semanticSorts(input.Sort),
		Limit:      requestLimit,
		Offset:     offset,
	}
	if input.Time != nil {
		request.Time = reportdef.QueryTime{Field: input.Time.Field, Grain: input.Time.Grain, Alias: input.Time.Alias}
	}
	return request, limit, nil
}

func semanticRowRequest(datasetID string, input api.SemanticPreviewRequest, includeExtraRow bool, cursorScope ...string) (reportdef.RowQuery, int, error) {
	limit, offset, err := semanticLimitAndOffset(input.Limit, input.PageToken, cursorScope...)
	if err != nil {
		return reportdef.RowQuery{}, 0, err
	}
	requestLimit := limit
	if includeExtraRow {
		requestLimit++
	}
	return reportdef.RowQuery{
		Dataset:    datasetID,
		Dimensions: semanticQueryFields(input.Dimensions),
		Metrics:    semanticQueryFields(input.Metrics),
		Filters:    semanticFilters(input.Filters),
		Sort:       semanticSorts(input.Sort),
		Limit:      requestLimit,
		Offset:     offset,
	}, limit, nil
}

func semanticLimitAndOffset(limitValue int, pageToken string, cursorScope ...string) (int, int, error) {
	limit := limitValue
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	if limit > maxQueryLimit {
		limit = maxQueryLimit
	}
	offset, err := decodeIndexCursor(pageToken, cursorScope...)
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

func semanticQueryFields(fields []api.SemanticFieldRef) []reportdef.QueryField {
	out := make([]reportdef.QueryField, 0, len(fields))
	for _, field := range fields {
		out = append(out, reportdef.QueryField{Field: field.Field, Alias: field.Alias})
	}
	return out
}

func semanticExplainAggregate(metrics Metrics, modelID string, request reportdef.AggregateQuery) (semanticquery.Plan, error) {
	compiled, ok := semanticPlanner(metrics, modelID)
	if !ok {
		return semanticquery.Plan{}, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return compiled.Plan(reportdef.SemanticAggregateRequest(request))
}

func semanticExplainRows(metrics Metrics, modelID string, request reportdef.RowQuery) (semanticquery.Plan, error) {
	compiled, ok := semanticPlanner(metrics, modelID)
	if !ok {
		return semanticquery.Plan{}, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return compiled.PlanRows(reportdef.SemanticRowRequest(request))
}

func semanticFilters(filters []api.SemanticFilter) []reportdef.QueryFilter {
	out := make([]reportdef.QueryFilter, 0, len(filters))
	for _, filter := range filters {
		out = append(out, reportdef.QueryFilter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   semanticFilterGroups(filter.Groups),
		})
	}
	return out
}

func semanticFilterGroups(groups []api.SemanticFilterGroup) []reportdef.QueryFilterGroup {
	out := make([]reportdef.QueryFilterGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, reportdef.QueryFilterGroup{Filters: semanticFilters(group.Filters)})
	}
	return out
}

func semanticSorts(sorts []api.SemanticSort) []reportdef.QuerySort {
	out := make([]reportdef.QuerySort, 0, len(sorts))
	for _, sortSpec := range sorts {
		out = append(out, reportdef.QuerySort{Field: sortSpec.Field, Direction: sortSpec.Direction})
	}
	return out
}

func semanticQueryResponse(columns []string, rows reportdef.QueryRows, limit, offset int, queryID, snapshot string, cursorScope ...string) api.SemanticQueryResponse {
	encodedRows := make([][]any, 0, min(len(rows), limit))
	descriptors := make([]api.QueryColumn, len(columns))
	for index, name := range columns {
		descriptors[index] = api.QueryColumn{Name: name, Type: queryColumnType(rows, name), Nullable: queryColumnNullable(rows, name)}
	}
	for i, row := range rows {
		if i >= limit {
			break
		}
		values := make([]any, len(columns))
		for index, column := range columns {
			values[index] = queryCellValue(row[column])
		}
		encodedRows = append(encodedRows, values)
	}
	nextCursor := ""
	if len(rows) > limit {
		scopes := append(append([]string{}, cursorScope...), snapshot)
		nextCursor = encodeIndexCursor(offset+limit, scopes...)
	}
	return api.SemanticQueryResponse{
		QueryID: queryID, ServingSnapshot: snapshot, Columns: descriptors, Rows: encodedRows,
		Completeness: api.QueryCompleteness{ReturnedRows: len(encodedRows), HasMore: nextCursor != ""},
		Page:         api.PageInfo{NextCursor: nextCursor},
	}
}

func queryColumnType(rows reportdef.QueryRows, column string) string {
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}
		switch value.(type) {
		case bool:
			return "boolean"
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return "int64"
		case float32, float64:
			return "float64"
		case time.Time:
			return "timestamp"
		case json.RawMessage, map[string]any, []any:
			return "json"
		default:
			return "string"
		}
	}
	return "string"
}

func queryColumnNullable(rows reportdef.QueryRows, column string) bool {
	for _, row := range rows {
		if value, ok := row[column]; !ok || value == nil {
			return true
		}
	}
	return len(rows) == 0
}

func queryCellValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return typed
	case []byte:
		return string(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		encoded, err := json.Marshal(value)
		if err == nil && (strings.HasPrefix(string(encoded), "{") || strings.HasPrefix(string(encoded), "[")) {
			return string(encoded)
		}
		return fmt.Sprint(value)
	}
}
