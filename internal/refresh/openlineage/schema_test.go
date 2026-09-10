package openlineage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPinnedSchemaProvenanceAndCustomSchemaCopies(t *testing.T) {
	if OpenLineageSchemaRelease != "1.52.0" {
		t.Fatalf("OpenLineage schema release = %q", OpenLineageSchemaRelease)
	}
	if OpenLineageSchemaCommit != "cfd47d6f3e1b13167136b2508768c94a2351af23" {
		t.Fatalf("OpenLineage schema commit = %q", OpenLineageSchemaCommit)
	}
	for _, artifact := range PinnedOpenLineageSchemaProvenance() {
		path := "schema/OpenLineage.json"
		if artifact.Name != "OpenLineage.json" {
			path = "schema/facets/" + artifact.Name
		}
		raw, err := schemaFiles.ReadFile(path)
		if err != nil {
			t.Fatalf("read embedded %s: %v", artifact.Name, err)
		}
		digest := sha256.Sum256(raw)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Errorf("embedded %s checksum = %s, want %s", artifact.Name, got, artifact.SHA256)
		}
	}

	publicDir := filepath.Join("..", "..", "..", "site", "static", "openlineage", "facets", "1-0-0")
	for _, name := range []string{"LeapViewPipelineJobFacet.json", "LeapViewInvocationRunFacet.json"} {
		embedded, err := schemaFiles.ReadFile("schema/facets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		public, err := os.ReadFile(filepath.Join(publicDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(public) {
			t.Fatalf("embedded %s differs from public schema copy", name)
		}
	}
}

func TestValidateJSONAcceptsPinnedEventAndEveryKnownFacet(t *testing.T) {
	if err := ValidateJSON(schemaTestEvent(t)); err != nil {
		t.Fatalf("pinned event rejected: %v", err)
	}
}

func TestValidateJSONRejectsSchemaDriftAndUnknownLeapViewFacets(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{
			name: "event schema URL",
			edit: func(event map[string]any) { event["schemaURL"] = "https://openlineage.io/spec/2-0-1/OpenLineage.json" },
		},
		{
			name: "standard facet schema URL",
			edit: func(event map[string]any) {
				inputs := event["inputs"].([]any)
				input := inputs[0].(map[string]any)
				facets := input["facets"].(map[string]any)
				facets["schema"].(map[string]any)["_schemaURL"] = SchemaFacetSchemaURL + "#drift"
			},
		},
		{
			name: "custom facet schema URL",
			edit: func(event map[string]any) {
				job := event["job"].(map[string]any)
				facets := job["facets"].(map[string]any)
				facets[PipelineFacetKey].(map[string]any)["_schemaURL"] = PipelineFacetSchemaURL + "-drift"
			},
		},
		{
			name: "unknown LeapView facet",
			edit: func(event map[string]any) {
				job := event["job"].(map[string]any)
				job["facets"].(map[string]any)["leapView_unknown"] = map[string]any{
					"_producer": Producer, "_schemaURL": "https://leapview.dev/openlineage/facets/9-9-9/unknown.json",
				}
			},
		},
		{
			name: "malformed custom facet",
			edit: func(event map[string]any) {
				job := event["job"].(map[string]any)
				job["facets"].(map[string]any)[PipelineFacetKey].(map[string]any)["unexpected"] = true
			},
		},
		{
			name: "malformed standard facet",
			edit: func(event map[string]any) {
				inputs := event["inputs"].([]any)
				input := inputs[0].(map[string]any)
				delete(input["inputFacets"].(map[string]any)[qualityAssertionsFacetKey].(map[string]any), "assertions")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := decodeSchemaTestEvent(t)
			tc.edit(event)
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateJSON(raw); err == nil {
				t.Fatalf("invalid %s accepted", tc.name)
			}
		})
	}
}

func TestValidateJSONRejectsTrailingValues(t *testing.T) {
	raw := append(schemaTestEvent(t), []byte(` {}`)...)
	if err := ValidateJSON(raw); err == nil {
		t.Fatal("trailing JSON value accepted")
	}
}

func TestValidateJSONIsConcurrentAndDeterministic(t *testing.T) {
	raw := schemaTestEvent(t)
	const workers = 24
	const iterations = 20
	var wg sync.WaitGroup
	errors := make(chan error, workers*iterations)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				errors <- ValidateJSON(raw)
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent validation failed: %v", err)
		}
	}
}

func schemaTestEvent(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(schemaTestEventObject())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeSchemaTestEvent(t *testing.T) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(schemaTestEvent(t), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func schemaTestEventObject() map[string]any {
	base := func(schemaURL string) map[string]any {
		return map[string]any{"_producer": Producer, "_schemaURL": schemaURL}
	}
	pipeline := base(PipelineFacetSchemaURL)
	pipeline["pipelineId"] = "pipeline:sales"
	invocation := base(InvocationFacetSchemaURL)
	invocation["leapViewRunId"] = "run-1"
	invocation["invocationSource"] = "manual"
	nominal := base(NominalTimeFacetSchemaURL)
	nominal["nominalStartTime"] = "2026-08-20T06:00:00Z"
	parent := base(ParentRunFacetSchemaURL)
	parent["run"] = map[string]any{"runId": "00000000-0000-4000-8000-000000000002"}
	parent["job"] = map[string]any{"namespace": "leapview://project/prod", "name": "pipeline:sales"}

	schema := base(SchemaFacetSchemaURL)
	schema["fields"] = []any{map[string]any{"name": "id", "type": "INTEGER"}}
	version := base(VersionFacetSchemaURL)
	version["datasetVersion"] = "version-1"
	assertions := base(QualityAssertionsSchemaURL)
	assertions["assertions"] = []any{map[string]any{"assertion": "not_null", "success": true}}
	metrics := base(QualityMetricsSchemaURL)
	metrics["columnMetrics"] = map[string]any{"id": map[string]any{"nullCount": 0}}
	inputStats := base(InputStatisticsSchemaURL)
	inputStats["rowCount"] = 1
	lineage := base(ColumnLineageSchemaURL)
	lineage["fields"] = map[string]any{"id": map[string]any{"inputFields": []any{map[string]any{"namespace": "leapview://project/prod", "name": "source:orders", "field": "id"}}}}
	outputStats := base(OutputStatisticsSchemaURL)
	outputStats["rowCount"] = 1

	input := map[string]any{
		"namespace": "leapview://project/prod", "name": "source:orders",
		"facets": map[string]any{"schema": schema, "version": version},
		"inputFacets": map[string]any{
			"dataQualityAssertions": assertions,
			"dataQualityMetrics":    metrics,
			"inputStatistics":       inputStats,
			"columnLineage":         lineage,
		},
	}
	output := map[string]any{
		"namespace": "leapview://project/prod", "name": "model:orders",
		"outputFacets": map[string]any{"outputStatistics": outputStats},
	}
	return map[string]any{
		"eventType": "COMPLETE", "eventTime": "2026-08-20T09:10:11Z",
		"producer": Producer, "schemaURL": SchemaURL,
		"run": map[string]any{"runId": "00000000-0000-4000-8000-000000000001", "facets": map[string]any{
			PipelineFacetKey: pipeline, InvocationFacetKey: invocation, "nominalTime": nominal, "parent": parent,
		}},
		"job":    map[string]any{"namespace": "leapview://project/prod", "name": "pipeline:sales", "facets": map[string]any{PipelineFacetKey: pipeline}},
		"inputs": []any{input}, "outputs": []any{output},
	}
}
