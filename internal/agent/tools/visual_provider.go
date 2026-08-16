package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	agentVisualToolName = QueryVisualToolName
	maxVisualRows       = 50
)

type VisualAuthorizeFunc func(ctx context.Context, scope Scope, request VisualAuthorizationRequest) (agentcore.ToolResult, bool)

type VisualQueryContextFunc func(ctx context.Context, scope Scope) context.Context

type VisualModelFunc func(projectID, modelID string) (*semanticmodel.Model, bool)

type VisualAggregateRowsFunc func(ctx context.Context, projectID, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error)

type VisualPreviewRowsFunc func(ctx context.Context, projectID, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error)

type VisualHistogramFunc func(ctx context.Context, projectID, modelID string, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error)

type VisualDistributionFunc func(ctx context.Context, projectID, modelID string, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error)

type VisualQueryMetadata struct {
	ServingSnapshot string
	Freshness       *agentcontracts.QueryFreshness
}

type VisualQueryMetadataFunc func(ctx context.Context, projectID, modelID string) VisualQueryMetadata

type VisualProvider struct {
	Authorize     VisualAuthorizeFunc
	Resolve       ResourceResolver
	QueryContext  VisualQueryContextFunc
	SemanticModel VisualModelFunc
	AggregateRows VisualAggregateRowsFunc
	PreviewRows   VisualPreviewRowsFunc
	Histogram     VisualHistogramFunc
	Distribution  VisualDistributionFunc
	QueryMetadata VisualQueryMetadataFunc
}

type VisualAuthorizationRequest struct {
	ToolName string
	CallID   string
	Type     string
	Model    string
	Dataset  string
}

type agentVisualInput struct {
	SemanticModelID string                                 `json:"semanticModelId"`
	Model           string                                 `json:"-"`
	Dataset         string                                 `json:"dataset"`
	Title           string                                 `json:"title"`
	Type            string                                 `json:"type"`
	Presentation    agentVisualPresentation                `json:"presentation"`
	Dimensions      []agentVisualFieldRef                  `json:"dimensions"`
	Series          *agentVisualFieldRef                   `json:"series"`
	Measures        []agentVisualFieldRef                  `json:"measures"`
	Fields          []agentVisualFieldRef                  `json:"fields"`
	Rows            []agentVisualFieldRef                  `json:"rows"`
	Columns         []dashboard.TableColumn                `json:"columns"`
	Filters         []agentVisualFilter                    `json:"filters"`
	Sort            []agentVisualSort                      `json:"sort"`
	Limit           int                                    `json:"limit"`
	Calculations    []dashboardauthoring.VisualCalculation `json:"calculations"`
}

type agentVisualPresentation = dashboardauthoring.VisualPresentation

type agentVisualFieldRef struct {
	Field string `json:"field"`
	Alias string `json:"alias,omitempty"`
}

type agentVisualSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction,omitempty"`
}

type agentVisualFilter struct {
	Field    string                   `json:"field,omitempty"`
	Fact     string                   `json:"fact,omitempty"`
	Operator string                   `json:"operator,omitempty"`
	Values   []string                 `json:"values,omitempty"`
	Groups   []agentVisualFilterGroup `json:"groups,omitempty"`
}

type agentVisualFilterGroup struct {
	Filters []agentVisualFilter `json:"filters"`
}

type agentVisualResult struct {
	Type    string                                                      `json:"type"`
	ID      string                                                      `json:"id"`
	Patch   map[string]map[string]visualizationir.VisualizationEnvelope `json:"patch"`
	Summary string                                                      `json:"summary"`
}

func (p VisualProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{{
		Name:         agentVisualToolName,
		Description:  "Create one read-only visual from LeapView semantic model fields. Data is queried from semantic models; do not provide inline data.",
		InputSchema:  json.RawMessage(agentcontracts.QueryVisualInputSchemaJSON),
		OutputSchema: json.RawMessage(agentcontracts.QueryVisualResultSchemaJSON),
		Effect:       "read",
		Tags:         []string{"analytics", "visualization"},
		Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
			return p.Run(ctx, scope, call), nil
		}),
	}}
}

func (p VisualProvider) Run(ctx context.Context, scope Scope, call agentcore.ToolCall) agentcore.ToolResult {
	input, err := decodeAgentVisualInput(call.Arguments)
	if err != nil {
		return apigenAgentToolError("invalid_arguments", err.Error())
	}
	runScope := scope
	if strings.TrimSpace(runScope.ProjectID) == "" {
		return apigenAgentToolError("authorization_failed", "active project runtime is required")
	}
	modelID, err := projectgraph.NewResourceID(input.SemanticModelID)
	if err != nil {
		return apigenAgentToolError("invalid_arguments", "semanticModelId is invalid")
	}
	if p.Resolve == nil {
		return apigenAgentToolError("catalog_unavailable", "authorized project catalog is not configured")
	}
	resolvedModel, err := p.Resolve(ctx, runScope, modelID, projectgraph.KindSemanticModel, access.CapabilityResourceUse)
	if err != nil {
		return apigenAgentToolError("catalog_not_found", "semantic model is unknown or unauthorized")
	}
	input.Model = resolvedModel.String()
	metadata := dataquery.Metadata{
		Surface:     dataquery.SurfaceAgent,
		Operation:   dataquery.OperationAgentQuery,
		PrincipalID: scope.PrincipalID,
		ObjectType:  "semantic_dataset",
		ObjectID:    input.Model + ":" + input.Dataset,
		RequestID:   call.ID,
	}
	ctx = dataquery.WithMetadata(ctx, metadata)
	if p.QueryContext != nil {
		ctx = p.QueryContext(ctx, runScope)
	}
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return apigenAgentToolError("authorization_failed", "agent visual tool requires an authenticated principal")
	}
	if p.Authorize != nil {
		if errResult, ok := p.Authorize(ctx, runScope, VisualAuthorizationRequest{
			ToolName: agentVisualToolName,
			CallID:   call.ID,
			Type:     input.Type,
			Model:    input.Model,
			Dataset:  input.Dataset,
		}); !ok {
			return errResult
		}
	}
	queryMetadata := VisualQueryMetadata{ServingSnapshot: "unversioned"}
	if p.QueryMetadata != nil {
		queryMetadata = p.QueryMetadata(ctx, runScope.ProjectID, input.Model)
		if strings.TrimSpace(queryMetadata.ServingSnapshot) == "" {
			queryMetadata.ServingSnapshot = "unversioned"
		}
	}
	result, model, err := p.queryAgentVisual(ctx, runScope.ProjectID, input, agentVisualID(call.ID))
	if err != nil {
		return apigenAgentToolError("query_visual_failed", err.Error())
	}
	compact, err := compactAgentVisualResult(runScope.ProjectID, call.ID, queryMetadata, model, input, result)
	if err != nil {
		return apigenAgentToolError("query_visual_failed", err.Error())
	}
	return agentcore.ToolResult{
		Content:        compact,
		DisplayContent: result,
	}
}

func decodeAgentVisualInput(rawArgs json.RawMessage) (agentVisualInput, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rawArgs, &raw); err != nil {
		return agentVisualInput{}, err
	}
	for _, forbidden := range []string{"filter", "interaction", "interactions", "data", "values"} {
		if _, ok := raw[forbidden]; ok {
			return agentVisualInput{}, fmt.Errorf("%s is not supported by %s", forbidden, agentVisualToolName)
		}
	}
	var input agentVisualInput
	decoder := json.NewDecoder(strings.NewReader(string(rawArgs)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return agentVisualInput{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return agentVisualInput{}, fmt.Errorf("arguments must contain exactly one JSON object")
	}
	input.Model = strings.TrimSpace(input.Model)
	input.SemanticModelID = strings.TrimSpace(input.SemanticModelID)
	input.Model = input.SemanticModelID
	input.Dataset = strings.TrimSpace(input.Dataset)
	input.Title = strings.TrimSpace(input.Title)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	if !isAgentVisualType(input.Type) {
		return agentVisualInput{}, fmt.Errorf("type must be a supported visual type")
	}
	if input.SemanticModelID == "" {
		return agentVisualInput{}, fmt.Errorf("semanticModelId is required")
	}
	input.Dataset = stripCatalogRefString(input.Dataset, input.Model)
	normalizeAgentVisualFieldRefs(input.Dimensions, input.Model)
	normalizeAgentVisualFieldRefs(input.Measures, input.Model)
	normalizeAgentVisualFieldRefs(input.Fields, input.Model)
	normalizeAgentVisualFieldRefs(input.Rows, input.Model)
	if input.Series != nil {
		input.Series.Field = stripCatalogRefString(input.Series.Field, input.Model)
	}
	for index := range input.Sort {
		input.Sort[index].Field = stripCatalogRefString(input.Sort[index].Field, input.Model)
	}
	normalizeAgentVisualFilters(input.Filters, input.Model)
	if err := validateAgentVisualFilters(input.Filters); err != nil {
		return agentVisualInput{}, err
	}
	if input.Dataset == "" {
		return agentVisualInput{}, fmt.Errorf("dataset is required")
	}
	input.Limit = agentVisualLimit(input.Limit)
	return input, nil
}

func normalizeAgentVisualFilters(filters []agentVisualFilter, modelID string) {
	for index := range filters {
		filters[index].Field = stripCatalogRefString(filters[index].Field, modelID)
		filters[index].Fact = stripCatalogRefString(filters[index].Fact, modelID)
		filters[index].Operator = strings.ToLower(strings.TrimSpace(filters[index].Operator))
		if filters[index].Operator == "" {
			filters[index].Operator = "equals"
		}
		for groupIndex := range filters[index].Groups {
			normalizeAgentVisualFilters(filters[index].Groups[groupIndex].Filters, modelID)
		}
	}
}

func validateAgentVisualFilters(filters []agentVisualFilter) error {
	for index, filter := range filters {
		if filter.Field == "" && len(filter.Groups) == 0 {
			return fmt.Errorf("filters[%d] requires field or groups", index)
		}
		if filter.Field != "" {
			switch filter.Operator {
			case "equals", "contains", "not_contains", "starts_with", "greater_than_or_equal", "less_than":
				if len(filter.Values) != 1 {
					return fmt.Errorf("filters[%d] operator %s requires one value", index, filter.Operator)
				}
			case "in":
				if len(filter.Values) == 0 {
					return fmt.Errorf("filters[%d] operator in requires at least one value", index)
				}
			case "is_null", "is_not_null":
				if len(filter.Values) != 0 {
					return fmt.Errorf("filters[%d] operator %s does not accept values", index, filter.Operator)
				}
			default:
				return fmt.Errorf("filters[%d] has unsupported operator %q", index, filter.Operator)
			}
		}
		for groupIndex, group := range filter.Groups {
			if len(group.Filters) == 0 {
				return fmt.Errorf("filters[%d].groups[%d] requires filters", index, groupIndex)
			}
			if err := validateAgentVisualFilters(group.Filters); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentVisualFilters(filters []agentVisualFilter) []reportdef.QueryFilter {
	out := make([]reportdef.QueryFilter, 0, len(filters))
	for _, filter := range filters {
		values := make([]any, len(filter.Values))
		for index, value := range filter.Values {
			values[index] = value
		}
		var groups []reportdef.QueryFilterGroup
		if len(filter.Groups) > 0 {
			groups = make([]reportdef.QueryFilterGroup, 0, len(filter.Groups))
		}
		for _, group := range filter.Groups {
			groups = append(groups, reportdef.QueryFilterGroup{Filters: agentVisualFilters(group.Filters)})
		}
		out = append(out, reportdef.QueryFilter{
			Field: filter.Field, Fact: filter.Fact, Operator: filter.Operator, Values: values, Groups: groups,
		})
	}
	return out
}

func normalizeAgentVisualFieldRefs(values []agentVisualFieldRef, modelID string) {
	for index := range values {
		values[index].Field = stripCatalogRefString(values[index].Field, modelID)
	}
}

func isAgentVisualType(value string) bool {
	switch value {
	case "line", "area", "bar", "column", "pie", "donut", "scatter", "funnel", "treemap", "gauge", "heatmap", "sankey", "graph", "map", "candlestick", "boxplot", "combo", "waterfall", "histogram", "radar", "tree", "sunburst", "kpi", "table", "matrix", "pivot":
		return true
	default:
		return false
	}
}

func (p VisualProvider) queryAgentVisual(ctx context.Context, projectID string, input agentVisualInput, id string) (agentVisualResult, *semanticmodel.Model, error) {
	if p.SemanticModel == nil {
		return agentVisualResult{}, nil, fmt.Errorf("semantic model provider is not configured")
	}
	model, ok := p.SemanticModel(projectID, input.Model)
	if !ok || model == nil {
		return agentVisualResult{}, nil, fmt.Errorf("unknown semantic model %q", input.Model)
	}
	if _, ok := model.Tables[input.Dataset]; !ok {
		return agentVisualResult{}, nil, fmt.Errorf("unknown dataset %q", input.Dataset)
	}
	var result agentVisualResult
	var err error
	switch input.Type {
	case "table", "matrix", "pivot":
		result, err = p.queryAgentTable(ctx, projectID, model, input, id)
	default:
		result, err = p.queryAgentChart(ctx, projectID, model, input, id)
	}
	return result, model, err
}

func compactAgentVisualResult(
	projectID string,
	queryID string,
	metadata VisualQueryMetadata,
	model *semanticmodel.Model,
	input agentVisualInput,
	result agentVisualResult,
) (agentcontracts.QueryVisualResult, error) {
	envelope, ok := result.Patch["visuals"][result.ID]
	if !ok {
		return agentcontracts.QueryVisualResult{}, fmt.Errorf("visualization envelope %q is missing", result.ID)
	}
	base, err := visualizationir.SpecificationBase(envelope.Spec)
	if err != nil {
		return agentcontracts.QueryVisualResult{}, fmt.Errorf("read visualization metadata: %w", err)
	}
	returnedRows := agentVisualReturnedRows(envelope)
	completenessStatus := "complete"
	if returnedRows == 0 {
		completenessStatus = "empty"
	} else if returnedRows >= input.Limit {
		completenessStatus = "limit_reached"
	}
	return agentcontracts.QueryVisualResult{
		Ok:               true,
		QueryID:          queryID,
		ServingSnapshot:  metadata.ServingSnapshot,
		Freshness:        metadata.Freshness,
		Type:             result.Type,
		ID:               result.ID,
		Title:            base.Title,
		SemanticModelRef: agentVisualCatalogRef(projectID, "semantic_model", input.Model),
		DatasetID:        input.Dataset,
		Fields:           agentVisualFieldUsages(projectID, input.Model, model, input),
		Filters:          agentVisualFilterUsages(projectID, input.Model, input.Filters, nil),
		Completeness: agentcontracts.QueryVisualCompleteness{
			ReturnedRows: int32(returnedRows),
			Limit:        int32(input.Limit),
			Status:       completenessStatus,
		},
		Status:      agentVisualStatus(envelope.Status),
		Diagnostics: agentVisualDiagnostics(envelope.Diagnostics),
		Summary:     result.Summary,
		Signal:      "visuals." + result.ID,
	}, nil
}

func agentVisualCatalogRef(projectID, typ, id string) agentcontracts.CatalogRef {
	_ = projectID // project-wide resource IDs have no project selector
	return agentcontracts.CatalogRef{Kind: agentcontracts.CatalogType(typ), ID: id}
}

func agentVisualReturnedRows(envelope visualizationir.VisualizationEnvelope) int {
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		total := 0
		for _, dataset := range state.Datasets {
			total += len(dataset.Rows)
		}
		return total
	case *visualizationir.WindowedVisualizationDataState:
		total := 0
		for _, block := range state.Blocks {
			total += len(block.Rows)
		}
		return total
	}
	return 0
}

func agentVisualStatus(status visualizationir.VisualizationStatus) agentcontracts.DashboardVisualStatus {
	return agentcontracts.DashboardVisualStatus{Kind: string(status.Kind), Message: status.Message}
}

func agentVisualDiagnostics(values []visualizationir.VisualizationDiagnostic) []agentcontracts.DashboardVisualDiagnostic {
	out := make([]agentcontracts.DashboardVisualDiagnostic, 0, len(values))
	for _, value := range values {
		out = append(out, agentcontracts.DashboardVisualDiagnostic{
			Code: value.Code, Severity: string(value.Severity), Message: value.Message, FieldID: value.FieldID,
		})
	}
	return out
}

func agentVisualFieldUsages(projectID, modelID string, model *semanticmodel.Model, input agentVisualInput) []agentcontracts.QueryVisualFieldUsage {
	type fieldRole struct {
		ref  agentVisualFieldRef
		role string
	}
	values := make([]fieldRole, 0, len(input.Dimensions)+len(input.Measures)+len(input.Fields)+len(input.Rows)+1)
	for _, field := range input.Dimensions {
		values = append(values, fieldRole{ref: field, role: "dimension"})
	}
	if input.Series != nil {
		values = append(values, fieldRole{ref: *input.Series, role: "series"})
	}
	for _, field := range input.Fields {
		values = append(values, fieldRole{ref: field, role: "table_field"})
	}
	for _, field := range input.Rows {
		values = append(values, fieldRole{ref: field, role: "table_row"})
	}
	for _, field := range input.Measures {
		values = append(values, fieldRole{ref: field, role: "measure"})
	}
	out := make([]agentcontracts.QueryVisualFieldUsage, 0, len(values))
	for _, value := range values {
		usage := agentVisualFieldUsage(projectID, modelID, model, value.ref, value.role)
		out = append(out, usage)
	}
	return out
}

func agentVisualFieldUsage(projectID, modelID string, model *semanticmodel.Model, ref agentVisualFieldRef, role string) agentcontracts.QueryVisualFieldUsage {
	usage := agentcontracts.QueryVisualFieldUsage{
		FieldID: ref.Field,
		Role:    role,
		Alias:   optionalString(ref.Alias),
		Label:   agentFieldAliasForRef(ref),
	}
	if dimension, err := model.ResolveDimension(ref.Field); err == nil {
		usage.Label = dimensionLabelForAgent(agentFieldAliasForRef(ref), dimension)
		usage.DataType = optionalString(dimension.Type)
		return usage
	}
	if dimension, err := model.ResolveSemanticDimension(ref.Field); err == nil {
		usage.Label = firstNonEmpty(dimension.Label, ref.Field)
		usage.DataType = optionalString(dimension.Type)
		return usage
	}
	if measure, err := model.ResolveMeasure(ref.Field); err == nil {
		usage.Label = measureLabelForAgent(ref.Field, measure)
		usage.Unit = optionalString(measure.Unit)
		usage.Format = optionalString(measure.Format)
		return usage
	}
	if metric, ok := model.Metrics[ref.Field]; ok {
		usage.Label = firstNonEmpty(metric.Label, ref.Field)
		usage.Unit = optionalString(metric.Unit)
		usage.Format = optionalString(metric.Format)
	}
	return usage
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func agentVisualFilterUsages(
	projectID string,
	modelID string,
	filters []agentVisualFilter,
	groupPath []int32,
) []agentcontracts.QueryVisualFilterUsage {
	out := []agentcontracts.QueryVisualFilterUsage{}
	for _, filter := range filters {
		if filter.Field != "" {
			usage := agentcontracts.QueryVisualFilterUsage{
				FieldID:  filter.Field,
				Operator: filter.Operator,
			}
			if len(filter.Values) > 0 {
				values := append([]string{}, filter.Values...)
				usage.Values = &values
			}
			if len(groupPath) > 0 {
				path := append([]int32{}, groupPath...)
				usage.Path = &path
			}
			if filter.Fact != "" {
				factID := filter.Fact
				usage.ResolvedFactID = &factID
			}
			out = append(out, usage)
		}
		for index, group := range filter.Groups {
			path := append(append([]int32{}, groupPath...), int32(index))
			out = append(out, agentVisualFilterUsages(projectID, modelID, group.Filters, path)...)
		}
	}
	return out
}

func (p VisualProvider) queryAgentChart(ctx context.Context, projectID string, model *semanticmodel.Model, input agentVisualInput, id string) (agentVisualResult, error) {
	semanticModelID, err := projectgraph.NewResourceID(input.Model)
	if err != nil {
		return agentVisualResult{}, fmt.Errorf("semantic model ID: %w", err)
	}
	shape := agentVisualShape(input)
	if err := validateAgentChartContract(input); err != nil {
		return agentVisualResult{}, err
	}
	data, err := p.agentChartData(ctx, projectID, input, shape, model)
	if err != nil {
		return agentVisualResult{}, err
	}
	measure, _ := model.ResolveMeasure(input.Measures[0].Field)
	title := input.Title
	if title == "" {
		title = measureLabelForAgent(input.Measures[0].Field, measure)
	}
	chartType := input.Type
	authored := agentReportVisual(input)
	authored.Title = title
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{
		ID: "agent-visual", Title: "Agent visual", SemanticModel: semanticModelID,
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{id: authored}),
	}, model)
	if err != nil {
		return agentVisualResult{}, fmt.Errorf("compile agent visualization: %w", err)
	}
	definition, ok := definitions[id]
	if !ok {
		return agentVisualResult{}, fmt.Errorf("compiled agent visualization %q is missing", id)
	}
	records := make([]map[string]any, len(data))
	for index, datum := range data {
		records[index] = map[string]any(datum)
	}
	frame, err := visualizationruntime.FrameFromRecords(definition, records)
	if err != nil {
		return agentVisualResult{}, fmt.Errorf("shape agent visualization: %w", err)
	}
	envelope, err := visualizationruntime.EnvelopeFromFrame(definition, frame, nil, 1, 1)
	if err != nil {
		return agentVisualResult{}, err
	}
	return agentVisualResult{
		Type:    chartType,
		ID:      id,
		Patch:   map[string]map[string]visualizationir.VisualizationEnvelope{"visuals": {id: envelope}},
		Summary: fmt.Sprintf("Created chart %q with %d data points.", title, len(data)),
	}, nil
}

func agentVisualShape(input agentVisualInput) string {
	return agentReportVisual(input).ResultShape()
}

func agentReportVisual(input agentVisualInput) dashboardauthoring.Visual {
	dimensions := make([]dashboardauthoring.FieldRef, len(input.Dimensions))
	for index, field := range input.Dimensions {
		dimensions[index] = dashboardauthoring.FieldRef{Field: field.Field, Alias: field.Alias}
	}
	measures := make([]dashboardauthoring.FieldRef, len(input.Measures))
	for index, field := range input.Measures {
		measures[index] = dashboardauthoring.FieldRef{Field: field.Field, Alias: field.Alias}
	}
	series := dashboardauthoring.FieldRef{}
	if input.Series != nil {
		series = dashboardauthoring.FieldRef{Field: input.Series.Field, Alias: input.Series.Alias}
	}
	return dashboardauthoring.Visual{
		Title: firstNonEmpty(input.Title, "Agent visual"), Type: input.Type, Presentation: input.Presentation,
		Query:        dashboardauthoring.VisualQuery{Table: input.Dataset, Dimensions: dimensions, Series: series, Measures: measures, Limit: input.Limit},
		Calculations: input.Calculations,
	}
}

func validateAgentChartContract(input agentVisualInput) error {
	visual := agentReportVisual(input)
	definition := dashboardauthoring.Dashboard{
		ID: "agent-visual", Title: "Agent visual", SemanticModel: "agent",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"visual": visual}),
		Pages: []dashboard.Page{{
			ID: "page", Title: "Page",
			Visuals: []dashboard.PageVisual{{ID: "visual", Kind: "visual", Visual: "visual", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 6, RowSpan: 4}}},
		}},
	}
	if err := definition.ValidateContract(); err != nil {
		return fmt.Errorf("invalid %s visual query: %w", input.Type, err)
	}
	return nil
}

func (p VisualProvider) agentChartData(ctx context.Context, projectID string, input agentVisualInput, shape string, model *semanticmodel.Model) ([]dashboard.Datum, error) {
	filters := agentVisualFilters(input.Filters)
	if shape == "binned_measure" {
		if p.Histogram == nil {
			return nil, fmt.Errorf("histogram query provider is not configured")
		}
		binCount := input.Presentation.HistogramBins
		if binCount <= 0 {
			binCount = 20
		}
		binCount = max(5, min(60, binCount))
		bins, err := p.Histogram(ctx, projectID, input.Model, reportdef.RawValueQuery{
			Table: input.Dataset, Measure: reportdef.QueryField{Field: input.Measures[0].Field, Alias: "value"},
			Filters: filters,
		}, binCount)
		if err != nil {
			return nil, err
		}
		out := make([]dashboard.Datum, 0, len(bins))
		for _, bin := range bins {
			out = append(out, dashboard.Datum{"label": fmt.Sprintf("%g–%g", bin.Start, bin.End), "binStart": bin.Start, "binEnd": bin.End, "value": bin.Count})
		}
		return out, nil
	}
	if shape == "distribution" {
		if p.Distribution == nil {
			return nil, fmt.Errorf("distribution query provider is not configured")
		}
		rows, err := p.Distribution(ctx, projectID, input.Model, reportdef.RawValueQuery{
			Table:      input.Dataset,
			Dimensions: []reportdef.QueryField{{Field: input.Dimensions[0].Field, Alias: "label"}},
			Measure:    reportdef.QueryField{Field: input.Measures[0].Field, Alias: "value"},
			Filters:    filters,
		}, agentVisualSorts(input.Sort, input.Dimensions, input.Series, input.Measures), input.Limit)
		return agentDatums(rows), err
	}
	if p.AggregateRows == nil {
		return nil, fmt.Errorf("aggregate query provider is not configured")
	}
	if shape == "single_value" {
		rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
			Table:    input.Dataset,
			Measures: []reportdef.QueryField{{Field: input.Measures[0].Field, Alias: "value"}},
			Filters:  filters,
			Limit:    1,
		})
		if err != nil {
			return nil, err
		}
		value := any(nil)
		if len(rows) > 0 {
			value = agentRowValue(rows[0], "value", input.Measures[0])
		}
		return []dashboard.Datum{{"label": firstNonEmpty(input.Title, measureLabelForAgent(input.Measures[0].Field, mustResolveMeasure(model, input.Measures[0].Field))), "value": value}}, nil
	}
	if shape == "category_multi_measure" || len(input.Measures) > 1 {
		if shape == "ohlc" {
			aliases := []string{"open", "close", "low", "high"}
			measures := make([]reportdef.QueryField, len(input.Measures))
			for index, measure := range input.Measures {
				measures[index] = reportdef.QueryField{Field: measure.Field, Alias: aliases[index]}
			}
			rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
				Table: input.Dataset, Dimensions: []reportdef.QueryField{{Field: input.Dimensions[0].Field, Alias: "label"}},
				Measures: measures, Filters: filters,
				Sort: agentVisualSorts(input.Sort, input.Dimensions, input.Series, input.Measures), Limit: input.Limit,
			})
			return agentDatums(rows), err
		}
		out := []dashboard.Datum{}
		for _, measureRef := range input.Measures {
			rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
				Table:      input.Dataset,
				Dimensions: []reportdef.QueryField{{Field: input.Dimensions[0].Field, Alias: "label"}},
				Measures:   []reportdef.QueryField{{Field: measureRef.Field, Alias: "value"}},
				Filters:    filters,
				Sort:       agentVisualSorts(input.Sort, input.Dimensions, input.Series, []agentVisualFieldRef{measureRef}),
				Limit:      input.Limit,
			})
			if err != nil {
				return nil, err
			}
			measure, _ := model.ResolveMeasure(measureRef.Field)
			for _, row := range rows {
				out = append(out, dashboard.Datum{
					"label":  agentRowValue(row, "label", input.Dimensions[0]),
					"series": measureLabelForAgent(measureRef.Field, measure),
					"value":  agentRowValue(row, "value", measureRef),
				})
			}
		}
		return out, nil
	}
	if shape == "hierarchy" {
		dimensions := make([]reportdef.QueryField, len(input.Dimensions))
		for index, dimension := range input.Dimensions {
			dimensions[index] = reportdef.QueryField{Field: dimension.Field, Alias: fmt.Sprintf("level_%d", index)}
		}
		rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
			Table: input.Dataset, Dimensions: dimensions, Measures: []reportdef.QueryField{{Field: input.Measures[0].Field, Alias: "value"}},
			Filters: filters, Limit: input.Limit,
		})
		if err != nil {
			return nil, err
		}
		out := make([]dashboard.Datum, 0, len(rows))
		for _, row := range rows {
			path := make([]string, 0, len(dimensions))
			for index := range dimensions {
				if value := fmt.Sprint(row[fmt.Sprintf("level_%d", index)]); value != "" && value != "<nil>" {
					path = append(path, value)
				}
			}
			out = append(out, dashboard.Datum{"path": path, "value": row["value"]})
		}
		return out, nil
	}
	if shape == "matrix" || shape == "graph" {
		left, right := "row", "column"
		if shape == "graph" {
			left, right = "source", "target"
		}
		rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
			Table:      input.Dataset,
			Dimensions: []reportdef.QueryField{{Field: input.Dimensions[0].Field, Alias: left}, {Field: input.Dimensions[1].Field, Alias: right}},
			Measures:   []reportdef.QueryField{{Field: input.Measures[0].Field, Alias: "value"}},
			Filters:    filters, Limit: input.Limit,
		})
		return agentDatums(rows), err
	}
	if shape == "geo" {
		rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
			Table: input.Dataset, Dimensions: []reportdef.QueryField{{Field: input.Dimensions[0].Field, Alias: "name"}},
			Measures: []reportdef.QueryField{{Field: input.Measures[0].Field, Alias: "value"}},
			Filters:  filters, Limit: input.Limit,
		})
		return agentDatums(rows), err
	}
	dimensions := []reportdef.QueryField{{Field: input.Dimensions[0].Field, Alias: "label"}}
	if input.Series != nil && input.Series.Field != "" {
		dimensions = append(dimensions, reportdef.QueryField{Field: input.Series.Field, Alias: "series"})
	}
	rows, err := p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
		Table:      input.Dataset,
		Dimensions: dimensions,
		Measures:   []reportdef.QueryField{{Field: input.Measures[0].Field, Alias: "value"}},
		Filters:    filters,
		Sort:       agentVisualSorts(input.Sort, input.Dimensions, input.Series, input.Measures),
		Limit:      input.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]dashboard.Datum, 0, len(rows))
	for _, row := range rows {
		datum := dashboard.Datum{"label": agentRowValue(row, "label", input.Dimensions[0]), "value": agentRowValue(row, "value", input.Measures[0])}
		if input.Series != nil && input.Series.Field != "" {
			datum["series"] = agentRowValue(row, "series", *input.Series)
		}
		out = append(out, datum)
	}
	if shape == "category_delta" {
		cumulative := 0.0
		for _, datum := range out {
			value := agentFloat(datum["value"])
			datum["start"] = cumulative
			cumulative += value
			datum["end"] = cumulative
			datum["positive"] = value >= 0
		}
	}
	return out, nil
}

func agentDatums(rows reportdef.QueryRows) []dashboard.Datum {
	out := make([]dashboard.Datum, 0, len(rows))
	for _, row := range rows {
		datum := dashboard.Datum{}
		for key, value := range row {
			datum[key] = value
		}
		out = append(out, datum)
	}
	return out
}

func agentFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func (p VisualProvider) queryAgentTable(ctx context.Context, projectID string, model *semanticmodel.Model, input agentVisualInput, id string) (agentVisualResult, error) {
	semanticModelID, err := projectgraph.NewResourceID(input.Model)
	if err != nil {
		return agentVisualResult{}, fmt.Errorf("semantic model ID: %w", err)
	}
	fields := input.Fields
	aggregate := len(fields) == 0 && (len(input.Rows) > 0 || len(input.Measures) > 0)
	if len(fields) == 0 {
		fields = append([]agentVisualFieldRef{}, input.Rows...)
		fields = append(fields, input.Measures...)
	}
	if len(fields) == 0 {
		return agentVisualResult{}, fmt.Errorf("table requires fields, or rows and measures")
	}
	dimensions, measures, columns, err := agentTableFields(model, fields, input.Columns)
	if err != nil {
		return agentVisualResult{}, err
	}
	var rows reportdef.QueryRows
	if aggregate {
		if p.AggregateRows == nil {
			return agentVisualResult{}, fmt.Errorf("aggregate query provider is not configured")
		}
		rows, err = p.AggregateRows(ctx, projectID, input.Model, reportdef.AggregateQuery{
			Table:      input.Dataset,
			Dimensions: dimensions,
			Measures:   measures,
			Filters:    agentVisualFilters(input.Filters),
			Sort:       agentTableSorts(input.Sort, fields),
			Limit:      input.Limit,
		})
	} else {
		if p.PreviewRows == nil {
			return agentVisualResult{}, fmt.Errorf("preview query provider is not configured")
		}
		rows, err = p.PreviewRows(ctx, projectID, input.Model, reportdef.RowQuery{
			Table:      input.Dataset,
			Dimensions: dimensions,
			Measures:   measures,
			Filters:    agentVisualFilters(input.Filters),
			Sort:       agentTableSorts(input.Sort, fields),
			Limit:      input.Limit,
		})
	}
	if err != nil {
		return agentVisualResult{}, err
	}
	tableRows := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, map[string]any(row))
	}
	title := firstNonEmpty(input.Title, "Table")
	sortSpec := dashboard.TableSort{}
	if len(input.Sort) > 0 {
		sortSpec = dashboard.TableSort{Key: agentFieldAlias(input.Sort[0].Field), Direction: normalizedSortDirection(input.Sort[0].Direction)}
	}
	authored := dashboardauthoring.TableVisual{Title: title, DefaultSort: sortSpec, Style: dashboard.TableStyle{}.WithDefaults(), Columns: columns, Query: dashboardauthoring.TableQuery{Table: input.Dataset}, Calculations: input.Calculations}
	for _, field := range fields {
		authored.DataColumns = append(authored.DataColumns, dashboardauthoring.FieldRef{Field: field.Field, Alias: agentFieldAliasForRef(field)})
	}
	if input.Type == "table" {
		for _, field := range fields {
			authored.Query.Fields = append(authored.Query.Fields, field.Field)
		}
	} else {
		for _, field := range dimensions {
			authored.Query.Rows = append(authored.Query.Rows, dashboardauthoring.FieldRef{Field: field.Field, Alias: field.Alias})
		}
		for _, field := range measures {
			authored.Query.Measures = append(authored.Query.Measures, dashboardauthoring.FieldRef{Field: field.Field, Alias: field.Alias})
		}
	}
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{
		ID: "agent", SemanticModel: semanticModelID,
		Visuals: dashboardauthoring.TabularVisualizations(input.Type, map[string]dashboardauthoring.TableVisual{id: authored}),
	}, model)
	if err != nil {
		return agentVisualResult{}, err
	}
	base, err := visualizationir.SpecificationBase(definitions[id].Spec)
	if err != nil {
		return agentVisualResult{}, err
	}
	limitReached := len(tableRows) >= input.Limit
	tableRows, columns, err = applyAgentTableCalculations(base, tableRows, columns, limitReached)
	if err != nil {
		return agentVisualResult{}, err
	}
	cardinality := dashboard.ExactCardinality(len(tableRows))
	if limitReached {
		cardinality = dashboard.LowerBoundCardinality(len(tableRows))
	}
	table := dashboard.Table{
		Version:       2,
		Kind:          map[string]string{"table": "data_table", "matrix": "matrix_table", "pivot": "pivot_table"}[input.Type],
		Title:         title,
		Style:         dashboard.TableStyle{}.WithDefaults(),
		Interaction:   dashboard.InteractionConfig{},
		Selection:     []dashboard.InteractionSelectionEntry{},
		Columns:       columns,
		Cardinality:   cardinality,
		AvailableRows: len(tableRows),
		IsCapped:      limitReached,
		RowCap:        maxVisualRows,
		ChunkSize:     dashboard.TableChunkSize,
		RowHeight:     dashboard.TableRowHeight,
		ResetVersion:  0,
		Sort:          sortSpec,
		Blocks: map[string]dashboard.TableBlock{
			"a": {Start: 0, RequestSeq: 0, ResetVersion: 0, Sort: sortSpec, Rows: tableRows},
		},
		LoadingBlock: "",
		Error:        "",
	}
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definitions[id], table, 1, 1)
	if err != nil {
		return agentVisualResult{}, err
	}
	return agentVisualResult{
		Type:    input.Type,
		ID:      id,
		Patch:   map[string]map[string]visualizationir.VisualizationEnvelope{"visuals": {id: envelope}},
		Summary: fmt.Sprintf("Created table %q with %d rows.", title, len(tableRows)),
	}, nil
}

func applyAgentTableCalculations(base visualizationir.VisualizationSpecBase, records []map[string]any, columns []dashboard.TableColumn, incomplete bool) ([]map[string]any, []dashboard.TableColumn, error) {
	if base.Calculations == nil || len(*base.Calculations) == 0 {
		return records, columns, nil
	}
	var schema *visualizationir.VisualizationDatasetSchema
	for index := range base.Datasets {
		if base.Datasets[index].ID == "primary" {
			schema = &base.Datasets[index]
			break
		}
	}
	if schema == nil {
		return nil, nil, fmt.Errorf("agent table visual calculation has no primary dataset")
	}
	sourceColumns := []string{}
	fields := map[string]visualizationir.VisualizationField{}
	for _, field := range schema.Fields {
		fields[field.ID] = field
		if field.Provenance == nil || field.Provenance.Kind != visualizationir.VisualizationFieldProvenanceKindVisualCalculation {
			sourceColumns = append(sourceColumns, field.ID)
		}
	}
	rows := make([][]any, len(records))
	for rowIndex, record := range records {
		rows[rowIndex] = make([]any, len(sourceColumns))
		for columnIndex, column := range sourceColumns {
			rows[rowIndex][columnIndex] = record[column]
		}
	}
	completeness := visualizationir.VisualizationCompletenessComplete
	if incomplete {
		completeness = visualizationir.VisualizationCompletenessTruncated
	} else if len(rows) == 0 {
		completeness = visualizationir.VisualizationCompletenessEmpty
	}
	frame, _, err := visualizationruntime.ApplyVisualCalculations(base, "primary", visualizationruntime.Frame{Columns: sourceColumns, Rows: rows, Completeness: completeness}, completeness)
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, len(records))
	for rowIndex, record := range records {
		next := make(map[string]any, len(frame.Columns))
		for key, value := range record {
			next[key] = value
		}
		for columnIndex, column := range frame.Columns {
			next[column] = frame.Rows[rowIndex][columnIndex]
		}
		out[rowIndex] = next
	}
	for _, calculation := range *base.Calculations {
		if calculation.Dataset != "primary" || calculation.Hidden {
			continue
		}
		field := fields[calculation.ID]
		format := "decimal"
		if field.Format != nil {
			if kind, kindErr := field.Format.Kind(); kindErr == nil {
				format = kind
			}
		}
		columns = append(columns, dashboard.TableColumn{Key: calculation.ID, Label: field.Label, Align: "right", Role: "measure", Measure: calculation.ID, Format: format})
	}
	return out, columns, nil
}

func agentTableFields(model *semanticmodel.Model, fields []agentVisualFieldRef, overrides []dashboard.TableColumn) ([]reportdef.QueryField, []reportdef.QueryField, []dashboard.TableColumn, error) {
	dimensions := []reportdef.QueryField{}
	measures := []reportdef.QueryField{}
	columns := make([]dashboard.TableColumn, 0, len(fields))
	overrideByKey := map[string]dashboard.TableColumn{}
	for _, column := range overrides {
		if column.Key != "" {
			overrideByKey[column.Key] = column
		}
	}
	for _, field := range fields {
		if strings.TrimSpace(field.Field) == "" {
			return nil, nil, nil, fmt.Errorf("table field is required")
		}
		alias := agentFieldAliasForRef(field)
		if dimension, err := model.ResolveDimension(field.Field); err == nil {
			dimensions = append(dimensions, reportdef.QueryField{Field: field.Field, Alias: alias})
			columns = append(columns, mergeAgentTableColumn(dashboard.TableColumn{Key: alias, Label: dimensionLabelForAgent(alias, dimension), Format: "text"}, overrideByKey[alias]))
			continue
		}
		measure, err := model.ResolveMeasure(field.Field)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unknown field %q", field.Field)
		}
		measures = append(measures, reportdef.QueryField{Field: field.Field, Alias: alias})
		columns = append(columns, mergeAgentTableColumn(dashboard.TableColumn{Key: alias, Label: measureLabelForAgent(field.Field, measure), Align: "right", Role: "measure", Measure: alias, Format: measure.Format}, overrideByKey[alias]))
	}
	return dimensions, measures, columns, nil
}

func agentVisualLimit(limit int) int {
	if limit <= 0 || limit > maxVisualRows {
		return maxVisualRows
	}
	return limit
}

func agentVisualSorts(sorts []agentVisualSort, dimensions []agentVisualFieldRef, series *agentVisualFieldRef, measures []agentVisualFieldRef) []reportdef.QuerySort {
	if len(sorts) == 0 {
		return []reportdef.QuerySort{{Field: "label", Direction: "asc"}}
	}
	out := make([]reportdef.QuerySort, 0, len(sorts))
	for _, sortSpec := range sorts {
		field := sortSpec.Field
		if agentSortMatches(field, dimensions) {
			field = "label"
		} else if series != nil && agentSortMatches(field, []agentVisualFieldRef{*series}) {
			field = "series"
		} else if agentSortMatches(field, measures) {
			field = "value"
		}
		out = append(out, reportdef.QuerySort{Field: field, Direction: normalizedSortDirection(sortSpec.Direction)})
	}
	return out
}

func agentTableSorts(sorts []agentVisualSort, fields []agentVisualFieldRef) []reportdef.QuerySort {
	out := make([]reportdef.QuerySort, 0, len(sorts))
	for _, sortSpec := range sorts {
		field := agentFieldAlias(sortSpec.Field)
		for _, ref := range fields {
			if sortSpec.Field == ref.Field || sortSpec.Field == ref.Alias {
				field = agentFieldAliasForRef(ref)
				break
			}
		}
		out = append(out, reportdef.QuerySort{Field: field, Direction: normalizedSortDirection(sortSpec.Direction)})
	}
	return out
}

func agentSortMatches(field string, refs []agentVisualFieldRef) bool {
	for _, ref := range refs {
		if field == ref.Field || field == ref.Alias || field == agentFieldAlias(ref.Field) {
			return true
		}
	}
	return false
}

func agentRowValue(row map[string]any, alias string, ref agentVisualFieldRef) any {
	for _, key := range []string{alias, ref.Alias, agentFieldAlias(ref.Field), ref.Field} {
		if key == "" {
			continue
		}
		if value, ok := row[key]; ok {
			return value
		}
	}
	return nil
}

func normalizedSortDirection(direction string) string {
	if strings.ToLower(direction) == "desc" {
		return "desc"
	}
	return "asc"
}

func agentVisualID(seed string) string {
	suffix := sanitizeAgentVisualIDSeed(seed)
	if suffix == "" {
		suffix = randomAgentVisualIDSuffix()
	}
	return "agent_visual_" + suffix
}

func sanitizeAgentVisualIDSeed(seed string) string {
	seed = strings.TrimSpace(strings.ToLower(seed))
	var b strings.Builder
	for _, r := range seed {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "_-")
}

func randomAgentVisualIDSuffix() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(bytes[:])
}

func displayAgentFields(fields []agentVisualFieldRef) []string {
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = displayAgentField(field)
	}
	return out
}

func displayAgentField(field agentVisualFieldRef) string {
	if field.Alias != "" {
		return field.Alias
	}
	return agentFieldAlias(field.Field)
}

func agentVisualSeries(series *agentVisualFieldRef) []string {
	if series == nil || series.Field == "" {
		return []string{}
	}
	return []string{displayAgentField(*series)}
}

func agentFieldAliasForRef(field agentVisualFieldRef) string {
	if field.Alias != "" {
		return field.Alias
	}
	return agentFieldAlias(field.Field)
}

func agentFieldAlias(field string) string {
	parts := strings.Split(field, ".")
	return parts[len(parts)-1]
}

func dimensionLabelForAgent(fallback string, dimension semanticmodel.MetricDimension) string {
	if dimension.Label != "" {
		return dimension.Label
	}
	return fallback
}

func measureLabelForAgent(fallback string, measure semanticmodel.MetricMeasure) string {
	if measure.Label != "" {
		return measure.Label
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mustResolveMeasure(model *semanticmodel.Model, field string) semanticmodel.MetricMeasure {
	measure, _ := model.ResolveMeasure(field)
	return measure
}

func mergeAgentTableColumn(base, override dashboard.TableColumn) dashboard.TableColumn {
	if override.Key != "" {
		base.Key = override.Key
	}
	if override.Label != "" {
		base.Label = override.Label
	}
	if override.Align != "" {
		base.Align = override.Align
	}
	if override.Role != "" {
		base.Role = override.Role
	}
	if override.Group != "" {
		base.Group = override.Group
	}
	if override.Measure != "" {
		base.Measure = override.Measure
	}
	if override.ColumnValue != "" {
		base.ColumnValue = override.ColumnValue
	}
	if override.Width > 0 {
		base.Width = override.Width
	}
	if override.Format != "" {
		base.Format = override.Format
	}
	if len(override.Formatting) > 0 {
		base.Formatting = append([]dashboard.TableFormattingRule{}, override.Formatting...)
	}
	return base
}
