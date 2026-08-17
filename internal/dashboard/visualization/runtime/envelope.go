// Package runtime shapes governed query results into the visualization IR.
package runtime

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// Frame is the renderer-independent result of a compiled visualization query.
// Columns must use compiled field aliases; rows are ordered to those columns.
type Frame struct {
	Columns      []string
	Rows         [][]any
	Completeness ir.VisualizationCompleteness
}

type SpatialTiledMetadata struct {
	Cardinality      int64
	Extent           ir.VisualizationSpatialBounds
	RawDomains       []ir.VisualizationSpatialScaleDomain
	AggregateDomains []ir.VisualizationSpatialScaleDomain
	TileURL          string
	RawMinimumZoom   int32
}

// FrameFromRecords orders named query values according to the immutable
// compiled dataset schema. It is the shared boundary for non-dashboard
// producers such as agent-generated visualizations.
func FrameFromRecords(definition visualizationdefinition.Definition, records []map[string]any) (Frame, error) {
	base, err := ir.SpecificationBase(definition.Spec)
	if err != nil {
		return Frame{}, err
	}
	schema, err := compiledDatasetSchema(base, definition.Query.DatasetID)
	if err != nil {
		return Frame{}, err
	}
	fields := sourceDatasetFields(schema.Fields)
	columns := make([]string, len(fields))
	for index, field := range fields {
		columns[index] = field.ID
	}
	rows := make([][]any, len(records))
	for rowIndex, record := range records {
		rows[rowIndex] = make([]any, len(columns))
		for columnIndex, column := range columns {
			rows[rowIndex][columnIndex] = record[column]
		}
	}
	return Frame{Columns: columns, Rows: rows}, nil
}

// SelectionEntriesFromDefinition projects canonical dashboard selection state
// into renderer-independent DatumRef values.
func SelectionEntriesFromDefinition(definition visualizationdefinition.Definition, entries []dashboard.InteractionSelectionEntry, dataRevision int64) ([]ir.VisualizationSelectionEntry, error) {
	return compiledSelections(definition.Spec, entries, dataRevision)
}

// EnvelopeFromFrame creates the canonical inline renderer boundary directly
// from a compiled query frame. No legacy visual presentation DTO participates
// in this path.
func EnvelopeFromFrame(definition visualizationdefinition.Definition, frame Frame, selections []dashboard.InteractionSelectionEntry, dataRevision, generation int64) (ir.VisualizationEnvelope, error) {
	return EnvelopeFromFrames(definition, map[string]Frame{definition.Query.DatasetID: frame}, selections, dataRevision, generation)
}

// EnvelopeFromFrames creates the canonical inline renderer boundary for a
// primary frame plus any compiler-owned context datasets. Dataset order follows
// the immutable specification, never map iteration or query completion order.
func EnvelopeFromFrames(definition visualizationdefinition.Definition, frames map[string]Frame, selections []dashboard.InteractionSelectionEntry, dataRevision, generation int64) (ir.VisualizationEnvelope, error) {
	if err := definition.Validate(); err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	base, err := ir.SpecificationBase(definition.Spec)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	datasets := make([]ir.VisualizationInlineDataset, 0, len(base.Datasets))
	diagnostics := []ir.VisualizationDiagnostic{}
	primaryRows := 0
	for _, schema := range base.Datasets {
		frame, ok := frames[schema.ID]
		if !ok {
			return ir.VisualizationEnvelope{}, fmt.Errorf("visualization %q has no frame for dataset %q", definition.ID, schema.ID)
		}
		wantSourceFields := sourceDatasetFields(schema.Fields)
		wantColumns := make([]string, len(wantSourceFields))
		for index, field := range wantSourceFields {
			wantColumns[index] = field.ID
		}
		if err := validateFrameColumns(definition.ID+" dataset "+schema.ID, frame.Columns, wantColumns); err != nil {
			return ir.VisualizationEnvelope{}, err
		}
		frameCompleteness := frame.Completeness
		if frameCompleteness == "" {
			frameCompleteness = completeness(frame.Rows)
		}
		frame, calculationDiagnostics, err := ApplyVisualCalculations(base, schema.ID, frame, frameCompleteness)
		if err != nil {
			return ir.VisualizationEnvelope{}, err
		}
		diagnostics = append(diagnostics, calculationDiagnostics...)
		datasets = append(datasets, ir.VisualizationInlineDataset{
			ID: schema.ID, SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation,
			Columns: append([]string{}, frame.Columns...), Rows: frame.Rows, Completeness: frameCompleteness,
		})
		if schema.ID == definition.Query.DatasetID {
			primaryRows = len(frame.Rows)
		}
	}
	state := ir.InlineVisualizationDataState{
		VisualizationDataStateBase: ir.VisualizationDataStateBase{Kind: "inline", SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation},
		Kind:                       "inline", Datasets: datasets,
	}
	envelope := ir.VisualizationEnvelope{
		SchemaVersion: ir.CurrentSchemaVersion, VisualID: definition.ID, RendererID: definition.RendererID, SpecRevision: definition.SpecRevision, Spec: definition.Spec,
		DataRevision: dataRevision, DataState: ir.VisualizationDataState{Value: &state}, Highlights: []ir.VisualizationHighlightState{}, Status: ir.VisualizationStatus{Kind: statusKind(primaryRows, "")}, Diagnostics: diagnostics,
	}
	if len(diagnostics) > 0 && envelope.Status.Kind == ir.VisualizationStatusKindReady {
		envelope.Status.Kind = ir.VisualizationStatusKindPartial
	}
	envelope.Selection, err = compiledSelections(definition.Spec, selections, dataRevision)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("compiled visualization %q: %w", definition.ID, err)
	}
	return envelope, nil
}

func sourceDatasetFields(fields []ir.VisualizationField) []ir.VisualizationField {
	out := make([]ir.VisualizationField, 0, len(fields))
	for _, field := range fields {
		if field.Provenance != nil && field.Provenance.Kind == ir.VisualizationFieldProvenanceKindVisualCalculation {
			continue
		}
		out = append(out, field)
	}
	return out
}

func validateFrameColumns(visualID string, got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("visualization %q frame has %d columns, want %d", visualID, len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf("visualization %q frame column %d is %q, want %q", visualID, index, got[index], want[index])
		}
	}
	return nil
}

func SpatialTiledEnvelopeFromMetadata(definition visualizationdefinition.Definition, metadata SpatialTiledMetadata, selections []dashboard.InteractionSelectionEntry, dataRevision, generation int64) (ir.VisualizationEnvelope, error) {
	if err := definition.Validate(); err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	geographic, ok := definition.Spec.Value.(*ir.GeographicVisualizationSpec)
	if !ok || definition.Query.Kind != visualizationdefinition.QuerySpatial || definition.Query.Spatial == nil || definition.Query.Spatial.Tiles == nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("visualization %q has no compiled spatial tile binding", definition.ID)
	}
	schema, err := compiledDatasetSchema(geographic.VisualizationSpecBase, definition.Query.DatasetID)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	tiles := definition.Query.Spatial.Tiles
	count := metadata.Cardinality
	state := ir.SpatialTiledVisualizationDataState{
		VisualizationDataStateBase: ir.VisualizationDataStateBase{Kind: "spatial_tiled", SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation},
		Kind:                       "spatial_tiled", Schema: schema, Cardinality: ir.VisualizationCardinality{Kind: ir.VisualizationCardinalityKindExact, Count: &count}, Extent: metadata.Extent,
		RawDomains: metadata.RawDomains, AggregateDomains: metadata.AggregateDomains, TileURL: metadata.TileURL,
		MinimumZoom: tiles.MinimumZoom, MaximumZoom: tiles.MaximumZoom, RawMinimumZoom: metadata.RawMinimumZoom, FeatureCap: tiles.FeatureCap, MaximumTileBytes: tiles.MaximumBytes,
	}
	status := ir.VisualizationStatusKindReady
	if count == 0 {
		status = ir.VisualizationStatusKindNoData
	}
	envelope := ir.VisualizationEnvelope{
		SchemaVersion: ir.CurrentSchemaVersion, VisualID: definition.ID, RendererID: definition.RendererID, SpecRevision: definition.SpecRevision, Spec: definition.Spec,
		DataRevision: dataRevision, DataState: ir.VisualizationDataState{Value: &state}, Highlights: []ir.VisualizationHighlightState{}, Status: ir.VisualizationStatus{Kind: status}, Diagnostics: []ir.VisualizationDiagnostic{},
	}
	envelope.Selection, err = compiledSelections(definition.Spec, selections, dataRevision)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("compiled spatial tiled visualization %q: %w", definition.ID, err)
	}
	return envelope, nil
}

func compiledDatasetSchema(base ir.VisualizationSpecBase, datasetID string) (ir.VisualizationDatasetSchema, error) {
	for _, schema := range base.Datasets {
		if schema.ID == datasetID {
			return schema, nil
		}
	}
	return ir.VisualizationDatasetSchema{}, fmt.Errorf("query targets unknown dataset %q", datasetID)
}

// WindowEnvelopeFromDefinition shapes a window while retaining the exact
// immutable grid specification selected by the compiler.
func WindowEnvelopeFromDefinition(definition visualizationdefinition.Definition, table dashboard.Table, dataRevision, generation int64) (ir.VisualizationEnvelope, error) {
	if err := definition.Validate(); err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	base, err := ir.SpecificationBase(definition.Spec)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	if len(table.Columns) == 0 {
		table.Columns = []dashboard.TableColumn{{Key: "value", Label: "Value"}}
	}
	schema := compiledWindowSchema(base, definition.Query.DatasetID, table)
	if table.Sort.Key == "" {
		table.Sort.Key = schema.Fields[0].ID
	}
	sortValue := ir.VisualizationSort{Field: ir.VisualizationFieldRef{Dataset: definition.Query.DatasetID, Field: table.Sort.Key}, Direction: sortDirection(table.Sort.Direction)}
	blocks := make(map[string]ir.VisualizationWindowBlock, len(table.Blocks))
	fieldNames := make([]string, len(schema.Fields))
	for index, field := range schema.Fields {
		fieldNames[index] = field.ID
	}
	for key, block := range table.Blocks {
		if len(block.Rows) == 0 || block.Start >= table.AvailableRows {
			continue
		}
		if excess := block.Start + len(block.Rows) - table.AvailableRows; excess > 0 {
			block.Rows = block.Rows[:len(block.Rows)-excess]
		}
		if block.Sort.Key == "" {
			block.Sort = table.Sort
		}
		rows := make([][]any, len(block.Rows))
		for index, value := range block.Rows {
			rows[index] = row(fieldNames, value)
		}
		blocks[key] = ir.VisualizationWindowBlock{
			ID: key, Start: int64(block.Start), Rows: rows, RequestSeq: int64(block.RequestSeq), ResetVersion: int64(block.ResetVersion),
			Sort: []ir.VisualizationSort{{Field: ir.VisualizationFieldRef{Dataset: definition.Query.DatasetID, Field: block.Sort.Key}, Direction: sortDirection(block.Sort.Direction)}},
		}
	}
	cardinality := ir.VisualizationCardinality{Kind: cardinalityKind(table.Cardinality.Kind)}
	if cardinality.Kind != ir.VisualizationCardinalityKindUnknown {
		count := int64(table.Cardinality.Value)
		cardinality.Count = &count
	}
	state := ir.WindowedVisualizationDataState{
		VisualizationDataStateBase: ir.VisualizationDataStateBase{Kind: "windowed", SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation},
		Kind:                       "windowed", Schema: schema, Cardinality: cardinality, AvailableRows: int64(table.AvailableRows), RowCap: base.DataBudget.MaxRows,
		ChunkSize: int64(max(table.ChunkSize, dashboard.TableChunkSize)), ResetVersion: int64(table.ResetVersion), Sort: []ir.VisualizationSort{sortValue}, Blocks: blocks,
	}
	message := table.Error
	diagnostics := []ir.VisualizationDiagnostic{}
	if base.Calculations != nil && len(*base.Calculations) > 0 &&
		(table.IsCapped || table.Cardinality.Kind != dashboard.CardinalityExact) {
		diagnostics = append(diagnostics, ir.VisualizationDiagnostic{
			Code: "visual_calculation_incomplete_frame", Severity: ir.VisualizationDiagnosticSeverityWarning,
			Message: "Visual calculations were evaluated over the bounded visible table frame; unavailable rows are excluded.",
		})
	}
	envelope := ir.VisualizationEnvelope{
		SchemaVersion: ir.CurrentSchemaVersion, VisualID: definition.ID, RendererID: definition.RendererID,
		SpecRevision: definition.SpecRevision, Spec: definition.Spec, DataRevision: dataRevision,
		DataState: ir.VisualizationDataState{Value: &state}, Selection: []ir.VisualizationSelectionEntry{}, Highlights: []ir.VisualizationHighlightState{},
		Status: ir.VisualizationStatus{Kind: statusKind(table.AvailableRows, message), Message: optional(message)}, Diagnostics: diagnostics,
	}
	if len(diagnostics) > 0 && envelope.Status.Kind == ir.VisualizationStatusKindReady {
		envelope.Status.Kind = ir.VisualizationStatusKindPartial
	}
	envelope.Selection, err = compiledSelections(definition.Spec, table.Selection, dataRevision)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("compiled visualization %q: %w", definition.ID, err)
	}
	return envelope, nil
}

func compiledWindowSchema(base ir.VisualizationSpecBase, datasetID string, table dashboard.Table) ir.VisualizationDatasetSchema {
	compiledFields := map[string]ir.VisualizationField{}
	for _, dataset := range base.Datasets {
		if dataset.ID != datasetID {
			continue
		}
		for _, field := range dataset.Fields {
			compiledFields[field.ID] = field
		}
	}
	fields := make([]ir.VisualizationField, len(table.Columns))
	for index, column := range table.Columns {
		if field, ok := compiledFields[column.Key]; ok {
			fields[index] = field
			continue
		}
		role := ir.VisualizationFieldRoleDimension
		if column.Role == "row_header" && index == 0 {
			role = ir.VisualizationFieldRoleIdentity
		} else if column.Role == "metric" || column.Align == "right" {
			role = ir.VisualizationFieldRoleMetric
		}
		fields[index] = ir.VisualizationField{
			ID: column.Key, Role: role, DataType: tableDataType(column, table), Nullable: true,
			Label: defaultText(column.Label, column.Key), Format: tableFormat(column),
			Grid: &ir.VisualizationGridFieldMetadata{Group: optional(column.Group), Metric: optional(column.Metric), ColumnValue: optional(column.ColumnValue), Formatting: tableFormatting(column.Formatting)},
		}
	}
	return ir.VisualizationDatasetSchema{ID: datasetID, Fields: fields}
}

// EmptyEnvelopeFromDefinition creates the initial renderer boundary without
// reconstructing any legacy chart or table presentation model.
func EmptyEnvelopeFromDefinition(definition visualizationdefinition.Definition, dataRevision, generation, resetVersion int64) (ir.VisualizationEnvelope, error) {
	if err := definition.Validate(); err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	base, err := ir.SpecificationBase(definition.Spec)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	schema, err := compiledDatasetSchema(base, definition.Query.DatasetID)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	envelope := ir.VisualizationEnvelope{
		SchemaVersion: ir.CurrentSchemaVersion, VisualID: definition.ID, RendererID: definition.RendererID,
		SpecRevision: definition.SpecRevision, Spec: definition.Spec, DataRevision: dataRevision,
		Selection: []ir.VisualizationSelectionEntry{}, Highlights: []ir.VisualizationHighlightState{}, Status: ir.VisualizationStatus{Kind: ir.VisualizationStatusKindNoData}, Diagnostics: []ir.VisualizationDiagnostic{},
	}
	if definition.Query.Kind == visualizationdefinition.QuerySpatial && definition.Query.Spatial != nil && definition.Query.Spatial.Tiles != nil {
		count := int64(0)
		tiles := definition.Query.Spatial.Tiles
		state := ir.SpatialTiledVisualizationDataState{
			VisualizationDataStateBase: ir.VisualizationDataStateBase{Kind: "spatial_tiled", SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation},
			Kind:                       "spatial_tiled", Schema: schema, Cardinality: ir.VisualizationCardinality{Kind: ir.VisualizationCardinalityKindExact, Count: &count},
			Extent: ir.VisualizationSpatialBounds{West: -180, South: -85.0511287798066, East: 180, North: 85.0511287798066}, RawDomains: []ir.VisualizationSpatialScaleDomain{}, AggregateDomains: []ir.VisualizationSpatialScaleDomain{},
			TileURL: "/tiles/unavailable/{z}/{x}/{y}.mvt", MinimumZoom: tiles.MinimumZoom, MaximumZoom: tiles.MaximumZoom, RawMinimumZoom: tiles.RawMinimumZoom, FeatureCap: tiles.FeatureCap, MaximumTileBytes: tiles.MaximumBytes,
		}
		envelope.DataState = ir.VisualizationDataState{Value: &state}
	} else if definition.Query.Kind == visualizationdefinition.QueryDetail || definition.Query.Kind == visualizationdefinition.QueryMatrix || definition.Query.Kind == visualizationdefinition.QueryPivot {
		sort := emptyWindowSort(definition.Spec, schema)
		state := ir.WindowedVisualizationDataState{
			VisualizationDataStateBase: ir.VisualizationDataStateBase{Kind: "windowed", SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation},
			Kind:                       "windowed", Schema: schema, Cardinality: ir.VisualizationCardinality{Kind: ir.VisualizationCardinalityKindUnknown},
			AvailableRows: 0, RowCap: base.DataBudget.MaxRows, ChunkSize: dashboard.TableChunkSize, ResetVersion: resetVersion,
			Sort: sort, Blocks: map[string]ir.VisualizationWindowBlock{},
		}
		envelope.DataState = ir.VisualizationDataState{Value: &state}
	} else {
		datasets := make([]ir.VisualizationInlineDataset, 0, len(base.Datasets))
		for _, datasetSchema := range base.Datasets {
			columns := make([]string, len(datasetSchema.Fields))
			for index, field := range datasetSchema.Fields {
				columns[index] = field.ID
			}
			datasets = append(datasets, ir.VisualizationInlineDataset{
				ID: datasetSchema.ID, SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation,
				Columns: columns, Rows: [][]any{}, Completeness: ir.VisualizationCompletenessEmpty,
			})
		}
		state := ir.InlineVisualizationDataState{
			VisualizationDataStateBase: ir.VisualizationDataStateBase{Kind: "inline", SpecRevision: definition.SpecRevision, DataRevision: dataRevision, Generation: generation},
			Kind:                       "inline", Datasets: datasets,
		}
		envelope.DataState = ir.VisualizationDataState{Value: &state}
	}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("compiled visualization %q: %w", definition.ID, err)
	}
	return envelope, nil
}

// ErrorEnvelopeFromDefinition preserves the immutable visualization boundary
// when querying or shaping one visual fails. The data state remains valid and
// empty for the compiled result shape, while status and diagnostics carry the
// target-local failure.
func ErrorEnvelopeFromDefinition(definition visualizationdefinition.Definition, queryErr error, dataRevision, generation int64) (ir.VisualizationEnvelope, error) {
	if queryErr == nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("visualization error envelope requires an error")
	}
	envelope, err := EmptyEnvelopeFromDefinition(definition, dataRevision, generation, 0)
	if err != nil {
		return ir.VisualizationEnvelope{}, err
	}
	message := queryErr.Error()
	envelope.Status = ir.VisualizationStatus{Kind: ir.VisualizationStatusKindError, Message: &message}
	envelope.Diagnostics = []ir.VisualizationDiagnostic{{
		Code: "query_failed", Severity: ir.VisualizationDiagnosticSeverityError, Message: message,
	}}
	if err := ir.ValidateEnvelope(envelope); err != nil {
		return ir.VisualizationEnvelope{}, fmt.Errorf("compiled visualization %q error envelope: %w", definition.ID, err)
	}
	return envelope, nil
}

func emptyWindowSort(spec ir.VisualizationSpec, schema ir.VisualizationDatasetSchema) []ir.VisualizationSort {
	if value, ok := spec.Value.(*ir.TableVisualizationSpec); ok && value.DefaultSort != nil && len(*value.DefaultSort) > 0 {
		return append([]ir.VisualizationSort(nil), (*value.DefaultSort)...)
	}
	if len(schema.Fields) == 0 {
		return []ir.VisualizationSort{}
	}
	return []ir.VisualizationSort{{Field: ir.VisualizationFieldRef{Dataset: schema.ID, Field: schema.Fields[0].ID}, Direction: ir.VisualizationSortDirectionAscending}}
}

func compiledSelections(spec ir.VisualizationSpec, entries []dashboard.InteractionSelectionEntry, dataRevision int64) ([]ir.VisualizationSelectionEntry, error) {
	base, err := ir.SpecificationBase(spec)
	if err != nil {
		return nil, err
	}
	interactions := base.Interactions
	if len(entries) == 0 || len(interactions) == 0 {
		return []ir.VisualizationSelectionEntry{}, nil
	}
	interaction := interactions[0]
	out := make([]ir.VisualizationSelectionEntry, 0, len(entries))
	for index, entry := range entries {
		identity := map[string]any{}
		datasetID := ""
		for _, mapping := range interaction.Mappings {
			if datasetID == "" {
				datasetID = mapping.Source.Dataset
			} else if datasetID != mapping.Source.Dataset {
				return nil, fmt.Errorf("selection %d spans multiple datasets", index)
			}
			matched := false
			for _, selected := range entry.Mappings {
				fact, grain := "", ""
				if mapping.TargetFactID != nil {
					fact = *mapping.TargetFactID
				}
				if mapping.Grain != nil {
					grain = *mapping.Grain
				}
				if selected.Field == mapping.TargetFieldID && selected.Fact == fact && selected.Grain == grain {
					identity[mapping.Source.Field] = selected.Value
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("selection %d omits compiled mapping for %q", index, mapping.TargetFieldID)
			}
		}
		label := optional(entry.Label)
		out = append(out, ir.VisualizationSelectionEntry{Datum: ir.VisualizationDatumRef{Dataset: datasetID, DataRevision: dataRevision, Identity: identity}, Label: label})
	}
	return out, nil
}

func row(columns []string, values map[string]any) []any {
	out := make([]any, len(columns))
	for index, column := range columns {
		out[index] = values[column]
	}
	return out
}
func completeness(rows [][]any) ir.VisualizationCompleteness {
	if len(rows) == 0 {
		return ir.VisualizationCompletenessEmpty
	}
	return ir.VisualizationCompletenessComplete
}
func statusKind(count int, message string) ir.VisualizationStatusKind {
	if message != "" {
		return ir.VisualizationStatusKindError
	}
	if count == 0 {
		return ir.VisualizationStatusKindNoData
	}
	return ir.VisualizationStatusKindReady
}
func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func tableDataType(column dashboard.TableColumn, table dashboard.Table) ir.VisualizationDataType {
	switch column.Format {
	case "integer", "days":
		return ir.VisualizationDataTypeInteger
	case "decimal", "currency":
		return ir.VisualizationDataTypeDecimal
	case "boolean":
		return ir.VisualizationDataTypeBoolean
	case "date":
		return ir.VisualizationDataTypeDate
	case "timestamp":
		return ir.VisualizationDataTypeTemporal
	}
	for _, block := range table.Blocks {
		for _, row := range block.Rows {
			switch row[column.Key].(type) {
			case bool:
				return ir.VisualizationDataTypeBoolean
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
				return ir.VisualizationDataTypeInteger
			case float32, float64:
				return ir.VisualizationDataTypeDecimal
			case string:
				return ir.VisualizationDataTypeString
			}
		}
	}
	return ir.VisualizationDataTypeString
}
func tableFormat(column dashboard.TableColumn) *ir.VisualizationFormat {
	switch column.Format {
	case "integer", "decimal":
		value := ir.VisualizationFormat{Value: &ir.NumberVisualizationFormat{Kind: "number"}}
		return &value
	case "currency":
		value := ir.VisualizationFormat{Value: &ir.CurrencyVisualizationFormat{Kind: "currency", Currency: "BRL"}}
		return &value
	case "days":
		value := ir.VisualizationFormat{Value: &ir.DurationVisualizationFormat{Kind: "duration", Unit: "days"}}
		return &value
	case "date", "timestamp":
		value := ir.VisualizationFormat{Value: &ir.TemporalVisualizationFormat{Kind: "temporal"}}
		return &value
	}
	return nil
}

func tableFormatting(rules []dashboard.TableFormattingRule) []ir.TableVisualizationFormattingRule {
	out := make([]ir.TableVisualizationFormattingRule, 0, len(rules))
	for _, rule := range rules {
		switch rule.Kind {
		case "badge":
			out = append(out, ir.TableVisualizationFormattingRule{Value: &ir.TableBadgeFormattingRule{Kind: rule.Kind, Values: cloneStringMap(rule.Values)}})
		case "text_color":
			values := cloneStringMap(rule.Values)
			var valuesPointer *map[string]string
			if len(values) > 0 {
				valuesPointer = &values
			}
			out = append(out, ir.TableVisualizationFormattingRule{Value: &ir.TableTextColorFormattingRule{Kind: rule.Kind, Color: rule.Color, Values: valuesPointer, Minimum: rule.Min, Maximum: rule.Max}})
		case "background_scale":
			out = append(out, ir.TableVisualizationFormattingRule{Value: &ir.TableBackgroundScaleFormattingRule{Kind: rule.Kind, Minimum: rule.Min, Maximum: rule.Max, LowColor: optional(rule.LowColor), HighColor: optional(rule.HighColor)}})
		case "data_bar":
			out = append(out, ir.TableVisualizationFormattingRule{Value: &ir.TableDataBarFormattingRule{Kind: rule.Kind, Minimum: rule.Min, Maximum: rule.Max, Color: rule.Color, Background: optional(rule.Background)}})
		}
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
func sortDirection(value string) ir.VisualizationSortDirection {
	if value == "desc" {
		return ir.VisualizationSortDirectionDescending
	}
	return ir.VisualizationSortDirectionAscending
}
func cardinalityKind(value string) ir.VisualizationCardinalityKind {
	switch value {
	case dashboard.CardinalityExact:
		return ir.VisualizationCardinalityKindExact
	case dashboard.CardinalityEstimated:
		return ir.VisualizationCardinalityKindEstimated
	case dashboard.CardinalityLowerBound:
		return ir.VisualizationCardinalityKindLowerBound
	default:
		return ir.VisualizationCardinalityKindUnknown
	}
}
