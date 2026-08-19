package arrowresult

import (
	"errors"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestBuilderRetainsBorrowedRecordsUntilFinalLeaseRelease(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)

	builder := array.NewInt64Builder(allocator)
	builder.AppendValues([]int64{1, 2, 3}, nil)
	values := builder.NewArray()
	builder.Release()
	record := array.NewRecordBatch(
		arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}}, nil),
		[]arrow.Array{values},
		3,
	)
	values.Release()

	collector := NewBuilderWithAllocator(allocator)
	if err := collector.WriteSchema(record.Schema()); err != nil {
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		t.Fatal(err)
	}
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	record.Release() // simulate duckdb-go advancing the reader

	first, err := result.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	second, err := result.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := first.Rows(), int64(3); got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	var retained int64
	if err := first.VisitRecords(func(record arrow.RecordBatch) error {
		retained = record.Column(0).(*array.Int64).Value(2)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := retained; got != int64(3) {
		t.Fatalf("retained value = %#v", got)
	}
	if first.Bytes() <= 0 {
		t.Fatalf("bytes = %d, want positive", first.Bytes())
	}
	sibling, err := first.Acquire()
	if err != nil {
		t.Fatal(err)
	}

	result.Release()
	first.Release()
	first.Release()
	if allocator.CurrentAlloc() == 0 {
		t.Fatal("second lease did not pin Arrow buffers")
	}
	second.Release()
	if allocator.CurrentAlloc() == 0 {
		t.Fatal("sibling lease did not independently pin Arrow buffers")
	}
	sibling.Release()
}

func TestBuilderRejectsInvalidLifecycle(t *testing.T) {
	collector := NewBuilder()
	if err := collector.WriteRecord(nil); !errors.Is(err, ErrSchemaRequired) {
		t.Fatalf("write before schema error = %v", err)
	}
	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}}, nil)
	if err := collector.WriteSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := collector.WriteSchema(schema); !errors.Is(err, ErrSchemaAlreadySet) {
		t.Fatalf("second schema error = %v", err)
	}
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()
	if _, err := collector.Finish(); !errors.Is(err, ErrBuilderFinished) {
		t.Fatalf("second finish error = %v", err)
	}
	if result.Rows() != 0 {
		t.Fatalf("empty rows = %d", result.Rows())
	}
}

func TestBuilderOwnsSlicedDictionaryBuffers(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	dictionaryType := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: arrow.BinaryTypes.String}
	dictionaryBuilder := array.NewDictionaryBuilder(allocator, dictionaryType)
	values := dictionaryBuilder.(*array.BinaryDictionaryBuilder)
	for _, value := range []string{"alpha", "beta", "alpha"} {
		if err := values.AppendString(value); err != nil {
			t.Fatal(err)
		}
	}
	dictionary := dictionaryBuilder.NewDictionaryArray()
	dictionaryBuilder.Release()
	sliced := array.NewSlice(dictionary, 1, 3)
	dictionary.Release()
	schema := arrow.NewSchema([]arrow.Field{{Name: "label", Type: dictionaryType}}, nil)
	record := array.NewRecordBatch(schema, []arrow.Array{sliced}, 2)
	sliced.Release()

	before := Stats()
	collector := NewBuilderWithAllocator(allocator)
	if err := collector.WriteSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		t.Fatal(err)
	}
	// A retained dictionary array is not sufficient for duckdb-go batches: its
	// values can still point at the reader's reusable native chunk.
	record.Column(0).(*array.Dictionary).Dictionary().(*array.String).Data().Buffers()[2].Bytes()[0] = 'z'
	record.Release()
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if got := Stats().TransientBytes; got != before.TransientBytes {
		t.Fatalf("transient bytes after finish = %d, want %d", got, before.TransientBytes)
	}
	lease, err := result.Acquire()
	result.Release()
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	if err := lease.VisitRecords(func(record arrow.RecordBatch) error {
		dictionary := record.Column(0).(*array.Dictionary)
		values := dictionary.Dictionary().(*array.String)
		for index := 0; index < int(record.NumRows()); index++ {
			labels = append(labels, values.Value(dictionary.GetValueIndex(index)))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if labels[0] != "beta" || labels[1] != "alpha" {
		t.Fatalf("copied sliced dictionary = %#v", labels)
	}
	lease.Release()
}

func TestAbortReleasesRetainedRecords(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	builder := array.NewStringBuilder(allocator)
	builder.Append("value")
	values := builder.NewArray()
	builder.Release()
	record := array.NewRecordBatch(arrow.NewSchema([]arrow.Field{{Name: "value", Type: arrow.BinaryTypes.String}}, nil), []arrow.Array{values}, 1)
	values.Release()

	collector := NewBuilderWithAllocator(allocator)
	before := Stats()
	if err := collector.WriteSchema(record.Schema()); err != nil {
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		t.Fatal(err)
	}
	if got := Stats().TransientBytes; got <= before.TransientBytes {
		t.Fatalf("transient bytes = %d, want more than %d", got, before.TransientBytes)
	}
	record.Release()
	collector.Abort()
	collector.Abort()
	if got := Stats().TransientBytes; got != before.TransientBytes {
		t.Fatalf("transient bytes after abort = %d, want %d", got, before.TransientBytes)
	}
}
