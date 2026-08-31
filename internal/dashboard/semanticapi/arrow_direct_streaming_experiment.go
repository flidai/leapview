//go:build fai543experiment

package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/pkg/arrowresult"
)

// DirectArrowExperimentConfig is the benchmark-only native-v1 response
// contract for FAI-543. It is excluded from normal builds and is not a serving
// API, feature flag, or production route.
type DirectArrowExperimentConfig struct {
	QueryID       string
	Snapshot      string
	CursorScope   string
	SchemaVersion string
	SpecRevision  string
	DataRevision  string
	LogicalTypes  map[string]string
	Labels        map[string]string
	Projection    []string
	Limit         int
	Offset        int
	Allocator     memory.Allocator
}

// directArrowExperimentSink decorates a borrowed governed schema with the
// FAI-541 public metadata contract, then delegates synchronous record writes
// to the existing native IPC sink. It never retains a record batch.
type directArrowExperimentSink struct {
	native *semanticArrowSink
	config DirectArrowExperimentConfig
	budget *dataquery.ResultBudget
	order  []int
}

func (s *directArrowExperimentSink) WriteSchema(schema *arrow.Schema) error {
	if s == nil || s.native == nil {
		return errors.New("direct Arrow experiment sink is not initialized")
	}
	if schema == nil {
		return errors.New("direct Arrow experiment schema is required")
	}
	sourceFields := schema.Fields()
	projection := append([]string(nil), s.config.Projection...)
	if len(projection) == 0 {
		projection = make([]string, len(sourceFields))
		for index, field := range sourceFields {
			projection[index] = field.Name
		}
	}
	if len(projection) != len(sourceFields) {
		return fmt.Errorf("direct Arrow experiment projection has %d fields, schema has %d", len(projection), len(sourceFields))
	}
	sourceByAlias := make(map[string]int, len(sourceFields))
	for index, field := range sourceFields {
		if _, exists := sourceByAlias[field.Name]; exists {
			return fmt.Errorf("direct Arrow experiment schema repeats alias %q", field.Name)
		}
		sourceByAlias[field.Name] = index
	}
	fields := make([]arrow.Field, len(projection))
	order := make([]int, len(projection))
	for index, alias := range projection {
		sourceIndex, exists := sourceByAlias[alias]
		if !exists {
			return fmt.Errorf("direct Arrow experiment projection alias %q is absent from governed schema", alias)
		}
		fields[index] = sourceFields[sourceIndex]
		order[index] = sourceIndex
		metadata := map[string]string{}
		if label, ok := fields[index].Metadata.GetValue("display.label"); ok && label != "" {
			metadata["display.label"] = label
		}
		if label := s.config.Labels[alias]; label != "" {
			metadata["display.label"] = label
		}
		if logicalType := s.config.LogicalTypes[alias]; logicalType != "" {
			metadata["leapview.logical_type"] = logicalType
		}
		fields[index].Metadata = arrow.MetadataFrom(metadata)
	}
	metadata := arrow.MetadataFrom(map[string]string{
		"leapview.visualization_schema_version": s.config.SchemaVersion,
		"leapview.visualization_spec_revision":  s.config.SpecRevision,
		"leapview.visualization_data_revision":  s.config.DataRevision,
	})
	if err := s.native.WriteSchema(arrow.NewSchema(fields, &metadata)); err != nil {
		return err
	}
	if s.budget != nil {
		// The governed producer already charged its physical schema. Charge only
		// the response-safe metadata added by the native-v1 boundary.
		delta := arrowresult.SchemaBytes(s.native.schema) - arrowresult.SchemaBytes(schema)
		if delta > 0 {
			if err := s.budget.ConsumeSize(0, delta); err != nil {
				return err
			}
		}
	}
	s.order = order
	return nil
}

func (s *directArrowExperimentSink) WriteRecord(record arrow.RecordBatch) error {
	if s == nil || s.native == nil {
		return errors.New("direct Arrow experiment sink is not initialized")
	}
	if record == nil {
		return nil
	}
	if len(s.order) != len(record.Columns()) {
		return fmt.Errorf("direct Arrow experiment record has %d columns, want %d", len(record.Columns()), len(s.order))
	}
	columns := make([]arrow.Array, len(s.order))
	for outputIndex, sourceIndex := range s.order {
		columns[outputIndex] = record.Column(sourceIndex)
	}
	// ipc.Writer encodes ordinary arrays synchronously but retains dictionary
	// values until Close. Copy only dictionary values so no producer-owned
	// Arrow memory survives this callback; indices remain borrowed and are
	// consumed synchronously by the existing native-v1 sink.
	ownedDictionaries := make([]arrow.Array, 0)
	for index, column := range columns {
		dictionary, ok := column.(*array.Dictionary)
		if !ok {
			continue
		}
		values, err := array.Concatenate([]arrow.Array{dictionary.Dictionary()}, s.allocator())
		if err != nil {
			for _, owned := range ownedDictionaries {
				owned.Release()
			}
			return fmt.Errorf("copy direct Arrow experiment dictionary %d: %w", index, err)
		}
		owned := array.NewDictionaryArray(dictionary.DataType(), dictionary.Indices(), values)
		values.Release()
		columns[index] = owned
		ownedDictionaries = append(ownedDictionaries, owned)
	}
	rebound := array.NewRecordBatch(s.native.schema, columns, record.NumRows())
	for _, owned := range ownedDictionaries {
		owned.Release()
	}
	defer rebound.Release()
	return s.native.WriteRecord(rebound)
}

func (s *directArrowExperimentSink) allocator() memory.Allocator {
	if s != nil && s.config.Allocator != nil {
		return s.config.Allocator
	}
	return memory.DefaultAllocator
}

func (s *directArrowExperimentSink) RowsWritten() int {
	if s == nil || s.native == nil {
		return 0
	}
	return s.native.RowsWritten()
}

var _ arrowquery.Sink = (*directArrowExperimentSink)(nil)
var _ arrowquery.SinkStats = (*directArrowExperimentSink)(nil)

type directArrowExperimentWriter struct {
	stdhttp.ResponseWriter
	mu      sync.Mutex
	aborted bool
	wrote   bool
}

func (w *directArrowExperimentWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.aborted {
		return len(payload), nil
	}
	w.wrote = true
	return w.ResponseWriter.Write(payload)
}

func (w *directArrowExperimentWriter) abortCommittedStream() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.wrote || w.aborted {
		return
	}
	// Arrow stream EOS is optional, so a clean EOF after a complete record can
	// look successful. Append a bounded incomplete message before discarding the
	// sink's close output so post-commit execution failures stay detectable.
	_, _ = w.ResponseWriter.Write([]byte{0xff, 0xff, 0xff, 0xff, 0x04, 0x00, 0x00, 0x00, 0x42, 0x42})
	w.aborted = true
}

// ExecuteDirectArrowExperiment runs the FAI-543 benchmark candidate. The
// executor remains the existing governed ExecuteDataQueryArrow path and the
// response encoder remains the existing semantic native IPC sink.
func ExecuteDirectArrowExperiment(
	ctx context.Context,
	w stdhttp.ResponseWriter,
	executor arrowquery.Executor,
	request dataquery.Query,
	config DirectArrowExperimentConfig,
) (dataquery.Result, error) {
	if executor == nil {
		return dataquery.Result{}, errors.New("direct Arrow experiment executor is required")
	}
	if w == nil {
		return dataquery.Result{}, errors.New("direct Arrow experiment response writer is required")
	}
	request = request.WithMetadata(dataquery.MetadataFromContext(ctx))
	if request.Kind != dataquery.KindSemanticRows || request.Surface != dataquery.SurfaceAPI || request.Operation != dataquery.OperationDashboardRows {
		return dataquery.Result{}, errors.New("direct Arrow experiment only supports API detail-row queries")
	}
	if config.QueryID == "" || config.Snapshot == "" || config.CursorScope == "" || config.SchemaVersion == "" || config.SpecRevision == "" || config.DataRevision == "" || config.Limit <= 0 || config.Offset < 0 {
		return dataquery.Result{}, errors.New("direct Arrow experiment response identity and pagination are required")
	}
	if request.Offset != config.Offset || request.Limit != config.Limit+1 {
		return dataquery.Result{}, errors.New("direct Arrow experiment query and response pagination do not match")
	}

	output := &directArrowExperimentWriter{ResponseWriter: w}
	native := newSemanticArrowSink(output, config.QueryID, config.Snapshot, config.Limit)
	budget, _ := dataquery.ResultBudgetFromContext(ctx)
	sink := &directArrowExperimentSink{native: native, config: config, budget: budget}
	result, err := executor.ExecuteDataQueryArrow(ctx, request, sink)
	if err != nil {
		if native.writer != nil {
			output.abortCommittedStream()
			_ = native.Close()
		}
		return result, err
	}
	if err := native.Close(); err != nil {
		return result, err
	}
	if native.HasMore() {
		w.Header().Set("X-Next-Cursor", encodeIndexCursor(config.Offset+config.Limit, config.CursorScope, config.Snapshot))
	}
	return result, nil
}

// DecodeDirectArrowExperimentCursor exposes the existing semantic API cursor
// verifier only to experiment-tagged contract tests.
func DecodeDirectArrowExperimentCursor(token string, scopes ...string) (int, error) {
	return decodeIndexCursor(token, scopes...)
}
