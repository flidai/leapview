//go:build duckdb_arrow

package materialize

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	explorationexport "github.com/flidai/leapview/internal/analytics/exploration/export"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/stretchr/testify/require"
)

// Exercise real DuckDB decimal output, the production Arrow-to-domain boundary,
// and Parquet together. Hand-authored json.Number fixtures miss the canonical
// fixed-point string representation returned by the analytical runtime.
func TestDuckDBDecimalResultExportsExactParquet(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("duckdb", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "values", true: "empty"}[empty], func(t *testing.T) {
			collector := arrowresult.NewBuilder()
			defer collector.Abort()
			err := conn.Raw(func(raw any) error {
				arrowConn, err := duckdb.NewArrowFromConn(raw.(driver.Conn))
				if err != nil {
					return err
				}
				statement := `SELECT
					CAST('12345678901234567890.1234567890' AS DECIMAL(38,10)) AS fractional,
					CAST('99999999999999999999999999999999999999' AS DECIMAL(38,0)) AS integral,
					CAST('0' AS DECIMAL(1,0)) AS zero,
					CAST(NULL AS DECIMAL(12,4)) AS missing,
					'00123.4500' AS id`
				if empty {
					statement += " WHERE FALSE"
				}
				reader, err := arrowConn.QueryContext(ctx, statement)
				if err != nil {
					return err
				}
				defer reader.Release()
				if err := collector.WriteSchema(reader.Schema()); err != nil {
					return err
				}
				for reader.Next() {
					if err := collector.WriteRecord(reader.RecordBatch()); err != nil {
						return err
					}
				}
				return reader.Err()
			})
			require.NoError(t, err)
			captured, err := collector.Finish()
			require.NoError(t, err)
			defer captured.Release()
			lease, err := captured.Acquire()
			require.NoError(t, err)
			result, err := decodeArrowQueryResult(dataquery.Query{}, lease, resultcache.Metadata{}, dataquery.Result{})
			lease.Release()
			require.NoError(t, err)
			require.Equal(t, []dataquery.Column{
				{Name: "fractional", DecimalPrecision: 38, DecimalScale: 10},
				{Name: "integral", DecimalPrecision: 38},
				{Name: "zero", DecimalPrecision: 1},
				{Name: "missing", DecimalPrecision: 12, DecimalScale: 4},
				{Name: "id"},
			}, result.Columns)
			if !empty {
				require.Equal(t, []dataquery.Row{{"fractional": "12345678901234567890.1234567890", "integral": "99999999999999999999999999999999999999", "zero": "0", "missing": nil, "id": "00123.4500"}}, result.Rows)
			}
			body, err := explorationexport.Encode(ctx, result, explorationexport.Parquet, explorationexport.Limits{MaxRows: 10, MaxBytes: 1 << 20})
			require.NoError(t, err)
			table, err := pqarrow.ReadTable(ctx, bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
			require.NoError(t, err)
			defer table.Release()
			require.Equal(t, int64(len(result.Rows)), table.NumRows())
			for index, column := range result.Columns[:4] {
				require.Equal(t, &arrow.Decimal128Type{Precision: column.DecimalPrecision, Scale: column.DecimalScale}, table.Column(index).DataType())
				if empty {
					continue
				}
				values := table.Column(index).Data().Chunk(0).(*array.Decimal128)
				if column.Name == "missing" {
					require.True(t, values.IsNull(0))
				} else {
					require.Equal(t, result.Rows[0][column.Name], values.Value(0).ToString(column.DecimalScale))
				}
			}
			require.Equal(t, arrow.STRING, table.Column(4).DataType().ID())
			if !empty {
				require.Equal(t, "00123.4500", table.Column(4).Data().Chunk(0).(*array.String).Value(0))
			}
		})
	}
}
