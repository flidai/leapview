package runtime

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
)

// BenchmarkSortAggregateTableRows measures the in-process ordering applied to
// an interactive table after a user sorts a calculated, pivot, or matrix
// column. Each operation alternates direction so the input is never already
// ordered by the requested key.
func BenchmarkSortAggregateTableRows(b *testing.B) {
	for _, rowCount := range []int{100, 1_000, 10_000} {
		for _, shape := range []string{"text", "mixed"} {
			b.Run(shape+"/rows_"+strconv.Itoa(rowCount), func(b *testing.B) {
				rows := tableSortBenchmarkRows(rowCount, shape)
				b.ReportAllocs()
				b.ResetTimer()
				direction := "asc"
				for range b.N {
					sortAggregateTableRows(rows, dashboard.TableSort{Key: "label", Direction: direction})
					if direction == "asc" {
						direction = "desc"
					} else {
						direction = "asc"
					}
				}
				b.ReportMetric(float64(rowCount), "rows/op")
			})
		}
	}
}

func tableSortBenchmarkRows(count int, shape string) []map[string]any {
	rows := make([]map[string]any, count)
	for index := range rows {
		var label any
		if shape == "mixed" && index%3 == 0 {
			label = float64((index * 37) % count)
		} else {
			label = fmt.Sprintf("label-%05d", (index*104729)%count)
		}
		rows[index] = map[string]any{"label": label, "index": index}
	}
	return rows
}
