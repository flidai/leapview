package query

import (
	"fmt"
	"math"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// PlanHistogram compiles the complete histogram, including bounds, into one
// physical statement so execution does not require a row-oriented side query.
func (p *Planner) PlanHistogram(request RawValueRequest, binCount int, options ...HistogramOptions) (Plan, error) {
	if binCount <= 0 {
		return Plan{}, fmt.Errorf("histogram bin count must be positive")
	}
	if binCount > 100000 {
		return Plan{}, fmt.Errorf("histogram bin count must not exceed 100000")
	}
	histogram := HistogramOptions{NullPolicy: "omit", Approximation: "exact"}
	if len(options) > 1 {
		return Plan{}, fmt.Errorf("histogram accepts at most one options value")
	}
	if len(options) == 1 {
		histogram = options[0]
		if histogram.NullPolicy == "" {
			histogram.NullPolicy = "omit"
		}
		if histogram.Approximation == "" {
			histogram.Approximation = "exact"
		}
	}
	if histogram.NullPolicy != "omit" && histogram.NullPolicy != "include" {
		return Plan{}, fmt.Errorf("histogram null policy must be omit or include")
	}
	if histogram.Approximation != "exact" && histogram.Approximation != "approximate" {
		return Plan{}, fmt.Errorf("histogram approximation must be exact or approximate")
	}
	if histogram.Domain != nil && (!finiteFloat(histogram.Domain.Minimum) || !finiteFloat(histogram.Domain.Maximum) || histogram.Domain.Minimum >= histogram.Domain.Maximum) {
		return Plan{}, fmt.Errorf("histogram domain requires finite minimum less than maximum")
	}
	request.IncludeNull = histogram.NullPolicy == "include"
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
	envelope := planir.AnalyticalEnvelope{NodeMeta: meta, Operation: planir.AnalyticalEnvelopeHistogram, Input: raw.IR.Output, Value: valueColumn, ValueType: valueType, BinCount: binCount, NullPolicy: histogram.NullPolicy, Approximation: histogram.Approximation}
	if histogram.Domain != nil {
		envelope.DomainMinimum = &histogram.Domain.Minimum
		envelope.DomainMaximum = &histogram.Domain.Maximum
	}
	return renderAnalyticalEnvelopePlan(raw.IR, envelope, "histogram")
}

func (p *Planner) PlanDistribution(request RawValueRequest, sorts []Sort, limit int, options ...DistributionOptions) (Plan, error) {
	if limit < 0 {
		return Plan{}, fmt.Errorf("distribution limit cannot be negative")
	}
	distribution := DistributionOptions{Quantiles: []float64{0.25, 0.5, 0.75}, Outliers: "include", Approximation: "exact"}
	if len(options) > 1 {
		return Plan{}, fmt.Errorf("distribution accepts at most one options value")
	}
	if len(options) == 1 {
		distribution = options[0]
		if distribution.Outliers == "" {
			distribution.Outliers = "include"
		}
		if distribution.Approximation == "" {
			distribution.Approximation = "exact"
		}
	}
	if err := validateDistributionOptions(distribution); err != nil {
		return Plan{}, err
	}
	// Distribution statistics operate on numeric observations; null metric
	// inputs never form a quantile population and are deterministically omitted.
	request.IncludeNull = false
	raw, err := p.PlanRawValues(request)
	if err != nil {
		return Plan{}, err
	}
	valueColumn := request.Metric.Alias
	if valueColumn == "" {
		valueColumn = "value"
	}
	groupColumn := ""
	if len(request.Dimensions) > 0 {
		groupColumn = request.Dimensions[0].Alias
		if groupColumn == "" {
			groupColumn = request.Dimensions[0].Field
			if dot := strings.LastIndex(groupColumn, "."); dot >= 0 {
				groupColumn = groupColumn[dot+1:]
			}
		}
	}
	if err := validatePlanAlias(valueColumn); err != nil {
		return Plan{}, err
	}
	if groupColumn != "" {
		if err := validatePlanAlias(groupColumn); err != nil {
			return Plan{}, err
		}
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
	columns := distributionColumns(distribution.Quantiles)
	meta := spatialEnvelopeMeta(raw.IR, columns, "distribution")
	envelope := planir.AnalyticalEnvelope{NodeMeta: meta, Operation: planir.AnalyticalEnvelopeDistribution, Input: raw.IR.Output, Value: valueColumn, ValueType: valueType, Group: groupColumn, Sort: planSort, Limit: limit, Quantiles: append([]float64(nil), distribution.Quantiles...), Outliers: distribution.Outliers, Approximation: distribution.Approximation, DistributionColumns: columns}
	if distribution.Whiskers != nil {
		envelope.WhiskerLower = &distribution.Whiskers.Lower
		envelope.WhiskerUpper = &distribution.Whiskers.Upper
	}
	return renderAnalyticalEnvelopePlan(raw.IR, envelope, "distribution")
}

func finiteFloat(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validateDistributionOptions(options DistributionOptions) error {
	if len(options.Quantiles) == 0 {
		return fmt.Errorf("distribution requires at least one quantile")
	}
	previous := 0.0
	for index, quantile := range options.Quantiles {
		if !finiteFloat(quantile) || quantile <= 0 || quantile >= 1 {
			return fmt.Errorf("distribution quantile %d must be finite and strictly between 0 and 1", index)
		}
		if index > 0 && quantile <= previous {
			return fmt.Errorf("distribution quantiles must be strictly increasing and unique")
		}
		previous = quantile
	}
	if options.Whiskers != nil && (!finiteFloat(options.Whiskers.Lower) || !finiteFloat(options.Whiskers.Upper) || options.Whiskers.Lower <= 0 || options.Whiskers.Upper >= 1 || options.Whiskers.Lower >= options.Whiskers.Upper) {
		return fmt.Errorf("distribution whiskers require finite probabilities 0 < lower < upper < 1")
	}
	if options.Outliers == "omit" && options.Whiskers == nil {
		return fmt.Errorf("distribution outliers omit requires whiskers")
	}
	if options.Outliers == "include" && options.Whiskers != nil {
		return fmt.Errorf("distribution whiskers require outliers omit")
	}
	if options.Outliers != "omit" && options.Outliers != "include" {
		return fmt.Errorf("distribution outliers must be omit or include")
	}
	if options.Approximation != "exact" && options.Approximation != "approximate" {
		return fmt.Errorf("distribution approximation must be exact or approximate")
	}
	return nil
}

func distributionColumns(quantiles []float64) []string {
	columns := make([]string, 0, len(quantiles)+3)
	columns = append(columns, "label", "min")
	canonical := len(quantiles) == 3 && quantiles[0] == 0.25 && quantiles[1] == 0.5 && quantiles[2] == 0.75
	for index, quantile := range quantiles {
		name := fmt.Sprintf("q%d", index)
		// Preserve the established distribution result names for the canonical
		// quartiles while remaining deterministic for arbitrary quantile sets.
		switch {
		case canonical && quantile == 0.25:
			name = "q1"
		case canonical && quantile == 0.5:
			name = "median"
		case canonical && quantile == 0.75:
			name = "q3"
		}
		columns = append(columns, name)
	}
	return append(columns, "max")
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
