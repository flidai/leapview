// Package arrowdecode converts immutable Arrow result leases into LeapView
// domain-row values at the analytical data-plane boundary.
package arrowdecode

import (
	"fmt"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/flidai/leapview/pkg/arrowresult"
)

// DecodeRows is the single boundary conversion from the analytical Arrow data
// plane into Go domain values. It never mutates or retains the leased records.
func DecodeRows(lease *arrowresult.Lease) ([]map[string]any, error) {
	if lease == nil || lease.Schema() == nil {
		return nil, arrowresult.ErrResultReleased
	}
	rows := make([]map[string]any, 0, lease.Rows())
	err := lease.VisitRecords(func(record arrow.RecordBatch) error {
		decoders := make([]columnDecoder, int(record.NumCols()))
		for columnIndex := range decoders {
			name := record.ColumnName(columnIndex)
			decode, err := compileValueDecoder(record.Column(columnIndex))
			if err != nil {
				return fmt.Errorf("decode Arrow column %q: %w", name, err)
			}
			decoders[columnIndex] = columnDecoder{name: name, value: decode}
		}
		for rowIndex := 0; rowIndex < int(record.NumRows()); rowIndex++ {
			row := make(map[string]any, len(decoders))
			for _, decoder := range decoders {
				row[decoder.name] = decoder.value(rowIndex)
			}
			rows = append(rows, row)
		}
		return nil
	})
	return rows, err
}

type marshalValue interface{ GetOneForMarshal(int) interface{} }
type valueDecoder func(int) any

type columnDecoder struct {
	name  string
	value valueDecoder
}

func compileValueDecoder(values arrow.Array) (valueDecoder, error) {
	if values == nil {
		return func(int) any { return nil }, nil
	}
	var decode valueDecoder
	switch values := values.(type) {
	case *array.Boolean:
		decode = func(index int) any { return values.Value(index) }
	case *array.Int8:
		decode = func(index int) any { return values.Value(index) }
	case *array.Int16:
		decode = func(index int) any { return values.Value(index) }
	case *array.Int32:
		decode = func(index int) any { return values.Value(index) }
	case *array.Int64:
		decode = func(index int) any { return values.Value(index) }
	case *array.Uint8:
		decode = func(index int) any { return values.Value(index) }
	case *array.Uint16:
		decode = func(index int) any { return values.Value(index) }
	case *array.Uint32:
		decode = func(index int) any { return values.Value(index) }
	case *array.Uint64:
		decode = func(index int) any { return values.Value(index) }
	case *array.Float32:
		decode = func(index int) any { return values.Value(index) }
	case *array.Float64:
		decode = func(index int) any { return values.Value(index) }
	case *array.String:
		decode = ownedStringDecoder(values)
	case *array.LargeString:
		decode = ownedLargeStringDecoder(values)
	case *array.Binary:
		decode = ownedBinaryDecoder(values)
	case *array.LargeBinary:
		decode = ownedLargeBinaryDecoder(values)
	case *array.Date32:
		decode = func(index int) any { return values.Value(index).ToTime() }
	case *array.Date64:
		decode = func(index int) any { return values.Value(index).ToTime() }
	case *array.Timestamp:
		typeInfo, ok := values.DataType().(*arrow.TimestampType)
		if !ok {
			return nil, fmt.Errorf("timestamp has invalid data type %T", values.DataType())
		}
		decode = func(index int) any { return values.Value(index).ToTime(typeInfo.Unit).In(time.UTC) }
	case *array.Decimal32:
		typeInfo := values.DataType().(*arrow.Decimal32Type)
		decode = func(index int) any {
			return decodeDecimal(values.Value(index).ToString(typeInfo.Scale))
		}
	case *array.Decimal64:
		typeInfo := values.DataType().(*arrow.Decimal64Type)
		decode = func(index int) any {
			return decodeDecimal(values.Value(index).ToString(typeInfo.Scale))
		}
	case *array.Decimal128:
		typeInfo := values.DataType().(*arrow.Decimal128Type)
		decode = func(index int) any {
			return decodeDecimal(values.Value(index).ToString(typeInfo.Scale))
		}
	case *array.Decimal256:
		typeInfo := values.DataType().(*arrow.Decimal256Type)
		decode = func(index int) any {
			return decodeDecimal(values.Value(index).ToString(typeInfo.Scale))
		}
	case *array.Dictionary:
		dictionary, err := compileValueDecoder(values.Dictionary())
		if err != nil {
			return nil, err
		}
		decode = func(index int) any { return dictionary(values.GetValueIndex(index)) }
	default:
		if marshal, ok := values.(marshalValue); ok {
			decode = func(index int) any { return marshal.GetOneForMarshal(index) }
			break
		}
		return nil, fmt.Errorf("unsupported Arrow type %s", values.DataType())
	}
	if values.NullN() == 0 {
		return decode, nil
	}
	return func(index int) any {
		if values.IsNull(index) {
			return nil
		}
		return decode(index)
	}, nil
}

func ownedStringDecoder(values *array.String) valueDecoder {
	offsets := values.ValueOffsets()
	base := int(offsets[0])
	owned := string(values.ValueBytes())
	return func(index int) any {
		start, end := int(offsets[index])-base, int(offsets[index+1])-base
		return owned[start:end]
	}
}

func ownedLargeStringDecoder(values *array.LargeString) valueDecoder {
	offsets := values.ValueOffsets()
	base := int(offsets[0])
	owned := string(values.ValueBytes())
	return func(index int) any {
		start, end := int(offsets[index])-base, int(offsets[index+1])-base
		return owned[start:end]
	}
}

func ownedBinaryDecoder(values *array.Binary) valueDecoder {
	offsets := values.ValueOffsets()
	base := int(offsets[0])
	owned := append([]byte(nil), values.ValueBytes()...)
	return func(index int) any {
		start, end := int(offsets[index])-base, int(offsets[index+1])-base
		return owned[start:end:end]
	}
}

func ownedLargeBinaryDecoder(values *array.LargeBinary) valueDecoder {
	offsets := values.ValueOffsets()
	base := int(offsets[0])
	owned := append([]byte(nil), values.ValueBytes()...)
	return func(index int) any {
		start, end := int(offsets[index])-base, int(offsets[index+1])-base
		return owned[start:end:end]
	}
}

func decodeDecimal(value string) any {
	// Decimal values cross the Arrow boundary as canonical fixed-point text,
	// including scale-zero values. Returning *big.Int for integral DECIMAL
	// values makes the transport type depend on scale and invites lossy JSON
	// coercion downstream. Keep the exact base-10 representation uniform for
	// every physical DECIMAL width and scale.
	return value
}
