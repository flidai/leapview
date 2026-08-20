package planir

import (
	"fmt"
	"sort"
	"strings"
)

func (g *Graph) Explain() (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "PlanIR output=%s roots=%v\n", g.Output, sortedStrings(g.Roots))
	for _, id := range ids {
		n := g.Nodes[id]
		m := n.Meta()
		fmt.Fprintf(&b, "%s [%s] grain=%s phase=%s inputs=%v roots=%v fields=%v metrics=%v", id, n.Kind(), explainGrain(m.OutputGrain), m.FilterPhase, sortedStrings(n.Inputs()), sortedStrings(m.RootDatasets), explainFields(m.AvailableFields), explainMetrics(m.AvailableMetrics))
		switch value := n.(type) {
		case ScanDataset:
			fmt.Fprintf(&b, " dataset=%s", value.Dataset)
		case TraverseRelationship:
			fmt.Fprintf(&b, " path=%s(%s->%s)", value.Path.Name, value.Path.FromDataset, value.Path.ToDataset)
		case FilterRows:
			fmt.Fprintf(&b, " source=%s", value.Source)
			if value.Name != "" {
				fmt.Fprintf(&b, " name=%s", value.Name)
			}
			fmt.Fprintf(&b, " predicate=%s", predicateExplain(value.Predicate))
		case AggregateMetrics:
			fmt.Fprintf(&b, " group_by=%v", value.GroupBy)
			if value.Spatial != nil {
				fmt.Fprintf(&b, " spatial=%+v", *value.Spatial)
			}
			for _, metric := range canonicalMetricSpecs(value.Metrics) {
				for _, filter := range metric.Filters {
					fmt.Fprintf(&b, " filter=%s/%s:%s", filter.Source, filter.Name, filter.Phase)
				}
			}
		case StitchAggregates:
			fmt.Fprintf(&b, " keys=%v", value.Keys)
		case ComputeRatio:
			fmt.Fprintf(&b, " ratio=%s/%s as %s", value.Numerator, value.Denominator, value.Output)
		case ComputeDerived:
			fmt.Fprintf(&b, " derived=%s", value.Output)
		case SortLimit:
			fmt.Fprintf(&b, " sort=%v limit=%d offset=%d", value.Sort, value.Limit, value.Offset)
		case TotalRows:
			fmt.Fprintf(&b, " total_field=%s", value.TotalField)
		case BundleBranches:
			fmt.Fprintf(&b, " branches=%v", value.Branches)
		case SpatialEnvelope:
			fmt.Fprintf(&b, " operation=%s", value.Operation)
			if value.Latitude != "" || value.Longitude != "" {
				fmt.Fprintf(&b, " coordinates=%s/%s", value.Latitude, value.Longitude)
			}
		case AnalyticalEnvelope:
			fmt.Fprintf(&b, " operation=%s value=%s", value.Operation, value.Value)
			if value.NullPolicy != "" || value.Approximation != "" {
				fmt.Fprintf(&b, " null_policy=%s approximation=%s", value.NullPolicy, value.Approximation)
			}
			if value.DomainMinimum != nil || value.DomainMaximum != nil {
				fmt.Fprintf(&b, " domain=%v..%v", value.DomainMinimum, value.DomainMaximum)
			}
			if len(value.Quantiles) > 0 {
				fmt.Fprintf(&b, " quantiles=%v", value.Quantiles)
			}
			if value.WhiskerLower != nil || value.WhiskerUpper != nil {
				fmt.Fprintf(&b, " whiskers=%v..%v outliers=%s", value.WhiskerLower, value.WhiskerUpper, value.Outliers)
			}
			if len(value.DistributionColumns) > 0 {
				fmt.Fprintf(&b, " columns=%v", value.DistributionColumns)
			}
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func explainGrain(g Grain) string {
	if g.TimeGrain == "" {
		return fmt.Sprintf("(%s)", strings.Join(g.Fields, ","))
	}
	return fmt.Sprintf("(%s @ %s)", strings.Join(g.Fields, ","), g.TimeGrain)
}
func explainFields(fields []Field) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Type == "" {
			out = append(out, f.Name)
		} else {
			out = append(out, f.Name+":"+f.Type)
		}
	}
	sort.Strings(out)
	return out
}
func explainMetrics(metrics []Metric) []string {
	out := make([]string, 0, len(metrics))
	for _, m := range metrics {
		value := m.Name
		if m.Type != "" {
			value += ":" + m.Type
		}
		if m.Empty != "" {
			value += "[empty=" + m.Empty + "]"
		}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func predicateExplain(predicate Predicate) string {
	switch predicate.Kind {
	case PredicateCompare:
		return predicate.Field + " " + strings.ToUpper(predicate.Operator) + " " + literalExplain(predicate.Value)
	case PredicateIsNull:
		if predicate.Negated {
			return predicate.Field + " IS NOT NULL"
		}
		return predicate.Field + " IS NULL"
	case PredicateIn:
		values := make([]string, len(predicate.Values))
		for i, value := range predicate.Values {
			values[i] = literalExplain(value)
		}
		return predicate.Field + " IN (" + strings.Join(values, ", ") + ")"
	case PredicateAnd, PredicateOr:
		parts := make([]string, len(predicate.Children))
		for i, child := range predicate.Children {
			parts[i] = predicateExplain(child)
		}
		return "(" + strings.Join(parts, " "+strings.ToUpper(string(predicate.Kind))+" ") + ")"
	case PredicateNot:
		if len(predicate.Children) == 1 {
			return "NOT (" + predicateExplain(predicate.Children[0]) + ")"
		}
	case PredicateSpatial:
		if predicate.Spatial != nil {
			return predicate.Spatial.Kind + "(" + predicate.Spatial.Latitude + "," + predicate.Spatial.Longitude + ")"
		}
	}
	return "<invalid-predicate>"
}

func literalExplain(value Literal) string {
	switch value.Kind {
	case LiteralString:
		return fmt.Sprintf("%q", value.String)
	case LiteralNumber:
		return value.NumberText
	case LiteralBool:
		return fmt.Sprintf("%t", value.Bool)
	case LiteralNull:
		return "NULL"
	default:
		return "<invalid-literal>"
	}
}
