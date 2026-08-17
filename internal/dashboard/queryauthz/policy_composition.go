package authz

import (
	"fmt"
	"sort"
	"strings"

	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

// dataPolicyComposition is the executable result of the policy algebra. Its
// filters are conjunctive at the top level; alternatives are represented by a
// single filter containing OR groups.
type dataPolicyComposition struct {
	Filters   []dataquery.Filter
	Masks     []columnMaskPolicy
	PolicyIDs []string
}

type rowPolicyGroup struct {
	filters []dataquery.Filter
}

type rowPolicyBoundary struct {
	global  rowPolicyGroup
	subject map[string]*rowPolicyGroup
}

type maskComposition struct {
	field     string
	mask      accesspolicy.Mask
	policyIDs []string
}

type columnMaskPolicy struct {
	PolicyIDs []string
	Fields    []string
	Mask      accesspolicy.Mask
}

// composeDataPolicies applies the authorization algebra before a query reaches
// any planner:
//   - policies for the same subject and protected object are ANDed;
//   - applicable subjects on the same protected object are ORed;
//   - global policies and different protected objects are ANDed; and
//   - candidate restrictions are always ANDed as a separate mandatory layer.
//
// Column masks are cumulative. Equivalent masks collapse, while contradictory
// masks fail closed instead of relying on repository or traversal order.
func composeDataPolicies(active, mandatory []accesssnapshot.DataPolicy) (dataPolicyComposition, error) {
	composition := dataPolicyComposition{}
	all := append(append([]accesssnapshot.DataPolicy(nil), active...), mandatory...)
	policyIDs := map[string]struct{}{}
	for _, policy := range all {
		if id := strings.TrimSpace(policy.ID); id != "" {
			policyIDs[id] = struct{}{}
		}
	}
	for id := range policyIDs {
		composition.PolicyIDs = append(composition.PolicyIDs, id)
	}
	sort.Strings(composition.PolicyIDs)

	boundaries := map[string]*rowPolicyBoundary{}
	for _, policy := range sortedPolicies(active) {
		switch policy.PolicyType {
		case "row_filter":
			clause, err := rowClauseFromPolicy(policy)
			if err != nil {
				return dataPolicyComposition{}, err
			}
			boundaryKey := policyBoundaryKey(policy)
			boundary := boundaries[boundaryKey]
			if boundary == nil {
				boundary = &rowPolicyBoundary{subject: map[string]*rowPolicyGroup{}}
				boundaries[boundaryKey] = boundary
			}
			if policy.Subject == nil {
				boundary.global.filters = append(boundary.global.filters, clause...)
				continue
			}
			groupKey := string(policy.Subject.Kind) + ":" + strings.TrimSpace(policy.Subject.ID)
			group := boundary.subject[groupKey]
			if group == nil {
				group = &rowPolicyGroup{}
				boundary.subject[groupKey] = group
			}
			group.filters = append(group.filters, clause...)
		case "column_mask":
		case "":
			return dataPolicyComposition{}, fmt.Errorf("data policy %q requires policy type", policy.ID)
		default:
			return dataPolicyComposition{}, fmt.Errorf("data policy %q has unsupported type %q", policy.ID, policy.PolicyType)
		}
	}

	boundaryKeys := make([]string, 0, len(boundaries))
	for key := range boundaries {
		boundaryKeys = append(boundaryKeys, key)
	}
	sort.Strings(boundaryKeys)
	for _, key := range boundaryKeys {
		boundary := boundaries[key]
		composition.Filters = append(composition.Filters, boundary.global.filters...)
		if len(boundary.subject) == 0 {
			continue
		}
		subjectKeys := make([]string, 0, len(boundary.subject))
		for subject := range boundary.subject {
			subjectKeys = append(subjectKeys, subject)
		}
		sort.Strings(subjectKeys)
		if len(subjectKeys) == 1 {
			composition.Filters = append(composition.Filters, boundary.subject[subjectKeys[0]].filters...)
			continue
		}
		groups := make([]dataquery.FilterGroup, 0, len(subjectKeys))
		allowAll := false
		for _, subject := range subjectKeys {
			filters := boundary.subject[subject].filters
			if len(filters) == 0 {
				allowAll = true
				break
			}
			groups = append(groups, dataquery.FilterGroup{Filters: filters})
		}
		if !allowAll {
			composition.Filters = append(composition.Filters, dataquery.Filter{Groups: groups})
		}
	}

	for _, policy := range sortedPolicies(mandatory) {
		switch policy.PolicyType {
		case "row_filter":
			clause, err := rowClauseFromPolicy(policy)
			if err != nil {
				return dataPolicyComposition{}, err
			}
			composition.Filters = append(composition.Filters, clause...)
		case "column_mask":
		case "":
			return dataPolicyComposition{}, fmt.Errorf("data policy %q requires policy type", policy.ID)
		default:
			return dataPolicyComposition{}, fmt.Errorf("data policy %q has unsupported type %q", policy.ID, policy.PolicyType)
		}
	}

	masks, err := composeColumnMasks(all)
	if err != nil {
		return dataPolicyComposition{}, err
	}
	composition.Masks = masks
	return composition, nil
}

func sortedPolicies(policies []accesssnapshot.DataPolicy) []accesssnapshot.DataPolicy {
	out := append([]accesssnapshot.DataPolicy(nil), policies...)
	sort.SliceStable(out, func(i, j int) bool {
		leftSubject, rightSubject := "", ""
		if out[i].Subject != nil {
			leftSubject = string(out[i].Subject.Kind) + "\x00" + out[i].Subject.ID
		}
		if out[j].Subject != nil {
			rightSubject = string(out[j].Subject.Kind) + "\x00" + out[j].Subject.ID
		}
		left := policyBoundaryKey(out[i]) + "\x00" + leftSubject + "\x00" + out[i].ID
		right := policyBoundaryKey(out[j]) + "\x00" + rightSubject + "\x00" + out[j].ID
		return left < right
	})
	return out
}

func policyBoundaryKey(policy accesssnapshot.DataPolicy) string {
	return policy.Resource.CanonicalID()
}

func rowClauseFromPolicy(policy accesssnapshot.DataPolicy) ([]dataquery.Filter, error) {
	if !policy.Compiled.Matches(policy.PolicyType, policy.ExpressionJSON) || policy.Compiled.Type != accesspolicy.TypeRowFilter || policy.Compiled.RowFilter == nil {
		return nil, fmt.Errorf("row_filter data policy %q is not compiled", policy.ID)
	}
	if policy.Compiled.RowFilter.AllowAll {
		return nil, nil
	}
	return dataQueryFilters(policy.Compiled.RowFilter.Filters), nil
}

func dataQueryFilters(filters []accesspolicy.Filter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		converted := dataquery.Filter{
			Field: filter.Field, Fact: filter.Fact, Operator: filter.Operator,
			Values: append([]any(nil), filter.Values...),
		}
		for _, group := range filter.Groups {
			converted.Groups = append(converted.Groups, dataquery.FilterGroup{Filters: dataQueryFilters(group.Filters)})
		}
		if spatial := filter.Spatial; spatial != nil {
			converted.Spatial = &dataquery.SpatialFilter{
				Kind: spatial.Kind, LatitudeField: spatial.LatitudeField, LongitudeField: spatial.LongitudeField,
				Fact: spatial.Fact, West: spatial.West, South: spatial.South, East: spatial.East, North: spatial.North,
				Center:       dataquery.SpatialPoint{Longitude: spatial.Center.Longitude, Latitude: spatial.Center.Latitude},
				RadiusMeters: spatial.RadiusMeters,
			}
			for _, point := range spatial.Points {
				converted.Spatial.Points = append(converted.Spatial.Points, dataquery.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude})
			}
		}
		out = append(out, converted)
	}
	return out
}

func composeColumnMasks(policies []accesssnapshot.DataPolicy) ([]columnMaskPolicy, error) {
	byField := map[string]*maskComposition{}
	for _, policy := range sortedPolicies(policies) {
		if policy.PolicyType != "column_mask" {
			continue
		}
		if !policy.Compiled.Matches(policy.PolicyType, policy.ExpressionJSON) || policy.Compiled.Type != accesspolicy.TypeColumnMask || policy.Compiled.ColumnMask == nil {
			return nil, fmt.Errorf("column_mask data policy %q is not compiled", policy.ID)
		}
		mask := policy.Compiled.ColumnMask
		for _, field := range mask.Fields {
			key := strings.ToLower(strings.TrimSpace(field))
			if key == "" {
				return nil, fmt.Errorf("column_mask data policy %q requires non-empty fields", policy.ID)
			}
			current := byField[key]
			if current == nil {
				byField[key] = &maskComposition{field: strings.TrimSpace(field), mask: mask.Mask, policyIDs: []string{policy.ID}}
				continue
			}
			current.policyIDs = append(current.policyIDs, policy.ID)
			if current.mask != mask.Mask {
				ids := uniqueSortedStrings(current.policyIDs)
				return nil, fmt.Errorf("column mask conflict for %q between policies %s", current.field, strings.Join(ids, ", "))
			}
		}
	}
	keys := make([]string, 0, len(byField))
	for key := range byField {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]columnMaskPolicy, 0, len(keys))
	for _, key := range keys {
		mask := byField[key]
		out = append(out, columnMaskPolicy{PolicyIDs: uniqueSortedStrings(mask.policyIDs), Fields: []string{mask.field}, Mask: mask.mask})
	}
	return out, nil
}

func selectedColumnMasks(request dataquery.Query, masks []columnMaskPolicy) ([]dataquery.ColumnMask, error) {
	resolved := map[string]*maskComposition{}
	for _, mask := range masks {
		fields := selectedMaskedFields(request, mask)
		if request.Kind == dataquery.KindSemanticAggregate || request.Kind == dataquery.KindSemanticSpatialTile || request.Kind == dataquery.KindSemanticSpatialTileBudget || request.Kind == dataquery.KindSemanticSpatialMetadata {
			fields = append(fields, mask.Fields...)
		}
		for _, field := range uniqueStrings(fields) {
			key := strings.ToLower(strings.TrimSpace(field))
			current := resolved[key]
			if current == nil {
				resolved[key] = &maskComposition{field: field, mask: mask.Mask, policyIDs: append([]string(nil), mask.PolicyIDs...)}
				continue
			}
			current.policyIDs = append(current.policyIDs, mask.PolicyIDs...)
			if current.mask != mask.Mask {
				return nil, fmt.Errorf("column mask conflict for selected field %q between policies %s", current.field, strings.Join(uniqueSortedStrings(current.policyIDs), ", "))
			}
		}
	}
	keys := make([]string, 0, len(resolved))
	for key := range resolved {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]dataquery.ColumnMask, 0, len(keys))
	for _, key := range keys {
		mask := resolved[key]
		out = append(out, dataquery.ColumnMask{Field: mask.field, Mask: string(mask.mask)})
	}
	return out, nil
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
