package planir

import (
	"fmt"
	"strings"
)

func (r *duckRenderer) renderTotalRows(id string, n TotalRows) (string, []string, error) {
	sortNode, ok := as[SortLimit](r.graph.Nodes[n.Input])
	if !ok {
		return "", nil, fmt.Errorf("total-rows input %q is not a SortLimit", n.Input)
	}
	if n.CountOnly {
		query, _, err := r.renderSortLimit(n.Input, sortNode, "")
		if err != nil {
			return "", nil, err
		}
		return "SELECT COUNT(*) AS " + quoteName(n.TotalField) + " FROM " + quoteName(query), []string{n.TotalField}, nil
	}
	return r.renderSortLimit(id, sortNode, n.TotalField)
}

func (r *duckRenderer) renderSortLimit(id string, n SortLimit, totalField string) (string, []string, error) {
	var columns []string
	from := ""
	var source *sourceContext
	if _, aggregate := as[AggregateMetrics](r.graph.Nodes[n.Input]); !aggregate {
		if _, stitch := as[StitchAggregates](r.graph.Nodes[n.Input]); !stitch {
			if computed := isComputeSource(r.graph.Nodes[n.Input]); !computed {
				ctx, sourceErr := r.source(n.Input)
				if sourceErr != nil {
					return "", nil, sourceErr
				}
				from = ctx.from
				source = &ctx
				columns = nodeColumns(r.graph.Nodes[n.Input])
			}
		}
	}
	if source == nil {
		input, renderedColumns, renderErr := r.renderNode(n.Input)
		if renderErr != nil {
			return "", nil, renderErr
		}
		from = quoteName(input)
		columns = renderedColumns
	}
	selectSQL := "*"
	if len(n.Projection) > 0 {
		parts := make([]string, 0, len(n.Projection))
		for _, projection := range n.Projection {
			if err := validName(projection.Source); err != nil {
				return "", nil, fmt.Errorf("projection source %q: %w", projection.Source, err)
			}
			expr := quoteName(columnName(projection.Source))
			if source != nil {
				resolved, resolveErr := r.fieldExpr(projection.Source, *source)
				if resolveErr != nil {
					return "", nil, resolveErr
				}
				expr = resolved
			}
			if projection.Mask != "" {
				switch strings.ToLower(projection.Mask) {
				case "null":
					expr = "NULL"
				case "redact", "redacted":
					expr = "'REDACTED'"
				case "zero":
					expr = "0"
				default:
					return "", nil, fmt.Errorf("unsupported projection mask %q", projection.Mask)
				}
			}
			parts = append(parts, expr+" AS "+quoteName(columnName(projection.Name)))
		}
		selectSQL = strings.Join(parts, ", ")
		columns = projectionColumns(n.Projection)
	}
	if totalField != "" {
		if err := validUnqualifiedName(totalField); err != nil {
			return "", nil, fmt.Errorf("total field %q: %w", totalField, err)
		}
		if selectSQL == "*" {
			selectSQL = "*, COUNT(*) OVER () AS " + quoteName(totalField)
		} else {
			selectSQL += ", COUNT(*) OVER () AS " + quoteName(totalField)
		}
		columns = append(columns, totalField)
	}
	sql := "SELECT " + selectSQL + " FROM " + from
	if source != nil && len(source.where) > 0 {
		sql += " WHERE " + strings.Join(source.where, " AND ")
	}
	if len(n.Sort) > 0 {
		keys := make([]string, len(n.Sort))
		for i, key := range n.Sort {
			keys[i] = quoteName(columnName(key.Field))
			if key.Descending {
				keys[i] += " DESC"
			} else {
				keys[i] += " ASC"
			}
		}
		sql += " ORDER BY " + strings.Join(keys, ", ")
	}
	if n.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", n.Limit)
	}
	if n.Offset > 0 {
		sql += fmt.Sprintf(" OFFSET %d", n.Offset)
	}
	if id == r.graph.Output {
		return sql, columns, nil
	}
	name := r.cteName(id)
	r.ctes = append(r.ctes, name+" AS ("+sql+")")
	r.names[id] = name
	return name, columns, nil
}
