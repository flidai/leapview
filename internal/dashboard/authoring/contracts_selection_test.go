package authoring

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelectionMappingDecodesFactAndGrain(t *testing.T) {
	var interaction Interaction
	err := yaml.Unmarshal([]byte(`
point_selection:
  mappings:
    - field: ratings.rated_at
      fact: ratings
      grain: month
      value: label
      label: label
  targets: [activity]
`), &interaction)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	mapping := interaction.PointSelection.Mappings[0]
	if mapping.Field != "ratings.rated_at" || mapping.Fact != "ratings" || mapping.Grain != "month" {
		t.Fatalf("mapping = %#v", mapping)
	}
}
