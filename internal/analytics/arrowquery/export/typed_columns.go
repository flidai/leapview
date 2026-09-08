package export

import (
	"fmt"
	"math"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func parquetTypeForColumn(columnType dataquery.ColumnType) (arrow.DataType, error) {
	switch columnType.Kind {
	case dataquery.ColumnTypeBoolean:
		if columnType.BitWidth != 0 {
			return nil, fmt.Errorf("invalid boolean bit width %d", columnType.BitWidth)
		}
		return arrow.FixedWidthTypes.Boolean, nil
	case dataquery.ColumnTypeInteger:
		switch columnType.BitWidth {
		case 8:
			return arrow.PrimitiveTypes.Int8, nil
		case 16:
			return arrow.PrimitiveTypes.Int16, nil
		case 32:
			return arrow.PrimitiveTypes.Int32, nil
		case 64:
			return arrow.PrimitiveTypes.Int64, nil
		default:
			return nil, fmt.Errorf("invalid integer bit width %d", columnType.BitWidth)
		}
	case dataquery.ColumnTypeUnsigned:
		switch columnType.BitWidth {
		case 8:
			return arrow.PrimitiveTypes.Uint8, nil
		case 16:
			return arrow.PrimitiveTypes.Uint16, nil
		case 32:
			return arrow.PrimitiveTypes.Uint32, nil
		case 64:
			return arrow.PrimitiveTypes.Uint64, nil
		default:
			return nil, fmt.Errorf("invalid unsigned bit width %d", columnType.BitWidth)
		}
	case dataquery.ColumnTypeFloat:
		switch columnType.BitWidth {
		case 32:
			return arrow.PrimitiveTypes.Float32, nil
		case 64:
			return arrow.PrimitiveTypes.Float64, nil
		default:
			return nil, fmt.Errorf("invalid float bit width %d", columnType.BitWidth)
		}
	case dataquery.ColumnTypeString:
		if columnType.BitWidth != 0 {
			return nil, fmt.Errorf("invalid string bit width %d", columnType.BitWidth)
		}
		return arrow.BinaryTypes.String, nil
	case dataquery.ColumnTypeDecimal:
		if columnType.Precision < 1 || columnType.Precision > 38 || columnType.Scale < 0 || columnType.Scale > columnType.Precision {
			return nil, fmt.Errorf("invalid decimal precision/scale %d/%d", columnType.Precision, columnType.Scale)
		}
		return &arrow.Decimal128Type{Precision: columnType.Precision, Scale: columnType.Scale}, nil
	case dataquery.ColumnTypeDate:
		switch columnType.Unit {
		case "day":
			return arrow.FixedWidthTypes.Date32, nil
		case "millisecond":
			return arrow.FixedWidthTypes.Date64, nil
		default:
			return nil, fmt.Errorf("invalid date unit %q", columnType.Unit)
		}
	case dataquery.ColumnTypeTimestamp:
		unit, err := parquetTimeUnit(columnType.Unit)
		if err != nil {
			return nil, err
		}
		return &arrow.TimestampType{Unit: unit, TimeZone: columnType.TimeZone}, nil
	default:
		return nil, fmt.Errorf("unsupported result column type %q", columnType.Kind)
	}
}

func parquetTimeUnit(value string) (arrow.TimeUnit, error) {
	switch value {
	case "second":
		return arrow.Second, nil
	case "millisecond":
		return arrow.Millisecond, nil
	case "microsecond":
		return arrow.Microsecond, nil
	case "nanosecond":
		return arrow.Nanosecond, nil
	default:
		return arrow.Second, fmt.Errorf("invalid timestamp unit %q", value)
	}
}

func validateTypedValue(value any, columnType dataquery.ColumnType) error {
	switch columnType.Kind {
	case dataquery.ColumnTypeBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("incompatible boolean value type %T", value)
		}
	case dataquery.ColumnTypeInteger:
		if !isSignedInteger(value, columnType.BitWidth) {
			return fmt.Errorf("incompatible integer value type %T for bit width %d", value, columnType.BitWidth)
		}
	case dataquery.ColumnTypeUnsigned:
		if !isUnsignedInteger(value, columnType.BitWidth) {
			return fmt.Errorf("incompatible unsigned value type %T for bit width %d", value, columnType.BitWidth)
		}
	case dataquery.ColumnTypeFloat:
		if !isFiniteFloat(value, columnType.BitWidth) {
			return fmt.Errorf("incompatible or non-finite float value type %T for bit width %d", value, columnType.BitWidth)
		}
	case dataquery.ColumnTypeString:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("incompatible string value type %T", value)
		}
	case dataquery.ColumnTypeDecimal:
		_, err := decimalValueText(value, columnType.Scale, columnType.Precision)
		return err
	case dataquery.ColumnTypeDate, dataquery.ColumnTypeTimestamp:
		_, err := temporalJSONValue(value, columnType)
		return err
	default:
		return fmt.Errorf("unsupported result column type %q", columnType.Kind)
	}
	return nil
}

func isSignedInteger(value any, bitWidth int32) bool {
	switch bitWidth {
	case 8:
		_, ok := value.(int8)
		return ok
	case 16:
		_, ok := value.(int16)
		return ok
	case 32:
		_, ok := value.(int32)
		return ok
	case 64:
		_, ok := value.(int64)
		return ok
	default:
		return false
	}
}

func isUnsignedInteger(value any, bitWidth int32) bool {
	switch bitWidth {
	case 8:
		_, ok := value.(uint8)
		return ok
	case 16:
		_, ok := value.(uint16)
		return ok
	case 32:
		_, ok := value.(uint32)
		return ok
	case 64:
		_, ok := value.(uint64)
		return ok
	default:
		return false
	}
}

func isFiniteFloat(value any, bitWidth int32) bool {
	switch bitWidth {
	case 32:
		v, ok := value.(float32)
		return ok && !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
	case 64:
		v, ok := value.(float64)
		return ok && !math.IsNaN(v) && !math.IsInf(v, 0)
	default:
		return false
	}
}
