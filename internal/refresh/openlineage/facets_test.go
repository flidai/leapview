package openlineage

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestEventProjectsPublishedSchemaVersionQualityAndSafeLineageFacets(t *testing.T) {
	p := facetTestPipeline(t)
	stats := map[string]DatasetStatistics{
		"source:orders": {RowCount: 7, FileCount: 2, SizeBytes: 99},
		"model:orders":  {RowCount: 10, FileCount: 1, SizeBytes: 120},
	}
	gate := facetTestGateEvidence(t, []release.GateCheckEvidence{
		{Identity: "model:orders\x00authored\x00id_present", Kind: "non_null", ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error", Queries: 1},
		{Identity: "model:orders\x00authored\x00row_bounds", Kind: "row_count", ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error", ObservedRows: 10},
	})
	r := PipelineRun{
		ID: "run-facets", EventTime: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		GateEvidence: &gate, Statistics: stats,
		ColumnLineage: map[string][]PhysicalLineage{
			"model:orders": {
				{Logical: "id", Dataset: "source:orders", Field: "id", Route: []string{"orders"}},
				{Logical: "id", Dataset: "source:orders", Field: "id", Route: []string{"orders"}},
				{Logical: "id", Dataset: "source:orders", Field: "order_id"},
			},
		},
	}
	event, err := EventForPipelineRun(p, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEvent(event); err != nil {
		t.Fatalf("complete event does not validate: %v", err)
	}

	model := event.Outputs[0]
	schema := facetValue(t, model.Facets, "schema")
	if schema["_schemaURL"] != SchemaFacetSchemaURL {
		t.Fatalf("schema URL = %v", schema["_schemaURL"])
	}
	fields := schema["fields"].([]any)
	if len(fields) != 2 || fields[0].(map[string]any)["name"] != "amount" || fields[1].(map[string]any)["name"] != "id" {
		t.Fatalf("schema fields = %#v", fields)
	}
	version := facetValue(t, model.Facets, "version")
	if version["datasetVersion"] != "1.2.3" {
		t.Fatalf("dataset version = %v", version["datasetVersion"])
	}
	quality := facetValue(t, model.OutputFacets, "dataQualityAssertions")
	assertions := quality["assertions"].([]any)
	if len(assertions) != 2 {
		t.Fatalf("quality assertions = %#v", assertions)
	}
	first := assertions[0].(map[string]any)
	if first["name"] != "id_present" || first["column"] != "id" || first["severity"] != "error" {
		t.Fatalf("authored assertion = %#v", first)
	}
	second := assertions[1].(map[string]any)
	if second["name"] != "row_bounds" || second["expected"] != "1..100" || second["actual"] != "10" {
		t.Fatalf("threshold assertion = %#v", second)
	}
	metrics := facetValue(t, model.Facets, "dataQualityMetrics")
	if metrics["rowCount"] != float64(10) || len(metrics["columnMetrics"].(map[string]any)) != 0 {
		t.Fatalf("quality metrics = %#v", metrics)
	}
	lineage := facetValue(t, model.Facets, "columnLineage")
	lineageFields := lineage["fields"].(map[string]any)["id"].(map[string]any)["inputFields"].([]any)
	if len(lineageFields) != 2 || lineageFields[0].(map[string]any)["field"] != "id" || lineageFields[1].(map[string]any)["field"] != "order_id" {
		t.Fatalf("deduplicated lineage = %#v", lineageFields)
	}
	transformation := lineageFields[0].(map[string]any)["transformations"].([]any)[0].(map[string]any)
	if transformation["subtype"] != "RELATIONSHIP" || transformation["description"] != "orders" {
		t.Fatalf("lineage route = %#v", transformation)
	}

	inputStats := facetValue(t, event.Inputs[0].InputFacets, "inputStatistics")
	if inputStats["rowCount"] != float64(7) || inputStats["size"] != float64(99) || inputStats["fileCount"] != float64(2) {
		t.Fatalf("input statistics = %#v", inputStats)
	}
	outputStats := facetValue(t, model.OutputFacets, "outputStatistics")
	if outputStats["rowCount"] != float64(10) || outputStats["size"] != float64(120) || outputStats["fileCount"] != float64(1) {
		t.Fatalf("output statistics = %#v", outputStats)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"EXECUTABLE_SQL_SENTINEL", "secret", "\x00authored\x00"} {
		if bytes.Contains(eventJSON, []byte(forbidden)) {
			t.Fatalf("event leaked %q: %s", forbidden, eventJSON)
		}
	}
}

func TestQualityProjectionTranslatesWarningAndRejectsUnrepresentableOutcome(t *testing.T) {
	p := facetTestPipeline(t)
	warning := facetTestGateEvidence(t, []release.GateCheckEvidence{{
		Identity: "model:orders\x00authored\x00amount_present", Kind: "non_null", ResourceID: "model:orders",
		Outcome: release.GateWarning, Severity: "warning", ObservedRows: 1, Queries: 1,
	}})
	event, err := EventForPipelineRun(p, PipelineRun{ID: "run-warning", Outputs: []string{"model:orders"}, GateEvidence: &warning})
	if err != nil {
		t.Fatal(err)
	}
	assertions := facetValue(t, event.Outputs[0].OutputFacets, "dataQualityAssertions")["assertions"].([]any)
	assertion := assertions[0].(map[string]any)
	if assertion["name"] != "amount_present" || assertion["severity"] != "warn" || assertion["success"] != false {
		t.Fatalf("warning assertion = %#v", assertion)
	}

	unavailable := facetTestGateEvidence(t, []release.GateCheckEvidence{{
		Identity: "model:orders\x00authored\x00amount_present", Kind: "non_null", ResourceID: "model:orders",
		Outcome: release.GateUnavailable, Severity: "warning",
	}})
	if _, err := EventForPipelineRun(p, PipelineRun{ID: "run-unavailable", Outputs: []string{"model:orders"}, GateEvidence: &unavailable}); err == nil || !strings.Contains(err.Error(), "cannot be represented") {
		t.Fatalf("unrepresentable quality outcome error = %v", err)
	}
}

func TestQualityMetricsRejectConflictingRowCountEvidence(t *testing.T) {
	p := facetTestPipeline(t)
	conflicting := facetTestGateEvidence(t, []release.GateCheckEvidence{
		{Identity: "model:orders\x00authored\x00row_bounds", Kind: "row_count", ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error", ObservedRows: 10},
		{Identity: "model:orders\x00authored\x00row_bounds_secondary", Kind: "row_count", ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error", ObservedRows: 11},
	})
	if _, err := EventForPipelineRun(p, PipelineRun{ID: "run-row-count-conflict", Outputs: []string{"model:orders"}, GateEvidence: &conflicting}); err == nil || !strings.Contains(err.Error(), "row-count checks disagree") {
		t.Fatalf("conflicting row-count evidence error = %v", err)
	}
}

func TestEventRejectsInvalidPublicationVersionAndColumnLineageFields(t *testing.T) {
	p := facetTestPipeline(t)
	p.ContractPublications[0].Version = "not-semver"
	if _, err := EventForPipelineRun(p, PipelineRun{ID: "run-version"}); err == nil || !strings.Contains(err.Error(), "identity does not match canonical envelope") {
		t.Fatalf("invalid publication version error = %v", err)
	}

	p = facetTestPipeline(t)
	_, err := EventForPipelineRun(p, PipelineRun{ID: "run-lineage", Outputs: []string{"model:orders"}, ColumnLineage: map[string][]PhysicalLineage{
		"model:orders": {{Logical: "missing", Dataset: "source:orders", Field: "id"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "lineage output field") {
		t.Fatalf("invalid lineage output error = %v", err)
	}

	_, err = EventForPipelineRun(p, PipelineRun{ID: "run-lineage-source", Outputs: []string{"model:orders"}, ColumnLineage: map[string][]PhysicalLineage{
		"model:orders": {{Logical: "id", Dataset: "source:orders", Field: "missing"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "lineage source field") {
		t.Fatalf("invalid lineage source error = %v", err)
	}

	_, err = EventForPipelineRun(p, PipelineRun{ID: "run-lineage-input", Inputs: []string{}, Outputs: []string{"model:orders"}, ColumnLineage: map[string][]PhysicalLineage{
		"model:orders": {{Logical: "id", Dataset: "source:orders", Field: "id"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "not an event input") {
		t.Fatalf("dangling lineage input error = %v", err)
	}
}

func TestEventRejectsGateCheckThatDisagreesWithPublishedAuthoredCheck(t *testing.T) {
	p := facetTestPipeline(t)
	gate := facetTestGateEvidence(t, []release.GateCheckEvidence{{
		Identity: "model:orders\x00authored\x00wrong_id", Kind: "non_null", ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error",
	}})
	_, err := EventForPipelineRun(p, PipelineRun{ID: "run-mismatch", Outputs: []string{"model:orders"}, GateEvidence: &gate})
	if err == nil || !strings.Contains(err.Error(), "published contract") {
		t.Fatalf("mismatched gate check error = %v", err)
	}
}

func TestEventRejectsUnsafeAndOutOfScopeEvidence(t *testing.T) {
	p := facetTestPipeline(t)
	unsafe := facetTestGateEvidence(t, []release.GateCheckEvidence{{
		Identity: "model:orders\x00authored\x00id_present\nsecret", Kind: "non_null", ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error",
	}})
	if _, err := EventForPipelineRun(p, PipelineRun{ID: "run-unsafe", Outputs: []string{"model:orders"}, GateEvidence: &unsafe}); err == nil {
		t.Fatal("unsafe gate identity was accepted")
	}
	outOfScope := facetTestGateEvidence(t, []release.GateCheckEvidence{{
		Identity: "model:other\x00authored\x00id", Kind: "non_null", ResourceID: "model:other", Outcome: release.GateSuccess, Severity: "error",
	}})
	if _, err := EventForPipelineRun(p, PipelineRun{ID: "run-scope", GateEvidence: &outOfScope}); err == nil {
		t.Fatal("out-of-scope gate evidence was accepted")
	}
}

func TestLineageProjectionIsDeterministicAcrossInputOrder(t *testing.T) {
	p := facetTestPipeline(t)
	first := PipelineRun{ID: "run-a", Outputs: []string{"model:orders"}, ColumnLineage: map[string][]PhysicalLineage{
		"model:orders": {
			{Logical: "id", Dataset: "source:orders", Field: "z"},
			{Logical: "amount", Dataset: "source:orders", Field: "a"},
		},
	}}
	second := first
	second.ID = "run-b"
	second.ColumnLineage = map[string][]PhysicalLineage{"model:orders": {
		{Logical: "amount", Dataset: "source:orders", Field: "a"},
		{Logical: "id", Dataset: "source:orders", Field: "z"},
	}}
	one, err := EventForPipelineRun(p, first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := EventForPipelineRun(p, second)
	if err != nil {
		t.Fatal(err)
	}
	left := one.Outputs[0].Facets["columnLineage"]
	right := two.Outputs[0].Facets["columnLineage"]
	if !bytes.Equal(left, right) {
		t.Fatalf("lineage facet changed with input order:\n%s\n%s", left, right)
	}
}

func facetTestPipeline(t *testing.T) Pipeline {
	t.Helper()
	return Pipeline{
		ProjectID: "project:commerce", Environment: "prod", ID: "pipeline:orders",
		SourceInputs: []string{"source:orders"}, MaterializationScope: []string{"model:orders"},
		ContractPublications: []ContractPublication{facetTestSourcePublication(t), facetTestModelPublication(t)},
	}
}

func facetTestSourcePublication(t *testing.T) ContractPublication {
	t.Helper()
	var authored projectcontracts.Source
	if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"connection:warehouse","location":{"type":"path","path":"/orders.parquet","format":"parquet"},"schema":{"mode":"strict","fields":{"id":{"datatype":"String"},"order_id":{"datatype":"String"},"a":{"datatype":"String"},"z":{"datatype":"String"}}}}}`), &authored); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(authored, contractprojection.Contract{Version: "1.0.0", Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := contractpublication.PrepareContractPublication(contractpublication.ContractPublicationInput{
		InstanceID: "instance:commerce", Projection: projection,
		Validation: contractpublication.ValidationEvidence{Version: 1, Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "test"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func facetTestModelPublication(t *testing.T) ContractPublication {
	t.Helper()
	var authored projectcontracts.Model
	if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders"},"spec":{"definition":{"type":"direct","source":"source:orders"},"entities":{"order":{"type":"primary","fields":["id"]}},"grain":{"entity":"order"},"fields":{"id":{"datatype":"String"},"amount":{"datatype":"Decimal"}},"checks":[{"id":"id_present","type":"non_null","field":"id","severity":"error"},{"id":"amount_present","type":"non_null","field":"amount","severity":"warning"},{"id":"row_bounds","type":"row_count","minimum":1,"maximum":100,"severity":"error"},{"id":"row_bounds_secondary","type":"row_count","minimum":1,"maximum":100,"severity":"error"}]}}`), &authored); err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "source:orders", Name: "orders_source", Kind: projectgraph.KindSource}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectModel(authored, contractprojection.Contract{Version: "1.2.3", Compatibility: "backward"}, context)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := contractpublication.PrepareContractPublication(contractpublication.ContractPublicationInput{
		InstanceID: "instance:commerce", Projection: projection,
		Validation: contractpublication.ValidationEvidence{Version: 1, Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "test"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func facetTestGateEvidence(t *testing.T, checks []release.GateCheckEvidence) GateEvidence {
	t.Helper()
	outcome := release.GateSuccess
	queries := 0
	var observedRows int64
	rank := map[release.GateOutcome]int{
		release.GateSuccess: 1, release.GateWarning: 2, release.GateEmpty: 3,
		release.GateUnavailable: 4, release.GateBlocking: 5, release.GateTimeout: 6,
	}
	for _, check := range checks {
		queries += check.Queries
		observedRows += check.ObservedRows
		if rank[check.Outcome] > rank[outcome] {
			outcome = check.Outcome
		}
	}
	evidence, err := (release.GateEvidence{
		Version: 1, CandidateID: "candidate:orders", SourceDigest: "sha256:" + strings.Repeat("a", 64), BindingGeneration: "sha256:" + strings.Repeat("b", 64),
		RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}, Outcome: outcome,
		EvaluatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC), Queries: queries, ObservedRows: observedRows, DurationMillis: 1, Checks: checks,
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func facetValue(t *testing.T, facets Facets, name string) map[string]any {
	t.Helper()
	raw, ok := facets[name]
	if !ok {
		t.Fatalf("facet %q missing", name)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode facet %q: %v", name, err)
	}
	return value
}

func facetString(value string) *string { return &value }
