// Package policy compiles persisted access-policy expressions into the typed,
// immutable form consumed by query authorization.
package policy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	TypeRowFilter  = "row_filter"
	TypeColumnMask = "column_mask"
)

type Compiled struct {
	Type       string
	RowFilter  *RowFilter
	ColumnMask *ColumnMask
	sourceHash [sha256.Size]byte
}

// Clone returns a detached copy of the compiled policy. Compiled policies are
// retained by immutable authorization snapshots, but their exported filter
// trees contain pointers and slices; returning those values directly would let
// a caller mutate the installed snapshot through a getter. Keep the source
// hash as well so Clone preserves the same cache/validation semantics without
// requiring recompilation.
func (compiled Compiled) Clone() Compiled {
	clone := Compiled{Type: compiled.Type, sourceHash: compiled.sourceHash}
	if compiled.RowFilter != nil {
		row := RowFilter{AllowAll: compiled.RowFilter.AllowAll, Filters: cloneFilters(compiled.RowFilter.Filters)}
		clone.RowFilter = &row
	}
	if compiled.ColumnMask != nil {
		mask := ColumnMask{Fields: append([]string(nil), compiled.ColumnMask.Fields...), Mask: compiled.ColumnMask.Mask}
		clone.ColumnMask = &mask
	}
	return clone
}

func cloneFilters(input []Filter) []Filter {
	if input == nil {
		return nil
	}
	output := make([]Filter, len(input))
	for i, filter := range input {
		output[i] = Filter{
			Field: filter.Field, Fact: filter.Fact, Operator: filter.Operator,
			Values: append([]any(nil), filter.Values...),
			Groups: cloneFilterGroups(filter.Groups),
		}
		if filter.Spatial != nil {
			spatial := *filter.Spatial
			spatial.Points = append([]SpatialPoint(nil), filter.Spatial.Points...)
			output[i].Spatial = &spatial
		}
	}
	return output
}

func cloneFilterGroups(input []FilterGroup) []FilterGroup {
	if input == nil {
		return nil
	}
	output := make([]FilterGroup, len(input))
	for i, group := range input {
		output[i] = FilterGroup{Filters: cloneFilters(group.Filters)}
	}
	return output
}

type RowFilter struct {
	AllowAll bool
	Filters  []Filter
}

type ColumnMask struct {
	Fields []string
	Mask   Mask
}

type Filter struct {
	Field    string
	Fact     string
	Operator string
	Values   []any
	Groups   []FilterGroup
	Spatial  *SpatialFilter
}

type FilterGroup struct {
	Filters []Filter
}

type SpatialFilter struct {
	Kind           string
	LatitudeField  string
	LongitudeField string
	Fact           string
	West           float64
	South          float64
	East           float64
	North          float64
	Points         []SpatialPoint
	Center         SpatialPoint
	RadiusMeters   float64
}

type SpatialPoint struct {
	Longitude float64
	Latitude  float64
}

type Mask string

const (
	MaskNull   Mask = "null"
	MaskRedact Mask = "redact"
	MaskZero   Mask = "zero"
)

type expression struct {
	AllowAll bool     `json:"allowAll"`
	Field    string   `json:"field"`
	Columns  []string `json:"columns"`
	Operator string   `json:"operator"`
	Values   []any    `json:"values"`
	Value    any      `json:"value"`
	Filters  []Filter `json:"filters"`
	Mask     string   `json:"mask"`
}

func Compile(id, policyType, expressionJSON string) (Compiled, error) {
	if err := validateCanonicalLiteral(id, "policy id"); err != nil {
		return Compiled{}, err
	}
	if err := validateCanonicalLiteral(policyType, "policy type"); err != nil {
		return Compiled{}, err
	}
	var value expression
	decoder := json.NewDecoder(strings.NewReader(expressionJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Compiled{}, compileError(id, "expression is invalid: %v", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Compiled{}, compileError(id, "expression is invalid: %v", err)
	}
	switch policyType {
	case TypeRowFilter:
		row, err := compileRowFilter(id, value)
		if err != nil {
			return Compiled{}, err
		}
		return Compiled{Type: TypeRowFilter, RowFilter: &row, sourceHash: policySourceHash(policyType, expressionJSON)}, nil
	case TypeColumnMask:
		mask, err := compileColumnMask(id, value)
		if err != nil {
			return Compiled{}, err
		}
		return Compiled{Type: TypeColumnMask, ColumnMask: &mask, sourceHash: policySourceHash(policyType, expressionJSON)}, nil
	default:
		return Compiled{}, compileError(id, "has unsupported type %q", policyType)
	}
}

func (compiled Compiled) Matches(policyType, expressionJSON string) bool {
	return compiled.Type == policyType && compiled.sourceHash == policySourceHash(policyType, expressionJSON)
}

func policySourceHash(policyType, expressionJSON string) [sha256.Size]byte {
	return sha256.Sum256([]byte(policyType + "\x00" + expressionJSON))
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("contains multiple JSON values")
}

func compileRowFilter(id string, value expression) (RowFilter, error) {
	hasField := value.Field != ""
	hasFilters := len(value.Filters) > 0
	if value.AllowAll {
		if hasField || hasFilters {
			return RowFilter{}, compileError(id, "cannot combine allowAll with field or filters")
		}
		return RowFilter{AllowAll: true}, nil
	}
	if hasField && hasFilters {
		return RowFilter{}, compileError(id, "cannot combine field with filters")
	}
	filters := append([]Filter(nil), value.Filters...)
	if !hasFilters {
		if !hasField {
			return RowFilter{}, compileError(id, "requires field or filters")
		}
		operator := value.Operator
		if operator == "" {
			operator = "equals"
		}
		values := append([]any(nil), value.Values...)
		if len(values) == 0 && value.Value != nil {
			values = append(values, value.Value)
		}
		filters = []Filter{{Field: value.Field, Operator: operator, Values: values}}
	}
	for index := range filters {
		normalized, err := normalizeFilter(id, fmt.Sprintf("filters[%d]", index), filters[index])
		if err != nil {
			return RowFilter{}, err
		}
		filters[index] = normalized
	}
	return RowFilter{Filters: filters}, nil
}

func normalizeFilter(id, path string, filter Filter) (Filter, error) {
	for label, value := range map[string]string{"field": filter.Field, "fact": filter.Fact, "operator": filter.Operator} {
		if err := validateCanonicalLiteral(value, fmt.Sprintf("%s.%s", path, label)); err != nil {
			return Filter{}, compileError(id, "%v", err)
		}
	}
	hasField := filter.Field != ""
	hasGroups := len(filter.Groups) > 0
	hasSpatial := filter.Spatial != nil
	forms := 0
	for _, present := range []bool{hasField, hasGroups, hasSpatial} {
		if present {
			forms++
		}
	}
	if forms != 1 {
		return Filter{}, compileError(id, "%s must contain exactly one of field, groups, or spatial", path)
	}
	if hasGroups {
		if filter.Fact != "" || filter.Operator != "" || len(filter.Values) != 0 {
			return Filter{}, compileError(id, "%s group cannot contain fact, operator, or values", path)
		}
		groups := make([]FilterGroup, len(filter.Groups))
		for groupIndex, group := range filter.Groups {
			if len(group.Filters) == 0 {
				return Filter{}, compileError(id, "%s.groups[%d] requires filters", path, groupIndex)
			}
			children := make([]Filter, len(group.Filters))
			for filterIndex, child := range group.Filters {
				normalized, err := normalizeFilter(id, fmt.Sprintf("%s.groups[%d].filters[%d]", path, groupIndex, filterIndex), child)
				if err != nil {
					return Filter{}, err
				}
				children[filterIndex] = normalized
			}
			groups[groupIndex] = FilterGroup{Filters: children}
		}
		return Filter{Groups: groups}, nil
	}
	if hasSpatial {
		if filter.Fact != "" || filter.Operator != "" || len(filter.Values) != 0 {
			return Filter{}, compileError(id, "%s spatial filter cannot contain scalar fields", path)
		}
		spatial := *filter.Spatial
		for label, value := range map[string]string{"kind": spatial.Kind, "latitudeField": spatial.LatitudeField, "longitudeField": spatial.LongitudeField, "fact": spatial.Fact} {
			if err := validateCanonicalLiteral(value, fmt.Sprintf("%s.spatial.%s", path, label)); err != nil {
				return Filter{}, compileError(id, "%v", err)
			}
		}
		spatial.Points = append([]SpatialPoint(nil), spatial.Points...)
		if spatial.LatitudeField == "" || spatial.LongitudeField == "" {
			return Filter{}, compileError(id, "%s spatial filter requires coordinate fields", path)
		}
		if err := validateSpatialFilter(spatial); err != nil {
			return Filter{}, compileError(id, "%s spatial filter is invalid: %v", path, err)
		}
		return Filter{Spatial: &spatial}, nil
	}
	if err := validateScalarFilter(id, path, filter); err != nil {
		return Filter{}, err
	}
	if filter.Operator == "" {
		filter.Operator = "equals"
	}
	filter.Values = append([]any(nil), filter.Values...)
	return filter, nil
}

func validateScalarFilter(id, path string, filter Filter) error {
	operator := filter.Operator
	if operator == "" {
		operator = "equals"
	}
	want := 1
	switch operator {
	case "equals", "not_equals", "contains", "not_contains", "starts_with", "ends_with",
		"greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
	case "in", "not_in":
		if len(filter.Values) == 0 {
			return compileError(id, "%s %s requires at least one value", path, operator)
		}
		return nil
	case "is_null", "is_not_null":
		want = 0
	default:
		return compileError(id, "%s has unsupported operator %q", path, operator)
	}
	if len(filter.Values) != want {
		return compileError(id, "%s %s requires %d values", path, operator, want)
	}
	return nil
}

func validateSpatialFilter(filter SpatialFilter) error {
	switch filter.Kind {
	case "box":
		if !finite(filter.West) || !finite(filter.South) || !finite(filter.East) || !finite(filter.North) ||
			filter.West < -180 || filter.West > 180 || filter.East < -180 || filter.East > 180 ||
			filter.South < -90 || filter.South > 90 || filter.North < -90 || filter.North > 90 ||
			filter.South >= filter.North || filter.West == filter.East {
			return fmt.Errorf("invalid bounds")
		}
		return nil
	case "lasso":
		if len(filter.Points) < 3 || len(filter.Points) > 256 {
			return fmt.Errorf("lasso requires between 3 and 256 points")
		}
		west, east := math.Inf(1), math.Inf(-1)
		south, north := math.Inf(1), math.Inf(-1)
		for _, point := range filter.Points {
			if err := validateSpatialPoint(point); err != nil {
				return err
			}
			west, east = math.Min(west, point.Longitude), math.Max(east, point.Longitude)
			south, north = math.Min(south, point.Latitude), math.Max(north, point.Latitude)
		}
		if east-west >= 180 || south == north || west == east {
			return fmt.Errorf("lasso must enclose a non-zero area without crossing the antimeridian")
		}
		return nil
	case "radius":
		if err := validateSpatialPoint(filter.Center); err != nil {
			return err
		}
		if !finite(filter.RadiusMeters) || filter.RadiusMeters <= 0 || filter.RadiusMeters > 5_000_000 {
			return fmt.Errorf("radius must be greater than zero and at most 5000000 meters")
		}
		return nil
	default:
		return fmt.Errorf("unsupported kind %q", filter.Kind)
	}
}

func validateSpatialPoint(point SpatialPoint) error {
	if !finite(point.Longitude) || !finite(point.Latitude) || point.Longitude < -180 || point.Longitude > 180 || point.Latitude < -90 || point.Latitude > 90 {
		return fmt.Errorf("invalid coordinate")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func compileColumnMask(id string, value expression) (ColumnMask, error) {
	if value.AllowAll {
		return ColumnMask{}, compileError(id, "column mask cannot use allowAll")
	}
	fields := append([]string(nil), value.Columns...)
	if value.Field != "" {
		fields = append(fields, value.Field)
	}
	if len(fields) == 0 {
		return ColumnMask{}, compileError(id, "column mask requires field or columns")
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(fields))
	for _, field := range fields {
		if err := validateCanonicalLiteral(field, "column field"); err != nil {
			return ColumnMask{}, compileError(id, "%v", err)
		}
		if field == "" {
			return ColumnMask{}, compileError(id, "column mask requires non-empty fields")
		}
		key := field
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, field)
	}
	mask, err := compileMask(value.Mask)
	if err != nil {
		return ColumnMask{}, compileError(id, "%v", err)
	}
	return ColumnMask{Fields: normalized, Mask: mask}, nil
}

func compileMask(value string) (Mask, error) {
	switch value {
	case "", string(MaskNull):
		return MaskNull, nil
	case string(MaskRedact):
		return MaskRedact, nil
	case string(MaskZero):
		return MaskZero, nil
	default:
		return "", fmt.Errorf("unsupported column mask %q", value)
	}
}

func validateCanonicalLiteral(value, label string) error {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%s must use its canonical spelling", label)
	}
	return nil
}

func compileError(id, format string, args ...any) error {
	return fmt.Errorf("data policy %q %s", id, fmt.Sprintf(format, args...))
}
