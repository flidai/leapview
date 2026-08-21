package openlineage

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testPipeline() Pipeline {
	return Pipeline{
		ProjectID: "project:commerce", Environment: "prod", ID: "pipeline:sales",
		SemanticModelID: "semantic-model:sales", GenerationID: "generation-17",
		PlanDigest: "sha256:plan", SelectionDigest: "sha256:selection",
		SourceInputs:         []string{"source:orders", "source:customers", "source:orders"},
		MaterializationScope: []string{"model:customers", "model:orders"},
	}
}

func facet(t *testing.T, facets Facets, name string) map[string]any {
	t.Helper()
	value, ok := facets[name]
	if !ok {
		t.Fatalf("facet %q missing from %#v", name, facets)
	}
	var result map[string]any
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatalf("decode %s facet: %v", name, err)
	}
	return result
}

func TestJobForPipelineMapsPipelineIdentityAndScopedFacet(t *testing.T) {
	job, err := JobForPipeline(testPipeline())
	if err != nil {
		t.Fatal(err)
	}
	if job.Namespace != NamespaceFor("project:commerce", "prod") || job.Name != "pipeline:sales" {
		t.Fatalf("job identity = %#v", job)
	}
	scoped := facet(t, job.Facets, PipelineFacetKey)
	for key, want := range map[string]string{"generationId": "generation-17", "planDigest": "sha256:plan", "selectionDigest": "sha256:selection"} {
		if scoped[key] != want {
			t.Errorf("leapview %s = %v, want %q", key, scoped[key], want)
		}
	}
}

func TestEventForPipelineRunMapsRunAndMaterialization(t *testing.T) {
	p := testPipeline()
	eventTime := time.Date(2026, 8, 20, 9, 10, 11, 0, time.UTC)
	event, err := EventForPipelineRun(p, PipelineRun{ID: "run-1", PipelineID: p.ID, EventType: EventComplete, EventTime: eventTime})
	if err != nil {
		t.Fatal(err)
	}
	if event.EventType != EventComplete || !event.EventTime.Equal(eventTime) || event.Run.RunID != openLineageRunID("run-1") {
		t.Fatalf("event = %#v", event)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(event.Run.RunID) {
		t.Fatalf("OpenLineage run id = %q, want UUID v5", event.Run.RunID)
	}
	if got := facet(t, event.Run.Facets, InvocationFacetKey)["leapViewRunId"]; got != "run-1" {
		t.Fatalf("LeapView run identity = %v, want run-1", got)
	}
	if len(event.Inputs) != 2 || event.Inputs[0].Name != "source:orders" || len(event.Outputs) != 2 || event.Outputs[1].Name != "model:orders" {
		t.Fatalf("datasets inputs=%#v outputs=%#v", event.Inputs, event.Outputs)
	}
	if event.Producer != Producer || event.SchemaURL != SchemaURL {
		t.Fatalf("event metadata = %q %q", event.Producer, event.SchemaURL)
	}
	if got := facet(t, event.Run.Facets, PipelineFacetKey)["generationId"]; got != "generation-17" {
		t.Errorf("run generation = %v", got)
	}
}

func TestScheduledRunEmitsNominalTimeFacet(t *testing.T) {
	nominal := time.Date(2026, 8, 20, 6, 0, 0, 123000000, time.FixedZone("CEST", 2*60*60))
	event, err := EventForPipelineRun(testPipeline(), PipelineRun{ID: "run-scheduled", EventTime: nominal.Add(3 * time.Minute), NominalTime: &nominal, InvocationSource: "schedule", MatchingScheduleIDs: []string{"weekdays-0600"}})
	if err != nil {
		t.Fatal(err)
	}
	nominalFacet := facet(t, event.Run.Facets, "nominalTime")
	if nominalFacet["nominalStartTime"] != nominal.UTC().Format(time.RFC3339Nano) {
		t.Errorf("nominal start = %v", nominalFacet["nominalStartTime"])
	}
	trigger := facet(t, event.Run.Facets, InvocationFacetKey)
	if trigger["invocationSource"] != "schedule" || trigger["matchingScheduleIds"].([]any)[0] != "weekdays-0600" {
		t.Errorf("trigger facet = %#v", trigger)
	}
}

func TestModelRunEmitsParentFacet(t *testing.T) {
	p := testPipeline()
	event, err := ModelRun(p, PipelineRun{ID: "model-run-1", ParentRunID: "pipeline-run-1", EventTime: time.Now()}, "model:orders")
	if err != nil {
		t.Fatal(err)
	}
	if event.Job.Name != "model:orders" || event.Run.RunID != openLineageRunID("model-run-1") {
		t.Fatalf("model event identity = %#v %#v", event.Job, event.Run)
	}
	parent := facet(t, event.Run.Facets, "parent")
	parentRun, ok := parent["parent"].(map[string]any)
	if !ok {
		t.Fatalf("parent facet = %#v", parent)
	}
	parentRunID, ok := parentRun["run"].(map[string]any)
	if !ok || parentRunID["runId"] != openLineageRunID("pipeline-run-1") {
		t.Fatalf("parent run = %#v", parentRun)
	}
	parentJob, ok := parentRun["job"].(map[string]any)
	if !ok || parentJob["name"] != p.ID || parentJob["namespace"] != NamespaceFor(p.ProjectID, p.Environment) {
		t.Fatalf("parent job = %#v", parentRun["job"])
	}
	if len(event.Outputs) != 1 || event.Outputs[0].Name != "model:orders" {
		t.Fatalf("model outputs = %#v", event.Outputs)
	}
}

func TestModelRunConvenienceDerivesChildID(t *testing.T) {
	event, err := ModelRun(testPipeline(), PipelineRun{ID: "pipeline-run-1"}, "model:orders")
	if err != nil {
		t.Fatal(err)
	}
	if event.Run.RunID != openLineageRunID("pipeline-run-1/model/model:orders") {
		t.Fatalf("derived child run id = %q", event.Run.RunID)
	}
	if got := facet(t, event.Run.Facets, "parent")["parent"].(map[string]any)["run"].(map[string]any)["runId"]; got != openLineageRunID("pipeline-run-1") {
		t.Fatalf("derived parent = %v", got)
	}
}

func TestCustomFacetContractUsesLeapViewPrefixAndImmutableSchemas(t *testing.T) {
	p := testPipeline()
	p.InvocationSource = "schedule"
	p.MatchingScheduleIDs = []string{"daily", "weekday"}
	p.StartingDeadlineSeconds = 3600
	p.ConcurrencyPolicy = "Replace"
	job, err := JobForPipeline(p)
	if err != nil {
		t.Fatal(err)
	}
	for key, raw := range job.Facets {
		if !strings.HasPrefix(key, "leapView_") {
			t.Fatalf("job facet key %q is not LeapView-prefixed", key)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if value["_producer"] != Producer || value["_schemaURL"] != PipelineFacetSchemaURL {
			t.Fatalf("job facet metadata = %#v", value)
		}
	}
	nominal := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	event, err := EventForPipelineRun(p, PipelineRun{ID: "run-contract", InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily", "weekday"}, NominalTime: &nominal})
	if err != nil {
		t.Fatal(err)
	}
	for key, raw := range event.Run.Facets {
		if key == "nominalTime" || key == "parent" {
			continue
		}
		if !strings.HasPrefix(key, "leapView_") {
			t.Fatalf("run facet key %q is not LeapView-prefixed", key)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if value["_producer"] != Producer {
			t.Fatalf("run facet producer = %#v", value)
		}
		if key == InvocationFacetKey && value["_schemaURL"] != InvocationFacetSchemaURL {
			t.Fatalf("invocation schema = %#v", value)
		}
	}
}

func TestEventUsesPlanInvocationEvidenceWhenRunOmitsIt(t *testing.T) {
	p := testPipeline()
	p.InvocationSource = "schedule"
	p.MatchingScheduleIDs = []string{"daily"}
	event, err := EventForPipelineRun(p, PipelineRun{ID: "run-plan-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := facet(t, event.Run.Facets, InvocationFacetKey)
	if invocation["invocationSource"] != "schedule" || invocation["matchingScheduleIds"].([]any)[0] != "daily" {
		t.Fatalf("plan invocation evidence = %#v", invocation)
	}
}
