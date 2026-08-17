package runtime

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func testCartesianDefinition(t *testing.T, id string, fields []ir.VisualizationField, interactions []ir.VisualizationInteraction) visualizationdefinition.Definition {
	t.Helper()
	spec := ir.VisualizationSpec{Value: &ir.CartesianVisualizationSpec{
		VisualizationSpecBase: ir.VisualizationSpecBase{
			Kind: "cartesian", Title: "Compiled title", Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
			DataBudget:    ir.VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: ir.VisualizationCompletenessComplete},
			Accessibility: ir.VisualizationAccessibility{Title: "Compiled title", Description: "Compiled title"}, Interactions: interactions,
		},
		Kind: "cartesian", Mark: ir.VisualizationCartesianMarkLine,
		X: ir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: []ir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
		Presentation: ir.CartesianVisualizationPresentation{VisualizationPresentation: ir.VisualizationPresentation{
			Legend: ir.VisualizationLegendPositionHidden,
			LabelPolicy: ir.VisualizationLabelPolicy{
				Density: ir.VisualizationLabelDensityHidden, Priority: []ir.VisualizationLabelPriority{},
				MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true,
			},
		}},
	}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "sales", DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "value"}}, Limit: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func testCartesianFields() []ir.VisualizationField {
	return []ir.VisualizationField{
		{ID: "label", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: true, Label: "Label"},
		{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Value"},
	}
}

func testGridDefinition(t *testing.T, id string, table dashboard.Table) visualizationdefinition.Definition {
	t.Helper()
	visualType := map[string]string{"matrix_table": "matrix", "pivot_table": "pivot"}[table.Kind]
	if visualType == "" {
		visualType = "table"
	}
	fields := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		fields[index] = column.Key
	}
	authored := dashboardauthoring.TableVisual{Title: table.Title, Columns: table.Columns, DefaultSort: table.Sort, Style: table.Style, Query: dashboardauthoring.TableQuery{Table: "table", Fields: fields}}
	if visualType != "table" {
		authored.Query.Fields = nil
		for _, column := range table.Columns {
			field := dashboardauthoring.FieldRef{Field: column.Key, Alias: column.Key}
			if column.Role == "metric" || column.Align == "right" {
				authored.Query.Metrics = append(authored.Query.Metrics, field)
			} else {
				authored.Query.Rows = append(authored.Query.Rows, field)
			}
		}
	}
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{ID: "test", SemanticModel: "model", Visuals: dashboardauthoring.TabularVisualizations(visualType, map[string]dashboardauthoring.TableVisual{id: authored})})
	if err != nil {
		t.Fatal(err)
	}
	return definitions[id]
}

func TestEnvelopeFromFrameKeepsCompiledSpecAndStreamRevision(t *testing.T) {
	definition := testCartesianDefinition(t, "revenue", testCartesianFields(), nil)
	envelope, err := EnvelopeFromFrame(definition, Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"Jan", 10.5}}}, nil, 9, 4)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.SpecRevision != definition.SpecRevision || envelope.Spec.Value.(*ir.CartesianVisualizationSpec).Title != "Compiled title" {
		t.Fatalf("envelope did not retain compiled specification: %#v", envelope)
	}
	state := envelope.DataState.Value.(*ir.InlineVisualizationDataState)
	if envelope.DataRevision != 9 || state.DataRevision != 9 || state.Datasets[0].SpecRevision != definition.SpecRevision {
		t.Fatalf("stream revision was not applied: %#v", state)
	}
}

func TestFrameFromRecordsUsesCompiledDatasetOrdering(t *testing.T) {
	fields := testCartesianFields()
	fields[0], fields[1] = fields[1], fields[0]
	definition := testCartesianDefinition(t, "revenue", fields, nil)
	frame, err := FrameFromRecords(definition, []map[string]any{{"value": 10.5, "label": "Jan"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := frame.Columns, []string{"value", "label"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("columns = %#v, want compiled order %#v", got, want)
	}
}

func TestEnvelopeFromFrameProjectsSelectionAsDatumRef(t *testing.T) {
	fact := "orders"
	fields := testCartesianFields()
	fields[0].Role = ir.VisualizationFieldRoleIdentity
	interaction := ir.VisualizationInteraction{
		ID: "point_selection", Kind: ir.VisualizationInteractionKindSelect, Mode: ir.VisualizationSelectionModeSingle, RequiresStableIdentity: true,
		Mappings: []ir.VisualizationInteractionMapping{{Source: ir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, TargetFieldID: "orders.status", TargetFactID: &fact}},
	}
	definition := testCartesianDefinition(t, "orders", fields, []ir.VisualizationInteraction{interaction})
	selection := []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{{Field: "orders.status", Fact: "orders", Value: "delivered"}}, Label: "Delivered"}}
	envelope, err := EnvelopeFromFrame(definition, Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"delivered", 42}}}, selection, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Selection) != 1 || envelope.Selection[0].Datum.DataRevision != 8 || envelope.Selection[0].Datum.Identity["label"] != "delivered" {
		t.Fatalf("selection = %#v", envelope.Selection)
	}
}

func TestEnvelopeFromFrameUsesColumnarTypedIR(t *testing.T) {
	t.Parallel()
	definition := testCartesianDefinition(t, "revenue", testCartesianFields(), nil)
	envelope, err := EnvelopeFromFrame(definition, Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"Jan", 10.5}}}, nil, 4, 2)
	if err != nil {
		t.Fatalf("EnvelopeFromFrame: %v", err)
	}
	if envelope.RendererID != "echarts" {
		t.Fatalf("renderer = %q", envelope.RendererID)
	}
	state := envelope.DataState.Value.(*ir.InlineVisualizationDataState)
	if len(state.Datasets) != 1 || len(state.Datasets[0].Rows) != 1 || len(state.Datasets[0].Rows[0]) != 2 {
		t.Fatalf("unexpected columnar state: %#v", state)
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		t.Fatalf("ValidateEnvelope: %v", err)
	}
}

func TestTableEnvelopePreservesWindowIdentity(t *testing.T) {
	t.Parallel()
	count := 1
	table := dashboard.Table{Kind: "data_table", Title: "Orders", Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order", Role: "row_header"}}, Cardinality: dashboard.ExactCardinality(count), AvailableRows: count, RowCap: 100, ChunkSize: 50, RowHeight: 34, ResetVersion: 3, Sort: dashboard.TableSort{Key: "order_id", Direction: "asc"}, Blocks: map[string]dashboard.TableBlock{"a": {Start: 0, RequestSeq: 7, ResetVersion: 3, Sort: dashboard.TableSort{Key: "order_id", Direction: "asc"}, Rows: []map[string]any{{"order_id": "one"}}}}}
	envelope, err := WindowEnvelopeFromDefinition(testGridDefinition(t, "orders", table), table, 8, 5)
	if err != nil {
		t.Fatalf("TableEnvelope: %v", err)
	}
	state := envelope.DataState.Value.(*ir.WindowedVisualizationDataState)
	if state.Blocks["a"].RequestSeq != 7 || state.ResetVersion != 3 {
		t.Fatalf("window identity lost: %#v", state)
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		t.Fatalf("ValidateEnvelope: %v", err)
	}
}

func TestTableEnvelopeOmitsUnknownCardinalityCount(t *testing.T) {
	t.Parallel()
	table := dashboard.Table{
		Kind: "data_table", Title: "Orders", Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order", Role: "row_header"}},
		Cardinality: dashboard.TableCardinality{Kind: dashboard.CardinalityUnknown}, AvailableRows: 10000,
		RowCap: 10000, ChunkSize: 50, RowHeight: 34, Sort: dashboard.TableSort{Key: "order_id", Direction: "asc"}, Blocks: map[string]dashboard.TableBlock{},
	}
	envelope, err := WindowEnvelopeFromDefinition(testGridDefinition(t, "orders", table), table, 1, 1)
	if err != nil {
		t.Fatalf("TableEnvelope: %v", err)
	}
	state, ok := envelope.DataState.Value.(*ir.WindowedVisualizationDataState)
	if !ok {
		t.Fatalf("data state = %T", envelope.DataState.Value)
	}
	if state.Cardinality.Count != nil {
		t.Fatalf("unknown cardinality count = %v, want nil", *state.Cardinality.Count)
	}
}

func TestErrorEnvelopeFromDefinitionPreservesCompiledBoundary(t *testing.T) {
	table := dashboard.Table{
		Kind: "data_table", Title: "Orders",
		Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order", Role: "row_header"}},
	}
	definition := testGridDefinition(t, "orders", table)

	envelope, err := ErrorEnvelopeFromDefinition(definition, errors.New("query failed"), 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.VisualID != "orders" || envelope.SpecRevision != definition.SpecRevision || envelope.DataRevision != 7 {
		t.Fatalf("error envelope identity = %#v", envelope)
	}
	if envelope.Status.Kind != ir.VisualizationStatusKindError || envelope.Status.Message == nil || *envelope.Status.Message != "query failed" {
		t.Fatalf("error envelope status = %#v", envelope.Status)
	}
	if len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Code != "query_failed" || envelope.Diagnostics[0].Severity != ir.VisualizationDiagnosticSeverityError {
		t.Fatalf("error envelope diagnostics = %#v", envelope.Diagnostics)
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		t.Fatalf("error envelope validation: %v", err)
	}
}
