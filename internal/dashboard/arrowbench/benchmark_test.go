package arrowbench

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

var dashboardBenchmarkRows [][]any

func BenchmarkDashboardRowShaping(b *testing.B) {
	shapes := []struct {
		name    string
		columns int
	}{
		{name: "narrow", columns: 8},
		{name: "wide", columns: 32},
	}
	for _, shape := range shapes {
		for _, rows := range []int{1, 50, 1_000, 10_000} {
			b.Run(shape.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				columns, records := dashboardBenchmarkFixture(rows, shape.columns)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					dashboardBenchmarkRows = shapeDashboardRows(columns, records)
				}
				b.ReportMetric(float64(rows), "rows/op")
				b.ReportMetric(float64(shape.columns), "columns/op")
			})
		}
	}
}

func BenchmarkDashboardStringProjection(b *testing.B) {
	shapes := []struct {
		name    string
		columns int
	}{
		{name: "narrow", columns: 8},
		{name: "wide", columns: 32},
	}
	for _, shape := range shapes {
		for _, rows := range []int{1, 50, 1_000, 10_000} {
			b.Run(shape.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				columns, records := dashboardBenchmarkFixture(rows, shape.columns)
				ordered := shapeDashboardRows(columns, records)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					projected := make([][]any, len(ordered))
					for rowIndex, row := range ordered {
						projected[rowIndex] = make([]any, len(row))
						for columnIndex, value := range row {
							projected[rowIndex][columnIndex] = dashboardBenchmarkCellString(value)
						}
					}
					dashboardBenchmarkRows = projected
				}
				b.ReportMetric(float64(rows), "rows/op")
				b.ReportMetric(float64(shape.columns), "columns/op")
			})
		}
	}
}

func TestDashboardBenchmarkFixtureIsDeterministic(t *testing.T) {
	columns, records := dashboardBenchmarkFixture(50, 8)
	first := shapeDashboardRows(columns, records)
	second := shapeDashboardRows(columns, records)
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("identical dashboard evidence fixtures produced different shaped rows")
	}
}

func dashboardBenchmarkFixture(rows, columnCount int) ([]string, []map[string]any) {
	columns := make([]string, columnCount)
	for column := range columns {
		columns[column] = fmt.Sprintf("field_%02d", column)
	}
	records := make([]map[string]any, rows)
	for row := range records {
		records[row] = make(map[string]any, columnCount)
		for column, name := range columns {
			records[row][name] = dashboardBenchmarkValue(row, column)
		}
	}
	return columns, records
}

func shapeDashboardRows(columns []string, records []map[string]any) [][]any {
	rows := make([][]any, len(records))
	for rowIndex, record := range records {
		row := make([]any, len(columns))
		for columnIndex, column := range columns {
			row[columnIndex] = record[column]
		}
		rows[rowIndex] = row
	}
	return rows
}

func dashboardBenchmarkValue(row, column int) any {
	if (row+column)%13 == 0 {
		return nil
	}
	value := int64(row*37 + column + 1)
	switch column % 8 {
	case 0:
		return value
	case 1:
		return float64(value)/7.0 + 0.125
	case 2:
		return (row+column)%2 == 0
	case 3:
		return "value-" + strconv.Itoa((row*17+column)%997)
	case 4:
		return []byte("bytes-" + strconv.Itoa((row*19+column)%997))
	case 5:
		return time.UnixMicro(1_700_000_000_000_000 + value*1_000).UTC()
	case 6:
		return strconv.FormatInt(value, 10) + ".000"
	default:
		return time.Unix(0, 0).UTC().AddDate(0, 0, int(value%2_000))
	}
}

func dashboardBenchmarkCellString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		encoded, err := json.Marshal(typed)
		if err == nil && (len(encoded) == 0 || encoded[0] == '{' || encoded[0] == '[') {
			return string(encoded)
		}
		return fmt.Sprint(typed)
	}
}
