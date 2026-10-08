package http

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/api"
)

const arrowStreamMediaType = "application/vnd.apache.arrow.stream"

func writeSemanticQueryResponse(w stdhttp.ResponseWriter, r *stdhttp.Request, response api.SemanticQueryResponse) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, stdhttp.StatusOK, response)
}

func acceptsMediaType(header, mediaType string) bool {
	for _, item := range strings.Split(header, ",") {
		value := strings.TrimSpace(strings.SplitN(item, ";", 2)[0])
		if value == mediaType {
			return true
		}
	}
	return false
}

type semanticArrowSink struct {
	w        stdhttp.ResponseWriter
	queryID  string
	snapshot string
	limit    int64
	written  int64
	seen     int64
	schema   *arrow.Schema
	writer   *ipc.Writer
	err      error
}

func newSemanticArrowSink(w stdhttp.ResponseWriter, queryID, snapshot string, limit int) *semanticArrowSink {
	return &semanticArrowSink{w: w, queryID: queryID, snapshot: snapshot, limit: int64(limit)}
}

func (s *semanticArrowSink) WriteSchema(schema *arrow.Schema) error {
	if s == nil || s.w == nil {
		return errors.New("Arrow response sink is not initialized")
	}
	if schema == nil {
		return errors.New("Arrow response schema is required")
	}
	if s.schema != nil {
		return errors.New("Arrow response schema was already written")
	}
	metadata := schema.Metadata()
	metadata = arrow.NewMetadata(
		append(metadata.Keys(), "leapview.arrow_contract", "leapview.query_id", "leapview.serving_snapshot"),
		append(metadata.Values(), "native-v1", s.queryID, s.snapshot),
	)
	outputSchema := arrow.NewSchema(schema.Fields(), &metadata)
	s.schema = outputSchema
	return nil
}

func (s *semanticArrowSink) start() error {
	if s.writer != nil {
		return nil
	}
	if s.schema == nil {
		return errors.New("Arrow response schema must be written before records")
	}
	s.w.Header().Set("Content-Type", arrowStreamMediaType)
	s.w.Header().Set("Cache-Control", "no-store")
	s.w.Header().Set("X-Query-ID", s.queryID)
	s.w.Header().Set("X-Serving-Snapshot", s.snapshot)
	s.w.Header().Set("X-LeapView-Arrow-Contract", "native-v1")
	s.w.Header().Add("Trailer", "X-Next-Cursor")
	s.writer = ipc.NewWriter(s.w, ipc.WithSchema(s.schema))
	return nil
}

func (s *semanticArrowSink) WriteRecord(record arrow.RecordBatch) error {
	if s == nil {
		return errors.New("Arrow response sink is not initialized")
	}
	if record == nil {
		return nil
	}
	s.seen += record.NumRows()
	remaining := s.limit - s.written
	if remaining <= 0 {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	write := record
	if record.NumRows() > remaining {
		write = record.NewSlice(0, remaining)
		defer write.Release()
	}
	if err := s.writer.Write(write); err != nil {
		s.err = err
		return err
	}
	s.written += write.NumRows()
	if flusher, ok := s.w.(stdhttp.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (s *semanticArrowSink) HasMore() bool { return s != nil && s.seen > s.limit }
func (s *semanticArrowSink) RowsWritten() int {
	if s == nil {
		return 0
	}
	return int(s.written)
}

func (s *semanticArrowSink) Close() error {
	if s == nil {
		return nil
	}
	if s.writer == nil {
		if err := s.start(); err != nil {
			s.err = errors.Join(s.err, err)
			return s.err
		}
	}
	err := s.writer.Close()
	s.writer = nil
	s.err = errors.Join(s.err, err)
	return s.err
}

var _ arrowquery.Sink = (*semanticArrowSink)(nil)

func writeSemanticArrowResponse(
	w stdhttp.ResponseWriter,
	r *stdhttp.Request,
	metrics Metrics,
	request dataquery.Query,
	limit, offset int,
	queryID, snapshot, cursorScope string,
) {
	executor, ok := metrics.(arrowquery.Executor)
	if !ok {
		writeJSONError(w, errors.New("native Arrow execution is unavailable"), stdhttp.StatusInternalServerError)
		return
	}
	sink := newSemanticArrowSink(w, queryID, snapshot, limit)
	request.ProjectID = metrics.Catalog().Project.ID
	_, err := executor.ExecuteDataQueryArrow(r.Context(), request, sink)
	if err != nil {
		if sink.writer == nil {
			writeJSONError(w, err, statusForDataExecutionError(err))
		}
		return
	}
	if sink.HasMore() {
		next := encodeIndexCursor(offset+limit, cursorScope, snapshot)
		w.Header().Set("X-Next-Cursor", next)
	}
	_ = sink.Close()
}
