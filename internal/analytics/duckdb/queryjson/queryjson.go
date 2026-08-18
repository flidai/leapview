package queryjson

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
)

type SQLAnalysis struct {
	SourceRefs                []string
	ModelRefs                 []string
	QualifiedSourceColumnRefs []string
	CTEs                      []string
	Aliases                   map[string]TableRef
	TableRefs                 []TableRef
}

// AnalyzeSQLText is the canonical authored-SQL parser boundary. It always
// serializes through the pinned DuckDB JSON extension on an isolated in-memory
// connection before the closed AST visitor accepts the query.
func AnalyzeSQLText(ctx context.Context, sqlText string) (SQLAnalysis, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return SQLAnalysis{}, fmt.Errorf("open SQL analysis database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET enable_external_access = false",
		// The JSON extension is pinned and linked into the LeapView DuckDB
		// build. Load it only after the analysis sandbox is closed to external
		// access and automatic extension resolution is disabled.
		"LOAD json",
		"SET lock_configuration = true",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return SQLAnalysis{}, fmt.Errorf("configure SQL analysis database: %w", err)
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT CAST(json_serialize_sql(CAST(? AS VARCHAR), skip_default := true, skip_empty := true, skip_null := true) AS VARCHAR)", sqlText)
	if err != nil {
		return SQLAnalysis{}, fmt.Errorf("serialize SQL: %w", err)
	}
	defer rows.Close()
	payload, err := singleStringResult(rows, "json_serialize_sql")
	if err != nil {
		return SQLAnalysis{}, err
	}
	return AnalyzeSQL([]byte(payload))
}

type TableRef struct {
	Schema        string
	Table         string
	Alias         string
	QueryLocation int
}

type ExplainAnalysis struct {
	Scans []Scan
}

type Scan struct {
	Operator    string
	Catalog     string
	Schema      string
	Table       string
	Projections []string
}

func AnalyzeSQL(input []byte) (SQLAnalysis, error) {
	var root any
	if err := json.Unmarshal(input, &root); err != nil {
		return SQLAnalysis{}, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return SQLAnalysis{}, fmt.Errorf("duckdb SQL JSON root must be an object")
	}
	if err := requireKeys(object, "error", "statements", "error_type", "error_message"); err != nil {
		return SQLAnalysis{}, err
	}
	errorValue, ok := object["error"].(bool)
	if !ok {
		return SQLAnalysis{}, fmt.Errorf("DuckDB SQL JSON error must be a boolean")
	}
	if raw, present := object["error_type"]; present && raw != nil {
		if _, ok := raw.(string); !ok {
			return SQLAnalysis{}, fmt.Errorf("DuckDB SQL JSON error_type must be a string")
		}
	}
	if raw, present := object["error_message"]; present && raw != nil {
		if _, ok := raw.(string); !ok {
			return SQLAnalysis{}, fmt.Errorf("DuckDB SQL JSON error_message must be a string")
		}
	}
	if errorValue {
		message, _ := object["error_message"].(string)
		if message == "" {
			message = "unknown error"
		}
		return SQLAnalysis{}, fmt.Errorf("duckdb SQL JSON error: %s", message)
	}
	statements, ok := object["statements"].([]any)
	if !ok || len(statements) != 1 {
		return SQLAnalysis{}, fmt.Errorf("model SQL must contain exactly one SELECT statement")
	}
	analysis := SQLAnalysis{Aliases: map[string]TableRef{}}
	visitor := astVisitor{analysis: &analysis, sourceRefs: map[string]struct{}{}, modelRefs: map[string]struct{}{}, qualifiedSourceColumnRefs: map[string]struct{}{}, ctes: map[string]struct{}{}, cteRefs: map[string]struct{}{}}
	statement, ok := statements[0].(map[string]any)
	if !ok {
		return SQLAnalysis{}, fmt.Errorf("DuckDB SQL statement must be an object")
	}
	if err := requireKeys(statement, "node"); err != nil {
		return SQLAnalysis{}, err
	}
	node, ok := statement["node"].(map[string]any)
	if !ok {
		return SQLAnalysis{}, fmt.Errorf("DuckDB SQL statement has no SELECT node")
	}
	if err := visitor.selectNode(node); err != nil {
		return SQLAnalysis{}, err
	}
	analysis.SourceRefs = sortedSet(visitor.sourceRefs)
	analysis.ModelRefs = sortedSet(visitor.modelRefs)
	analysis.QualifiedSourceColumnRefs = sortedSet(visitor.qualifiedSourceColumnRefs)
	analysis.CTEs = sortedSet(visitor.cteRefs)
	return analysis, nil
}

func AnalyzeExplain(input []byte) (ExplainAnalysis, error) {
	var roots []planNode
	if err := json.Unmarshal(input, &roots); err != nil {
		return ExplainAnalysis{}, err
	}
	analysis := ExplainAnalysis{}
	for _, root := range roots {
		walkPlan(root, &analysis)
	}
	return analysis, nil
}

type astVisitor struct {
	analysis                  *SQLAnalysis
	sourceRefs, modelRefs     map[string]struct{}
	qualifiedSourceColumnRefs map[string]struct{}
	ctes                      map[string]struct{}
	cteRefs                   map[string]struct{}
}

func (v *astVisitor) selectNode(node map[string]any) error {
	typ, _ := node["type"].(string)
	switch typ {
	case "SELECT_NODE":
		if _, ok := node["select_list"]; !ok {
			return fmt.Errorf("SELECT_NODE is missing select_list")
		}
		if _, ok := node["from_table"]; !ok {
			return fmt.Errorf("SELECT_NODE is missing from_table")
		}
		if err := requireKeys(node, "type", "modifiers", "cte_map", "select_list", "from_table", "where_clause", "group_expressions", "group_sets", "aggregate_handling", "having", "sample", "qualify", "window_definitions"); err != nil {
			return err
		}
		if raw, present := node["cte_map"]; present && raw != nil {
			if _, ok := raw.(map[string]any); !ok {
				return fmt.Errorf("SELECT_NODE cte_map must be an object")
			}
		}
		previousCTEs := v.ctes
		localCTEs := make(map[string]struct{}, len(previousCTEs))
		for name := range previousCTEs {
			localCTEs[name] = struct{}{}
		}
		v.ctes = localCTEs
		defer func() { v.ctes = previousCTEs }()
		if cteMap, ok := node["cte_map"].(map[string]any); ok {
			if err := v.cteMap(cteMap); err != nil {
				return err
			}
		}
		if list, ok := node["select_list"].([]any); ok {
			for _, item := range list {
				if err := v.expressionObject(item); err != nil {
					return err
				}
			}
		} else if node["select_list"] != nil {
			return fmt.Errorf("SELECT_NODE select_list must be an array")
		}
		if from, ok := node["from_table"].(map[string]any); ok {
			if err := v.relation(from); err != nil {
				return err
			}
		} else if node["from_table"] != nil {
			return fmt.Errorf("SELECT_NODE from_table must be an object")
		}
		for _, key := range []string{"where_clause", "having", "qualify"} {
			if value, ok := node[key].(map[string]any); ok {
				if err := v.expression(value); err != nil {
					return err
				}
			} else if node[key] != nil {
				return fmt.Errorf("SELECT_NODE %s must be an expression", key)
			}
		}
		if groups, ok := node["group_expressions"].([]any); ok {
			for _, item := range groups {
				if err := v.expressionObject(item); err != nil {
					return err
				}
			}
		} else if node["group_expressions"] != nil {
			return fmt.Errorf("SELECT_NODE group_expressions must be an array")
		}
		if raw, present := node["group_sets"]; present && raw != nil {
			sets, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("SELECT_NODE group_sets must be an array")
			}
			for _, rawSet := range sets {
				set, ok := rawSet.([]any)
				if !ok {
					return fmt.Errorf("SELECT_NODE group_sets entry must be an array")
				}
				for _, item := range set {
					if !nonNegativeInteger(item) {
						return fmt.Errorf("SELECT_NODE group_sets entries must be non-negative integer indexes")
					}
				}
			}
		}
		if raw, present := node["aggregate_handling"]; present && raw != nil {
			handling, ok := raw.(string)
			if !ok || !validAggregateHandling(handling) {
				return fmt.Errorf("SELECT_NODE aggregate_handling has an unknown mode")
			}
		}
		if raw, present := node["sample"]; present && raw != nil {
			return fmt.Errorf("SELECT_NODE samples are not supported in model SQL")
		}
		if raw, present := node["window_definitions"]; present && raw != nil {
			definitions, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("SELECT_NODE window_definitions must be an array")
			}
			seenNames := map[string]struct{}{}
			for _, rawDefinition := range definitions {
				definition, ok := rawDefinition.(map[string]any)
				if !ok {
					return fmt.Errorf("SELECT_NODE window definition must be an object")
				}
				if err := requireKeys(definition, "name", "expression"); err != nil {
					return err
				}
				name, ok := definition["name"].(string)
				if !ok || !validIdentifier(name) {
					return fmt.Errorf("SELECT_NODE window definition name must be a string")
				}
				if _, duplicate := seenNames[strings.ToLower(name)]; duplicate {
					return fmt.Errorf("SELECT_NODE window definition name %q is duplicated", name)
				}
				seenNames[strings.ToLower(name)] = struct{}{}
				rawExpression, present := definition["expression"]
				if !present || rawExpression == nil {
					return fmt.Errorf("SELECT_NODE window definition expression is required")
				}
				expression, ok := rawExpression.(map[string]any)
				if !ok {
					return fmt.Errorf("SELECT_NODE window definition expression must be an object")
				}
				if err := v.expression(expression); err != nil {
					return err
				}
			}
		}
		if modifiers, ok := node["modifiers"].([]any); ok {
			for _, item := range modifiers {
				if err := v.modifier(item); err != nil {
					return err
				}
			}
		} else if node["modifiers"] != nil {
			return fmt.Errorf("SELECT_NODE modifiers must be an array")
		}
		return nil
	case "SET_OPERATION_NODE":
		if err := requireKeys(node, "type", "setop_type", "setop_all", "left", "right"); err != nil {
			return err
		}
		setopType, ok := node["setop_type"].(string)
		if !ok || setopType != "UNION" {
			return fmt.Errorf("set operation %q is not allowed in model SQL", setopType)
		}
		if rawAll, ok := node["setop_all"]; ok {
			if _, ok := rawAll.(bool); !ok {
				return fmt.Errorf("SET_OPERATION_NODE setop_all must be a boolean")
			}
		}
		for _, key := range []string{"left", "right"} {
			child, ok := node[key].(map[string]any)
			if !ok {
				return fmt.Errorf("SET_OPERATION_NODE %s must be a SELECT or set operation node", key)
			}
			if err := v.selectNode(child); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported DuckDB SQL AST node %q", typ)
	}
}

func (v *astVisitor) cteMap(value map[string]any) error {
	if err := requireKeys(value, "map"); err != nil {
		return err
	}
	entries, ok := value["map"].([]any)
	if !ok && value["map"] != nil {
		return fmt.Errorf("CTE map must be an array")
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("CTE map entry must be an object")
		}
		if err := requireKeys(entry, "key", "value"); err != nil {
			return err
		}
		name, ok := entry["key"].(string)
		if !ok || !validIdentifier(name) {
			return fmt.Errorf("invalid CTE name")
		}
		value, ok := entry["value"].(map[string]any)
		if !ok {
			return fmt.Errorf("CTE %q value must be an object", name)
		}
		if err := requireKeys(value, "query", "materialized"); err != nil {
			return err
		}
		if raw, present := value["materialized"]; present && raw != nil {
			materialized, ok := raw.(string)
			if !ok || !validMaterializedMode(materialized) {
				return fmt.Errorf("CTE %q materialized has an unknown mode", name)
			}
		}
		query, ok := value["query"].(map[string]any)
		if !ok {
			return fmt.Errorf("CTE %q query must be an object", name)
		}
		if err := requireKeys(query, "node"); err != nil {
			return err
		}
		node, ok := query["node"].(map[string]any)
		if !ok {
			return fmt.Errorf("CTE %q query has no node", name)
		}
		if err := v.selectNode(node); err != nil {
			return err
		}
		// A CTE is visible to later siblings and the enclosing SELECT, but not
		// to its own query (recursive CTEs are outside the initial profile).
		v.ctes[strings.ToLower(name)] = struct{}{}
		v.cteRefs[strings.ToLower(name)] = struct{}{}
	}
	return nil
}

func (v *astVisitor) relation(obj map[string]any) error {
	typ, _ := obj["type"].(string)
	switch typ {
	case "BASE_TABLE":
		if _, ok := obj["table_name"]; !ok {
			return fmt.Errorf("BASE_TABLE is missing table_name")
		}
		if err := requireKeys(obj, "type", "alias", "query_location", "schema_name", "table_name", "catalog_name", "column_name_alias", "sample"); err != nil {
			return err
		}
		for _, key := range []string{"alias", "schema_name", "table_name", "catalog_name"} {
			if raw, present := obj[key]; present && raw != nil {
				if _, ok := raw.(string); !ok {
					return fmt.Errorf("BASE_TABLE %s must be a string", key)
				}
			}
		}
		if err := validateOptionalQueryLocation(obj, "BASE_TABLE"); err != nil {
			return err
		}
		if raw, present := obj["column_name_alias"]; present && raw != nil {
			aliases, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("BASE_TABLE column_name_alias must be an array")
			}
			for _, alias := range aliases {
				name, ok := alias.(string)
				if !ok || !validIdentifier(name) {
					return fmt.Errorf("BASE_TABLE column_name_alias entries must be strings")
				}
			}
		}
		if raw, present := obj["sample"]; present && raw != nil {
			return fmt.Errorf("BASE_TABLE samples are not supported in model SQL")
		}
		schema, _ := obj["schema_name"].(string)
		table, _ := obj["table_name"].(string)
		catalog, _ := obj["catalog_name"].(string)
		alias, _ := obj["alias"].(string)
		if catalog != "" {
			return fmt.Errorf("external catalog %q is not allowed in model SQL", catalog)
		}
		if !validRelationName(table) {
			return fmt.Errorf("relation %q is not a governed identifier", table)
		}
		if alias != "" && !validIdentifier(alias) {
			return fmt.Errorf("relation alias %q is invalid", alias)
		}
		ref := TableRef{Schema: schema, Table: table, Alias: alias, QueryLocation: queryLocation(obj["query_location"])}
		switch strings.ToLower(schema) {
		case "source":
			v.sourceRefs[table] = struct{}{}
		case "model":
			v.modelRefs[table] = struct{}{}
		case "raw":
			return fmt.Errorf("raw namespace relations are not allowed in model SQL")
		case "":
			if _, ok := v.ctes[strings.ToLower(table)]; !ok {
				return fmt.Errorf("unqualified relation %q is not governed", table)
			}
		default:
			return fmt.Errorf("relation schema %q is not governed", schema)
		}
		v.analysis.TableRefs = append(v.analysis.TableRefs, ref)
		if alias != "" {
			v.analysis.Aliases[alias] = ref
		}
		return nil
	case "EMPTY":
		return requireKeys(obj, "type")
	case "JOIN":
		if err := requireKeys(obj, "type", "query_location", "left", "right", "condition", "join_type", "ref_type", "using_columns"); err != nil {
			return err
		}
		if err := validateOptionalQueryLocation(obj, "JOIN"); err != nil {
			return err
		}
		joinType, ok := optionalString(obj, "join_type")
		if !ok || (joinType != "" && !allowedJoinType(joinType)) {
			return fmt.Errorf("JOIN join_type must be a known DuckDB join type")
		}
		refType, ok := optionalString(obj, "ref_type")
		if !ok || (refType != "" && !allowedJoinRefType(refType)) {
			return fmt.Errorf("JOIN ref_type must be a known DuckDB join reference type")
		}
		if raw, ok := obj["using_columns"]; ok {
			if raw == nil {
				return fmt.Errorf("JOIN using_columns must be an array")
			}
			columns, ok := raw.([]any)
			if !ok || len(columns) == 0 {
				return fmt.Errorf("JOIN using_columns must be a non-empty array")
			}
			seen := make(map[string]struct{}, len(columns))
			for _, rawColumn := range columns {
				column, ok := rawColumn.(string)
				if !ok || !validIdentifier(column) {
					return fmt.Errorf("JOIN using_columns must contain valid identifiers")
				}
				if _, duplicate := seen[strings.ToLower(column)]; duplicate {
					return fmt.Errorf("JOIN using_columns contains duplicate column %q", column)
				}
				seen[strings.ToLower(column)] = struct{}{}
			}
		}
		for _, key := range []string{"left", "right"} {
			child, ok := obj[key].(map[string]any)
			if !ok {
				return fmt.Errorf("JOIN %s must be a relation", key)
			}
			if err := v.relation(child); err != nil {
				return err
			}
		}
		if condition, ok := obj["condition"].(map[string]any); ok {
			return v.expression(condition)
		}
		if obj["condition"] != nil {
			return fmt.Errorf("JOIN condition must be an expression")
		}
		return nil
	case "SUBQUERY":
		if err := requireKeys(obj, "type", "alias", "query_location", "subquery", "column_name_alias"); err != nil {
			return err
		}
		if err := validateOptionalQueryLocation(obj, "SUBQUERY"); err != nil {
			return err
		}
		if raw, present := obj["alias"]; present && raw != nil {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("SUBQUERY alias must be a string")
			}
		}
		if alias, _ := obj["alias"].(string); alias != "" && !validIdentifier(alias) {
			return fmt.Errorf("subquery alias %q is invalid", alias)
		}
		if raw, present := obj["column_name_alias"]; present && raw != nil {
			aliases, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("SUBQUERY column_name_alias must be an array")
			}
			for _, alias := range aliases {
				name, ok := alias.(string)
				if !ok || !validIdentifier(name) {
					return fmt.Errorf("SUBQUERY column_name_alias entries must be strings")
				}
			}
		}
		query, ok := obj["subquery"].(map[string]any)
		if !ok {
			return fmt.Errorf("subquery has no query")
		}
		if err := requireKeys(query, "node"); err != nil {
			return err
		}
		node, ok := query["node"].(map[string]any)
		if !ok {
			return fmt.Errorf("subquery has no node")
		}
		return v.selectNode(node)
	case "EXPRESSION_LIST":
		if err := requireKeys(obj, "type", "alias", "values"); err != nil {
			return err
		}
		values, ok := obj["values"].([]any)
		if !ok {
			return fmt.Errorf("expression list values must be an array")
		}
		for _, row := range values {
			items, ok := row.([]any)
			if !ok {
				return fmt.Errorf("expression list row must be an array")
			}
			for _, item := range items {
				if err := v.expressionObject(item); err != nil {
					return err
				}
			}
		}
		return nil
	case "PIVOT":
		return fmt.Errorf("PIVOT relations are not supported in model SQL")
		/*
			if err := requireKeys(obj, "type", "query_location", "source", "aggregates", "pivots"); err != nil {
				return err
			}
			if err := validateOptionalQueryLocation(obj, "PIVOT"); err != nil {
				return err
			}
			source, ok := obj["source"].(map[string]any)
			if !ok {
				return fmt.Errorf("pivot source must be a relation")
			}
			if err := v.relation(source); err != nil {
				return err
			}
			if raw, present := obj["aggregates"]; present && raw != nil {
				values, ok := raw.([]any)
				if !ok {
					return fmt.Errorf("pivot aggregates must be an array")
				}
				for _, item := range values {
					if err := v.expressionObject(item); err != nil {
						return err
					}
				}
			}
			if raw, present := obj["pivots"]; present && raw != nil {
				pivots, ok := raw.([]any)
				if !ok {
					return fmt.Errorf("pivot pivots must be an array")
				}
				for _, item := range pivots {
					p, ok := item.(map[string]any)
					if !ok {
						return fmt.Errorf("pivot entry must be an object")
					}
					if err := requireKeys(p, "pivot_expressions", "entries"); err != nil {
						return err
					}
					exprs, ok := p["pivot_expressions"].([]any)
					if !ok {
						return fmt.Errorf("pivot pivot_expressions must be an array")
					}
					for _, expr := range exprs {
						if err := v.expressionObject(expr); err != nil {
							return err
						}
					}
					if entries, ok := p["entries"].([]any); !ok {
						return fmt.Errorf("pivot entries must be an array")
					} else {
						for _, entry := range entries {
							if _, ok := entry.(string); !ok {
								return fmt.Errorf("pivot entries must contain strings")
							}
						}
					}
				}
			}
			return nil
		*/
	case "TABLE_FUNCTION":
		return fmt.Errorf("table functions are not allowed in model SQL")
	default:
		return fmt.Errorf("unsupported DuckDB relation node %q", typ)
	}
}

func (v *astVisitor) expressionObject(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("SQL expression must be an object")
	}
	return v.expression(obj)
}

func (v *astVisitor) expression(obj map[string]any) error {
	class, _ := obj["class"].(string)
	typ, _ := obj["type"].(string)
	if class == "" {
		return fmt.Errorf("SQL expression is missing class")
	}
	if typ == "" {
		return fmt.Errorf("SQL expression class %q is missing type", class)
	}
	if !validExpressionType(class, typ) {
		return fmt.Errorf("SQL expression class %q does not allow type %q", class, typ)
	}
	allowed := map[string][]string{
		"CONSTANT": {"class", "type", "alias", "query_location", "value"}, "COLUMN_REF": {"class", "type", "alias", "query_location", "column_names"}, "STAR": {"class", "type", "alias", "query_location"},
		"FUNCTION": {"class", "type", "alias", "query_location", "function_name", "schema", "children", "filter", "order_bys", "distinct", "is_operator", "export_state", "catalog"}, "OPERATOR": {"class", "type", "alias", "query_location", "children"}, "COMPARISON": {"class", "type", "alias", "query_location", "left", "right"}, "CAST": {"class", "type", "alias", "query_location", "child", "cast_type", "try_cast"}, "CASE": {"class", "type", "alias", "query_location", "case_checks", "else_expr"}, "WINDOW": {"class", "type", "alias", "query_location", "function_name", "partitions", "orders", "start", "end", "children", "filter"}, "SUBQUERY": {"class", "type", "query_location", "subquery"}, "BETWEEN": {"class", "type", "alias", "query_location", "input", "lower", "upper"}, "CONJUNCTION": {"class", "type", "alias", "query_location", "children"},
	}
	keys, ok := allowed[class]
	if !ok {
		return fmt.Errorf("unsupported DuckDB SQL expression class %q", class)
	}
	if err := requireKeys(obj, keys...); err != nil {
		return err
	}
	if raw, present := obj["alias"]; present && raw != nil {
		if _, ok := raw.(string); !ok {
			return fmt.Errorf("%s alias must be a string", class)
		}
	}
	if err := validateOptionalQueryLocation(obj, class); err != nil {
		return err
	}
	if alias, _ := obj["alias"].(string); alias != "" && !validIdentifier(alias) {
		return fmt.Errorf("expression alias %q is invalid", alias)
	}
	switch class {
	case "COLUMN_REF":
		if _, ok := obj["column_names"]; !ok {
			return fmt.Errorf("COLUMN_REF is missing column_names")
		}
		names := stringArray(obj["column_names"])
		if len(names) == 0 || len(names) > 3 {
			return fmt.Errorf("column reference must contain one to three identifiers")
		}
		if len(names) == 3 && strings.EqualFold(names[0], "source") {
			v.qualifiedSourceColumnRefs[strings.Join(names[:3], ".")] = struct{}{}
		}
		if len(names) == 2 && strings.EqualFold(names[0], "source") { /* aliases are required for source columns */
		}
	case "FUNCTION":
		if _, ok := obj["function_name"]; !ok {
			return fmt.Errorf("FUNCTION is missing function_name")
		}
		name, ok := obj["function_name"].(string)
		if !ok {
			return fmt.Errorf("FUNCTION function_name must be a string")
		}
		if !approvedFunction(name) {
			return fmt.Errorf("function %q is not allowed in model SQL", name)
		}
		if raw, present := obj["schema"]; present && raw != nil {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("FUNCTION schema must be a string")
			}
		}
		if schema, _ := obj["schema"].(string); schema != "" && !strings.EqualFold(schema, "main") {
			return fmt.Errorf("qualified function %q is not allowed", name)
		}
		if raw, present := obj["catalog"]; present && raw != nil {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("FUNCTION catalog must be a string")
			}
		}
		if catalog, _ := obj["catalog"].(string); catalog != "" {
			return fmt.Errorf("external function catalog is not allowed")
		}
		for _, key := range []string{"distinct", "is_operator", "export_state"} {
			if value, present := obj[key]; present && value != nil {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("FUNCTION %s must be a boolean", key)
				}
			}
		}
		children, ok := optionalArray(obj, "children")
		if !ok {
			return fmt.Errorf("FUNCTION children must be an array")
		}
		if children != nil {
			for _, child := range children {
				if err := v.expressionObject(child); err != nil {
					return err
				}
			}
		}
		filter, ok := optionalObject(obj, "filter")
		if !ok {
			return fmt.Errorf("FUNCTION filter must be an expression")
		}
		if filter != nil {
			if err := v.expression(filter); err != nil {
				return err
			}
		}
		order, ok := optionalObject(obj, "order_bys")
		if !ok {
			return fmt.Errorf("FUNCTION order_bys must be an order modifier")
		}
		if order != nil {
			if err := v.orderBys(order); err != nil {
				return err
			}
		}
	case "OPERATOR", "CONJUNCTION":
		if !approvedOperator(typ) {
			return fmt.Errorf("operator %q is not allowed in model SQL", typ)
		}
		children, ok := optionalArray(obj, "children")
		if !ok {
			return fmt.Errorf("%s children must be an array", class)
		}
		if children != nil {
			for _, child := range children {
				if err := v.expressionObject(child); err != nil {
					return err
				}
			}
		}
	case "COMPARISON":
		for _, key := range []string{"left", "right"} {
			child, ok := obj[key].(map[string]any)
			if !ok {
				return fmt.Errorf("comparison %s must be an expression", key)
			}
			if err := v.expression(child); err != nil {
				return err
			}
		}
	case "CAST":
		if castType, present := obj["cast_type"]; present {
			castObject, ok := castType.(map[string]any)
			if !ok {
				return fmt.Errorf("cast cast_type must be an object")
			}
			if err := requireKeys(castObject, "id", "type_modifiers", "type_info"); err != nil {
				return err
			}
			if id, ok := castObject["id"]; !ok || !validCastType(id) {
				return fmt.Errorf("cast cast_type.id must be a known type")
			}
			if raw, present := castObject["type_modifiers"]; present && raw != nil {
				if _, ok := raw.([]any); !ok {
					return fmt.Errorf("cast cast_type.type_modifiers must be an array")
				}
			}
			if raw, present := castObject["type_info"]; present && raw != nil {
				typeInfo, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("cast cast_type.type_info must be an object")
				}
				if err := requireKeys(typeInfo, "type", "width", "scale"); err != nil {
					return err
				}
				if rawType, present := typeInfo["type"]; present && rawType != nil {
					if _, ok := rawType.(string); !ok {
						return fmt.Errorf("cast cast_type.type_info.type must be a string")
					}
				}
				for _, key := range []string{"width", "scale"} {
					if rawNumber, present := typeInfo[key]; present && rawNumber != nil {
						switch rawNumber.(type) {
						case float64, int, int64:
						default:
							return fmt.Errorf("cast cast_type.type_info.%s must be numeric", key)
						}
					}
				}
			}
		}
		if tryCast, present := obj["try_cast"]; present {
			if _, ok := tryCast.(bool); !ok {
				return fmt.Errorf("cast try_cast must be a boolean")
			}
		}
		child, ok := obj["child"].(map[string]any)
		if !ok {
			return fmt.Errorf("cast child must be an expression")
		}
		if err := v.expression(child); err != nil {
			return err
		}
	case "CASE":
		checks, ok := obj["case_checks"].([]any)
		if !ok {
			return fmt.Errorf("CASE checks must be an array")
		}
		for _, raw := range checks {
			check, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("CASE check must be an object")
			}
			if err := requireKeys(check, "when_expr", "then_expr"); err != nil {
				return err
			}
			for _, key := range []string{"when_expr", "then_expr"} {
				child, ok := check[key].(map[string]any)
				if !ok {
					return fmt.Errorf("CASE %s must be an expression", key)
				}
				if err := v.expression(child); err != nil {
					return err
				}
			}
		}
		if elseExpr, present := obj["else_expr"]; present && elseExpr != nil {
			child, ok := elseExpr.(map[string]any)
			if !ok {
				return fmt.Errorf("CASE else_expr must be an expression")
			}
			return v.expression(child)
		}
	case "WINDOW":
		name, ok := obj["function_name"].(string)
		if !ok {
			return fmt.Errorf("WINDOW function_name must be a string")
		}
		if !approvedFunction(name) {
			return fmt.Errorf("window function %q is not allowed", name)
		}
		for _, key := range []string{"partitions", "children"} {
			values, ok := optionalArray(obj, key)
			if !ok {
				return fmt.Errorf("WINDOW %s must be an array", key)
			}
			if values != nil {
				for _, child := range values {
					if err := v.expressionObject(child); err != nil {
						return err
					}
				}
			}
		}
		orders, ok := optionalArray(obj, "orders")
		if !ok {
			return fmt.Errorf("WINDOW orders must be an array")
		}
		if orders != nil {
			for _, raw := range orders {
				order, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("window order must be an object")
				}
				if err := v.order(order); err != nil {
					return err
				}
			}
		}
		filter, ok := optionalObject(obj, "filter")
		if !ok {
			return fmt.Errorf("WINDOW filter must be an expression")
		}
		if filter != nil {
			if err := v.expression(filter); err != nil {
				return err
			}
		}
		for _, key := range []string{"start", "end"} {
			if raw, present := obj[key]; present && raw != nil {
				if boundaryType, ok := raw.(string); ok {
					if !validWindowBoundaryType(boundaryType) {
						return fmt.Errorf("WINDOW %s type is unknown", key)
					}
					continue
				}
				boundary, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("WINDOW %s must be an object", key)
				}
				if err := v.windowBoundary(boundary, key); err != nil {
					return err
				}
			}
		}
	case "SUBQUERY":
		query, ok := obj["subquery"].(map[string]any)
		if !ok {
			return fmt.Errorf("subquery expression has no node")
		}
		if err := requireKeys(query, "node"); err != nil {
			return err
		}
		node, ok := query["node"].(map[string]any)
		if !ok {
			return fmt.Errorf("subquery expression has no SELECT node")
		}
		return v.selectNode(node)
	case "BETWEEN":
		for _, key := range []string{"input", "lower", "upper"} {
			child, ok := obj[key].(map[string]any)
			if !ok {
				return fmt.Errorf("BETWEEN %s must be an expression", key)
			}
			if err := v.expression(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *astVisitor) modifier(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("SELECT modifier must be an object")
	}
	typ, _ := obj["type"].(string)
	switch typ {
	case "DISTINCT_MODIFIER":
		return requireKeys(obj, "type")
	case "ORDER_MODIFIER":
		if err := requireKeys(obj, "type", "orders"); err != nil {
			return err
		}
		orders, ok := obj["orders"].([]any)
		if !ok {
			return fmt.Errorf("ORDER modifier orders must be an array")
		}
		for _, item := range orders {
			if err := v.order(item); err != nil {
				return err
			}
		}
		return nil
	case "LIMIT_MODIFIER":
		if err := requireKeys(obj, "type", "limit", "offset"); err != nil {
			return err
		}
		for _, key := range []string{"limit", "offset"} {
			if value, present := obj[key]; present && value != nil {
				child, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("LIMIT modifier %s must be an expression", key)
				}
				if err := v.expression(child); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported SELECT modifier %q", typ)
	}
}
func (v *astVisitor) order(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("ORDER item must be an object")
	}
	if err := requireKeys(obj, "type", "null_order", "expression"); err != nil {
		return err
	}
	orderType, ok := optionalString(obj, "type")
	if !ok || (orderType != "ASCENDING" && orderType != "DESCENDING" && orderType != "ORDER_DEFAULT") {
		return fmt.Errorf("ORDER item type is unknown")
	}
	nullOrder, ok := optionalString(obj, "null_order")
	if !ok || (nullOrder != "" && nullOrder != "ORDER_DEFAULT" && nullOrder != "NULLS_FIRST" && nullOrder != "NULLS_LAST") {
		return fmt.Errorf("ORDER null_order is unknown")
	}
	expr, ok := obj["expression"].(map[string]any)
	if !ok {
		return fmt.Errorf("ORDER expression must be an expression")
	}
	return v.expression(expr)
}
func (v *astVisitor) orderBys(obj map[string]any) error {
	if err := requireKeys(obj, "type", "orders"); err != nil {
		return err
	}
	if raw, present := obj["orders"]; present && raw != nil {
		values, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("ORDER modifier orders must be an array")
		}
		for _, item := range values {
			if err := v.order(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *astVisitor) windowBoundary(obj map[string]any, name string) error {
	if err := requireKeys(obj, "type", "preceding", "following", "expression"); err != nil {
		return err
	}
	if raw, present := obj["type"]; present && raw != nil {
		typeName, ok := raw.(string)
		if !ok {
			return fmt.Errorf("WINDOW %s type must be a string", name)
		}
		if !validWindowBoundaryType(typeName) {
			return fmt.Errorf("WINDOW %s type is unknown", name)
		}
	}
	for _, key := range []string{"preceding", "following"} {
		if raw, present := obj[key]; present && raw != nil {
			switch raw.(type) {
			case float64, int, int64:
			default:
				return fmt.Errorf("WINDOW %s %s must be numeric", name, key)
			}
		}
	}
	if raw, present := obj["expression"]; present && raw != nil {
		expression, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("WINDOW %s expression must be an expression", name)
		}
		return v.expression(expression)
	}
	return nil
}

func validWindowBoundaryType(value string) bool {
	switch value {
	case "UNBOUNDED_PRECEDING", "UNBOUNDED_FOLLOWING", "CURRENT_ROW", "EXPR_PRECEDING", "EXPR_FOLLOWING", "CURRENT_ROW_RANGE":
		return true
	default:
		return false
	}
}

func validMaterializedMode(value string) bool {
	switch value {
	case "CTE_MATERIALIZE_DEFAULT", "CTE_MATERIALIZE_ALWAYS", "CTE_MATERIALIZE_NEVER":
		return true
	default:
		return false
	}
}

func validAggregateHandling(value string) bool {
	switch value {
	case "STANDARD_HANDLING", "FORCE_NULL_HANDLING", "NON_EMPTY_HANDLING":
		return true
	default:
		return false
	}
}

func nonNegativeInteger(value any) bool {
	switch value := value.(type) {
	case int:
		return value >= 0
	case int64:
		return value >= 0
	case float64:
		return value >= 0 && value == float64(int64(value))
	default:
		return false
	}
}

func validateOptionalQueryLocation(obj map[string]any, owner string) error {
	raw, present := obj["query_location"]
	if !present || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case float64:
		if value != float64(int(value)) || value < 0 {
			return fmt.Errorf("%s query_location must be a non-negative integer", owner)
		}
	case int, int64:
	default:
		return fmt.Errorf("%s query_location must be a number", owner)
	}
	return nil
}

// validateASTKeys rejects version-drifted/unknown fields. Callers separately
// check required fields because DuckDB omits null/default fields when the
// serializer options are enabled.
func validateASTKeys(obj map[string]any, allowed ...string) error {
	set := map[string]struct{}{}
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range obj {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("unknown DuckDB SQL AST field %q", key)
		}
	}
	return nil
}

func requireKeys(obj map[string]any, allowed ...string) error {
	return validateASTKeys(obj, allowed...)
}
func validIdentifier(value string) bool {
	if value == "" || !(value[0] == '_' || value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') {
		return false
	}
	for _, char := range value[1:] {
		if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func validRelationName(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for _, char := range value[1:] {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '.' || char == '-') {
			return false
		}
	}
	return true
}
func approvedOperator(value string) bool {
	if value == "CONJUNCTION_AND" || value == "CONJUNCTION_OR" {
		return true
	}
	if strings.HasPrefix(value, "OPERATOR_") {
		switch value {
		case "OPERATOR_COALESCE", "OPERATOR_IS_NULL", "OPERATOR_IS_NOT_NULL", "OPERATOR_NOT", "OPERATOR_NEGATE", "OPERATOR_PLUS", "OPERATOR_MINUS", "OPERATOR_MULTIPLY", "OPERATOR_DIVIDE", "OPERATOR_MOD", "OPERATOR_CONCAT":
			return true
		}
	}
	return false
}

func validExpressionType(class, typ string) bool {
	switch class {
	case "CONSTANT":
		return typ == "VALUE_CONSTANT"
	case "COLUMN_REF":
		return typ == "COLUMN_REF"
	case "STAR":
		return typ == "STAR"
	case "FUNCTION":
		return typ == "FUNCTION"
	case "OPERATOR":
		return approvedOperator(typ)
	case "COMPARISON":
		switch typ {
		case "COMPARE_EQUAL", "COMPARE_NOTEQUAL", "COMPARE_LESSTHAN", "COMPARE_GREATERTHAN", "COMPARE_LESSTHANOREQUALTO", "COMPARE_GREATERTHANOREQUALTO", "COMPARE_DISTINCT_FROM", "COMPARE_NOT_DISTINCT_FROM", "COMPARE_IN", "COMPARE_NOT_IN", "COMPARE_BETWEEN", "COMPARE_NOT_BETWEEN", "COMPARE_IS_NULL", "COMPARE_IS_NOT_NULL":
			return true
		default:
			return false
		}
	case "CAST":
		return typ == "OPERATOR_CAST"
	case "CASE":
		return typ == "CASE_EXPR"
	case "WINDOW":
		switch typ {
		case "WINDOW", "WINDOW_AGGREGATE", "WINDOW_ROW_NUMBER", "WINDOW_RANK", "WINDOW_DENSE_RANK", "WINDOW_PERCENT_RANK", "WINDOW_CUME_DIST", "WINDOW_NTILE", "WINDOW_LEAD", "WINDOW_LAG", "WINDOW_FIRST_VALUE", "WINDOW_LAST_VALUE", "WINDOW_NTH_VALUE":
			return true
		default:
			return false
		}
	case "SUBQUERY":
		return typ == "SUBQUERY"
	case "BETWEEN":
		return typ == "COMPARE_BETWEEN" || typ == "COMPARE_NOT_BETWEEN"
	case "CONJUNCTION":
		return typ == "CONJUNCTION_AND" || typ == "CONJUNCTION_OR"
	default:
		return false
	}
}

func allowedJoinType(value string) bool {
	switch value {
	case "INNER", "LEFT", "RIGHT", "FULL", "SEMI", "ANTI", "MARK", "ASOF", "POSITIONAL", "LEFT_DELIM", "RIGHT_DELIM", "FULL_DELIM":
		return true
	default:
		return false
	}
}

func allowedJoinRefType(value string) bool {
	switch value {
	case "REGULAR", "NATURAL", "POSITIONAL", "ASOF":
		return true
	default:
		return false
	}
}

func validCastType(value any) bool {
	id, ok := value.(string)
	if !ok {
		return false
	}
	switch strings.ToUpper(id) {
	case "ANY", "UNKNOWN", "NULL", "BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT", "FLOAT", "DOUBLE", "DECIMAL", "VARCHAR", "BLOB", "DATE", "TIME", "TIMESTAMP", "TIMESTAMP_TZ", "TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMP_SEC", "INTERVAL", "UUID", "JSON", "ENUM", "LIST", "STRUCT", "MAP":
		return true
	default:
		return false
	}
}

func optionalArray(obj map[string]any, key string) ([]any, bool) {
	value, present := obj[key]
	if !present || value == nil {
		return nil, true
	}
	array, ok := value.([]any)
	return array, ok
}

func optionalObject(obj map[string]any, key string) (map[string]any, bool) {
	value, present := obj[key]
	if !present || value == nil {
		return nil, true
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func optionalString(obj map[string]any, key string) (string, bool) {
	value, present := obj[key]
	if !present || value == nil {
		return "", true
	}
	text, ok := value.(string)
	return text, ok
}

func approvedFunction(value string) bool {
	if strings.EqualFold(value, "lpad") || strings.EqualFold(value, "list_contains") || strings.EqualFold(value, "str_split") {
		return true
	}
	switch strings.ToLower(value) {
	case "+", "-", "*", "/", "%", "||", "and", "or", "not", "coalesce", "nullif", "if", "greatest", "least", "abs", "ceil", "ceiling", "floor", "round", "trunc", "sign", "sqrt", "pow", "power", "exp", "ln", "log", "log10", "lower", "upper", "trim", "ltrim", "rtrim", "replace", "regexp_replace", "regexp_matches", "regexp_extract", "length", "len", "left", "right", "substr", "substring", "concat", "concat_ws", "split_part", "printf", "date_trunc", "date_part", "date_diff", "datediff", "strftime", "to_timestamp", "make_date", "make_timestamp", "year", "month", "day", "hour", "minute", "second", "hash", "md5", "sha256", "try_cast", "count", "count_star", "sum", "avg", "min", "max", "median", "mode", "quantile", "quantile_cont", "quantile_disc", "stddev", "stddev_pop", "stddev_samp", "variance", "var_pop", "var_samp", "bool_and", "bool_or", "string_agg", "array_agg", "list", "list_value", "row_number", "rank", "dense_rank", "percent_rank", "cume_dist", "ntile", "lag", "lead", "first_value", "last_value":
		return true
	default:
		return false
	}
}

func stringArray(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil
		}
		result = append(result, text)
	}
	return result
}

func queryLocation(value any) int {
	switch typed := value.(type) {
	case float64:
		if typed > float64(lenSentinel()) {
			return -1
		}
		return int(typed)
	case int:
		return typed
	default:
		return -1
	}
}

func lenSentinel() int {
	return int(^uint(0) >> 1)
}

type planNode struct {
	Name      string         `json:"name"`
	Children  []planNode     `json:"children"`
	ExtraInfo map[string]any `json:"extra_info"`
}

func walkPlan(node planNode, analysis *ExplainAnalysis) {
	if tableText, ok := node.ExtraInfo["Table"].(string); ok && tableText != "" {
		catalog, schema, table := normalizeTableName(tableText)
		analysis.Scans = append(analysis.Scans, Scan{
			Operator:    node.Name,
			Catalog:     catalog,
			Schema:      schema,
			Table:       table,
			Projections: normalizeProjections(node.ExtraInfo["Projections"]),
		})
	}
	for _, child := range node.Children {
		walkPlan(child, analysis)
	}
}

func normalizeTableName(value string) (string, string, string) {
	parts := splitSQLName(value)
	if len(parts) == 0 {
		return "", "", ""
	}
	if len(parts) == 1 {
		return "", "", parts[0]
	}
	if len(parts) == 2 {
		return "", parts[0], parts[1]
	}
	return parts[len(parts)-3], parts[len(parts)-2], parts[len(parts)-1]
}

func splitSQLName(value string) []string {
	parts := []string{}
	var builder strings.Builder
	quoted := false
	for index := 0; index < len(value); index++ {
		char := value[index]
		if quoted {
			if char == '"' {
				if index+1 < len(value) && value[index+1] == '"' {
					builder.WriteByte('"')
					index++
					continue
				}
				quoted = false
				continue
			}
			builder.WriteByte(char)
			continue
		}
		switch char {
		case '"':
			quoted = true
		case '.':
			parts = append(parts, strings.TrimSpace(builder.String()))
			builder.Reset()
		default:
			builder.WriteByte(char)
		}
	}
	parts = append(parts, strings.TrimSpace(builder.String()))
	return parts
}

func normalizeProjections(value any) []string {
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			result = appendProjection(result, text)
		}
		return result
	case string:
		return appendProjection(nil, typed)
	default:
		return nil
	}
}

func appendProjection(result []string, value string) []string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result = append(result, line)
	}
	return result
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func singleStringResult(rows *sql.Rows, preferredColumn string) (string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	values := make([]sql.NullString, len(columns))
	scan := make([]any, len(values))
	for index := range values {
		scan[index] = &values[index]
	}
	if err := rows.Scan(scan...); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	index := len(values) - 1
	for columnIndex, column := range columns {
		if strings.EqualFold(column, preferredColumn) {
			index = columnIndex
			break
		}
	}
	if index < 0 || index >= len(values) || !values[index].Valid {
		return "", fmt.Errorf("query returned no %s payload", preferredColumn)
	}
	return values[index].String, nil
}

func MarshalForTest(value any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
	return buffer.Bytes()
}
