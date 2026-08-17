package reportmodel

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
)

func TestResolveSelectionInteractionConformedAcrossFacts(t *testing.T) {
	dashboard, model := selectionFixture()
	dashboard.Visuals["source"] = selectionVisual(
		[]dashboardauthoring.FieldRef{{Field: "release_decade", Alias: "label"}},
		[]dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
		dashboardauthoring.QueryTime{},
		dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{Field: "release_decade", Value: "label"}},
			Targets:  []string{"cross_fact"},
		},
	)

	resolved, err := ResolveSelectionInteraction(dashboard, model, "visual", "source")
	if err != nil {
		t.Fatalf("ResolveSelectionInteraction() error = %v", err)
	}
	if len(resolved.Mappings) != 1 {
		t.Fatalf("mappings = %#v", resolved.Mappings)
	}
	mapping := resolved.Mappings[0]
	if mapping.Scope != SelectionScopeConformed || mapping.Field != "release_decade" || mapping.Fact != "" || mapping.Type != "string" {
		t.Fatalf("mapping = %#v", mapping)
	}
	if len(resolved.Targets) != 1 || strings.Join(resolved.Targets[0].Facts, ",") != "ratings,tags" {
		t.Fatalf("targets = %#v", resolved.Targets)
	}
}

func TestResolveSelectionInteractionFactLocal(t *testing.T) {
	dashboard, model := selectionFixture()
	dashboard.Visuals["source"] = selectionVisual(
		[]dashboardauthoring.FieldRef{{Field: "ratings.rating_bucket", Alias: "label"}},
		[]dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
		dashboardauthoring.QueryTime{},
		dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{Field: "ratings.rating_bucket", Fact: "ratings", Value: "label"}},
			Targets:  []string{"cross_fact"},
		},
	)

	resolved, err := ResolveSelectionInteraction(dashboard, model, "visual", "source")
	if err != nil {
		t.Fatalf("ResolveSelectionInteraction() error = %v", err)
	}
	if got := resolved.Mappings[0]; got.Scope != SelectionScopeFactLocal || got.Fact != "ratings" || got.Type != "string" {
		t.Fatalf("mapping = %#v", got)
	}
}

func TestResolveSelectionInteractionFactLocalThroughSafeRelationship(t *testing.T) {
	dashboard, model := selectionFixture()
	dashboard.Visuals["source"] = selectionVisual(
		[]dashboardauthoring.FieldRef{{Field: "movies.release_decade", Alias: "label"}},
		[]dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
		dashboardauthoring.QueryTime{},
		dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{Field: "movies.release_decade", Fact: "ratings", Value: "label"}},
			Targets:  []string{"cross_fact"},
		},
	)

	resolved, err := ResolveSelectionInteraction(dashboard, model, "visual", "source")
	if err != nil {
		t.Fatalf("ResolveSelectionInteraction() error = %v", err)
	}
	if got := resolved.Mappings[0]; got.Scope != SelectionScopeFactLocal || got.Fact != "ratings" {
		t.Fatalf("mapping = %#v", got)
	}

	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "ratings_movies_alternate", From: "ratings.alternate_movie_id", To: "movies.movie_id", Cardinality: "many_to_one",
	})
	_, err = ResolveSelectionInteraction(dashboard, model, "visual", "source")
	if err == nil || !strings.Contains(err.Error(), `ambiguous relationship path from "ratings" to "movies"`) {
		t.Fatalf("ResolveSelectionInteraction() ambiguous error = %v", err)
	}
}

func TestResolveSelectionInteractionGrainedTime(t *testing.T) {
	dashboard, model := selectionFixture()
	dashboard.Visuals["source"] = selectionVisual(
		nil,
		[]dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
		dashboardauthoring.QueryTime{Field: "activity_date", Grain: "month", Alias: "label"},
		dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{Field: "activity_date", Grain: "month", Value: "label"}},
			Targets:  []string{"cross_fact"},
		},
	)

	resolved, err := ResolveSelectionInteraction(dashboard, model, "visual", "source")
	if err != nil {
		t.Fatalf("ResolveSelectionInteraction() error = %v", err)
	}
	if got := resolved.Mappings[0]; got.Grain != "month" || got.Type != "timestamp" {
		t.Fatalf("mapping = %#v", got)
	}
}

func TestResolvedSelectionInteractionCanonicalizesCompleteTuple(t *testing.T) {
	resolved := ResolvedSelectionInteraction{Mappings: []ResolvedSelectionMapping{
		{Field: "release_decade", Scope: SelectionScopeConformed},
		{Field: "activity_date", Grain: "month", Scope: SelectionScopeConformed},
	}}
	canonical, err := resolved.CanonicalizeMappings([]SelectionMappingIdentity{
		{Field: "activity_date", Grain: "month"},
		{Field: "release_decade"},
	})
	if err != nil {
		t.Fatalf("CanonicalizeMappings() error = %v", err)
	}
	if canonical[0].Field != "release_decade" || canonical[1].Field != "activity_date" {
		t.Fatalf("canonical mappings = %#v", canonical)
	}

	for _, test := range []struct {
		name     string
		incoming []SelectionMappingIdentity
		want     string
	}{
		{name: "incomplete", incoming: []SelectionMappingIdentity{{Field: "release_decade"}}, want: "want 2"},
		{name: "forged fact", incoming: []SelectionMappingIdentity{{Field: "release_decade", Fact: "ratings"}, {Field: "activity_date", Grain: "month"}}, want: "unknown mapping identity"},
		{name: "duplicate", incoming: []SelectionMappingIdentity{{Field: "release_decade"}, {Field: "release_decade"}}, want: "duplicate mapping identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolved.CanonicalizeMappings(test.incoming)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CanonicalizeMappings() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestResolveSelectionInteractionRejectsInvalidMappings(t *testing.T) {
	tests := []struct {
		name       string
		dimensions []dashboardauthoring.FieldRef
		time       dashboardauthoring.QueryTime
		mappings   []dashboardauthoring.SelectionMapping
		targets    []string
		want       string
	}{
		{name: "unknown semantic dimension", dimensions: refs("missing"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "missing", Value: "label"}), want: `unknown semantic dimension "missing"`},
		{name: "fact on conformed", dimensions: refs("release_decade"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "release_decade", Fact: "ratings", Value: "label"}), want: `semantic dimension "release_decade" must not specify fact`},
		{name: "missing local fact", dimensions: refs("ratings.rating_bucket"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "ratings.rating_bucket", Value: "label"}), want: `physical field "ratings.rating_bucket" requires fact`},
		{name: "unknown fact", dimensions: refs("ratings.rating_bucket"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "ratings.rating_bucket", Fact: "unknown", Value: "label"}), want: `references unknown fact "unknown"`},
		{name: "unsafe local path", dimensions: refs("movies.release_decade"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "movies.release_decade", Fact: "tags", Value: "label"}), want: `no safe relationship path`},
		{name: "source missing field", dimensions: refs("release_decade"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "activity_date", Grain: "month", Value: "label"}), want: `is not exposed by the source query`},
		{name: "missing grain", time: dashboardauthoring.QueryTime{Field: "activity_date", Grain: "month", Alias: "label"}, mappings: mappings(dashboardauthoring.SelectionMapping{Field: "activity_date", Value: "label"}), want: `requires grain "month"`},
		{name: "wrong grain", time: dashboardauthoring.QueryTime{Field: "activity_date", Grain: "month", Alias: "label"}, mappings: mappings(dashboardauthoring.SelectionMapping{Field: "activity_date", Grain: "year", Value: "label"}), want: `requires grain "month"`},
		{name: "unsupported semantic grain", time: dashboardauthoring.QueryTime{Field: "activity_date", Grain: "day", Alias: "label"}, mappings: mappings(dashboardauthoring.SelectionMapping{Field: "activity_date", Grain: "day", Value: "label"}), want: `does not support grain "day"`},
		{name: "grain outside time", dimensions: refs("activity_date"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "activity_date", Grain: "month", Value: "label"}), want: `grain is only valid for a grained query time field`},
		{name: "mixed scopes", dimensions: refs("release_decade", "ratings.rating_bucket"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "label"}, dashboardauthoring.SelectionMapping{Field: "ratings.rating_bucket", Fact: "ratings", Value: "series"}), want: `must be entirely conformed or fact-local to one fact`},
		{name: "different local facts", dimensions: refs("ratings.rating_bucket", "tags.tag"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "ratings.rating_bucket", Fact: "ratings", Value: "label"}, dashboardauthoring.SelectionMapping{Field: "tags.tag", Fact: "tags", Value: "series"}), want: `must be entirely conformed or fact-local to one fact`},
		{name: "duplicate identity", dimensions: refs("release_decade"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "label"}, dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "series"}), want: `contains duplicate mapping identity`},
		{name: "target missing binding", dimensions: refs("ratings_only"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "ratings_only", Value: "label"}), targets: []string{"cross_fact"}, want: `has no binding for target fact "tags"`},
		{name: "target missing local fact", dimensions: refs("ratings.rating_bucket"), mappings: mappings(dashboardauthoring.SelectionMapping{Field: "ratings.rating_bucket", Fact: "ratings", Value: "label"}), targets: []string{"tags_only"}, want: `target "tags_only" does not participate in fact "ratings"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dashboard, model := selectionFixture()
			dashboard.Visuals["source"] = selectionVisual(
				test.dimensions,
				[]dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
				test.time,
				dashboardauthoring.SelectionInteraction{Mappings: test.mappings, Targets: test.targets},
			)
			_, err := ResolveSelectionInteraction(dashboard, model, "visual", "source")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResolveSelectionInteraction() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func selectionFixture() (*dashboardauthoring.Dashboard, *semanticmodel.Model) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"ratings": {Dimensions: map[string]semanticmodel.MetricDimension{
				"rating_bucket": {Type: "string"},
				"rated_at":      {Type: "timestamp"},
			}},
			"tags": {Dimensions: map[string]semanticmodel.MetricDimension{
				"tag":       {Type: "string"},
				"tagged_at": {Type: "timestamp"},
			}},
			"movies": {Dimensions: map[string]semanticmodel.MetricDimension{
				"release_decade": {Type: "string"},
			}},
		},
		Relationships: []semanticmodel.Relationship{{ID: "ratings_movies", From: "ratings.movie_id", To: "movies.movie_id", Cardinality: "many_to_one"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"release_decade": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{
				"ratings": {Field: "movies.release_decade", Path: []string{"ratings_movies"}},
				"tags":    {Field: "tags.tag"},
			}},
			"activity_date": {Type: "timestamp", Grains: []string{"month", "year"}, Bindings: map[string]semanticmodel.DimensionBinding{
				"ratings": {Field: "ratings.rated_at"},
				"tags":    {Field: "tags.tagged_at"},
			}},
			"ratings_only": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{
				"ratings": {Field: "ratings.rating_bucket"},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"rating_count": {Type: "aggregate", Dataset: "ratings"},
			"tag_count":    {Type: "aggregate", Dataset: "tags"},
		},
	}
	dashboard := &dashboardauthoring.Dashboard{Visuals: map[string]dashboardauthoring.AuthoringVisualization{
		"cross_fact": selectionVisual(refs("release_decade"), []dashboardauthoring.FieldRef{{Field: "rating_count"}, {Field: "tag_count"}}, dashboardauthoring.QueryTime{}, dashboardauthoring.SelectionInteraction{}),
		"tags_only":  selectionVisual(refs("tags.tag"), []dashboardauthoring.FieldRef{{Field: "tag_count"}}, dashboardauthoring.QueryTime{}, dashboardauthoring.SelectionInteraction{}),
	}}
	return dashboard, model
}

func selectionVisual(dimensions, metrics []dashboardauthoring.FieldRef, time dashboardauthoring.QueryTime, selection dashboardauthoring.SelectionInteraction) dashboardauthoring.AuthoringVisualization {
	return dashboardauthoring.ChartVisualization(dashboardauthoring.Visual{Type: "bar", Query: dashboardauthoring.VisualQuery{Dimensions: dimensions, Metrics: metrics, Time: time}, Interaction: dashboardauthoring.Interaction{PointSelection: selection}})
}

func refs(fields ...string) []dashboardauthoring.FieldRef {
	refs := make([]dashboardauthoring.FieldRef, len(fields))
	for index, field := range fields {
		refs[index] = dashboardauthoring.FieldRef{Field: field, Alias: "label"}
	}
	return refs
}

func mappings(values ...dashboardauthoring.SelectionMapping) []dashboardauthoring.SelectionMapping {
	return values
}
