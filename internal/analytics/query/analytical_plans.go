package query

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// PlanHistogram compiles the complete histogram, including bounds, into one
// physical statement so execution does not require a row-oriented side query.
func (p *Planner) PlanHistogram(request RawValueRequest, binCount int) (Plan, error) {
	if binCount <= 0 {
		return Plan{}, fmt.Errorf("histogram bin count must be positive")
	}
	raw, err := p.PlanRawValues(request)
	if err != nil {
		return Plan{}, err
	}
	valueColumn := request.Metric.Alias
	if valueColumn == "" {
		valueColumn = "value"
	}
	if err := validatePlanAlias(valueColumn); err != nil {
		return Plan{}, err
	}
	valueType, err := p.analyticalValueType(request)
	if err != nil {
		return Plan{}, err
	}
	meta := spatialEnvelopeMeta(raw.IR, []string{"bucket", "count", "start", "end"}, "histogram")
	envelope := planir.AnalyticalEnvelope{NodeMeta: meta, Operation: planir.AnalyticalEnvelopeHistogram, Input: raw.IR.Output, Value: valueColumn, ValueType: valueType, BinCount: binCount}
	return renderAnalyticalEnvelopePlan(raw.IR, envelope, "histogram")
}

func (p *Planner) PlanDistribution(request RawValueRequest, sorts []Sort, limit int) (Plan, error) {
	raw, err := p.PlanRawValues(request)
	if err != nil {
		return Plan{}, err
	}
	valueColumn := request.Metric.Alias
	if valueColumn == "" {
		valueColumn = "value"
	}
	groupColumn := "label"
	if len(request.Dimensions) > 0 && request.Dimensions[0].Alias != "" {
		groupColumn = request.Dimensions[0].Alias
	}
	if err := validatePlanAlias(valueColumn); err != nil {
		return Plan{}, err
	}
	if err := validatePlanAlias(groupColumn); err != nil {
		return Plan{}, err
	}
	valueType, err := p.analyticalValueType(request)
	if err != nil {
		return Plan{}, err
	}
	if _, err := distributionPlanOrderBy(sorts); err != nil {
		return Plan{}, err
	}
	planSort := make([]planir.SortKey, 0, len(sorts))
	for _, sortSpec := range sorts {
		field := sortSpec.Field
		if field == "" {
			field = "label"
		}
		planSort = append(planSort, planir.SortKey{Field: field, Descending: strings.EqualFold(sortSpec.Direction, "desc")})
	}
	meta := spatialEnvelopeMeta(raw.IR, []string{"label", "min", "q1", "median", "q3", "max"}, "distribution")
	envelope := planir.AnalyticalEnvelope{NodeMeta: meta, Operation: planir.AnalyticalEnvelopeDistribution, Input: raw.IR.Output, Value: valueColumn, ValueType: valueType, Group: groupColumn, Sort: planSort, Limit: limit}
	return renderAnalyticalEnvelopePlan(raw.IR, envelope, "distribution")
}

func (p *Planner) analyticalValueType(request RawValueRequest) (string, error) {
	view, err := p.rawValueView(request)
	if err != nil {
		return "", err
	}
	_, metric, err := view.ResolveMetricRef(request.Metric.Field)
	if err != nil {
		return "", err
	}
	physical, err := p.resolveDimension(metric.InputField)
	if err != nil {
		return "", err
	}
	typ := strings.ToLower(string(physical.Datatype))
	switch typ {
	case "decimal", "integer", "float":
		return typ, nil
	default:
		return "", fmt.Errorf("analytical value %q has unsupported logical type %q", request.Metric.Field, typ)
	}
}

func validatePlanAlias(value string) error {
	if value == "" {
		return fmt.Errorf("empty column alias")
	}
	for index, r := range value {
		validLetter := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		if !validLetter && r != '_' && (index == 0 || r < '0' || r > '9') {
			return fmt.Errorf("invalid column alias %q", value)
		}
	}
	return nil
}

func distributionPlanOrderBy(sorts []Sort) (string, error) {
	if len(sorts) == 0 {
		return "label ASC", nil
	}
	parts := make([]string, 0, len(sorts))
	for _, sortSpec := range sorts {
		field := sortSpec.Field
		if field == "" {
			field = "label"
		}
		switch field {
		case "label", "min", "q1", "median", "q3", "max":
		default:
			return "", fmt.Errorf("unsupported distribution sort field %q", sortSpec.Field)
		}
		direction := "ASC"
		if strings.EqualFold(sortSpec.Direction, "desc") {
			direction = "DESC"
		} else if sortSpec.Direction != "" && !strings.EqualFold(sortSpec.Direction, "asc") {
			return "", fmt.Errorf("unsupported sort direction %q", sortSpec.Direction)
		}
		parts = append(parts, field+" "+direction)
	}
	return strings.Join(parts, ", "), nil
}
