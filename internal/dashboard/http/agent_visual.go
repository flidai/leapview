package http

import (
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/go-chi/chi/v5"
)

func requestsCompactDashboardVisual(r *nethttp.Request) bool {
	return dashboardapi.RequestsAgentVisualProjection(r.Context())
}

type dashboardAppliedFilters struct {
	Controls          map[string]dashboardFilterControl `json:"controls"`
	Selections        []map[string]any                  `json:"selections"`
	SpatialSelections []map[string]any                  `json:"spatialSelections"`
}

type dashboardFilterControl struct {
	Type     string    `json:"type"`
	Operator *string   `json:"operator,omitempty"`
	Preset   *string   `json:"preset,omitempty"`
	From     *string   `json:"from,omitempty"`
	To       *string   `json:"to,omitempty"`
	Value    *string   `json:"value,omitempty"`
	Values   *[]string `json:"values,omitempty"`
}

type dashboardVisualColumn struct {
	ID        string          `json:"id"`
	SourceRef *string         `json:"sourceRef,omitempty"`
	Label     string          `json:"label"`
	Role      string          `json:"role"`
	DataType  string          `json:"dataType"`
	Nullable  bool            `json:"nullable"`
	Format    *map[string]any `json:"format,omitempty"`
	Grain     *string         `json:"grain,omitempty"`
}

type dashboardVisualCompleteness struct {
	ReturnedRows     int32  `json:"returnedRows"`
	AvailableRows    *int64 `json:"availableRows,omitempty"`
	Cardinality      string `json:"cardinality"`
	CardinalityCount *int64 `json:"cardinalityCount,omitempty"`
	State            string `json:"state"`
}

type dashboardVisualDiagnostic struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"`
	Message  string  `json:"message"`
	FieldID  *string `json:"fieldId,omitempty"`
}

type dashboardVisualStatus struct {
	Kind    string  `json:"kind"`
	Message *string `json:"message,omitempty"`
}

type dashboardVisualQueryResult struct {
	QueryID         string                       `json:"queryId"`
	ServingSnapshot string                       `json:"servingSnapshot"`
	Freshness       *dashboardapi.QueryFreshness `json:"freshness,omitempty"`
	VisualID        string                       `json:"visualId"`
	Title           string                       `json:"title"`
	Type            string                       `json:"type"`
	Mark            *string                      `json:"mark,omitempty"`
	DatasetID       string                       `json:"datasetId"`
	Columns         []dashboardVisualColumn      `json:"columns"`
	Rows            [][]any                      `json:"rows"`
	AppliedFilters  dashboardAppliedFilters      `json:"appliedFilters"`
	Status          dashboardVisualStatus        `json:"status"`
	Diagnostics     []dashboardVisualDiagnostic  `json:"diagnostics"`
	Completeness    dashboardVisualCompleteness  `json:"completeness"`
	HasMore         bool                         `json:"hasMore"`
	NextCursor      *string                      `json:"nextCursor,omitempty"`
}

func (h Handler) dashboardVisualAgentProjection(
	r *nethttp.Request,
	metrics Metrics,
	envelope visualizationir.VisualizationEnvelope,
	filters dashboard.Filters,
	start, limit int,
	cursorScope, snapshot string,
) (dashboardVisualQueryResult, error) {
	base, err := visualizationir.SpecificationBase(envelope.Spec)
	if err != nil {
		return dashboardVisualQueryResult{}, err
	}
	kind, err := envelope.Spec.Kind()
	if err != nil {
		return dashboardVisualQueryResult{}, err
	}
	if limit <= 0 || limit > maxAgentDashboardVisualRows {
		limit = maxAgentDashboardVisualRows
	}
	datasetID, fields, rows, completeness, err := dashboardVisualRows(envelope, base, start, limit)
	if err != nil {
		return dashboardVisualQueryResult{}, err
	}
	hasMore := completeness.AvailableRows != nil && int64(start+len(rows)) < *completeness.AvailableRows
	var nextCursor *string
	if hasMore {
		value := encodeIndexCursor(start+len(rows), cursorScope, snapshot)
		nextCursor = &value
	}
	queryID := r.Header.Get("X-Request-ID")
	if queryID == "" {
		digest := sha256String(cursorScope)
		queryID = "query_" + digest[:24]
	}
	result := dashboardVisualQueryResult{
		QueryID:         queryID,
		ServingSnapshot: snapshot,
		VisualID:        envelope.VisualID,
		Title:           base.Title,
		Type:            kind,
		Mark:            dashboardVisualMark(envelope.Spec),
		DatasetID:       datasetID,
		Columns:         dashboardVisualColumns(fields),
		Rows:            rows,
		AppliedFilters:  projectDashboardAppliedFilters(filters),
		Status: dashboardVisualStatus{
			Kind: string(envelope.Status.Kind), Message: envelope.Status.Message,
		},
		Diagnostics:  dashboardVisualDiagnostics(envelope.Diagnostics),
		Completeness: completeness,
		HasMore:      hasMore,
		NextCursor:   nextCursor,
	}
	workspaceID := chi.URLParam(r, "workspace")
	if strings.TrimSpace(workspaceID) == "" {
		return dashboardVisualQueryResult{}, fmt.Errorf("workspace ID is required")
	}
	if h.QueryFreshness != nil {
		modelID := metrics.ModelIDForDashboard(chi.URLParam(r, "dashboard"))
		if freshness, ok := h.QueryFreshness(r.Context(), workspaceID, modelID, snapshot); ok {
			result.Freshness = &freshness
		}
	}
	return result, nil
}

const maxAgentDashboardVisualRows = 50

func dashboardVisualRows(
	envelope visualizationir.VisualizationEnvelope,
	base visualizationir.VisualizationSpecBase,
	start, limit int,
) (string, []visualizationir.VisualizationField, [][]any, dashboardVisualCompleteness, error) {
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		if len(state.Datasets) == 0 {
			return "", nil, nil, dashboardVisualCompleteness{}, fmt.Errorf("visualization %q has no inline dataset", envelope.VisualID)
		}
		dataset := state.Datasets[0]
		schema := dashboardVisualSchema(base.Datasets, dataset.ID)
		rows := dashboardVisualPage(dataset.Rows, start, limit)
		available := int64(len(dataset.Rows))
		count := available
		return dataset.ID, dashboardVisualFieldsForColumns(schema.Fields, dataset.Columns), rows, dashboardVisualCompleteness{
			ReturnedRows: int32(len(rows)), AvailableRows: &available,
			Cardinality: "exact", CardinalityCount: &count, State: string(dataset.Completeness),
		}, nil
	case *visualizationir.WindowedVisualizationDataState:
		block, ok := state.Blocks["a"]
		if !ok {
			return "", nil, nil, dashboardVisualCompleteness{}, fmt.Errorf("visualization %q omitted window block a", envelope.VisualID)
		}
		rows := block.Rows
		if len(rows) > limit {
			rows = rows[:limit]
		}
		completeness := dashboardWindowCompleteness(len(rows), state.AvailableRows, start, state.Cardinality)
		return state.Schema.ID, state.Schema.Fields, rows, completeness, nil
	default:
		return "", nil, nil, dashboardVisualCompleteness{}, fmt.Errorf("visualization %q has unsupported data state %T", envelope.VisualID, envelope.DataState.Value)
	}
}

func dashboardWindowCompleteness(returned int, available int64, start int, cardinality visualizationir.VisualizationCardinality) dashboardVisualCompleteness {
	state := "partial"
	switch {
	case available == 0:
		state = "empty"
	case int64(start+returned) >= available && cardinality.Kind == visualizationir.VisualizationCardinalityKindExact:
		state = "complete"
	}
	return dashboardVisualCompleteness{
		ReturnedRows: int32(returned), AvailableRows: &available,
		Cardinality: string(cardinality.Kind), CardinalityCount: cardinality.Count, State: state,
	}
}

func dashboardVisualPage(rows [][]any, start, limit int) [][]any {
	if start >= len(rows) {
		return [][]any{}
	}
	end := min(len(rows), start+limit)
	return rows[start:end]
}

func dashboardVisualSchema(schemas []visualizationir.VisualizationDatasetSchema, id string) visualizationir.VisualizationDatasetSchema {
	for _, schema := range schemas {
		if schema.ID == id {
			return schema
		}
	}
	return visualizationir.VisualizationDatasetSchema{ID: id}
}

func dashboardVisualFieldsForColumns(fields []visualizationir.VisualizationField, columns []string) []visualizationir.VisualizationField {
	byID := make(map[string]visualizationir.VisualizationField, len(fields))
	for _, field := range fields {
		byID[field.ID] = field
	}
	out := make([]visualizationir.VisualizationField, 0, len(columns))
	for _, id := range columns {
		if field, ok := byID[id]; ok {
			out = append(out, field)
			continue
		}
		out = append(out, visualizationir.VisualizationField{
			ID: id, Label: id, Role: visualizationir.VisualizationFieldRoleMetadata,
			DataType: visualizationir.VisualizationDataTypeString, Nullable: true,
		})
	}
	return out
}

func dashboardVisualColumns(fields []visualizationir.VisualizationField) []dashboardVisualColumn {
	out := make([]dashboardVisualColumn, 0, len(fields))
	for _, field := range fields {
		column := dashboardVisualColumn{
			ID: field.ID, SourceRef: field.SourceRef, Label: field.Label, Role: string(field.Role),
			DataType: string(field.DataType), Nullable: field.Nullable,
		}
		if field.Format != nil {
			var format map[string]any
			if encoded, err := json.Marshal(field.Format); err == nil && json.Unmarshal(encoded, &format) == nil {
				column.Format = &format
			}
		}
		if field.Time != nil && field.Time.Grain != "" {
			grain := field.Time.Grain
			column.Grain = &grain
		}
		out = append(out, column)
	}
	return out
}

func projectDashboardAppliedFilters(filters dashboard.Filters) dashboardAppliedFilters {
	filters = filters.WithDefaults()
	result := dashboardAppliedFilters{
		Controls: map[string]dashboardFilterControl{},
	}
	if encoded, err := json.Marshal(filters.Selections); err == nil {
		_ = json.Unmarshal(encoded, &result.Selections)
	}
	if encoded, err := json.Marshal(filters.SpatialSelections); err == nil {
		_ = json.Unmarshal(encoded, &result.SpatialSelections)
	}
	if result.Selections == nil {
		result.Selections = []map[string]any{}
	}
	if result.SpatialSelections == nil {
		result.SpatialSelections = []map[string]any{}
	}
	if filters.CompiledState == nil {
		return result
	}
	for key, applied := range filters.CompiledState.AppliedControls {
		if applied.Expression.Kind == dashboardfilter.ExpressionUnfiltered {
			continue
		}
		result.Controls[key] = dashboardFilterControlFromExpression(applied.Expression)
	}
	return result
}

func dashboardFilterControlFromExpression(expression dashboardfilter.Expression) dashboardFilterControl {
	control := dashboardFilterControl{Type: string(expression.Kind)}
	if expression.Operator != "" {
		operator := string(expression.Operator)
		control.Operator = &operator
	}
	switch expression.Kind {
	case dashboardfilter.ExpressionSet:
		values := make([]string, 0, len(expression.Values))
		for _, value := range expression.Values {
			values = append(values, fmt.Sprint(value.Value))
		}
		control.Values = &values
	case dashboardfilter.ExpressionComparison:
		if expression.Value != nil {
			value := fmt.Sprint(expression.Value.Value)
			control.Value = &value
		}
	case dashboardfilter.ExpressionRange:
		if expression.Lower != nil {
			from := fmt.Sprint(expression.Lower.Value.Value)
			control.From = &from
		}
		if expression.Upper != nil {
			to := fmt.Sprint(expression.Upper.Value.Value)
			control.To = &to
		}
	case dashboardfilter.ExpressionRelativePeriod:
		preset := fmt.Sprintf("%s:%d:%s", expression.Direction, expression.Count, expression.Unit)
		control.Preset = &preset
	}
	return control
}

func dashboardVisualDiagnostics(input []visualizationir.VisualizationDiagnostic) []dashboardVisualDiagnostic {
	out := make([]dashboardVisualDiagnostic, 0, len(input))
	for _, diagnostic := range input {
		out = append(out, dashboardVisualDiagnostic{
			Code: diagnostic.Code, Severity: string(diagnostic.Severity),
			Message: diagnostic.Message, FieldID: diagnostic.FieldID,
		})
	}
	return out
}

func dashboardVisualMark(spec visualizationir.VisualizationSpec) *string {
	var mark string
	switch value := spec.Value.(type) {
	case *visualizationir.CartesianVisualizationSpec:
		mark = string(value.Mark)
	case *visualizationir.PointVisualizationSpec:
		mark = "scatter"
	case *visualizationir.ProportionalVisualizationSpec:
		mark = string(value.Mark)
	case *visualizationir.HierarchyVisualizationSpec:
		mark = string(value.Mark)
	case *visualizationir.PolarVisualizationSpec:
		mark = string(value.Mark)
	}
	if mark == "" {
		return nil
	}
	return &mark
}
