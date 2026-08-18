package compiler

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestMovieLensExperimentLoadsAndUsesApproximateScoreTypes(t *testing.T) {
	project, err := LoadProject("../../../dashboards/experiments/movielens/leapview.yaml")
	if err != nil {
		t.Fatalf("LoadProject MovieLens experiment: %v", err)
	}

	checks := map[string]string{
		"ratings":       "rating",
		"rating_genres": "rating",
		"users":         "average_rating",
	}
	for modelName, fieldName := range checks {
		table, ok := project.Models[modelName]
		if !ok {
			t.Fatalf("MovieLens model %q is missing", modelName)
		}
		field, ok := table.Dimensions[fieldName]
		if !ok {
			t.Fatalf("MovieLens model %q field %q is missing", modelName, fieldName)
		}
		if field.Datatype != semanticmodel.DataTypeFloat {
			t.Errorf("MovieLens model %q field %q datatype = %q, want %q", modelName, fieldName, field.Datatype, semanticmodel.DataTypeFloat)
		}
	}
	derived := project.Models["rating_genres"]
	if !reflect.DeepEqual(derived.ModelDependencies, []string{"movies", "ratings"}) {
		t.Fatalf("MovieLens rating_genres model dependencies = %#v, want [movies ratings]", derived.ModelDependencies)
	}
}
