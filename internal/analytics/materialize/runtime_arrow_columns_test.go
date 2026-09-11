package materialize

import (
	"reflect"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func TestDataQueryColumnsFromArrowSchemaPreservesSupportedTypes(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "enabled", Type: arrow.FixedWidthTypes.Boolean},
		{Name: "small_number", Type: arrow.PrimitiveTypes.Int8},
		{Name: "number", Type: arrow.PrimitiveTypes.Int64},
		{Name: "unsigned", Type: arrow.PrimitiveTypes.Uint32},
		{Name: "small_fraction", Type: arrow.PrimitiveTypes.Float32},
		{Name: "fraction", Type: arrow.PrimitiveTypes.Float64},
		{Name: "label", Type: arrow.BinaryTypes.String},
		{Name: "amount", Type: &arrow.Decimal128Type{Precision: 20, Scale: 4}},
		{Name: "day", Type: arrow.FixedWidthTypes.Date32},
		{Name: "millisecond_day", Type: arrow.FixedWidthTypes.Date64},
		{Name: "created_at", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}},
	}, nil)
	want := []dataquery.Column{
		{Name: "enabled", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeBoolean}},
		{Name: "small_number", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 8}},
		{Name: "number", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 64}},
		{Name: "unsigned", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 32}},
		{Name: "small_fraction", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeFloat, BitWidth: 32}},
		{Name: "fraction", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeFloat, BitWidth: 64}},
		{Name: "label", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeString}},
		{Name: "amount", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 20, Scale: 4}},
		{Name: "day", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "day"}},
		{Name: "millisecond_day", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "millisecond"}},
		{Name: "created_at", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeTimestamp, Unit: "microsecond", TimeZone: "UTC"}},
	}
	if got := dataQueryColumnsFromArrowSchema(schema, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("schema columns = %#v, want %#v", got, want)
	}
	if got := dataQueryColumnsFromArrowSchema(schema, "number"); len(got) != len(want)-1 {
		t.Fatalf("excluded schema columns = %d, want %d", len(got), len(want)-1)
	}
}

func TestRuntimeBundleCacheHitPreservesArrowColumnMetadata(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(t, database)
	requests := bundleCacheRequests()
	first, err := runtime.ExecuteDataQueryBundle(t.Context(), requests)
	if err != nil {
		t.Fatal(err)
	}
	want := []dataquery.Column{{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 64}}}
	if got := first.Results["orders"].Columns; !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle miss columns = %#v, want %#v", got, want)
	}
	second, err := runtime.ExecuteDataQueryBundle(t.Context(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Results["orders"].Columns; !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle cache-hit columns = %#v, want %#v", got, want)
	}
}
