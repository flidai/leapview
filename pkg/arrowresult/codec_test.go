package arrowresult

import (
	"errors"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func codecFixture(t *testing.T) *Result {
	t.Helper()
	allocator := memory.DefaultAllocator
	b := array.NewInt64Builder(allocator)
	b.AppendValues([]int64{1, 2, 3}, nil)
	values := b.NewArray()
	b.Release()
	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}}, nil)
	record := array.NewRecordBatch(schema, []arrow.Array{values}, 3)
	values.Release()
	collector := NewBuilder()
	if err := collector.WriteSchema(schema); err != nil {
		record.Release()
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		record.Release()
		t.Fatal(err)
	}
	record.Release()
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIPCCodecRoundTripOwnsDecodedResult(t *testing.T) {
	before := Stats()
	original := codecFixture(t)
	lease, err := original.Acquire()
	if err != nil {
		original.Release()
		t.Fatal(err)
	}
	payload, err := EncodeIPC(lease)
	lease.Release()
	if err != nil {
		original.Release()
		t.Fatal(err)
	}
	decoded, err := DecodeIPC(payload)
	if err != nil {
		original.Release()
		t.Fatal(err)
	}
	decodedLease, err := decoded.Acquire()
	if err != nil {
		original.Release()
		decoded.Release()
		t.Fatal(err)
	}
	if decoded.Rows() != 3 || decodedLease.Schema().Fields()[0].Name != "id" {
		decodedLease.Release()
		original.Release()
		decoded.Release()
		t.Fatalf("decoded result = rows %d schema %#v", decoded.Rows(), decodedLease.Schema())
	}
	decodedLease.Release()
	original.Release()
	decoded.Release()
	if got := Stats(); got.Results != before.Results || got.Leases != before.Leases || got.Bytes != before.Bytes {
		t.Fatalf("codec leaked ownership: before=%+v after=%+v", before, got)
	}
}

func TestIPCCodecBoundsAndRejectsCorruption(t *testing.T) {
	original := codecFixture(t)
	defer original.Release()
	lease, err := original.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeIPC(lease)
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeIPCWithLimit(payload, int64(len(payload)-1)); !errors.Is(err, ErrIPCTooLarge) {
		t.Fatalf("undersized decode error = %v, want ErrIPCTooLarge", err)
	}
	corrupt := append(append([]byte(nil), payload...), 0x7f)
	if _, err := DecodeIPC(corrupt); err == nil {
		t.Fatal("trailing bytes decoded as a valid IPC stream")
	}
	if _, err := DecodeIPC([]byte("not-arrow")); err == nil {
		t.Fatal("malformed IPC decoded without error")
	}
}
