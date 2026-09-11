// Package export encodes the result of a governed exploration execution.
// Encoding is deliberately separate from execution: callers must obtain a
// complete, successful result through the saved-exploration service before
// any response headers or file bytes are sent.
package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

type Format string

const (
	CSV     Format = "csv"
	Parquet Format = "parquet"
)

const (
	DefaultMaxRows  = 10_000
	DefaultMaxBytes = int64(32 << 20)
	MaximumMaxRows  = 10_000
	MaximumMaxBytes = int64(32 << 20)
)

var (
	ErrInvalidFormat  = errors.New("unsupported exploration export format")
	ErrInvalidRequest = errors.New("invalid exploration export request")
	ErrPartial        = errors.New("exploration result is incomplete")
	ErrCanceled       = errors.New("exploration export canceled")
)

// Limits are independent from the interactive query budget. Export callers
// must install these limits with dataquery.WithIndependentResultBudget before
// executing, then Encode applies the same limits to the final wire bytes.
type Limits = dataquery.ResultLimits

func (f Format) Validate() error {
	switch f {
	case CSV, Parquet:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidFormat, f)
	}
}

func validateLimits(l Limits) error {
	if err := l.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if l.MaxRows > MaximumMaxRows {
		return fmt.Errorf("%w: export row limit %d exceeds maximum %d", ErrInvalidRequest, l.MaxRows, MaximumMaxRows)
	}
	if l.MaxBytes > MaximumMaxBytes {
		return fmt.Errorf("%w: export byte limit %d exceeds maximum %d", ErrInvalidRequest, l.MaxBytes, MaximumMaxBytes)
	}
	return nil
}

// Encode validates the execution terminal state, checks both independent
// bounds, and only then returns a complete file. It never writes to an HTTP
// response, which makes cancellation and oversized output atomic at the
// transport boundary.
func Encode(ctx context.Context, result dataquery.Result, format Format, limits Limits) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := format.Validate(); err != nil {
		return nil, err
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	if strings.TrimSpace(result.Error) != "" || (result.Status != "" && result.Status != dataquery.StatusSuccess) || (result.ExecutionState != "" && result.ExecutionState != dataquery.ExecutionSucceeded) {
		if result.ExecutionState == dataquery.ExecutionCanceled || errors.Is(ctx.Err(), context.Canceled) {
			return nil, ErrCanceled
		}
		return nil, ErrPartial
	}
	if result.TotalRowsKnown && result.TotalRows != len(result.Rows) {
		return nil, ErrPartial
	}
	if result.TotalRowsKnown && result.TotalRows > limits.MaxRows {
		return nil, &dataquery.ResultLimitError{Reason: dataquery.ResultRows, Limit: int64(limits.MaxRows), Observed: int64(result.TotalRows)}
	}
	if len(result.Rows) > limits.MaxRows {
		return nil, &dataquery.ResultLimitError{Reason: dataquery.ResultRows, Limit: int64(limits.MaxRows), Observed: int64(len(result.Rows))}
	}
	if err := validateColumns(result.Columns); err != nil {
		return nil, err
	}
	if err := validateRows(result); err != nil {
		return nil, err
	}
	var (
		body []byte
		err  error
	)
	switch format {
	case CSV:
		body, err = encodeCSV(ctx, result, limits.MaxBytes)
	case Parquet:
		body, err = encodeParquet(ctx, result, limits.MaxBytes)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	if int64(len(body)) > limits.MaxBytes {
		return nil, &dataquery.ResultLimitError{Reason: dataquery.ResultBytes, Limit: limits.MaxBytes, Observed: int64(len(body))}
	}
	return body, nil
}

func validateColumns(columns []dataquery.Column) error {
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.Name)
		if name == "" {
			return errors.New("export result contains an empty column name")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("export result contains duplicate column %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateRows(result dataquery.Result) error {
	columns := make(map[string]struct{}, len(result.Columns))
	for _, column := range result.Columns {
		columns[column.Name] = struct{}{}
	}
	for index, row := range result.Rows {
		for name := range columns {
			if _, ok := row[name]; !ok {
				return fmt.Errorf("export result row %d is missing column %q", index+1, name)
			}
		}
		for name := range row {
			if _, ok := columns[name]; !ok {
				return fmt.Errorf("export result row %d contains unknown column %q", index+1, name)
			}
		}
	}
	return nil
}

func encodeCSV(ctx context.Context, result dataquery.Result, maxBytes int64) ([]byte, error) {
	output := &boundedBuffer{max: maxBytes}
	writer := csv.NewWriter(output)
	header := make([]string, len(result.Columns))
	for i, column := range result.Columns {
		header[i] = safeFormulaText(column.Name)
	}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, row := range result.Rows {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
		}
		values := make([]string, len(result.Columns))
		for i, column := range result.Columns {
			values[i] = csvValue(row[column.Name])
		}
		if err := writer.Write(values); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	return output.Bytes(), nil
}

func csvValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return safeFormulaText(text)
	}
	if raw, ok := value.([]byte); ok {
		return safeFormulaText(string(raw))
	}
	return scalarString(value)
}

func safeFormulaText(value string) string {
	if value == "" {
		return value
	}
	first := 0
	for first < len(value) {
		switch value[first] {
		case '\t', '\r', '\n':
			first++
		default:
			goto checked
		}
	}
	return value

checked:
	switch value[first] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

func scalarString(value any) string {
	switch value := value.(type) {
	case json.Number:
		return value.String()
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano)
	case bool:
		return strconv.FormatBool(value)
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func encodeParquet(ctx context.Context, result dataquery.Result, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	types, err := parquetTypes(result)
	if err != nil {
		return nil, err
	}
	fields := make([]arrow.Field, len(result.Columns))
	for index, column := range result.Columns {
		fields[index] = arrow.Field{Name: column.Name, Type: types[index], Nullable: true}
	}
	schema := arrow.NewSchema(fields, nil)
	// RecordFromJSON is the Arrow v18 adapter available here, but do not first
	// marshal the whole result into one unbounded temporary. Stream one row at a
	// time through a bounded, cancellation-aware JSON staging buffer; the same
	// byte ceiling therefore applies before and during Parquet encoding.
	jsonRows := &boundedBuffer{max: maxBytes}
	if _, err := jsonRows.Write([]byte{'['}); err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(&contextBoundedWriter{ctx: ctx, buffer: jsonRows})
	for index, row := range result.Rows {
		if index > 0 {
			if _, err := jsonRows.Write([]byte{','}); err != nil {
				return nil, err
			}
		}
		encoded := make(map[string]any, len(result.Columns))
		for columnIndex, column := range result.Columns {
			value := row[column.Name]
			if value != nil {
				converted, conversionErr := parquetJSONValue(value, types[columnIndex], column.Type)
				if conversionErr != nil {
					return nil, fmt.Errorf("export column %q: %w", column.Name, conversionErr)
				}
				encoded[column.Name] = converted
			}
		}
		if err := encoder.Encode(encoded); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
			}
			return nil, err
		}
	}
	if _, err := jsonRows.Write([]byte{']'}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	// Preserve integer and decimal lexemes through JSON; the default decoder's
	// float64 path rounds large integers before Arrow sees them.
	record, _, err := array.RecordFromJSON(memory.DefaultAllocator, schema, bytes.NewReader(jsonRows.Bytes()), array.WithUseNumber())
	if err != nil {
		return nil, err
	}
	defer record.Release()
	output := &boundedBuffer{max: maxBytes}
	// Persist the Arrow schema so declared widths and decimal/temporal logical
	// types survive a Parquet round trip; physical storage alone cannot preserve
	// all of those distinctions.
	writer, err := pqarrow.NewFileWriter(schema, output, parquet.NewWriterProperties(), pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema()))
	if err != nil {
		return nil, err
	}
	if err := writer.Write(record); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	return output.Bytes(), nil
}

func parquetTypes(result dataquery.Result) ([]arrow.DataType, error) {
	types := make([]arrow.DataType, len(result.Columns))
	for index, column := range result.Columns {
		if column.Type.Kind != "" {
			typed, err := parquetTypeForColumn(column.Type)
			if err != nil {
				return nil, fmt.Errorf("export column %q: %w", column.Name, err)
			}
			for _, row := range result.Rows {
				if value := row[column.Name]; value != nil {
					if err := validateTypedValue(value, column.Type); err != nil {
						return nil, fmt.Errorf("export column %q: %w", column.Name, err)
					}
				}
			}
			types[index] = typed
			continue
		}
		var selected arrow.DataType
		var decimalPrecision, decimalScale int32
		for _, row := range result.Rows {
			value := row[column.Name]
			if value == nil {
				continue
			}
			candidate, err := parquetType(value)
			if err != nil {
				return nil, fmt.Errorf("export column %q: %w", column.Name, err)
			}
			if decimalType, ok := candidate.(*arrow.Decimal128Type); ok {
				if decimalType.Precision > decimalPrecision {
					decimalPrecision = decimalType.Precision
				}
				if decimalType.Scale > decimalScale {
					decimalScale = decimalType.Scale
				}
			}
			if selected == nil {
				selected = candidate
			} else if selected.ID() != candidate.ID() {
				return nil, fmt.Errorf("export column %q contains incompatible value types", column.Name)
			}
		}
		if selected == nil {
			// Producers without retained schema metadata carry only the projected
			// name. With no non-null value, the conservative legacy contract is
			// nullable UTF-8 text; Arrow-backed producers use the typed path above.
			selected = arrow.BinaryTypes.String
		} else if selected.ID() == arrow.DECIMAL128 {
			// Align all fixed-scale decimals to one scale without converting them
			// through float64. Values with fewer fractional digits need extra
			// precision after alignment.
			for _, row := range result.Rows {
				number, ok := row[column.Name].(json.Number)
				if !ok || !strings.Contains(number.String(), ".") {
					continue
				}
				precision, scale, err := decimalShape(number.String())
				if err != nil {
					return nil, fmt.Errorf("export column %q: %w", column.Name, err)
				}
				if required := precision + decimalScale - scale; required > decimalPrecision {
					decimalPrecision = required
				}
			}
			if decimalPrecision < decimalScale {
				decimalPrecision = decimalScale
			}
			if decimalPrecision < 1 {
				decimalPrecision = 1
			}
			if decimalPrecision > 38 || decimalScale > 38 {
				return nil, fmt.Errorf("decimal precision exceeds Arrow decimal128 capacity")
			}
			selected = &arrow.Decimal128Type{Precision: decimalPrecision, Scale: decimalScale}
		}
		types[index] = selected
	}
	return types, nil
}

func parquetType(value any) (arrow.DataType, error) {
	switch value := value.(type) {
	case bool:
		return arrow.FixedWidthTypes.Boolean, nil
	case int, int8, int16, int32, int64:
		return arrow.PrimitiveTypes.Int64, nil
	case uint, uint8, uint16, uint32, uint64:
		return arrow.PrimitiveTypes.Uint64, nil
	case json.Number:
		if strings.ContainsAny(value.String(), ".eE") {
			precision, scale, err := decimalShape(value.String())
			if err != nil {
				return nil, err
			}
			return &arrow.Decimal128Type{Precision: precision, Scale: scale}, nil
		}
		if _, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			return arrow.PrimitiveTypes.Int64, nil
		}
		if _, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
			return arrow.PrimitiveTypes.Uint64, nil
		}
		return nil, fmt.Errorf("invalid numeric value %q", value)
	case float32, float64:
		if number, ok := any(value).(float32); ok && (math.IsNaN(float64(number)) || math.IsInf(float64(number), 0)) {
			return nil, fmt.Errorf("non-finite floating-point value is unsupported")
		}
		if number, ok := any(value).(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
			return nil, fmt.Errorf("non-finite floating-point value is unsupported")
		}
		return arrow.PrimitiveTypes.Float64, nil
	case string:
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("invalid UTF-8 string value")
		}
		return arrow.BinaryTypes.String, nil
	case []byte:
		if !utf8.Valid(value) {
			return nil, fmt.Errorf("invalid UTF-8 binary value")
		}
		return arrow.BinaryTypes.String, nil
	case time.Time:
		return arrow.BinaryTypes.String, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}
}

func parquetJSONValue(value any, dtype arrow.DataType, columnType dataquery.ColumnType) (any, error) {
	if dtype.ID() == arrow.INT64 {
		switch value := value.(type) {
		case json.Number:
			parsed, _ := strconv.ParseInt(value.String(), 10, 64)
			return parsed, nil
		case int:
			return int64(value), nil
		case int8:
			return int64(value), nil
		case int16:
			return int64(value), nil
		case int32:
			return int64(value), nil
		}
	}
	if dtype.ID() == arrow.UINT64 {
		switch value := value.(type) {
		case json.Number:
			return value.String(), nil
		case uint64:
			return strconv.FormatUint(value, 10), nil
		case uint:
			return strconv.FormatUint(uint64(value), 10), nil
		case uint8:
			return strconv.FormatUint(uint64(value), 10), nil
		case uint16:
			return strconv.FormatUint(uint64(value), 10), nil
		case uint32:
			return strconv.FormatUint(uint64(value), 10), nil
		}
	}
	if dtype.ID() == arrow.DECIMAL128 {
		if columnType.Kind == dataquery.ColumnTypeDecimal {
			text, err := decimalValueText(value, columnType.Scale, columnType.Precision)
			if err != nil {
				return nil, err
			}
			return text, nil
		}
		if number, ok := value.(json.Number); ok {
			// The decimal builder accepts strings and parses them exactly. Keeping
			// this as a JSON number makes some Arrow decoder paths round it through
			// binary floating point before the decimal builder sees it.
			return number.String(), nil
		}
	}
	if columnType.Kind == dataquery.ColumnTypeDate || columnType.Kind == dataquery.ColumnTypeTimestamp {
		encoded, err := temporalJSONValue(value, columnType)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	}
	if dtype.ID() == arrow.FLOAT64 {
		if value, ok := value.(float32); ok {
			return float64(value), nil
		}
	}
	if dtype.ID() == arrow.STRING {
		if value, ok := value.([]byte); ok {
			return string(value), nil
		}
		if value, ok := value.(time.Time); ok {
			return value.UTC().Format(time.RFC3339Nano), nil
		}
		if _, ok := value.(string); !ok {
			return scalarString(value), nil
		}
	}
	return value, nil
}

func decimalValueText(value any, scale, precision int32) (string, error) {
	if precision < 1 || precision > 38 || scale < 0 || scale > precision {
		return "", fmt.Errorf("invalid decimal precision/scale %d/%d", precision, scale)
	}
	var text string
	switch value := value.(type) {
	case string:
		text = value
	case []byte:
		text = string(value)
	case json.Number:
		text = value.String()
	default:
		return "", fmt.Errorf("incompatible decimal value type %T", value)
	}
	if text == "" || strings.ContainsAny(text, "eE") {
		return "", fmt.Errorf("invalid decimal value %q", text)
	}
	negative := strings.HasPrefix(text, "-")
	if negative || strings.HasPrefix(text, "+") {
		text = text[1:]
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || text == "" {
		return "", fmt.Errorf("invalid decimal value %q", text)
	}
	whole, fraction := parts[0], ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" && fraction == "" {
		return "", fmt.Errorf("invalid decimal value %q", text)
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || !allDigits(fraction) || int32(len(fraction)) > scale {
		return "", fmt.Errorf("decimal value %q is incompatible with scale %d", text, scale)
	}
	trimmedWhole := strings.TrimLeft(whole, "0")
	if trimmedWhole == "" {
		trimmedWhole = "0"
	}
	fraction = fraction + strings.Repeat("0", int(scale)-len(fraction))
	unscaled := strings.TrimLeft(trimmedWhole+fraction, "0")
	if unscaled == "" {
		unscaled = "0"
	}
	if int32(len(unscaled)) > precision {
		return "", fmt.Errorf("decimal value exceeds precision %d", precision)
	}
	sign := ""
	if negative && unscaled != "0" {
		sign = "-"
	}
	if scale == 0 {
		return sign + trimmedWhole, nil
	}
	return sign + trimmedWhole + "." + fraction, nil
}

func temporalJSONValue(value any, columnType dataquery.ColumnType) (any, error) {
	var instant time.Time
	switch value := value.(type) {
	case time.Time:
		instant = value
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, fmt.Errorf("invalid temporal value %q", value)
		}
		instant = parsed
	default:
		return nil, fmt.Errorf("incompatible temporal value type %T", value)
	}
	instant = instant.UTC()
	if columnType.Kind == dataquery.ColumnTypeDate {
		if instant.Hour() != 0 || instant.Minute() != 0 || instant.Second() != 0 || instant.Nanosecond() != 0 {
			return nil, fmt.Errorf("date value %q contains time-of-day", instant.Format(time.RFC3339Nano))
		}
		switch columnType.Unit {
		case "day":
			return json.Number(strconv.FormatInt(int64(arrow.Date32FromTime(instant)), 10)), nil
		case "millisecond":
			return json.Number(strconv.FormatInt(int64(arrow.Date64FromTime(instant)), 10)), nil
		default:
			return nil, fmt.Errorf("invalid date unit %q", columnType.Unit)
		}
	}
	unit, err := parquetTimeUnit(columnType.Unit)
	if err != nil {
		return nil, err
	}
	if _, err := arrow.TimestampFromTime(instant, unit); err != nil {
		return nil, err
	}
	// RecordFromJSON accepts RFC3339 text for timestamps and applies the
	// declared unit without losing the retained timezone semantics. Numeric
	// timestamp text is not accepted by every Arrow builder version.
	return instant.Format(time.RFC3339Nano), nil
}

func decimalShape(value string) (precision, scale int32, err error) {
	if strings.ContainsAny(value, "eE") {
		return 0, 0, fmt.Errorf("decimal exponent %q is unsupported; use a fixed-scale decimal", value)
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(value, "+"), "-")
	parts := strings.Split(trimmed, ".")
	if len(parts) > 2 || (len(parts) == 1 && parts[0] == "") {
		return 0, 0, fmt.Errorf("invalid decimal value %q", value)
	}
	whole := parts[0]
	fract := ""
	if len(parts) == 2 {
		fract = parts[1]
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || !allDigits(fract) || (whole == "0" && fract == "") {
		return 0, 0, fmt.Errorf("invalid decimal value %q", value)
	}
	precision = int32(len(strings.TrimLeft(whole, "0")) + len(fract))
	if precision == 0 {
		precision = 1
	}
	scale = int32(len(fract))
	if precision < scale {
		precision = scale
	}
	if precision > 38 || scale > 38 {
		return 0, 0, fmt.Errorf("decimal value %q exceeds decimal128 precision", value)
	}
	return precision, scale, nil
}

func allDigits(value string) bool {
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

// boundedBuffer makes the no-partial-response guarantee useful even while an
// encoder is producing a file. Parquet metadata is written during Close, so a
// final post-close length check alone would allow unbounded allocation.
type boundedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.max {
		return 0, &dataquery.ResultLimitError{Reason: dataquery.ResultBytes, Limit: b.max, Observed: int64(b.Len()) + int64(len(p))}
	}
	return b.Buffer.Write(p)
}

type contextBoundedWriter struct {
	ctx    context.Context
	buffer *boundedBuffer
}

func (w *contextBoundedWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.buffer.Write(p)
}
