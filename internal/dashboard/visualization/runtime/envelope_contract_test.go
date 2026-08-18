package runtime

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalCartesianDefinition(t *testing.T, id string, fields []ir.VisualizationField, interactions []ir.VisualizationInteraction) visualizationdefinition.Definition {
	t.Helper()
	spec := ir.VisualizationSpec{Value: &ir.CartesianVisualizationSpec{VisualizationSpecBase: ir.VisualizationSpecBase{Kind: "cartesian", Title: "Compiled title", Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, DataBudget: ir.VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: ir.VisualizationCompletenessComplete}, Accessibility: ir.VisualizationAccessibility{Title: "Compiled title", Description: "Compiled title"}, Interactions: interactions}, Kind: "cartesian", Mark: ir.VisualizationCartesianMarkLine, X: ir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: []ir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: ir.CartesianVisualizationPresentation{VisualizationPresentation: ir.VisualizationPresentation{Legend: ir.VisualizationLegendPositionHidden, LabelPolicy: ir.VisualizationLabelPolicy{Density: ir.VisualizationLabelDensityHidden, Priority: []ir.VisualizationLabelPriority{}, MaxCharacters: 24, TooltipFallback: true}}}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "sales", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func canonicalCartesianFields() []ir.VisualizationField {
	return []ir.VisualizationField{{ID: "label", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: true, Label: "Label"}, {ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Value"}}
}

func canonicalGridDefinition(t *testing.T, id string) visualizationdefinition.Definition {
	t.Helper()
	base := ir.VisualizationSpecBase{Kind: "table", Title: "Orders", Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{{ID: "order_id", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Label: "Order"}}}}, DataBudget: ir.VisualizationDataBudget{MaxRows: 100}, Accessibility: ir.VisualizationAccessibility{Title: "Orders", Description: "Orders"}}
	spec := ir.VisualizationSpec{Value: &ir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: []ir.TableVisualizationColumn{{Field: ir.VisualizationFieldRef{Dataset: "primary", Field: "order_id"}, Label: "Order", Formatting: []ir.TableVisualizationFormattingRule{}}}, Presentation: ir.GridVisualizationPresentation{RowHeight: 34, ShowHeader: true}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: "model", DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: "table", Fields: []visualizationdefinition.FieldBinding{{FieldID: "order_id", Alias: "order_id"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestCanonicalEnvelopeFromFrameKeepsCompiledSpecAndStreamRevision(t *testing.T) {
	definition := canonicalCartesianDefinition(t, "revenue", canonicalCartesianFields(), nil)
	envelope, err := EnvelopeFromFrame(definition, Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"Jan", "10.5"}}}, nil, 9, 4)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.SpecRevision != definition.SpecRevision || envelope.Spec.Value.(*ir.CartesianVisualizationSpec).Title != "Compiled title" {
		t.Fatalf("envelope=%#v", envelope)
	}
	state := envelope.DataState.Value.(*ir.InlineVisualizationDataState)
	if envelope.DataRevision != 9 || state.DataRevision != 9 || state.Datasets[0].SpecRevision != definition.SpecRevision {
		t.Fatalf("state=%#v", state)
	}
}

func TestCanonicalFrameFromRecordsUsesCompiledDatasetOrdering(t *testing.T) {
	fields := canonicalCartesianFields()
	fields[0], fields[1] = fields[1], fields[0]
	frame, err := FrameFromRecords(canonicalCartesianDefinition(t, "revenue", fields, nil), []map[string]any{{"value": "10.5", "label": "Jan"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := frame.Columns, []string{"value", "label"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("columns=%#v", got)
	}
}

func TestCanonicalNormalizeDecimalFrameUsesColumnIDs(t *testing.T) {
	frame := Frame{Columns: []string{"value", "label"}, Rows: [][]any{{int64(42), "Jan"}}}
	if err := normalizeDecimalFrame([]ir.VisualizationField{{ID: "label", DataType: ir.VisualizationDataTypeString}, {ID: "value", DataType: ir.VisualizationDataTypeDecimal}}, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Rows[0][0] != "42" {
		t.Fatalf("value=%#v", frame.Rows[0][0])
	}
}

func TestCanonicalEnvelopeFromFrameProjectsSelectionAsDatumRef(t *testing.T) {
	dataset := "orders"
	fields := canonicalCartesianFields()
	fields[0].Role = ir.VisualizationFieldRoleIdentity
	interaction := ir.VisualizationInteraction{ID: "point_selection", Kind: ir.VisualizationInteractionKindSelect, Mode: ir.VisualizationSelectionModeSingle, RequiresStableIdentity: true, Mappings: []ir.VisualizationInteractionMapping{{Source: ir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, TargetFieldID: "orders.status", TargetDatasetID: &dataset}}}
	definition := canonicalCartesianDefinition(t, "orders", fields, []ir.VisualizationInteraction{interaction})
	envelope, err := EnvelopeFromFrame(definition, Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"delivered", "42"}}}, []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{{Field: "orders.status", Dataset: "orders", Value: "delivered"}}, Label: "Delivered"}}, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Selection) != 1 || envelope.Selection[0].Datum.DataRevision != 8 || envelope.Selection[0].Datum.Identity["label"] != "delivered" {
		t.Fatalf("selection=%#v", envelope.Selection)
	}
}

func TestCanonicalEnvelopeFromFrameUsesColumnarTypedIR(t *testing.T) {
	envelope, err := EnvelopeFromFrame(canonicalCartesianDefinition(t, "revenue", canonicalCartesianFields(), nil), Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"Jan", "10.5"}}}, nil, 4, 2)
	if err != nil || envelope.RendererID != "echarts" {
		t.Fatalf("envelope=%#v err=%v", envelope, err)
	}
	state := envelope.DataState.Value.(*ir.InlineVisualizationDataState)
	if len(state.Datasets) != 1 || len(state.Datasets[0].Rows) != 1 || len(state.Datasets[0].Rows[0]) != 2 {
		t.Fatalf("state=%#v", state)
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalTableEnvelopePreservesWindowIdentity(t *testing.T) {
	table := dashboard.Table{Kind: "data_table", Title: "Orders", Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order", Role: "row_header"}}, Cardinality: dashboard.ExactCardinality(1), AvailableRows: 1, RowCap: 100, ChunkSize: 50, RowHeight: 34, ResetVersion: 3, Sort: dashboard.TableSort{Key: "order_id", Direction: "asc"}, Blocks: map[string]dashboard.TableBlock{"a": {Start: 0, RequestSeq: 7, ResetVersion: 3, Sort: dashboard.TableSort{Key: "order_id", Direction: "asc"}, Rows: []map[string]any{{"order_id": "one"}}}}}
	envelope, err := WindowEnvelopeFromDefinition(canonicalGridDefinition(t, "orders"), table, 8, 5)
	if err != nil {
		t.Fatal(err)
	}
	state := envelope.DataState.Value.(*ir.WindowedVisualizationDataState)
	if state.Blocks["a"].RequestSeq != 7 || state.ResetVersion != 3 {
		t.Fatalf("state=%#v", state)
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalTableEnvelopeOmitsUnknownCardinalityCount(t *testing.T) {
	table := dashboard.Table{Kind: "data_table", Title: "Orders", Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order", Role: "row_header"}}, Cardinality: dashboard.TableCardinality{Kind: dashboard.CardinalityUnknown}, AvailableRows: 10000, RowCap: 10000, ChunkSize: 50, RowHeight: 34, Sort: dashboard.TableSort{Key: "order_id", Direction: "asc"}, Blocks: map[string]dashboard.TableBlock{}}
	envelope, err := WindowEnvelopeFromDefinition(canonicalGridDefinition(t, "orders"), table, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := envelope.DataState.Value.(*ir.WindowedVisualizationDataState)
	if !ok || state.Cardinality.Count != nil {
		t.Fatalf("state=%#v", envelope.DataState)
	}
}

func TestCanonicalErrorEnvelopePreservesCompiledBoundary(t *testing.T) {
	definition := canonicalGridDefinition(t, "orders")
	envelope, err := ErrorEnvelopeFromDefinition(definition, errors.New("query failed"), 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.VisualID != "orders" || envelope.SpecRevision != definition.SpecRevision || envelope.DataRevision != 7 || envelope.Status.Kind != ir.VisualizationStatusKindError || envelope.Status.Message == nil || *envelope.Status.Message != "query failed" || len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Code != "query_failed" {
		t.Fatalf("envelope=%#v", envelope)
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		t.Fatal(err)
	}
}
