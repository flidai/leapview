package query

import (
	"fmt"
	"strings"
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
	lastBucket := binCount - 1
	sql := fmt.Sprintf(`WITH raw AS (%s),
bounds AS (
  SELECT MIN(%s) AS min_value, MAX(%s) AS max_value
  FROM raw
),
bucketed AS (
  SELECT CASE
    WHEN bounds.min_value = bounds.max_value THEN 0
    ELSE LEAST(%d, CAST(FLOOR(((raw.%s - bounds.min_value) / NULLIF(bounds.max_value - bounds.min_value, 0)) * %d) AS INTEGER))
  END AS bucket,
  bounds.min_value,
  bounds.max_value
  FROM raw CROSS JOIN bounds
)
SELECT bucket,
       COUNT(*) AS count,
       MIN(min_value) + bucket * ((MIN(max_value) - MIN(min_value)) / %d) AS start,
       CASE WHEN MIN(min_value) = MIN(max_value) THEN MIN(max_value)
            ELSE MIN(min_value) + (bucket + 1) * ((MIN(max_value) - MIN(min_value)) / %d)
       END AS end
FROM bucketed
GROUP BY bucket
ORDER BY bucket ASC`, raw.SQL, valueColumn, valueColumn, lastBucket, valueColumn, binCount, binCount, binCount)
	return Plan{SQL: sql, Args: raw.Args, Columns: []string{"bucket", "count", "start", "end"}}, nil
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
	orderBy, err := distributionPlanOrderBy(sorts)
	if err != nil {
		return Plan{}, err
	}
	sql := fmt.Sprintf(`WITH raw AS (%s)
SELECT %s AS label,
       MIN(%s) AS min,
       quantile_cont(%s, 0.25) AS q1,
       median(%s) AS median,
       quantile_cont(%s, 0.75) AS q3,
       MAX(%s) AS max
FROM raw
GROUP BY label
ORDER BY %s`, raw.SQL, groupColumn, valueColumn, valueColumn, valueColumn, valueColumn, valueColumn, orderBy)
	if limit > 0 {
		sql += fmt.Sprintf("\nLIMIT %d", limit)
	}
	return Plan{SQL: sql, Args: raw.Args, Columns: []string{"label", "min", "q1", "median", "q3", "max"}}, nil
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
