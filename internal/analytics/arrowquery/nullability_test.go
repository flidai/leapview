package arrowquery

import (
	"errors"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestOutputSchemaSinkReconcilesTriStateNullability(t *testing.T) {
	descriptor := semanticquery.OutputSchemaDescriptor{Fields: []semanticquery.OutputFieldDescriptor{
		{Alias: "proved", LogicalType: "integer", Nullability: semanticquery.OutputDefinitelyNonNull},
		{Alias: "nullable", LogicalType: "integer", Nullability: semanticquery.OutputNullable},
		{Alias: "unknown", LogicalType: "integer", Nullability: semanticquery.OutputNullabilityUnknown},
	}}
	upstream := arrow.NewSchema([]arrow.Field{
		{Name: "proved", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "nullable", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
		{Name: "unknown", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
	}, nil)
	capture := &outputSchemaCaptureSink{}
	sink, err := NewOutputSchemaSink(descriptor, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteSchema(upstream); err != nil {
		t.Fatal(err)
	}
	if len(capture.fields) != 3 {
		t.Fatalf("captured fields = %d, want 3", len(capture.fields))
	}
	if capture.fields[0].Nullable || !capture.fields[1].Nullable || !capture.fields[2].Nullable {
		t.Fatalf("reconciled nullability = [%v %v %v]", capture.fields[0].Nullable, capture.fields[1].Nullable, capture.fields[2].Nullable)
	}
	if upstream.Field(0).Nullable != true || upstream.Field(1).Nullable != false || upstream.Field(2).Nullable != false {
		t.Fatalf("borrowed upstream schema was mutated: %#v", upstream.Fields())
	}
}

func TestOutputSchemaSinkRejectsRuntimeNullAcrossBatches(t *testing.T) {
	descriptor := semanticquery.OutputSchemaDescriptor{Fields: []semanticquery.OutputFieldDescriptor{
		{Alias: "id", LogicalType: "integer", Nullability: semanticquery.OutputDefinitelyNonNull},
		{Alias: "optional", LogicalType: "integer", Nullability: semanticquery.OutputNullable},
	}}
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "optional", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
	}, nil)
	capture := &outputSchemaCaptureSink{}
	sink, err := NewOutputSchemaSink(descriptor, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteSchema(schema); err != nil {
		t.Fatal(err)
	}
	good := outputSchemaRecord(t, schema, []bool{true, true}, []bool{true, false})
	if err := sink.WriteRecord(good); err != nil {
		good.Release()
		t.Fatal(err)
	}
	good.Release()
	bad := outputSchemaRecord(t, schema, []bool{true, false}, []bool{true, true})
	err = sink.WriteRecord(bad)
	bad.Release()
	var mismatch *NullabilityViolationError
	if !errors.As(err, &mismatch) || mismatch.Alias != "id" || mismatch.NullCount != 1 {
		t.Fatalf("runtime mismatch = %#v / %v", mismatch, err)
	}
	if capture.records != 1 {
		t.Fatalf("downstream records = %d, want only the valid first batch", capture.records)
	}
}

func TestOutputSchemaSinkKeepsEmptySchemaStableAndRejectsAliasMismatch(t *testing.T) {
	descriptor := semanticquery.OutputSchemaDescriptor{Fields: []semanticquery.OutputFieldDescriptor{
		{Alias: "id", LogicalType: "integer", Nullability: semanticquery.OutputDefinitelyNonNull},
	}}
	capture := &outputSchemaCaptureSink{}
	sink, err := NewOutputSchemaSink(descriptor, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteSchema(arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true}}, nil)); err != nil {
		t.Fatal(err)
	}
	if len(capture.fields) != 1 || capture.fields[0].Name != "id" || capture.fields[0].Nullable {
		t.Fatalf("empty result schema = %#v", capture.fields)
	}
	if capture.records != 0 {
		t.Fatalf("empty result wrote %d records", capture.records)
	}

	mismatched, err := NewOutputSchemaSink(descriptor, &outputSchemaCaptureSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mismatched.WriteSchema(arrow.NewSchema([]arrow.Field{{Name: "source_id", Type: arrow.PrimitiveTypes.Int64, Nullable: true}}, nil)); err == nil {
		t.Fatal("schema with a non-governed alias was accepted")
	}
}

type outputSchemaCaptureSink struct {
	fields  []arrow.Field
	records int
}

func (s *outputSchemaCaptureSink) WriteSchema(schema *arrow.Schema) error {
	s.fields = schema.Fields()
	for index := range s.fields {
		metadata := s.fields[index].Metadata
		s.fields[index].Metadata = arrow.NewMetadata(append([]string(nil), metadata.Keys()...), append([]string(nil), metadata.Values()...))
	}
	return nil
}

func (s *outputSchemaCaptureSink) WriteRecord(arrow.RecordBatch) error {
	s.records++
	return nil
}

func outputSchemaRecord(t testing.TB, schema *arrow.Schema, idValid, optionalValid []bool) arrow.RecordBatch {
	t.Helper()
	allocator := memory.DefaultAllocator
	id := array.NewInt64Builder(allocator)
	id.AppendValues([]int64{1, 2}, idValid)
	optional := array.NewInt64Builder(allocator)
	optional.AppendValues([]int64{3, 4}, optionalValid)
	columns := []arrow.Array{id.NewArray(), optional.NewArray()}
	id.Release()
	optional.Release()
	record := array.NewRecordBatch(schema, columns, 2)
	for _, column := range columns {
		column.Release()
	}
	return record
}
