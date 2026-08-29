package runtime

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
)

var (
	warmDashboardDatumsSink   []dashboard.Datum
	warmDashboardValuesSink   []any
	warmDashboardFrameSink    visualizationruntime.Frame
	warmDashboardEnvelopeSink visualizationir.VisualizationEnvelope
	warmDashboardRecordsSink  []map[string]any
)

// BenchmarkDashboardWarmShapingStages attributes the allocation-heavy
// post-decode stages without changing the production dashboard pipeline.
func BenchmarkDashboardWarmShapingStages(b *testing.B) {
	for _, workload := range []struct {
		name    string
		rows    int
		columns int
	}{
		{name: "kpi/rows_1", rows: 1, columns: 1},
		{name: "wide_chart/rows_50", rows: 50, columns: 32},
		{name: "wide_chart/rows_1000", rows: 1_000, columns: 32},
	} {
		b.Run(workload.name, func(b *testing.B) {
			definition := warmDashboardInlineDefinition(b, workload.name, workload.columns)
			rows := warmDashboardDataQueryRows(workload.rows, workload.columns)
			datums := datumsFromDataQuery(rows)
			frame, err := frameFromDatums(definition, datums)
			if err != nil {
				b.Fatal(err)
			}
			base, err := visualizationir.SpecificationBase(definition.Spec)
			if err != nil {
				b.Fatal(err)
			}

			b.Run("datum_maps", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					warmDashboardDatumsSink = datumsFromDataQuery(rows)
				}
			})

			values := warmDashboardNormalizationValues(workload.rows, workload.columns)
			b.Run("normalization", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					normalized := make([]any, len(values))
					for index, value := range values {
						normalized[index] = normalizeDatumValue(value)
					}
					warmDashboardValuesSink = normalized
				}
			})

			b.Run("ordered_frame", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					ordered, err := frameFromDatums(definition, datums)
					if err != nil {
						b.Fatal(err)
					}
					warmDashboardFrameSink = ordered
				}
			})

			b.Run("calculation_clone", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					cloned, _, err := visualizationruntime.ApplyVisualCalculations(base, definition.Query.DatasetID, frame, visualizationir.VisualizationCompletenessComplete)
					if err != nil {
						b.Fatal(err)
					}
					warmDashboardFrameSink = cloned
				}
			})

			b.Run("envelope", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					envelope, err := visualizationruntime.EnvelopeFromFrame(definition, frame, nil, 1, 1)
					if err != nil {
						b.Fatal(err)
					}
					warmDashboardEnvelopeSink = envelope
				}
			})
		})
	}

	for _, workload := range []struct {
		name    string
		rows    int
		columns int
	}{
		{name: "table_window_narrow/rows_50", rows: 50, columns: 8},
		{name: "table_window_wide/rows_1000", rows: 1_000, columns: 32},
	} {
		b.Run(workload.name, func(b *testing.B) {
			definition := warmDashboardTableDefinition(b, workload.name, workload.columns)
			queryRows := warmDashboardReportRows(workload.rows, workload.columns)
			records := tableRowsFromAnalytics(queryRows)
			table := warmDashboardTable(workload.rows, workload.columns, records)

			b.Run("datum_maps", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					warmDashboardRecordsSink = tableRowsFromAnalytics(queryRows)
				}
			})

			b.Run("ordered_window", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, 1, 1)
					if err != nil {
						b.Fatal(err)
					}
					warmDashboardEnvelopeSink = envelope
				}
			})
		})
	}
}

func warmDashboardInlineDefinition(tb testing.TB, id string, columns int) visualizationdefinition.Definition {
	tb.Helper()
	if columns == 1 {
		base := warmDashboardSpecBase("kpi", []visualizationir.VisualizationField{{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "Value"}}, 1)
		spec := visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{
			VisualizationSpecBase: base, Kind: "kpi", Value: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"},
			Presentation: visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable, Ranges: []visualizationir.VisualizationKPIQualitativeRange{}},
		}}
		definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: "model", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "metric", Alias: "value"}}, Limit: 1}})
		if err != nil {
			tb.Fatal(err)
		}
		return definition
	}
	fields := make([]visualizationir.VisualizationField, columns)
	fields[0] = visualizationir.VisualizationField{ID: "label", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: "Label"}
	y := make([]visualizationir.VisualizationFieldRef, 0, columns-1)
	metrics := make([]visualizationdefinition.FieldBinding, 0, columns-1)
	for column := 1; column < columns; column++ {
		name := fmt.Sprintf("value_%02d", column-1)
		fields[column] = visualizationir.VisualizationField{ID: name, Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: name}
		y = append(y, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: name})
		metrics = append(metrics, visualizationdefinition.FieldBinding{FieldID: "metric_" + strconv.Itoa(column-1), Alias: name})
	}
	base := warmDashboardSpecBase("cartesian", fields, 2_000)
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
		VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkLine,
		X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: y,
		Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, Priority: []visualizationir.VisualizationLabelPriority{}, MaxCharacters: 24, TooltipFallback: true}}},
	}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryMultiMeasure, ModelID: "model", DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "orders.label", Alias: "label"}}, Metrics: metrics, Limit: 2_000},
	})
	if err != nil {
		tb.Fatal(err)
	}
	return definition
}

func warmDashboardTableDefinition(tb testing.TB, id string, columns int) visualizationdefinition.Definition {
	tb.Helper()
	fields := make([]visualizationir.VisualizationField, columns)
	tableColumns := make([]visualizationir.TableVisualizationColumn, columns)
	bindings := make([]visualizationdefinition.FieldBinding, columns)
	for column := range fields {
		name := fmt.Sprintf("field_%02d", column)
		fields[column] = visualizationir.VisualizationField{ID: name, Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: name}
		tableColumns[column] = visualizationir.TableVisualizationColumn{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: name}, Label: name, Formatting: []visualizationir.TableVisualizationFormattingRule{}}
		bindings[column] = visualizationdefinition.FieldBinding{FieldID: "orders." + name, Alias: name}
	}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: warmDashboardSpecBase("table", fields, 2_000), Kind: "table", Columns: tableColumns, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: "model", DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: bindings, Limit: 2_000}})
	if err != nil {
		tb.Fatal(err)
	}
	return definition
}

func warmDashboardSpecBase(kind string, fields []visualizationir.VisualizationField, maxRows int64) visualizationir.VisualizationSpecBase {
	return visualizationir.VisualizationSpecBase{
		Kind: kind, Title: kind, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: maxRows, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete},
		Accessibility: visualizationir.VisualizationAccessibility{Title: kind, Description: kind}, Interactions: []visualizationir.VisualizationInteraction{},
	}
}

func warmDashboardDataQueryRows(rows, columns int) []dataquery.Row {
	result := make([]dataquery.Row, rows)
	for row := range result {
		values := dataquery.Row{}
		if columns == 1 {
			values["value"] = strconv.FormatInt(int64(row+1), 10) + ".000"
		} else {
			values["label"] = "category-" + strconv.Itoa(row%97)
			for column := 1; column < columns; column++ {
				name := fmt.Sprintf("value_%02d", column-1)
				if (row+column)%13 == 0 {
					values[name] = nil
				} else {
					values[name] = strconv.FormatInt(int64(row*37+column), 10) + ".000"
				}
			}
		}
		result[row] = values
	}
	return result
}

func warmDashboardNormalizationValues(rows, columns int) []any {
	values := make([]any, 0, rows*columns)
	for row := 0; row < rows; row++ {
		for column := 0; column < columns; column++ {
			switch (row + column) % 6 {
			case 0:
				values = append(values, nil)
			case 1:
				values = append(values, []byte("value-"+strconv.Itoa(row)))
			case 2:
				values = append(values, time.Unix(int64(row*37+column), 0).UTC())
			case 3:
				values = append(values, float32(row)+0.125)
			case 4:
				values = append(values, float64(row)+0.375)
			default:
				values = append(values, int64(row*37+column))
			}
		}
	}
	return values
}

func warmDashboardReportRows(rows, columns int) reportdef.QueryRows {
	result := make(reportdef.QueryRows, rows)
	for row := range result {
		values := reportdef.QueryRow{}
		for column := 0; column < columns; column++ {
			name := fmt.Sprintf("field_%02d", column)
			switch (row + column) % 3 {
			case 0:
				values[name] = nil
			case 1:
				values[name] = []byte("value-" + strconv.Itoa(row+column))
			case 2:
				values[name] = time.Unix(int64(row*37+column), 0).UTC()
			}
		}
		result[row] = values
	}
	return result
}

func warmDashboardTable(rows, columns int, records []map[string]any) dashboard.Table {
	tableColumns := make([]dashboard.TableColumn, columns)
	for column := range tableColumns {
		name := fmt.Sprintf("field_%02d", column)
		tableColumns[column] = dashboard.TableColumn{Key: name, Label: name, Role: "dimension", DataType: "string"}
	}
	return dashboard.Table{
		Version: 2, Kind: "data_table", Title: "table", Columns: tableColumns, Cardinality: dashboard.ExactCardinality(rows), AvailableRows: rows,
		RowCap: 2_000, ChunkSize: rows, RowHeight: 28, Sort: dashboard.TableSort{Key: "field_00", Direction: "asc"},
		Blocks: map[string]dashboard.TableBlock{"a": {Start: 0, Sort: dashboard.TableSort{Key: "field_00", Direction: "asc"}, Rows: records}},
	}
}
