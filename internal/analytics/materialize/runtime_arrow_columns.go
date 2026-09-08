package materialize

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

// dataQueryColumnsFromArrowSchema is the one analytical type boundary for
// retained result schemas. The dataquery package stays independent from
// Arrow; only the small logical subset needed by result encoders crosses it.
func dataQueryColumnsFromArrowSchema(schema *arrow.Schema, excluded string) []dataquery.Column {
	if schema == nil {
		return nil
	}
	columns := make([]dataquery.Column, 0, len(schema.Fields()))
	for _, field := range schema.Fields() {
		if field.Name == excluded {
			continue
		}
		column := dataquery.Column{Name: field.Name}
		if metadata, ok := dataQueryColumnType(field.Type); ok {
			column.Type = metadata
		}
		columns = append(columns, column)
	}
	return columns
}

func dataQueryColumnType(dataType arrow.DataType) (dataquery.ColumnType, bool) {
	switch value := dataType.(type) {
	case *arrow.BooleanType:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeBoolean}, true
	case *arrow.Int8Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 8}, true
	case *arrow.Int16Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 16}, true
	case *arrow.Int32Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 32}, true
	case *arrow.Int64Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 64}, true
	case *arrow.Uint8Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 8}, true
	case *arrow.Uint16Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 16}, true
	case *arrow.Uint32Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 32}, true
	case *arrow.Uint64Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 64}, true
	case *arrow.Float32Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeFloat, BitWidth: 32}, true
	case *arrow.Float64Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeFloat, BitWidth: 64}, true
	case *arrow.StringType, *arrow.LargeStringType:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeString}, true
	case *arrow.Decimal32Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: value.Precision, Scale: value.Scale}, true
	case *arrow.Decimal64Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: value.Precision, Scale: value.Scale}, true
	case *arrow.Decimal128Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: value.Precision, Scale: value.Scale}, true
	case *arrow.Decimal256Type:
		// The renderer-neutral descriptor intentionally carries precision and
		// scale only. Export currently lowers all supported decimals to Arrow
		// decimal128, so a precision above its capacity is rejected there.
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: value.Precision, Scale: value.Scale}, true
	case *arrow.Date32Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "day"}, true
	case *arrow.Date64Type:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "millisecond"}, true
	case *arrow.TimestampType:
		return dataquery.ColumnType{Kind: dataquery.ColumnTypeTimestamp, Unit: arrowTimeUnit(value.Unit), TimeZone: value.TimeZone}, true
	default:
		return dataquery.ColumnType{}, false
	}
}

func arrowTimeUnit(unit arrow.TimeUnit) string {
	switch unit {
	case arrow.Second:
		return "second"
	case arrow.Millisecond:
		return "millisecond"
	case arrow.Microsecond:
		return "microsecond"
	case arrow.Nanosecond:
		return "nanosecond"
	default:
		return ""
	}
}
