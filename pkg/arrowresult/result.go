// Package arrowresult owns immutable, reference-counted analytical results.
package arrowresult

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	arrowutil "github.com/apache/arrow-go/v18/arrow/util"
)

var (
	ErrSchemaRequired   = errors.New("Arrow result schema is required")
	ErrSchemaAlreadySet = errors.New("Arrow result schema was already set")
	ErrBuilderFinished  = errors.New("Arrow result builder is finished")
	ErrResultReleased   = errors.New("Arrow result is released")
)

var globalStats struct {
	results        atomic.Int64
	leases         atomic.Int64
	bytes          atomic.Int64
	transientBytes atomic.Int64
}

type StatsSnapshot struct {
	Results        int64
	Leases         int64
	Bytes          int64
	TransientBytes int64
}

func Stats() StatsSnapshot {
	return StatsSnapshot{
		Results:        globalStats.results.Load(),
		Leases:         globalStats.leases.Load(),
		Bytes:          globalStats.bytes.Load(),
		TransientBytes: globalStats.transientBytes.Load(),
	}
}

type Builder struct {
	mu        sync.Mutex
	schema    *arrow.Schema
	records   []arrow.RecordBatch
	allocator memory.Allocator
	rows      int64
	transient int64
	finished  bool
}

func NewBuilder() *Builder { return NewBuilderWithAllocator(memory.DefaultAllocator) }

func NewBuilderWithAllocator(allocator memory.Allocator) *Builder {
	if allocator == nil {
		allocator = memory.DefaultAllocator
	}
	return &Builder{allocator: allocator}
}

func (b *Builder) WriteSchema(schema *arrow.Schema) error {
	if b == nil {
		return ErrBuilderFinished
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return ErrBuilderFinished
	}
	if schema == nil {
		return ErrSchemaRequired
	}
	if b.schema != nil {
		return ErrSchemaAlreadySet
	}
	metadata := schema.Metadata()
	b.schema = arrow.NewSchema(append([]arrow.Field{}, schema.Fields()...), &metadata)
	return nil
}

func (b *Builder) WriteRecord(record arrow.RecordBatch) error {
	if b == nil {
		return ErrBuilderFinished
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return ErrBuilderFinished
	}
	if b.schema == nil {
		return ErrSchemaRequired
	}
	if record == nil {
		return nil
	}
	if !b.schema.Equal(record.Schema()) {
		return fmt.Errorf("Arrow record schema does not match result schema")
	}
	// duckdb-go's record references memory owned by the current DuckDB data
	// chunk. Arrow Retain pins the Go array object but not that chunk after the
	// reader advances, so every accepted batch crosses a deep-copy boundary.
	// Concatenation copies the common scalar buffers directly. Complex layouts
	// whose concatenation may retain child buffers use IPC as the safe fallback.
	copied, err := copyRecord(record, b.schema, b.allocator)
	if err != nil {
		return err
	}
	recordBytes := arrowutil.TotalRecordSize(copied)
	b.records = append(b.records, copied)
	b.rows += record.NumRows()
	b.transient += recordBytes
	globalStats.transientBytes.Add(recordBytes)
	return nil
}

func copyRecord(record arrow.RecordBatch, schema *arrow.Schema, allocator memory.Allocator) (arrow.RecordBatch, error) {
	for index := 0; index < int(record.NumCols()); index++ {
		if !concatenateDeepCopies(record.Column(index).DataType()) {
			return copyRecordIPC(record, allocator)
		}
	}
	columns := make([]arrow.Array, int(record.NumCols()))
	for index := range columns {
		copied, err := array.Concatenate([]arrow.Array{record.Column(index)}, allocator)
		if err != nil {
			for _, column := range columns[:index] {
				column.Release()
			}
			return nil, fmt.Errorf("copy Arrow column %q: %w", record.ColumnName(index), err)
		}
		columns[index] = copied
	}
	copied := array.NewRecordBatch(schema, columns, record.NumRows())
	for _, column := range columns {
		column.Release()
	}
	return copied, nil
}

func concatenateDeepCopies(dataType arrow.DataType) bool {
	switch dataType.ID() {
	case arrow.DICTIONARY, arrow.EXTENSION, arrow.STRING_VIEW, arrow.BINARY_VIEW:
		return false
	}
	switch dataType.(type) {
	case *arrow.NullType, *arrow.BooleanType, arrow.FixedWidthDataType, arrow.BinaryDataType:
		return true
	default:
		return false
	}
}

func copyRecordIPC(record arrow.RecordBatch, allocator memory.Allocator) (arrow.RecordBatch, error) {
	var buffer bytes.Buffer
	writer := ipc.NewWriter(&buffer, ipc.WithSchema(record.Schema()), ipc.WithAllocator(allocator))
	if err := writer.Write(record); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("copy Arrow record: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish copied Arrow record: %w", err)
	}
	reader, err := ipc.NewReader(bytes.NewReader(buffer.Bytes()), ipc.WithAllocator(allocator))
	if err != nil {
		return nil, fmt.Errorf("open copied Arrow record: %w", err)
	}
	defer reader.Release()
	if !reader.Next() {
		if err := reader.Err(); err != nil {
			return nil, fmt.Errorf("decode copied Arrow record: %w", err)
		}
		return nil, fmt.Errorf("copied Arrow record stream is empty")
	}
	copied := reader.RecordBatch()
	copied.Retain()
	if reader.Next() {
		copied.Release()
		return nil, fmt.Errorf("copied Arrow record stream contains multiple batches")
	}
	if err := reader.Err(); err != nil {
		copied.Release()
		return nil, fmt.Errorf("decode copied Arrow record: %w", err)
	}
	return copied, nil
}

func (b *Builder) Finish() (*Result, error) {
	if b == nil {
		return nil, ErrBuilderFinished
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return nil, ErrBuilderFinished
	}
	if b.schema == nil {
		return nil, ErrSchemaRequired
	}
	b.finished = true
	retainedBytes := SchemaBytes(b.schema) + b.transient
	result := &Result{schema: b.schema, records: b.records, rows: b.rows, bytes: retainedBytes}
	result.refs.Store(1)
	globalStats.results.Add(1)
	globalStats.bytes.Add(retainedBytes)
	globalStats.transientBytes.Add(-b.transient)
	b.schema = nil
	b.records = nil
	b.transient = 0
	return result, nil
}

func (b *Builder) releaseTransientLocked() {
	for _, record := range b.records {
		record.Release()
	}
	globalStats.transientBytes.Add(-b.transient)
	b.transient = 0
	b.schema = nil
	b.records = nil
}

// SchemaBytes conservatively accounts the stable Arrow schema retained with a
// result. Query limits use the same function before publishing the schema so a
// cache miss and hit charge identical logical bytes.
func SchemaBytes(schema *arrow.Schema) int64 {
	if schema == nil {
		return 0
	}
	// Count stable schema strings and their backing headers conservatively.
	// Nested type descriptions are included through DataType.String().
	size := int64(len(schema.String()) + 64)
	metadata := schema.Metadata()
	for index, key := range metadata.Keys() {
		size += int64(len(key)+len(metadata.Values()[index])) + 32
	}
	for _, field := range schema.Fields() {
		size += int64(len(field.Name)+len(field.Type.String())) + 64
		for index, key := range field.Metadata.Keys() {
			size += int64(len(key)+len(field.Metadata.Values()[index])) + 32
		}
	}
	return size
}

func (b *Builder) Abort() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return
	}
	b.finished = true
	b.releaseTransientLocked()
}

type Result struct {
	refs          atomic.Int64
	ownerReleased atomic.Bool
	schema        *arrow.Schema
	records       []arrow.RecordBatch
	rows          int64
	bytes         int64
}

func (r *Result) Rows() int64 {
	if r == nil {
		return 0
	}
	return r.rows
}

func (r *Result) Bytes() int64 {
	if r == nil {
		return 0
	}
	return r.bytes
}

func (r *Result) Acquire() (*Lease, error) {
	if r == nil {
		return nil, ErrResultReleased
	}
	for {
		refs := r.refs.Load()
		if refs <= 0 {
			return nil, ErrResultReleased
		}
		if r.refs.CompareAndSwap(refs, refs+1) {
			globalStats.leases.Add(1)
			return &Lease{result: r}, nil
		}
	}
}

// Release relinquishes the creator's reference. It is idempotent; consumer
// references are released independently by their leases.
func (r *Result) Release() {
	if r == nil || !r.ownerReleased.CompareAndSwap(false, true) {
		return
	}
	r.releaseRef()
}

func (r *Result) releaseRef() {
	if r.refs.Add(-1) != 0 {
		return
	}
	for _, record := range r.records {
		record.Release()
	}
	r.records = nil
	r.schema = nil
	globalStats.results.Add(-1)
	globalStats.bytes.Add(-r.bytes)
}

type Lease struct {
	once   sync.Once
	result *Result
}

// Acquire creates an independent sibling lease while this lease pins the
// result. It is used when one coalesced execution fans out to multiple callers.
func (l *Lease) Acquire() (*Lease, error) {
	if l == nil || l.result == nil {
		return nil, ErrResultReleased
	}
	return l.result.Acquire()
}

func (l *Lease) Schema() *arrow.Schema {
	if l == nil || l.result == nil {
		return nil
	}
	return l.result.schema
}

func (l *Lease) Rows() int64 {
	if l == nil || l.result == nil {
		return 0
	}
	return l.result.rows
}

func (l *Lease) Bytes() int64 {
	if l == nil || l.result == nil {
		return 0
	}
	return l.result.bytes
}

// VisitRecords exposes borrowed records while the lease pins their buffers.
// The visitor must not retain a record after returning unless it calls Retain.
func (l *Lease) VisitRecords(visit func(arrow.RecordBatch) error) error {
	if l == nil || l.result == nil {
		return ErrResultReleased
	}
	if visit == nil {
		return nil
	}
	for _, record := range l.result.records {
		if err := visit(record); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.result != nil {
			globalStats.leases.Add(-1)
			l.result.releaseRef()
			l.result = nil
		}
	})
}
