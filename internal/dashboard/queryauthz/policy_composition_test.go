package authz

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/stretchr/testify/require"
)

func TestApplyDataPoliciesUsesOneAlgebraAcrossQueryKinds(t *testing.T) {
	repository := &candidateAuthorizationRepository{policies: mustCompileTestPolicies(t, []access.DataPolicy{
		{
			ID: "published", WorkspaceID: "sales", ObjectID: "ratings", PolicyType: "row_filter",
			ExpressionJSON: `{"field":"ratings.status","operator":"equals","values":["published"]}`,
		},
		{
			ID: "country-dk", WorkspaceID: "sales", ObjectID: "ratings", SubjectType: access.SubjectGroup, SubjectID: "dk",
			PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.country","operator":"equals","values":["DK"]}`,
		},
		{
			ID: "country-se", WorkspaceID: "sales", ObjectID: "ratings", SubjectType: access.SubjectGroup, SubjectID: "se",
			PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.country","operator":"equals","values":["SE"]}`,
		},
	})}
	model := governanceTestModel()
	ratings := model.Tables["ratings"]
	ratings.Dimensions["country"] = semanticDimension("string")
	model.Tables["ratings"] = ratings
	metrics := New(semanticModelMetrics{model: model}, Options{Repo: repository})

	tests := []struct {
		name    string
		request dataquery.Query
	}{
		{
			name: "raw rows",
			request: dataquery.Query{WorkspaceID: "sales", ModelID: model.Name, Kind: dataquery.KindSemanticRows, Target: "ratings",
				Fields: []dataquery.Field{{Field: "ratings.rating"}}},
		},
		{
			name: "semantic aggregate",
			request: dataquery.Query{WorkspaceID: "sales", ModelID: model.Name, Kind: dataquery.KindSemanticAggregate, Target: "ratings",
				Measures: []dataquery.Field{{Field: "rating_count"}}},
		},
		{
			name: "multi-fact aggregate",
			request: dataquery.Query{WorkspaceID: "sales", ModelID: model.Name, Kind: dataquery.KindSemanticAggregate,
				Measures: []dataquery.Field{{Field: "rating_count"}, {Field: "tag_count"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			governed, policies, err := metrics.applyDataPolicies(context.Background(), tt.request, []access.ObjectRef{access.WorkspaceObject("sales")})
			require.NoError(t, err)
			require.Len(t, policies, 3)
			require.Len(t, governed.Filters, 2)
			require.Equal(t, "ratings.status", governed.Filters[0].Field)
			require.Len(t, governed.Filters[1].Groups, 2)
			if tt.name == "multi-fact aggregate" {
				_, err = semanticquery.NewPlanner(model).Plan(semanticquery.Request{
					Measures: dataFieldsToSemanticFields(governed.Measures),
					Filters:  dataFiltersToSemanticFilters(governed.Filters),
				})
				require.NoError(t, err)
				for _, group := range governed.Filters[1].Groups {
					require.NotEmpty(t, group.Filters)
					for _, filter := range group.Filters {
						require.Equal(t, "ratings", filter.Fact)
					}
				}
			}
		})
	}
}

func semanticDimension(kind string) semanticmodel.MetricDimension {
	return semanticmodel.MetricDimension{Type: kind}
}

func TestComposeDataPoliciesRowFilterAlgebra(t *testing.T) {
	policy := func(id, object string, subjectType access.SubjectType, subjectID, field, value string) access.DataPolicy {
		return mustCompileTestPolicy(t, access.DataPolicy{
			ID: id, WorkspaceID: "sales", ObjectID: object,
			SubjectType: subjectType, SubjectID: subjectID, PolicyType: "row_filter",
			ExpressionJSON: `{"field":"` + field + `","operator":"equals","values":["` + value + `"]}`,
		})
	}
	allowAll := func(id, object string, subjectType access.SubjectType, subjectID string) access.DataPolicy {
		return mustCompileTestPolicy(t, access.DataPolicy{
			ID: id, WorkspaceID: "sales", ObjectID: object,
			SubjectType: subjectType, SubjectID: subjectID, PolicyType: "row_filter",
			ExpressionJSON: `{"allowAll":true}`,
		})
	}
	filter := func(field, value string) dataquery.Filter {
		return dataquery.Filter{Field: field, Operator: "equals", Values: []any{value}}
	}

	tests := []struct {
		name      string
		active    []access.DataPolicy
		mandatory []access.DataPolicy
		want      []dataquery.Filter
	}{
		{
			name: "same subject group is conjunctive",
			active: []access.DataPolicy{
				policy("country", "dataset-ratings", access.SubjectGroup, "analysts", "ratings.country", "DK"),
				policy("status", "dataset-ratings", access.SubjectGroup, "analysts", "ratings.status", "published"),
			},
			want: []dataquery.Filter{filter("ratings.country", "DK"), filter("ratings.status", "published")},
		},
		{
			name: "different subject groups are alternatives",
			active: []access.DataPolicy{
				policy("country", "dataset-ratings", access.SubjectGroup, "nordics", "ratings.country", "DK"),
				policy("region", "dataset-ratings", access.SubjectGroup, "emea", "ratings.region", "EU"),
			},
			want: []dataquery.Filter{{Groups: []dataquery.FilterGroup{
				{Filters: []dataquery.Filter{filter("ratings.region", "EU")}},
				{Filters: []dataquery.Filter{filter("ratings.country", "DK")}},
			}}},
		},
		{
			name: "global restrictions intersect subject alternatives",
			active: []access.DataPolicy{
				policy("published", "dataset-ratings", "", "", "ratings.status", "published"),
				policy("country", "dataset-ratings", access.SubjectGroup, "nordics", "ratings.country", "DK"),
				policy("region", "dataset-ratings", access.SubjectGroup, "emea", "ratings.region", "EU"),
			},
			want: []dataquery.Filter{
				filter("ratings.status", "published"),
				{Groups: []dataquery.FilterGroup{
					{Filters: []dataquery.Filter{filter("ratings.region", "EU")}},
					{Filters: []dataquery.Filter{filter("ratings.country", "DK")}},
				}},
			},
		},
		{
			name: "allow-all subject makes subject alternatives neutral",
			active: []access.DataPolicy{
				policy("published", "dataset-ratings", "", "", "ratings.status", "published"),
				policy("country", "dataset-ratings", access.SubjectGroup, "nordics", "ratings.country", "DK"),
				allowAll("executives", "dataset-ratings", access.SubjectGroup, "executives"),
			},
			want: []dataquery.Filter{filter("ratings.status", "published")},
		},
		{
			name: "different protected objects are conjunctive boundaries",
			active: []access.DataPolicy{
				policy("country", "dataset-ratings", access.SubjectGroup, "analysts", "ratings.country", "DK"),
				policy("status", "workspace-sales", access.SubjectGroup, "auditors", "ratings.status", "published"),
			},
			want: []dataquery.Filter{filter("ratings.country", "DK"), filter("ratings.status", "published")},
		},
		{
			name: "candidate restrictions are always mandatory",
			active: []access.DataPolicy{
				allowAll("executives", "dataset-ratings", access.SubjectGroup, "executives"),
			},
			mandatory: []access.DataPolicy{
				policy("candidate", "dataset-ratings", "", "", "ratings.region", "Hovedstaden"),
			},
			want: []dataquery.Filter{filter("ratings.region", "Hovedstaden")},
		},
		{name: "no applicable policy is neutral"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			composition, err := composeDataPolicies(tt.active, tt.mandatory)
			require.NoError(t, err)
			require.Equal(t, tt.want, composition.Filters)
		})
	}
}

func TestComposeDataPoliciesPreservesPolicyIDs(t *testing.T) {
	policies := mustCompileTestPolicies(t, []access.DataPolicy{
		{ID: "z-policy", WorkspaceID: "sales", ObjectID: "ratings", PolicyType: "row_filter", ExpressionJSON: `{"allowAll":true}`},
		{ID: "a-policy", WorkspaceID: "sales", ObjectID: "ratings", PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"null"}`},
	})

	composition, err := composeDataPolicies(policies, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"a-policy", "z-policy"}, composition.PolicyIDs)
}

func TestComposeDataPoliciesRejectsUncompiledPolicy(t *testing.T) {
	_, err := composeDataPolicies([]access.DataPolicy{{
		ID: "runtime-json", WorkspaceID: "sales", PolicyType: "row_filter",
		ExpressionJSON: `{"field":"ratings.country","value":"DK"}`,
	}}, nil)
	require.ErrorContains(t, err, `data policy "runtime-json" is not compiled`)
}

func TestComposeDataPoliciesRejectsConflictingColumnMasks(t *testing.T) {
	policies := mustCompileTestPolicies(t, []access.DataPolicy{
		{ID: "mask-null", WorkspaceID: "sales", ObjectID: "ratings.email", PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"null"}`},
		{ID: "mask-redact", WorkspaceID: "sales", ObjectID: "ratings.email", PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"redact"}`},
	})

	_, err := composeDataPolicies(policies, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "ratings.email")
	require.ErrorContains(t, err, "mask-null")
	require.ErrorContains(t, err, "mask-redact")
}

func TestComposeDataPoliciesDeduplicatesEquivalentColumnMasks(t *testing.T) {
	policies := mustCompileTestPolicies(t, []access.DataPolicy{
		{ID: "mask-group", WorkspaceID: "sales", ObjectID: "ratings.email", SubjectType: access.SubjectGroup, SubjectID: "analysts", PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"null"}`},
		{ID: "mask-global", WorkspaceID: "sales", ObjectID: "ratings.email", PolicyType: "column_mask", ExpressionJSON: `{"columns":["ratings.email"],"mask":"null"}`},
	})

	composition, err := composeDataPolicies(policies, nil)
	require.NoError(t, err)
	require.Equal(t, []columnMaskPolicy{{
		PolicyIDs: []string{"mask-global", "mask-group"},
		Fields:    []string{"ratings.email"},
		Mask:      accesspolicy.MaskNull,
	}}, composition.Masks)
}

func TestSelectedColumnMasksRejectsOverlappingFieldReferences(t *testing.T) {
	composition, err := composeDataPolicies(mustCompileTestPolicies(t, []access.DataPolicy{
		{ID: "mask-leaf", WorkspaceID: "sales", ObjectID: "ratings.email", PolicyType: "column_mask", ExpressionJSON: `{"field":"email","mask":"null"}`},
		{ID: "mask-qualified", WorkspaceID: "sales", ObjectID: "ratings.email", PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"redact"}`},
	}), nil)
	require.NoError(t, err)

	_, err = selectedColumnMasks(dataquery.Query{
		Kind: dataquery.KindSemanticRows, Fields: []dataquery.Field{{Field: "ratings.email"}},
	}, composition.Masks)
	require.Error(t, err)
	require.ErrorContains(t, err, "ratings.email")
	require.ErrorContains(t, err, "mask-leaf")
	require.ErrorContains(t, err, "mask-qualified")
}

func mustCompileTestPolicy(t testing.TB, value access.DataPolicy) access.DataPolicy {
	t.Helper()
	compiled, err := accesspolicy.Compile(value.ID, value.PolicyType, value.ExpressionJSON)
	require.NoError(t, err)
	value.Compiled = compiled
	return value
}

func mustCompileTestPolicies(t testing.TB, values []access.DataPolicy) []access.DataPolicy {
	t.Helper()
	compiled, err := compileTestPolicies(values)
	require.NoError(t, err)
	return compiled
}

func compileTestPolicies(values []access.DataPolicy) ([]access.DataPolicy, error) {
	out := append([]access.DataPolicy(nil), values...)
	for index := range out {
		compiled, err := accesspolicy.Compile(out[index].ID, out[index].PolicyType, out[index].ExpressionJSON)
		if err != nil {
			return nil, err
		}
		out[index].Compiled = compiled
	}
	return out, nil
}

func TestComposeDataPoliciesRejectsMalformedNestedFilters(t *testing.T) {
	tests := []struct {
		name       string
		expression string
	}{
		{name: "empty field", expression: `{"filters":[{"field":"","operator":"equals","values":["DK"]}]}`},
		{name: "empty in", expression: `{"filters":[{"field":"ratings.country","operator":"in","values":[]}]}`},
		{name: "mixed field and groups", expression: `{"filters":[{"field":"ratings.country","groups":[{"filters":[{"field":"ratings.region","operator":"equals","values":["EU"]}]}]}]}`},
		{name: "empty group", expression: `{"filters":[{"groups":[{"filters":[]}]}]}`},
		{name: "unsupported operator", expression: `{"filters":[{"field":"ratings.country","operator":"matches","values":["DK"]}]}`},
		{name: "null operator with value", expression: `{"filters":[{"field":"ratings.country","operator":"is_null","values":["DK"]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := composeDataPolicies([]access.DataPolicy{{
				ID: "nested", WorkspaceID: "sales", PolicyType: "row_filter", ExpressionJSON: tt.expression,
			}}, nil)
			require.Error(t, err)
			require.ErrorContains(t, err, "nested")
		})
	}
}
