package policy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompileRowFilterRepresentations(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		want       RowFilter
	}{
		{
			name:       "shorthand",
			expression: `{"field":"ratings.country","operator":"equals","value":"DK"}`,
			want: RowFilter{Filters: []Filter{{
				Field: "ratings.country", Operator: "equals", Values: []any{"DK"},
			}}},
		},
		{
			name:       "filter list",
			expression: `{"filters":[{"field":"ratings.country","operator":"in","values":["DK","SE"]},{"field":"ratings.deleted_at","operator":"is_null"}]}`,
			want: RowFilter{Filters: []Filter{
				{Field: "ratings.country", Operator: "in", Values: []any{"DK", "SE"}},
				{Field: "ratings.deleted_at", Operator: "is_null"},
			}},
		},
		{
			name:       "allow all",
			expression: `{"allowAll":true}`,
			want:       RowFilter{AllowAll: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := Compile("policy-row", TypeRowFilter, tt.expression)
			require.NoError(t, err)
			require.Equal(t, TypeRowFilter, compiled.Type)
			require.NotNil(t, compiled.RowFilter)
			require.Equal(t, tt.want, *compiled.RowFilter)
			require.Nil(t, compiled.ColumnMask)
		})
	}
}

func TestCompileColumnMask(t *testing.T) {
	compiled, err := Compile("policy-mask", TypeColumnMask, `{"field":"ratings.email","columns":["ratings.phone"],"mask":"redact"}`)
	require.NoError(t, err)
	require.Equal(t, TypeColumnMask, compiled.Type)
	require.Nil(t, compiled.RowFilter)
	require.Equal(t, &ColumnMask{
		Fields: []string{"ratings.phone", "ratings.email"}, Mask: MaskRedact,
	}, compiled.ColumnMask)
	require.True(t, compiled.Matches(TypeColumnMask, `{"field":"ratings.email","columns":["ratings.phone"],"mask":"redact"}`))
	require.False(t, compiled.Matches(TypeColumnMask, `{"field":"ratings.email","mask":"zero"}`))
}

func TestCompileRejectsNonCanonicalNestedIdentityLiterals(t *testing.T) {
	for name, expression := range map[string]string{
		"field":    `{"field":" ratings.country ","operator":"equals","value":"DK"}`,
		"fact":     `{"filters":[{"field":"ratings.country","fact":" ratings ","operator":"equals","values":["DK"]}]}`,
		"operator": `{"field":"ratings.country","operator":" equals ","value":"DK"}`,
		"spatial":  `{"filters":[{"spatial":{"kind":"radius","latitudeField":" ratings.latitude ","longitudeField":"ratings.longitude","center":{"longitude":12.5,"latitude":55.7},"radiusMeters":1000}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Compile("noncanonical", TypeRowFilter, expression)
			require.Error(t, err)
		})
	}
}

func TestCompileRejectsUnsafePolicyExpressions(t *testing.T) {
	tests := []struct {
		name       string
		policyType string
		expression string
	}{
		{name: "invalid json", policyType: TypeRowFilter, expression: `{`},
		{name: "unknown property", policyType: TypeRowFilter, expression: `{"field":"region","value":"EU","op":"="}`},
		{name: "ambiguous row form", policyType: TypeRowFilter, expression: `{"field":"region","value":"EU","filters":[{"field":"country","value":"DK"}]}`},
		{name: "allow all plus filter", policyType: TypeRowFilter, expression: `{"allowAll":true,"field":"region","value":"EU"}`},
		{name: "empty row", policyType: TypeRowFilter, expression: `{}`},
		{name: "empty group", policyType: TypeRowFilter, expression: `{"filters":[{"groups":[]}]}`},
		{name: "empty in", policyType: TypeRowFilter, expression: `{"field":"region","operator":"in","values":[]}`},
		{name: "unsupported operator", policyType: TypeRowFilter, expression: `{"field":"region","operator":"regex","value":"EU"}`},
		{name: "invalid spatial bounds", policyType: TypeRowFilter, expression: `{"filters":[{"spatial":{"kind":"box","latitudeField":"latitude","longitudeField":"longitude","west":-181,"south":0,"east":1,"north":2}}]}`},
		{name: "invalid spatial lasso", policyType: TypeRowFilter, expression: `{"filters":[{"spatial":{"kind":"lasso","latitudeField":"latitude","longitudeField":"longitude","points":[{"longitude":0,"latitude":0},{"longitude":1,"latitude":1}]}}]}`},
		{name: "invalid spatial radius", policyType: TypeRowFilter, expression: `{"filters":[{"spatial":{"kind":"radius","latitudeField":"latitude","longitudeField":"longitude","center":{"longitude":0,"latitude":0},"radiusMeters":0}}]}`},
		{name: "mask allow all", policyType: TypeColumnMask, expression: `{"allowAll":true,"field":"email"}`},
		{name: "empty mask field", policyType: TypeColumnMask, expression: `{"columns":[""]}`},
		{name: "unsupported mask", policyType: TypeColumnMask, expression: `{"field":"email","mask":"hash"}`},
		{name: "unsupported policy type", policyType: "grant", expression: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile("unsafe-policy", tt.policyType, tt.expression)
			require.Error(t, err)
			require.ErrorContains(t, err, "unsafe-policy")
		})
	}
}
