package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
	toon "github.com/toon-format/toon-go"
)

const (
	agentVisualToolName = QueryVisualToolName
	maxVisualRows       = 50
	maxVisualBytes      = 8 << 20
	maxVisualModelBytes = 48 << 10
)

type VisualAuthorizeFunc func(ctx context.Context, scope Scope, request VisualAuthorizationRequest) (agentcore.ToolResult, bool)

type VisualQueryContextFunc func(ctx context.Context, scope Scope) context.Context

type VisualModelFunc func(projectID, modelID string) (*semanticmodel.Model, bool)

// VisualDefinitionQueryFunc is the single canonical execution seam for agent
// visuals. Implementations execute the supplied compiler definition through
// the active runtime, preserving its exact bindings, result shape, and
// envelope framing.
type VisualDefinitionQueryFunc func(ctx context.Context, projectID string, definition dashboarddefinition.Definition, pageID, visualID string, filters dashboard.Filters) (visualizationir.VisualizationEnvelope, error)

type VisualQueryMetadata struct {
	ServingSnapshot string
	Freshness       *agentcontracts.QueryFreshness
}

type VisualQueryMetadataFunc func(ctx context.Context, projectID, modelID string) VisualQueryMetadata

type VisualProvider struct {
	Authorize       VisualAuthorizeFunc
	Resolve         ResourceResolver
	QueryContext    VisualQueryContextFunc
	SemanticModel   VisualModelFunc
	QueryDefinition VisualDefinitionQueryFunc
	QueryMetadata   VisualQueryMetadataFunc
}

type VisualAuthorizationRequest struct {
	ToolName string
	CallID   string
	Type     string
	Model    string
	Dataset  string
}

type agentVisualInput struct {
	SemanticModelID string                              `json:"semanticModelId"`
	Model           string                              `json:"-"`
	Visual          dashboarddocument.DashboardVisual   `json:"visual"`
	Filters         []dashboarddocument.DashboardFilter `json:"filters"`
}

type agentVisualFieldRef struct {
	Field string
	Alias string
}

func agentVisualType(input agentVisualInput) string { return string(input.Visual.Type) }

func agentDefinitionDataset(definition visualizationdefinition.Definition) string {
	if definition.Query.Aggregate != nil {
		return definition.Query.Aggregate.TableID
	}
	if definition.Query.Detail != nil {
		return definition.Query.Detail.TableID
	}
	if definition.Query.Matrix != nil {
		return definition.Query.Matrix.TableID
	}
	if definition.Query.Pivot != nil {
		return definition.Query.Pivot.TableID
	}
	if definition.Query.Spatial != nil {
		return definition.Query.Spatial.TableID
	}
	return definition.Query.DatasetID
}

func agentDefinitionLimit(definition visualizationdefinition.Definition) int {
	limit := int64(maxVisualRows)
	switch {
	case definition.Query.Aggregate != nil:
		limit = definition.Query.Aggregate.Limit
	case definition.Query.Detail != nil:
		limit = definition.Query.Detail.Limit
	case definition.Query.Matrix != nil:
		limit = definition.Query.Matrix.Limit
	case definition.Query.Pivot != nil:
		limit = definition.Query.Pivot.Limit
	case definition.Query.Spatial != nil:
		limit = definition.Query.Spatial.Limit
	}
	if limit <= 0 || limit > int64(maxVisualRows) {
		limit = maxVisualRows
	}
	if base, err := visualizationir.SpecificationBase(definition.Spec); err == nil && base.DataBudget.MaxRows > 0 && limit > int64(base.DataBudget.MaxRows) {
		limit = int64(base.DataBudget.MaxRows)
	}
	return int(limit)
}

func agentDefinitionTitle(definition visualizationdefinition.Definition) string {
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err == nil && strings.TrimSpace(base.Title) != "" {
		return base.Title
	}
	return definition.ID
}

type agentVisualResult struct {
	Type    string                                                      `json:"type"`
	ID      string                                                      `json:"id"`
	Patch   map[string]map[string]visualizationir.VisualizationEnvelope `json:"patch"`
	Result  *agentcontracts.QueryVisualResult                           `json:"result,omitempty"`
	Filters dashboard.Filters                                           `json:"-"`
	Summary string                                                      `json:"summary"`
}

func (p VisualProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{{
		Name:         agentVisualToolName,
		Description:  "Create one read-only visual from LeapView semantic model fields. semanticModelId must be the exact semantic-model ref.id returned by catalog search or list; short names and project-qualified dashboard stable IDs are not accepted. Data is queried from semantic models; do not provide inline data.",
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
	if p.SemanticModel == nil {
		return apigenAgentToolError("catalog_unavailable", "semantic model provider is not configured")
	}
	model, ok := p.SemanticModel(runScope.ProjectID, input.Model)
	if !ok || model == nil {
		return apigenAgentToolError("catalog_not_found", "semantic model is unknown or unauthorized")
	}
	if err := validateAgentRecordsFields(input.Visual); err != nil {
		return apigenAgentToolError("query_visual_failed", err.Error())
	}
	visualID := agentVisualID(call.ID)
	dashboardDefinition, err := compileAgentVisual(input, model, visualID)
	if err != nil {
		return apigenAgentToolError("query_visual_failed", err.Error())
	}
	definition, ok := dashboardDefinition.Visualizations[visualID]
	if !ok {
		return apigenAgentToolError("query_visual_failed", "compiled agent visualization is missing")
	}
	datasetID := agentDefinitionDataset(definition)
	metadata := dataquery.Metadata{
		Surface:     dataquery.SurfaceAgent,
		Operation:   dataquery.OperationAgentQuery,
		PrincipalID: scope.PrincipalID,
		ObjectType:  "semantic_dataset",
		ObjectID:    input.Model + ":" + datasetID,
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
			Type:     agentVisualType(input),
			Model:    input.Model,
			Dataset:  datasetID,
		}); !ok {
			return errResult
		}
	}
	if p.QueryMetadata == nil {
		return apigenAgentToolError("query_visual_failed", "serving snapshot is unavailable")
	}
	queryMetadata := p.QueryMetadata(ctx, runScope.ProjectID, input.Model)
	if strings.TrimSpace(queryMetadata.ServingSnapshot) == "" {
		return apigenAgentToolError("query_visual_failed", "serving snapshot is unavailable")
	}
	result, err := p.queryAgentVisual(ctx, runScope.ProjectID, input, visualID, model, dashboardDefinition, definition)
	if err != nil {
		return apigenAgentToolError("query_visual_failed", err.Error())
	}
	if envelope, ok := result.Patch["visuals"][result.ID]; ok {
		trimmed := trimAgentVisualEnvelopeRows(&envelope, agentDefinitionLimit(definition))
		if limit := agentDefinitionLimit(definition); trimmed || (limit > 0 && agentVisualReturnedRows(envelope) >= limit) {
			envelope.Diagnostics = append(envelope.Diagnostics, visualizationir.VisualizationDiagnostic{Code: "agent_row_limit_reached", Severity: visualizationir.VisualizationDiagnosticSeverityWarning, Message: fmt.Sprintf("Showing up to %d rows. More data may exist.", limit)})
			result.Patch["visuals"][result.ID] = envelope
		}
	}
	compact, err := compactAgentVisualResult(runScope.ProjectID, call.ID, queryMetadata, model, input, dashboardDefinition, definition, result)
	if err != nil {
		return apigenAgentToolError("query_visual_failed", err.Error())
	}
	result.Result = &compact
	modelContent := compactAgentVisualModelResult(compact, result.Patch["visuals"][result.ID], agentDefinitionLimit(definition))
	return agentcore.ToolResult{
		Content:        compact,
		ModelContent:   modelContent,
		DisplayContent: result,
	}
}

type agentVisualModelProjection struct {
	agentcontracts.QueryVisualResult
	Columns          []string                         `json:"columns"`
	Rows             [][]any                          `json:"rows"`
	DataCompleteness agentVisualModelDataCompleteness `json:"dataCompleteness"`
}

type agentVisualModelDataCompleteness struct {
	ReturnedRows      int32  `json:"returnedRows"`
	PrimaryResultRows int32  `json:"primaryResultRows"`
	Status            string `json:"status"`
}

func compactAgentVisualModelResult(compact agentcontracts.QueryVisualResult, envelope visualizationir.VisualizationEnvelope, rowLimit int) any {
	columns, sourceRows, available := agentVisualPrimaryInlineRows(envelope)
	availableRows := len(sourceRows)
	if rowLimit <= 0 || rowLimit > maxVisualRows {
		rowLimit = maxVisualRows
	}
	if len(sourceRows) > rowLimit {
		sourceRows = sourceRows[:rowLimit]
	}
	rows := cloneAgentVisualRows(sourceRows)
	projection := agentVisualModelProjection{
		QueryVisualResult: compact,
		Columns:           columns,
		Rows:              rows,
		DataCompleteness: agentVisualModelDataCompleteness{
			PrimaryResultRows: int32(availableRows),
			Status:            agentVisualModelDataStatus(available, availableRows, len(rows), compact.Completeness.Status),
		},
	}
	initialRowCount := len(projection.Rows)
	low, high := 0, initialRowCount
	for low < high {
		middle := low + (high-low+1)/2
		candidate := projection
		candidate.Rows = projection.Rows[:middle]
		if agentVisualModelProjectionBytes(candidate) <= maxVisualModelBytes {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low < initialRowCount {
		projection.Rows = projection.Rows[:low]
		projection.DataCompleteness.Status = "truncated"
	}
	projection.DataCompleteness.ReturnedRows = int32(len(projection.Rows))
	if agentVisualModelProjectionBytes(projection) > maxVisualModelBytes {
		// Preserve the existing compact result if even its row-free projection
		// cannot fit the model result budget.
		return compact
	}
	return projection
}

func agentVisualPrimaryInlineRows(envelope visualizationir.VisualizationEnvelope) ([]string, [][]any, bool) {
	state, ok := envelope.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || state == nil {
		return []string{}, [][]any{}, false
	}
	for _, dataset := range state.Datasets {
		if dataset.ID != "primary" {
			continue
		}
		columns := append([]string{}, dataset.Columns...)
		return columns, dataset.Rows, true
	}
	return []string{}, [][]any{}, false
}

func agentVisualModelDataStatus(available bool, sourceRows, returnedRows int, completeness string) string {
	if !available {
		return "unavailable"
	}
	if sourceRows == 0 {
		return "empty"
	}
	if returnedRows < sourceRows {
		return "truncated"
	}
	if completeness == "limit_reached" {
		return "limit_reached"
	}
	return "complete"
}

func cloneAgentVisualRows(rows [][]any) [][]any {
	cloned := make([][]any, len(rows))
	for index, row := range rows {
		cloned[index] = append([]any(nil), row...)
	}
	return cloned
}

func agentVisualModelProjectionBytes(projection agentVisualModelProjection) int {
	jsonBody, jsonErr := json.Marshal(projection)
	if jsonErr != nil {
		return maxVisualModelBytes + 1
	}
	decoder := json.NewDecoder(strings.NewReader(string(jsonBody)))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return maxVisualModelBytes + 1
	}
	toonBody, toonErr := toon.MarshalString(normalized)
	if toonErr != nil {
		return maxVisualModelBytes + 1
	}
	return max(len(jsonBody), len(toonBody))
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
	input.SemanticModelID = strings.TrimSpace(input.SemanticModelID)
	input.Model = input.SemanticModelID
	if strings.TrimSpace(agentVisualType(input)) == "" {
		return agentVisualInput{}, fmt.Errorf("visual.type is required")
	}
	if input.SemanticModelID == "" {
		return agentVisualInput{}, fmt.Errorf("semanticModelId is required")
	}
	return input, nil
}

func compileAgentVisual(input agentVisualInput, model *semanticmodel.Model, id string) (dashboarddefinition.Definition, error) {
	if model == nil {
		return dashboarddefinition.Definition{}, fmt.Errorf("semantic model is required")
	}
	input, err := normalizeAgentVisualInput(input)
	if err != nil {
		return dashboarddefinition.Definition{}, err
	}
	doc := agentVisualDocument(input, id, input.Model)
	compiled, err := dashboardcompiler.CompileDocument(doc, map[string]*semanticmodel.Model{input.Model: model})
	if err != nil {
		return dashboarddefinition.Definition{}, fmt.Errorf("compile agent visualization: %w", err)
	}
	if _, ok := compiled.Definition.Visualizations[id]; !ok {
		return dashboarddefinition.Definition{}, fmt.Errorf("compiled agent visualization %q is missing", id)
	}
	if err := validateAgentVisualBudget(compiled.Definition.Visualizations[id]); err != nil {
		return dashboarddefinition.Definition{}, err
	}
	return compiled.Definition, nil
}

func normalizeAgentVisualInput(input agentVisualInput) (agentVisualInput, error) {
	budget := int32(maxVisualRows)
	if input.Visual.DataBudget == nil {
		partial := visualizationir.VisualizationCompletenessPartial
		input.Visual.DataBudget = &dashboarddocument.DashboardDataBudget{MaxRows: budget, RequiredCompleteness: &partial}
	} else {
		budget = input.Visual.DataBudget.MaxRows
	}
	if budget <= 0 {
		return agentVisualInput{}, fmt.Errorf("visual.dataBudget.maxRows must be between 1 and %d", maxVisualRows)
	}
	if budget > maxVisualRows {
		return agentVisualInput{}, fmt.Errorf("visual.dataBudget.maxRows %d exceeds the agent limit %d; set it to at most %d", budget, maxVisualRows, maxVisualRows)
	}
	var err error
	input.Visual.Query, err = agentVisualQueryWithDefaultLimit(input.Visual.Query, budget, "visual.query")
	if err != nil {
		return agentVisualInput{}, err
	}
	if input.Visual.Datasets != nil {
		queries := make(map[string]dashboarddocument.DashboardQuery, len(*input.Visual.Datasets))
		for datasetID, query := range *input.Visual.Datasets {
			queries[datasetID], err = agentVisualQueryWithDefaultLimit(query, budget, fmt.Sprintf("visual.datasets[%q]", datasetID))
			if err != nil {
				return agentVisualInput{}, err
			}
		}
		input.Visual.Datasets = &queries
	}
	return input, nil
}

func agentVisualQueryWithDefaultLimit(query dashboarddocument.DashboardQuery, budget int32, path string) (dashboarddocument.DashboardQuery, error) {
	check := func(limit int32, field string) error {
		if limit <= 0 {
			return fmt.Errorf("%s.%s must be a positive integer no greater than visual.dataBudget.maxRows (%d)", path, field, budget)
		}
		if limit > budget {
			return fmt.Errorf("%s.%s %d exceeds visual.dataBudget.maxRows %d; lower the query limit or raise maxRows up to %d", path, field, limit, budget, maxVisualRows)
		}
		return nil
	}
	switch value := query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		if value == nil {
			return query, nil
		}
		copy := *value
		if copy.Limit == nil {
			copy.Limit = &budget
		}
		if err := check(*copy.Limit, "limit"); err != nil {
			return dashboarddocument.DashboardQuery{}, err
		}
		query.Value = &copy
	case *dashboarddocument.RecordsDashboardQuery:
		if value == nil {
			return query, nil
		}
		copy := *value
		if copy.Limit == nil {
			copy.Limit = &budget
		}
		if err := check(*copy.Limit, "limit"); err != nil {
			return dashboarddocument.DashboardQuery{}, err
		}
		query.Value = &copy
	case *dashboarddocument.DistributionDashboardQuery:
		if value == nil {
			return query, nil
		}
		copy := *value
		if copy.Limit == nil {
			copy.Limit = &budget
		}
		if err := check(*copy.Limit, "limit"); err != nil {
			return dashboarddocument.DashboardQuery{}, err
		}
		query.Value = &copy
	case *dashboarddocument.PivotDashboardQuery:
		if value == nil {
			return query, nil
		}
		copy := *value
		if copy.Window == nil {
			copy.Window = &dashboarddocument.DashboardPivotWindow{Limit: budget}
		} else {
			window := *copy.Window
			copy.Window = &window
		}
		if err := check(copy.Window.Limit, "window.limit"); err != nil {
			return dashboarddocument.DashboardQuery{}, err
		}
		if copy.Window.Offset != nil && *copy.Window.Offset < 0 {
			return dashboarddocument.DashboardQuery{}, fmt.Errorf("%s.window.offset must not be negative", path)
		}
		if copy.Window.Offset != nil && int64(*copy.Window.Offset)+int64(copy.Window.Limit) > int64(budget) {
			return dashboarddocument.DashboardQuery{}, fmt.Errorf("%s.window offset plus limit exceeds visual.dataBudget.maxRows %d; reduce the offset or limit", path, budget)
		}
		query.Value = &copy
	}
	return query, nil
}

func validateAgentVisualBudget(definition visualizationdefinition.Definition) error {
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil {
		return fmt.Errorf("inspect visualization budget: %w", err)
	}
	if base.DataBudget.MaxRows <= 0 {
		return fmt.Errorf("visualization %q has no positive row budget", definition.ID)
	}
	if base.DataBudget.MaxRows > maxVisualRows {
		return fmt.Errorf("visualization %q row budget %d exceeds agent limit %d", definition.ID, base.DataBudget.MaxRows, maxVisualRows)
	}
	check := func(datasetID string, binding visualizationdefinition.QueryBinding) error {
		limit, offset, tiled := agentDefinitionQueryWindow(binding)
		if tiled {
			return fmt.Errorf("visualization %q %s query uses unbounded spatial tiles", definition.ID, datasetID)
		}
		if !tiled && limit > int64(maxVisualRows) {
			return fmt.Errorf("visualization %q %s query limit %d exceeds agent limit %d", definition.ID, datasetID, limit, maxVisualRows)
		}
		if !tiled && (offset < 0 || offset+limit > int64(maxVisualRows)) {
			return fmt.Errorf("visualization %q %s query window exceeds agent limit %d", definition.ID, datasetID, maxVisualRows)
		}
		return nil
	}
	if err := check(definition.Query.DatasetID, definition.Query); err != nil {
		return err
	}
	for datasetID, query := range definition.SecondaryQueries {
		if err := check(datasetID, query); err != nil {
			return err
		}
	}
	return nil
}

func agentDefinitionQueryWindow(binding visualizationdefinition.QueryBinding) (limit, offset int64, tiled bool) {
	switch {
	case binding.Aggregate != nil:
		return binding.Aggregate.Limit, 0, false
	case binding.Detail != nil:
		return binding.Detail.Limit, 0, false
	case binding.Matrix != nil:
		return binding.Matrix.Limit, 0, false
	case binding.Pivot != nil:
		return binding.Pivot.Limit, binding.Pivot.Offset, false
	case binding.Spatial != nil:
		if binding.Spatial.Tiles != nil {
			return 0, 0, true
		}
		return binding.Spatial.Limit, 0, false
	default:
		return 0, 0, false
	}
}

func (p VisualProvider) queryAgentVisual(ctx context.Context, projectID string, input agentVisualInput, id string, model *semanticmodel.Model, dashboardDefinition dashboarddefinition.Definition, definition visualizationdefinition.Definition) (agentVisualResult, error) {
	if model == nil {
		return agentVisualResult{}, fmt.Errorf("semantic model is required")
	}
	if agentDefinitionDataset(definition) == "" {
		return agentVisualResult{}, fmt.Errorf("compiled visual query has no dataset")
	}
	if p.QueryDefinition == nil {
		return agentVisualResult{}, fmt.Errorf("canonical visualization runtime is not configured")
	}
	filters := dashboardDefinition.DefaultFilters()
	ctx = dataquery.WithIndependentResultBudget(ctx, dataquery.ResultLimits{MaxRows: agentDefinitionLimit(definition) + 1, MaxBytes: maxVisualBytes})
	envelope, err := p.QueryDefinition(ctx, projectID, dashboardDefinition, "page", id, filters)
	if err != nil {
		return agentVisualResult{}, err
	}
	return agentVisualResult{Type: agentVisualType(input), ID: id, Filters: filters, Patch: map[string]map[string]visualizationir.VisualizationEnvelope{"visuals": {id: envelope}}, Summary: fmt.Sprintf("Created visual %q.", agentDefinitionTitle(definition))}, nil
}

func trimAgentVisualEnvelopeRows(envelope *visualizationir.VisualizationEnvelope, maxRows int) bool {
	if envelope == nil || maxRows < 0 {
		return false
	}
	trimmed := false
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		if state == nil {
			return false
		}
		copy := *state
		copy.Datasets = append([]visualizationir.VisualizationInlineDataset(nil), state.Datasets...)
		for index := range copy.Datasets {
			if copy.Datasets[index].ID != "primary" {
				continue
			}
			if len(copy.Datasets[index].Rows) > maxRows {
				copy.Datasets[index].Rows = append([][]any(nil), copy.Datasets[index].Rows[:maxRows]...)
				trimmed = true
			}
		}
		envelope.DataState.Value = &copy
	case *visualizationir.WindowedVisualizationDataState:
		if state == nil {
			return false
		}
		copy := *state
		copy.Blocks = make(map[string]visualizationir.VisualizationWindowBlock, len(state.Blocks))
		blockIDs := make([]string, 0, len(state.Blocks))
		for id, block := range state.Blocks {
			copy.Blocks[id] = block
			blockIDs = append(blockIDs, id)
		}
		sort.Slice(blockIDs, func(i, j int) bool {
			left, right := copy.Blocks[blockIDs[i]], copy.Blocks[blockIDs[j]]
			if left.Start == right.Start {
				return blockIDs[i] < blockIDs[j]
			}
			return left.Start < right.Start
		})
		remaining := maxRows
		for _, id := range blockIDs {
			block := copy.Blocks[id]
			if len(block.Rows) > remaining {
				block.Rows = append([][]any(nil), block.Rows[:remaining]...)
				remaining = 0
				trimmed = true
			} else {
				remaining -= len(block.Rows)
			}
			copy.Blocks[id] = block
		}
		envelope.DataState.Value = &copy
	}
	return trimmed
}

func compactAgentVisualResult(
	projectID string,
	queryID string,
	metadata VisualQueryMetadata,
	model *semanticmodel.Model,
	input agentVisualInput,
	dashboardDefinition dashboarddefinition.Definition,
	definition visualizationdefinition.Definition,
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
	} else if agentDefinitionLimit(definition) > 0 && returnedRows >= agentDefinitionLimit(definition) {
		completenessStatus = "limit_reached"
	}
	fieldUsages := agentVisualFieldUsages(projectID, input.Model, model, definition)
	filterUsages, err := agentVisualFilterUsages(projectID, input.Model, dashboardDefinition, result.Filters)
	if err != nil {
		return agentcontracts.QueryVisualResult{}, err
	}
	return agentcontracts.QueryVisualResult{
		Ok:               envelope.Status.Kind != visualizationir.VisualizationStatusKindError,
		QueryID:          queryID,
		ServingSnapshot:  metadata.ServingSnapshot,
		Freshness:        metadata.Freshness,
		Type:             result.Type,
		ID:               result.ID,
		Title:            base.Title,
		SemanticModelRef: agentVisualCatalogRef(projectID, "semantic_model", input.Model),
		DatasetID:        agentDefinitionDataset(definition),
		Fields:           fieldUsages,
		Filters:          filterUsages,
		Completeness: agentcontracts.QueryVisualCompleteness{
			ReturnedRows: int32(returnedRows),
			Limit:        int32(agentDefinitionLimit(definition)),
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
			// The completeness contract describes the primary visual result.
			// Context/secondary datasets are auxiliary frames and must not
			// inflate the returned-row count (or make a bounded primary result
			// appear incomplete).
			if dataset.ID != "primary" {
				continue
			}
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

func agentVisualFieldUsages(projectID, modelID string, model *semanticmodel.Model, definition visualizationdefinition.Definition) []agentcontracts.QueryVisualFieldUsage {
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil {
		return nil
	}
	out := make([]agentcontracts.QueryVisualFieldUsage, 0)
	for _, dataset := range base.Datasets {
		if dataset.ID != "primary" {
			continue
		}
		for _, field := range dataset.Fields {
			role := "dimension"
			if field.Role == visualizationir.VisualizationFieldRoleMetric {
				role = "metric"
			}
			fieldID := field.ID
			if field.SourceRef != nil && *field.SourceRef != "" {
				fieldID = *field.SourceRef
			}
			ref := agentVisualFieldRef{Field: fieldID, Alias: field.ID}
			out = append(out, agentVisualFieldUsage(projectID, modelID, model, ref, role, agentDefinitionDataset(definition)))
		}
	}
	return out
}

func agentVisualFieldUsage(projectID, modelID string, model *semanticmodel.Model, ref agentVisualFieldRef, role, rootDataset string) agentcontracts.QueryVisualFieldUsage {
	usage := agentcontracts.QueryVisualFieldUsage{
		FieldID: qualifiedVisualFieldID(modelID, ref.Field),
		Role:    role,
		Alias:   optionalString(ref.Alias),
		Label:   agentFieldAliasForRef(ref),
	}
	if dimension, err := model.ResolveDimension(ref.Field); err == nil {
		usage.Label = dimensionLabelForAgent(agentFieldAliasForRef(ref), dimension)
		usage.DataType = optionalString(dimension.Type)
		if role == "dimension" {
			usage.ExplorerFieldID = optionalString(agentExplorerDimensionField(model, rootDataset, dimension.Field))
		}
		return usage
	}
	if dimension, err := model.ResolveSemanticDimension(ref.Field); err == nil {
		usage.Label = firstNonEmpty(dimension.Label, ref.Field)
		usage.DataType = optionalString(dimension.Type)
		if role == "dimension" {
			if binding, ok := dimension.Bindings[rootDataset]; ok && len(binding.Path) == 0 {
				usage.ExplorerFieldID = optionalString(agentExplorerDimensionField(model, rootDataset, binding.Field))
			}
		}
		return usage
	}
	if metric, ok := model.Metrics[ref.Field]; ok {
		usage.Label = metricLabelForAgent(ref.Field, metric)
		usage.Unit = optionalString(metric.Unit)
		usage.Format = optionalString(metric.Format)
	}
	return usage
}

func agentExplorerDimensionField(model *semanticmodel.Model, rootDataset, field string) string {
	if model == nil || strings.TrimSpace(rootDataset) == "" {
		return ""
	}
	rootTable := strings.TrimSpace(rootDataset)
	if dataset, ok := model.Datasets[rootDataset]; ok && strings.TrimSpace(dataset.Model) != "" {
		rootTable = strings.TrimSpace(dataset.Model)
	}
	dimension, err := model.ResolveDimension(strings.TrimSpace(field))
	if err != nil || dimension.Table != rootTable {
		return ""
	}
	// Explorer field IDs are qualified by the semantic dataset ID, including
	// aliases whose backing model has a different name.
	return strings.TrimSpace(rootDataset) + "." + dimension.Name
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func agentVisualFilterUsages(projectID, modelID string, definition dashboarddefinition.Definition, filters dashboard.Filters) ([]agentcontracts.QueryVisualFilterUsage, error) {
	_ = projectID
	if filters.CompiledState == nil {
		return nil, nil
	}
	bindings := definition.CompiledFilterBindings()
	keys := append([]string(nil), definition.FilterOrder...)
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	unsortedStart := len(keys)
	for key := range bindings {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	sort.Strings(keys[unsortedStart:])
	out := make([]agentcontracts.QueryVisualFilterUsage, 0, len(keys))
	for _, key := range keys {
		binding, ok := bindings[key]
		if !ok {
			continue
		}
		applied, ok := filters.CompiledState.AppliedControls[key]
		if !ok || applied.Expression.Kind == dashboardfilter.ExpressionUnfiltered {
			continue
		}
		filterDefinition, ok := definition.FilterDefinitions[binding.Filter]
		if !ok {
			continue
		}
		expression := applied.Expression
		usage := agentcontracts.QueryVisualFilterUsage{FieldID: qualifiedVisualFieldID(modelID, filterDefinition.Field)}
		canonical, err := dashboardFilterExpression(expression)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", key, err)
		}
		usage.Expression = canonical
		if filterDefinition.Dataset != "" {
			resolved := filterDefinition.Dataset
			usage.ResolvedDatasetID = &resolved
		}
		out = append(out, usage)
	}
	return out, nil
}

func dashboardFilterExpression(expression dashboardfilter.Expression) (dashboarddocument.DashboardFilterExpression, error) {
	switch expression.Kind {
	case dashboardfilter.ExpressionUnfiltered:
		return dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.UnfilteredDashboardFilterExpression{Type: "unfiltered"}}, nil
	case dashboardfilter.ExpressionNullCheck:
		operator, err := dashboardFilterOperator(expression.Operator)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		return dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.NullCheckDashboardFilterExpression{Type: "nullCheck", Operator: operator}}, nil
	case dashboardfilter.ExpressionSet:
		operator, err := dashboardFilterOperator(expression.Operator)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		values, err := dashboardFilterValues(expression.Values)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		return dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.SetDashboardFilterExpression{Type: "set", Operator: operator, Values: values}}, nil
	case dashboardfilter.ExpressionComparison:
		operator, err := dashboardFilterOperator(expression.Operator)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		if expression.Value == nil {
			return dashboarddocument.DashboardFilterExpression{}, fmt.Errorf("comparison expression has no value")
		}
		value, err := dashboardFilterValue(*expression.Value)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		return dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.ComparisonDashboardFilterExpression{Type: "comparison", Operator: operator, Value: value}}, nil
	case dashboardfilter.ExpressionRange:
		lower, err := dashboardFilterBound(expression.Lower)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		upper, err := dashboardFilterBound(expression.Upper)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		return dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.RangeDashboardFilterExpression{Type: "range", Lower: lower, Upper: upper}}, nil
	case dashboardfilter.ExpressionRelativePeriod:
		direction, err := dashboardRelativeDirection(expression.Direction)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		unit, err := dashboardRelativeUnit(expression.Unit)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		anchor, err := dashboardRelativeAnchor(expression.Anchor)
		if err != nil {
			return dashboarddocument.DashboardFilterExpression{}, err
		}
		var anchorValue *dashboarddocument.DashboardFilterValue
		if expression.AnchorValue != nil {
			value, err := dashboardFilterValue(*expression.AnchorValue)
			if err != nil {
				return dashboarddocument.DashboardFilterExpression{}, err
			}
			anchorValue = &value
		}
		return dashboarddocument.DashboardFilterExpression{Value: &dashboarddocument.RelativePeriodDashboardFilterExpression{Type: "relativePeriod", Direction: direction, Count: int32(expression.Count), Unit: unit, IncludeCurrent: expression.IncludeCurrent, Anchor: anchor, AnchorValue: anchorValue}}, nil
	default:
		return dashboarddocument.DashboardFilterExpression{}, fmt.Errorf("unsupported expression kind %q", expression.Kind)
	}
}

func dashboardFilterBound(bound *dashboardfilter.Bound) (*dashboarddocument.DashboardFilterBound, error) {
	if bound == nil {
		return nil, nil
	}
	value, err := dashboardFilterValue(bound.Value)
	if err != nil {
		return nil, err
	}
	return &dashboarddocument.DashboardFilterBound{Value: value, Inclusive: bound.Inclusive}, nil
}

func dashboardFilterValues(values []dashboardfilter.Value) ([]dashboarddocument.DashboardFilterValue, error) {
	out := make([]dashboarddocument.DashboardFilterValue, 0, len(values))
	for _, value := range values {
		converted, err := dashboardFilterValue(value)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func dashboardFilterValue(value dashboardfilter.Value) (dashboarddocument.DashboardFilterValue, error) {
	switch value.Kind {
	case dashboardfilter.ValueString:
		return dashboarddocument.DashboardFilterValue{Value: &dashboarddocument.StringDashboardFilterValue{Type: "string", Value: fmt.Sprint(value.Value)}}, nil
	case dashboardfilter.ValueBoolean:
		boolean, ok := value.Value.(bool)
		if !ok {
			return dashboarddocument.DashboardFilterValue{}, fmt.Errorf("boolean value has type %T", value.Value)
		}
		return dashboarddocument.DashboardFilterValue{Value: &dashboarddocument.BooleanDashboardFilterValue{Type: "boolean", Value: boolean}}, nil
	case dashboardfilter.ValueInteger:
		return dashboarddocument.DashboardFilterValue{Value: &dashboarddocument.IntegerDashboardFilterValue{Type: "integer", Value: fmt.Sprint(value.Value)}}, nil
	case dashboardfilter.ValueDecimal:
		return dashboarddocument.DashboardFilterValue{Value: &dashboarddocument.DecimalDashboardFilterValue{Type: "decimal", Value: fmt.Sprint(value.Value)}}, nil
	case dashboardfilter.ValueDate:
		return dashboarddocument.DashboardFilterValue{Value: &dashboarddocument.DateDashboardFilterValue{Type: "date", Value: fmt.Sprint(value.Value)}}, nil
	case dashboardfilter.ValueTimestamp:
		return dashboarddocument.DashboardFilterValue{Value: &dashboarddocument.TimestampDashboardFilterValue{Type: "timestamp", Value: fmt.Sprint(value.Value)}}, nil
	default:
		return dashboarddocument.DashboardFilterValue{}, fmt.Errorf("unsupported value kind %q", value.Kind)
	}
}

func dashboardFilterOperator(operator dashboardfilter.Operator) (dashboarddocument.DashboardFilterOperator, error) {
	value := map[dashboardfilter.Operator]dashboarddocument.DashboardFilterOperator{
		dashboardfilter.OperatorIsNull: dashboarddocument.DashboardFilterOperatorIsNull, dashboardfilter.OperatorIsNotNull: dashboarddocument.DashboardFilterOperatorIsNotNull,
		dashboardfilter.OperatorIn: dashboarddocument.DashboardFilterOperatorIn, dashboardfilter.OperatorNotIn: dashboarddocument.DashboardFilterOperatorNotIn,
		dashboardfilter.OperatorEquals: dashboarddocument.DashboardFilterOperatorEquals, dashboardfilter.OperatorNotEquals: dashboarddocument.DashboardFilterOperatorNotEquals,
		dashboardfilter.OperatorContains: dashboarddocument.DashboardFilterOperatorContains, dashboardfilter.OperatorNotContains: dashboarddocument.DashboardFilterOperatorNotContains,
		dashboardfilter.OperatorStartsWith: dashboarddocument.DashboardFilterOperatorStartsWith, dashboardfilter.OperatorEndsWith: dashboarddocument.DashboardFilterOperatorEndsWith,
		dashboardfilter.OperatorGreaterThan: dashboarddocument.DashboardFilterOperatorGreaterThan, dashboardfilter.OperatorGreaterThanOrEqual: dashboarddocument.DashboardFilterOperatorGreaterThanOrEqual,
		dashboardfilter.OperatorLessThan: dashboarddocument.DashboardFilterOperatorLessThan, dashboardfilter.OperatorLessThanOrEqual: dashboarddocument.DashboardFilterOperatorLessThanOrEqual,
	}
	converted, ok := value[operator]
	if !ok {
		return "", fmt.Errorf("unsupported filter operator %q", operator)
	}
	return converted, nil
}

func dashboardRelativeDirection(direction dashboardfilter.RelativeDirection) (dashboarddocument.DashboardRelativeDirection, error) {
	converted := dashboarddocument.DashboardRelativeDirection(direction)
	if converted != dashboarddocument.DashboardRelativeDirectionPrevious && converted != dashboarddocument.DashboardRelativeDirectionCurrent && converted != dashboarddocument.DashboardRelativeDirectionNext {
		return "", fmt.Errorf("unsupported relative direction %q", direction)
	}
	return converted, nil
}

func dashboardRelativeUnit(unit dashboardfilter.RelativeUnit) (dashboarddocument.DashboardRelativeUnit, error) {
	switch unit {
	case dashboardfilter.UnitMinute:
		return dashboarddocument.DashboardRelativeUnitMinute, nil
	case dashboardfilter.UnitHour:
		return dashboarddocument.DashboardRelativeUnitHour, nil
	case dashboardfilter.UnitDay:
		return dashboarddocument.DashboardRelativeUnitDay, nil
	case dashboardfilter.UnitWeek:
		return dashboarddocument.DashboardRelativeUnitWeek, nil
	case dashboardfilter.UnitMonth:
		return dashboarddocument.DashboardRelativeUnitMonth, nil
	case dashboardfilter.UnitQuarter:
		return dashboarddocument.DashboardRelativeUnitQuarter, nil
	case dashboardfilter.UnitYear:
		return dashboarddocument.DashboardRelativeUnitYear, nil
	default:
		return "", fmt.Errorf("unsupported relative unit %q", unit)
	}
}

func dashboardRelativeAnchor(anchor dashboardfilter.RelativeAnchor) (dashboarddocument.DashboardRelativeAnchor, error) {
	switch anchor {
	case dashboardfilter.AnchorCurrentTime:
		return dashboarddocument.DashboardRelativeAnchorCurrentTime, nil
	case dashboardfilter.AnchorFirstAvailable:
		return dashboarddocument.DashboardRelativeAnchorFirstAvailable, nil
	case dashboardfilter.AnchorLastAvailable:
		return dashboarddocument.DashboardRelativeAnchorLastAvailable, nil
	case dashboardfilter.AnchorFixed:
		return dashboarddocument.DashboardRelativeAnchorFixed, nil
	default:
		return "", fmt.Errorf("unsupported relative anchor %q", anchor)
	}
}

func qualifiedVisualFieldID(modelID, field string) string {
	field = strings.TrimSpace(field)
	if field == "" || strings.Contains(field, ".") || strings.TrimSpace(modelID) == "" {
		return field
	}
	return strings.TrimSpace(modelID) + "." + field
}

func agentVisualDocument(input agentVisualInput, id, semanticModel string) dashboarddocument.DashboardDocument {
	// Preserve the supplied generated visual exactly. The wrapper only adds
	// synthetic document identity and layout required by CompileDocument.
	visual := input.Visual
	return dashboarddocument.DashboardDocument{
		APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1, Kind: dashboarddocument.DashboardResourceKindDashboard,
		Metadata: dashboarddocument.DashboardMetadata{ID: "dashboard:agent-visual", Name: "agent-visual"},
		Spec: dashboarddocument.DashboardSpec{SemanticModel: semanticModel, Filters: append([]dashboarddocument.DashboardFilter(nil), input.Filters...), Visuals: map[string]dashboarddocument.DashboardVisual{id: visual},
			Layout: &dashboarddocument.DashboardLayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}, Pages: []dashboarddocument.DashboardPage{{ID: "page", Title: "Page", Components: []dashboarddocument.DashboardPageComponent{{Value: &dashboarddocument.VisualDashboardPageComponent{DashboardPageComponentBase: dashboarddocument.DashboardPageComponentBase{ID: "visual", Type: "visual", Placement: dashboarddocument.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: id}}}}}},
	}
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

func metricLabelForAgent(fallback string, metric semanticmodel.Metric) string {
	if metric.Label != "" {
		return metric.Label
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
