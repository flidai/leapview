package materialize

import (
	"context"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/compute"
	"github.com/apache/arrow-go/v18/arrow/memory"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func splitArrowBundle(ctx context.Context, bundle semanticquery.BundlePlan, source *arrowresult.Result) (map[string]*arrowresult.Result, error) {
	lease, err := source.Acquire()
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	schema := lease.Schema()
	ordinalIndex := schema.FieldIndices(semanticquery.BundleBranchColumn)
	if len(ordinalIndex) != 1 {
		return nil, fmt.Errorf("bundle Arrow result has no unique branch column")
	}
	builders := make(map[int]*arrowresult.Builder, len(bundle.Branches))
	branches := make(map[int]semanticquery.BundleBranch, len(bundle.Branches))
	for _, branch := range bundle.Branches {
		fields := make([]arrow.Field, len(branch.Columns))
		for index, column := range branch.Columns {
			physical := schema.FieldIndices(column.Physical)
			if len(physical) != 1 {
				return nil, fmt.Errorf("bundle Arrow result has no unique column %q", column.Physical)
			}
			fields[index] = schema.Field(physical[0])
			fields[index].Name = column.Output
		}
		builder := arrowresult.NewBuilder()
		if err := builder.WriteSchema(arrow.NewSchema(fields, nil)); err != nil {
			return nil, err
		}
		builders[branch.Ordinal] = builder
		branches[branch.Ordinal] = branch
	}
	abort := func() {
		for _, builder := range builders {
			builder.Abort()
		}
	}
	if err := lease.VisitRecords(func(record arrow.RecordBatch) error {
		selected := make(map[int][]uint64, len(branches))
		ordinals := record.Column(ordinalIndex[0])
		for row := 0; row < int(record.NumRows()); row++ {
			ordinal, err := arrowOrdinal(ordinals, row)
			if err != nil {
				return err
			}
			if _, ok := branches[ordinal]; !ok {
				return fmt.Errorf("unknown bundle branch ordinal %d", ordinal)
			}
			selected[ordinal] = append(selected[ordinal], uint64(row))
		}
		for ordinal, indices := range selected {
			indexBuilder := array.NewUint64Builder(memory.DefaultAllocator)
			indexBuilder.AppendValues(indices, nil)
			indexArray := indexBuilder.NewArray()
			indexBuilder.Release()
			branch := branches[ordinal]
			columns := make([]arrow.Array, len(branch.Columns))
			for index, column := range branch.Columns {
				physical := record.Schema().FieldIndices(column.Physical)[0]
				columns[index], err = compute.TakeArray(ctx, record.Column(physical), indexArray)
				if err != nil {
					for _, values := range columns {
						if values != nil {
							values.Release()
						}
					}
					indexArray.Release()
					return err
				}
			}
			indexArray.Release()
			fields := make([]arrow.Field, len(branch.Columns))
			for index, column := range branch.Columns {
				fields[index] = record.Schema().Field(record.Schema().FieldIndices(column.Physical)[0])
				fields[index].Name = column.Output
			}
			projected := array.NewRecordBatch(arrow.NewSchema(fields, nil), columns, int64(len(indices)))
			for _, values := range columns {
				values.Release()
			}
			err = builders[ordinal].WriteRecord(projected)
			projected.Release()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		abort()
		return nil, err
	}
	results := make(map[string]*arrowresult.Result, len(bundle.Branches))
	for _, branch := range bundle.Branches {
		result, err := builders[branch.Ordinal].Finish()
		if err != nil {
			for _, built := range results {
				built.Release()
			}
			abort()
			return nil, err
		}
		results[branch.ID] = result
	}
	return results, nil
}

func arrowOrdinal(values arrow.Array, index int) (int, error) {
	switch values := values.(type) {
	case *array.Int8:
		return int(values.Value(index)), nil
	case *array.Int16:
		return int(values.Value(index)), nil
	case *array.Int32:
		return int(values.Value(index)), nil
	case *array.Int64:
		return int(values.Value(index)), nil
	case *array.Uint8:
		return int(values.Value(index)), nil
	case *array.Uint16:
		return int(values.Value(index)), nil
	case *array.Uint32:
		return int(values.Value(index)), nil
	case *array.Uint64:
		return int(values.Value(index)), nil
	default:
		return 0, fmt.Errorf("bundle branch ordinal has Arrow type %s", values.DataType())
	}
}
