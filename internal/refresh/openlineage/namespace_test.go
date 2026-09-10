package openlineage

import (
	"strings"
	"testing"
	"time"
)

func TestPipelineRejectsNonCanonicalExplicitNamespace(t *testing.T) {
	for name, namespace := range map[string]string{
		"leading and trailing whitespace": " urn:leapview:instance:commerce ",
		"empty whitespace":                " \t\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := testPipeline()
			p.Namespace = namespace
			if _, err := EventForPipelineRun(p, PipelineRun{ID: "run-invalid-namespace"}); err == nil || !strings.Contains(err.Error(), "namespace") {
				t.Fatalf("namespace %q was accepted: %v", namespace, err)
			}
		})
	}
}

func TestExplicitNamespaceIsSharedByJobAndDatasets(t *testing.T) {
	p := testPipeline()
	p.Namespace = "urn:leapview:instance:commerce"
	event, err := EventForPipelineRun(p, PipelineRun{
		ID:        "run-explicit-namespace",
		EventTime: time.Date(2026, 9, 2, 12, 3, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Job.Namespace != p.Namespace {
		t.Fatalf("job namespace = %q, want %q", event.Job.Namespace, p.Namespace)
	}
	for _, dataset := range append(event.Inputs, event.Outputs...) {
		if dataset.Namespace != p.Namespace {
			t.Fatalf("dataset %q namespace = %q, want %q", dataset.Name, dataset.Namespace, p.Namespace)
		}
	}
}
