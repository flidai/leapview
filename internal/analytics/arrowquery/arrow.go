package arrowquery

import (
	"context"

	"github.com/apache/arrow-go/v18/arrow"
	arrowutil "github.com/apache/arrow-go/v18/arrow/util"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func ConsumeSchemaBudget(ctx context.Context, schema *arrow.Schema) error {
	if budget, ok := dataquery.ResultBudgetFromContext(ctx); ok {
		return budget.ConsumeSize(0, arrowresult.SchemaBytes(schema))
	}
	return nil
}

// Sink consumes DuckDB-owned record batches. Schema and record arguments are
// borrowed for the duration of each callback. A synchronous sink must not keep
// them after returning. An owning sink must establish independent ownership
// before returning and later release it. Retain is sufficient only when the
// producer's buffers are themselves Arrow-owned; duckdb-go batches require a
// deep copy because their buffers belong to the advancing DuckDB data chunk.
type Sink interface {
	WriteSchema(*arrow.Schema) error
	WriteRecord(arrow.RecordBatch) error
}

// SinkStats optionally reports logical rows actually delivered by a transport.
// It excludes pagination probes that were consumed but not emitted.
type SinkStats interface {
	RowsWritten() int
}

// Executor is the governed native-Arrow path used by Arrow transports. The call
// does not return until the sink has consumed every record batch, so the runtime
// generation, workload permit, and physical connection stay pinned.
type Executor interface {
	ExecuteDataQueryArrow(context.Context, dataquery.Query, Sink) (dataquery.Result, error)
}

func ConsumeResultBudget(ctx context.Context, record arrow.RecordBatch) error {
	if record == nil {
		return nil
	}
	if budget, ok := dataquery.ResultBudgetFromContext(ctx); ok {
		return budget.ConsumeSize(int(record.NumRows()), arrowutil.TotalRecordSize(record))
	}
	return nil
}
