//go:build duckdb_arrow

package ducklake

import (
	"context"
	"errors"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/workload"
)

func TestQueryArrowUnwrapsGuardedConnectionWithoutBypassingSharedPoolGuard(t *testing.T) {
	env, err := Open(context.Background(), fixtureConfig(t, Config{RootDir: t.TempDir(), MaxConnections: 2}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "shared-arrow")
	defer releaseWorkload()
	lease, err := env.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if err := env.QueryArrow(lease.Context(), semanticquery.Plan{SQL: "SELECT 42 AS value", Columns: []string{"value"}}, &countingArrowSink{}); err != nil {
		t.Fatalf("guarded SELECT through native Arrow: %v", err)
	}
	unsafeStatements := []struct {
		statement string
		want      error
	}{
		{statement: "CHECKPOINT lake", want: ErrUnsafeCheckpoint},
		{statement: "CALL ducklake_cleanup_old_files('lake')", want: ErrSharedPoolMaintenance},
	}
	for _, unsafe := range unsafeStatements {
		err := env.QueryArrow(lease.Context(), semanticquery.Plan{SQL: unsafe.statement}, &countingArrowSink{})
		if !errors.Is(err, unsafe.want) {
			t.Fatalf("native Arrow statement %q error = %v, want %v", unsafe.statement, err, unsafe.want)
		}
	}
}

func TestQueryArrowPreservesNativeTypesNullsAndUsesAdmittedConnection(t *testing.T) {
	env := openLeaseTestNode(t)
	defer env.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "sales")
	defer releaseWorkload()
	lease, err := env.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ctx = lease.Context()
	ctx = dataquery.WithResultBudget(ctx, dataquery.ResultLimits{MaxRows: 10, MaxBytes: 1 << 20})
	sink := &nativeCaptureSink{t: t}
	plan := semanticquery.Plan{
		SQL:     "SELECT CAST(9007199254740993 AS BIGINT) AS id, CAST(NULL AS DOUBLE) AS amount",
		Columns: []string{"id", "amount"},
	}
	if err := env.QueryArrow(ctx, plan, sink); err != nil {
		t.Fatalf("QueryArrow: %v", err)
	}
	if !sink.schema || sink.rows != 1 || sink.id != int64(9007199254740993) || !sink.nullAmount {
		t.Fatalf("capture = %#v", sink)
	}
	if rows, bytes := func() (int, int64) {
		budget, _ := dataquery.ResultBudgetFromContext(ctx)
		return budget.Usage()
	}(); rows != 1 || bytes <= 0 {
		t.Fatalf("budget usage = (%d, %d)", rows, bytes)
	}
}

func TestQueryArrowRequiresWorkloadAdmission(t *testing.T) {
	env := openLeaseTestNode(t)
	defer env.Close()
	err := env.QueryArrow(context.Background(), semanticquery.Plan{SQL: "SELECT 1 AS id", Columns: []string{"id"}}, &nativeCaptureSink{t: t})
	if err == nil {
		t.Fatal("expected unadmitted query to fail")
	}
}

func TestQueryArrowCancellationAndResultLimitStopBeforeSinkPublication(t *testing.T) {
	env := openLeaseTestNode(t)
	defer env.Close()
	ctx, releaseWorkload := admittedTestContext(t, workload.Interactive, "sales")
	defer releaseWorkload()
	lease, err := env.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	canceled, cancel := context.WithCancel(lease.Context())
	cancel()
	if err := env.QueryArrow(canceled, semanticquery.Plan{SQL: "SELECT * FROM range(1000000)", Columns: []string{"range"}}, &countingArrowSink{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled QueryArrow error = %v", err)
	}

	limited := dataquery.WithResultBudget(lease.Context(), dataquery.ResultLimits{MaxRows: 10, MaxBytes: 1})
	sink := &countingArrowSink{}
	err = env.QueryArrow(limited, semanticquery.Plan{SQL: "SELECT 'larger than one byte' AS value", Columns: []string{"value"}}, sink)
	if reason, ok := dataquery.ResultLimitReasonOf(err); !ok || reason != dataquery.ResultBytes {
		t.Fatalf("limited QueryArrow error = %v, want byte limit", err)
	}
	if sink.records != 0 {
		t.Fatalf("published records = %d, want zero", sink.records)
	}
}

type nativeCaptureSink struct {
	t          *testing.T
	schema     bool
	rows       int64
	id         int64
	nullAmount bool
}

type countingArrowSink struct{ records int }

func (s *countingArrowSink) WriteSchema(*arrow.Schema) error { return nil }
func (s *countingArrowSink) WriteRecord(arrow.RecordBatch) error {
	s.records++
	return nil
}

func (s *nativeCaptureSink) WriteSchema(schema *arrow.Schema) error {
	s.t.Helper()
	if schema.NumFields() != 2 || schema.Field(0).Type.ID() != arrow.INT64 || schema.Field(1).Type.ID() != arrow.FLOAT64 {
		s.t.Fatalf("schema = %s", schema)
	}
	s.schema = true
	return nil
}

func (s *nativeCaptureSink) WriteRecord(record arrow.RecordBatch) error {
	s.t.Helper()
	s.rows += record.NumRows()
	s.id = record.Column(0).(*array.Int64).Value(0)
	s.nullAmount = record.Column(1).IsNull(0)
	return nil
}
