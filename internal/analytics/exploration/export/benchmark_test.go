package export

import (
	"context"
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func BenchmarkEncodeGovernedResultCSVAndParquet(b *testing.B) {
	for _, rows := range []int{128, 1000} {
		result := benchmarkGovernedResult(rows)
		limits := Limits{MaxRows: len(result.Rows), MaxBytes: 1 << 20}
		for _, format := range []Format{CSV, Parquet} {
			b.Run(fmt.Sprintf("rows_%d/%s", rows, format), func(b *testing.B) {
				b.ReportAllocs()
				var encoded []byte
				for range b.N {
					var err error
					encoded, err = Encode(context.Background(), result, format, limits)
					if err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(len(result.Rows)), "rows/op")
				b.ReportMetric(float64(len(encoded)), "bytes/op")
			})
		}
	}
}

func benchmarkGovernedResult(rows int) dataquery.Result {
	values := make([]dataquery.Row, rows)
	for index := range values {
		values[index] = dataquery.Row{
			"order_id":     int64(index + 1),
			"order_status": []string{"paid", "pending", "fulfilled"}[index%3],
			"order_count":  int64((index % 7) + 1),
			"revenue":      float64(index+1) * 12.5,
			"note":         fmt.Sprintf("order-%03d", index+1),
		}
	}
	return dataquery.Result{
		Columns: []dataquery.Column{{Name: "order_id"}, {Name: "order_status"}, {Name: "order_count"}, {Name: "revenue"}, {Name: "note"}},
		Rows:    values, TotalRows: rows, TotalRowsKnown: true,
		Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded,
	}
}
