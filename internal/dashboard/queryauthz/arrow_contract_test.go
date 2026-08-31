package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func TestDashboardNativeArrowContractUsesGovernedMaskedProjection(t *testing.T) {
	_, identity, _, physical, _ := canonicalGraph(t)
	row, err := accesspolicy.Compile("rls", "row_filter", `{"field":"orders.region","operator":"equals","values":["EU"]}`)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := accesspolicy.Compile("mask", "column_mask", `{"field":"orders.email","mask":"null"}`)
	if err != nil {
		t.Fatal(err)
	}
	policies := []accesssnapshot.DataPolicy{
		{ID: "rls", Resource: physical, PolicyType: "row_filter", ExpressionJSON: `{"field":"orders.region","operator":"equals","values":["EU"]}`, Compiled: row},
		{ID: "mask", Resource: physical, PolicyType: "column_mask", ExpressionJSON: `{"field":"orders.email","mask":"null"}`, Compiled: mask},
	}
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"physical", physical, access.CapabilityResourceUse}}, policies)
	capture := &canonicalArrowCapture{schema: func(request dataquery.Query) *arrow.Schema {
		if len(request.Filters) != 1 || len(request.ColumnMasks) != 1 || request.EffectivePolicyFingerprint == "" {
			t.Fatalf("executor received ungoverned request: %#v", request)
		}
		filter := request.Filters[0]
		if filter.Field != "orders.region" || filter.Operator != "equals" || len(filter.Values) != 1 || filter.Values[0] != "EU" {
			t.Fatalf("executor row policy = %#v", filter)
		}
		if request.ColumnMasks[0].Field != "orders.email" || request.ColumnMasks[0].Mask != "null" {
			t.Fatalf("executor masks = %#v", request.ColumnMasks)
		}
		metadata := arrow.MetadataFrom(map[string]string{"leapview.logical_type": "string"})
		return arrow.NewSchema([]arrow.Field{{Name: request.Fields[0].Alias, Type: arrow.BinaryTypes.String, Nullable: true, Metadata: metadata}}, nil)
	}}
	recorder := &canonicalAuditRecorder{}
	metrics := canonicalMetricsWithSnapshot(t, snapshot, recorder, capture, "alice", "credential-a")
	sink := &canonicalSchemaCaptureSink{}
	request := dataquery.Query{
		ProjectID: canonicalProject,
		ModelID:   "semantic_sales",
		Target:    "orders",
		Kind:      dataquery.KindSemanticRows,
		Fields:    []dataquery.Field{{Field: "orders.email", Alias: "customer_email"}},
	}
	if _, err := metrics.ExecuteDataQueryArrow(context.Background(), request, sink); err != nil {
		t.Fatal(err)
	}
	if capture.calls != 1 {
		t.Fatalf("governed Arrow executor calls = %d, want 1", capture.calls)
	}
	if !sink.schema.observed || sink.schema.fieldCount != 1 {
		t.Fatalf("masked response schema = %#v", sink.schema)
	}
	if sink.schema.fieldName != "customer_email" || sink.schema.fieldType != arrow.STRING || !sink.schema.nullable {
		t.Fatalf("masked response field = %#v", sink.schema)
	}
	if sink.schema.fieldMetadata["leapview.logical_type"] != "string" {
		t.Fatalf("masked response field metadata = %#v", sink.schema.fieldMetadata)
	}
	if sink.schema.fieldName == "orders.email" {
		t.Fatal("response schema exposed the source field instead of the governed alias")
	}
	if len(recorder.events) != 1 || recorder.events[0].Status != "success" || recorder.events[0].PrincipalID != "alice" || recorder.events[0].Identity != identity {
		t.Fatalf("governed Arrow audit events = %#v", recorder.events)
	}

	deniedCapture := &canonicalArrowCapture{}
	deniedSink := &canonicalSchemaCaptureSink{}
	denied := canonicalMetricsWithSnapshot(t, canonicalSnapshot(t, nil, nil), nil, deniedCapture)
	if _, err := denied.ExecuteDataQueryArrow(context.Background(), request, deniedSink); err == nil {
		t.Fatal("unauthorized native Arrow request was accepted")
	}
	if deniedCapture.calls != 0 || deniedSink.schema.observed {
		t.Fatalf("unauthorized request reached executor/schema: calls=%d schema=%v", deniedCapture.calls, deniedSink.schema)
	}
}

type canonicalSchemaCaptureSink struct {
	schema canonicalSchemaSnapshot
}

type canonicalSchemaSnapshot struct {
	observed      bool
	fieldCount    int
	fieldName     string
	fieldType     arrow.Type
	nullable      bool
	fieldMetadata map[string]string
}

func (s *canonicalSchemaCaptureSink) WriteSchema(schema *arrow.Schema) error {
	if schema == nil {
		return errors.New("canonical Arrow schema is nil")
	}
	snapshot := canonicalSchemaSnapshot{
		observed:   true,
		fieldCount: schema.NumFields(),
	}
	if schema.NumFields() > 0 {
		field := schema.Field(0)
		snapshot.fieldName = field.Name
		snapshot.fieldType = field.Type.ID()
		snapshot.nullable = field.Nullable
		snapshot.fieldMetadata = make(map[string]string, field.Metadata.Len())
		for key, value := range field.Metadata.ToMap() {
			snapshot.fieldMetadata[key] = value
		}
	}
	s.schema = snapshot
	return nil
}

func (*canonicalSchemaCaptureSink) WriteRecord(arrow.RecordBatch) error { return nil }

var _ arrowquery.Sink = (*canonicalSchemaCaptureSink)(nil)
