package authoring

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelectionMappingDecodesDatasetAndGrain(t *testing.T) {
	var interaction Interaction
	err := yaml.Unmarshal([]byte(`
point_selection:
  mappings:
    - field: ratings.rated_at
      dataset: ratings
      grain: month
      value: label
      label: label
  targets: [activity]
`), &interaction)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	mapping := interaction.PointSelection.Mappings[0]
	if mapping.Field != "ratings.rated_at" || mapping.Dataset != "ratings" || mapping.Grain != "month" {
		t.Fatalf("mapping = %#v", mapping)
	}
}
