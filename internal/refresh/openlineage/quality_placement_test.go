package openlineage

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/release"
)

func TestQualityAssertionsUseDatasetFacetNotOutputFacet(t *testing.T) {
	gate := facetTestGateEvidence(t, []release.GateCheckEvidence{{
		Identity: "model:orders\x00authored\x00id_present", Kind: "non_null",
		ResourceID: "model:orders", Outcome: release.GateSuccess, Severity: "error", Queries: 1,
	}})
	event, err := EventForPipelineRun(facetTestPipeline(t), PipelineRun{ID: "quality-placement", GateEvidence: &gate})
	if err != nil {
		t.Fatal(err)
	}
	output := &event.Outputs[0]
	assertions, exists := output.Facets[qualityAssertionsFacetKey]
	if !exists {
		t.Fatal("quality assertions must be attached to the validated dataset's facets")
	}
	if _, exists := output.OutputFacets[qualityAssertionsFacetKey]; exists {
		t.Fatal("input-derived quality assertions must not masquerade as an output facet")
	}
	delete(output.Facets, qualityAssertionsFacetKey)
	output.OutputFacets = Facets{qualityAssertionsFacetKey: assertions}
	if err := ValidateEvent(event); err == nil || !strings.Contains(err.Error(), "outputFacets") {
		t.Fatalf("misplaced quality assertions were not rejected: %v", err)
	}
}
