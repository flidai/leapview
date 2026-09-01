package arrowquery

import (
	"errors"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// NullabilityViolationError reports a physical validity bitmap that violates
// a proved non-null governed output. It intentionally names only the governed
// response alias; physical lineage remains internal to the planner.
type NullabilityViolationError struct {
	Alias     string
	NullCount int
}

func (e *NullabilityViolationError) Error() string {
	if e == nil {
		return "governed Arrow nullability violation"
	}
	return fmt.Sprintf("governed Arrow field %q declared non-null contains %d null values", e.Alias, e.NullCount)
}

// OutputSchemaSink applies a governed output descriptor to one borrowed Arrow
// stream. It is a foundation component only: no production route constructs
// it yet. Schema and records are inspected and forwarded synchronously, and no
// borrowed Arrow object survives a callback.
type OutputSchemaSink struct {
	descriptor semanticquery.OutputSchemaDescriptor
	downstream Sink
	fields     []arrow.Field
	schema     *arrow.Schema
	wrote      bool
}

// NewOutputSchemaSink validates the governed descriptor before accepting any
// borrowed Arrow callbacks.
func NewOutputSchemaSink(descriptor semanticquery.OutputSchemaDescriptor, downstream Sink) (*OutputSchemaSink, error) {
	if downstream == nil {
		return nil, errors.New("governed output schema downstream sink is required")
	}
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	descriptor.Fields = append([]semanticquery.OutputFieldDescriptor(nil), descriptor.Fields...)
	return &OutputSchemaSink{descriptor: descriptor, downstream: downstream}, nil
}

func (s *OutputSchemaSink) WriteSchema(schema *arrow.Schema) error {
	if s == nil || s.downstream == nil {
		return errors.New("governed output schema sink is not initialized")
	}
	if schema == nil {
		return errors.New("governed output Arrow schema is required")
	}
	if s.wrote {
		return errors.New("governed output Arrow schema was already written")
	}
	if schema.NumFields() != len(s.descriptor.Fields) {
		return fmt.Errorf("governed output Arrow schema has %d fields, descriptor has %d", schema.NumFields(), len(s.descriptor.Fields))
	}
	fields := make([]arrow.Field, len(s.descriptor.Fields))
	for index, descriptor := range s.descriptor.Fields {
		source := schema.Field(index)
		if source.Name != descriptor.Alias {
			return fmt.Errorf("governed output Arrow field %d is %q, descriptor is %q", index, source.Name, descriptor.Alias)
		}
		fields[index] = source
		fields[index].Nullable = descriptor.ArrowNullable()
		fields[index].Metadata = cloneArrowMetadata(source.Metadata)
	}
	metadata := cloneArrowMetadata(schema.Metadata())
	reconciled := arrow.NewSchema(fields, &metadata)
	if err := s.downstream.WriteSchema(reconciled); err != nil {
		return err
	}
	s.fields = fields
	s.schema = reconciled
	s.wrote = true
	return nil
}

func (s *OutputSchemaSink) WriteRecord(record arrow.RecordBatch) error {
	if s == nil || s.downstream == nil {
		return errors.New("governed output schema sink is not initialized")
	}
	if !s.wrote {
		return errors.New("governed output Arrow schema must be written before records")
	}
	if record == nil {
		return nil
	}
	if record.NumCols() != int64(len(s.fields)) {
		return fmt.Errorf("governed output Arrow record has %d fields, want %d", record.NumCols(), len(s.fields))
	}
	for index, field := range s.fields {
		if record.ColumnName(index) != field.Name {
			return fmt.Errorf("governed output Arrow record field %d is %q, want %q", index, record.ColumnName(index), field.Name)
		}
		if field.Nullable {
			continue
		}
		nulls := record.Column(index).NullN()
		if nulls != 0 {
			return &NullabilityViolationError{Alias: field.Name, NullCount: nulls}
		}
	}
	// The downstream schema contains the governed nullability declarations,
	// while the producer record still references its physical schema. Rebind
	// the borrowed arrays synchronously so the downstream sees one stable
	// schema without copying or retaining producer-owned buffers.
	rebound := array.NewRecordBatch(s.schema, record.Columns(), record.NumRows())
	defer rebound.Release()
	return s.downstream.WriteRecord(rebound)
}

func (s *OutputSchemaSink) RowsWritten() int {
	if s == nil || s.downstream == nil {
		return 0
	}
	if stats, ok := s.downstream.(SinkStats); ok {
		return stats.RowsWritten()
	}
	return 0
}

func cloneArrowMetadata(metadata arrow.Metadata) arrow.Metadata {
	return arrow.NewMetadata(
		append([]string(nil), metadata.Keys()...),
		append([]string(nil), metadata.Values()...),
	)
}

var _ Sink = (*OutputSchemaSink)(nil)
var _ SinkStats = (*OutputSchemaSink)(nil)
