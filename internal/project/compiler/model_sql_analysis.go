package compiler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/modelsql"
)

// modelSQLDependencyError preserves the table identity while dependency
// analysis walks a sorted model map. Callers can therefore attach diagnostics
// to the exact authored resource instead of selecting an arbitrary map key.
type modelSQLDependencyError struct {
	ModelName string
	Err       error
}

func (e *modelSQLDependencyError) Error() string {
	return fmt.Sprintf("Model %q SQL: %v", e.ModelName, e.Err)
}

func (e *modelSQLDependencyError) Unwrap() error {
	return e.Err
}

// deriveModelSQLDependencies is the compiler-owned SQL boundary. It parses
// each authored SQL definition exactly through DuckDB's pinned JSON AST and
// persists only normalized lineage evidence on the runtime table.
func deriveModelSQLDependencies(model *semanticmodel.Model) error {
	if model == nil {
		return fmt.Errorf("semantic model is required")
	}
	names := make([]string, 0, len(model.Tables))
	for name := range model.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, tableName := range names {
		table := model.Tables[tableName]
		sqlText := strings.TrimSpace(table.Execution.SQL)
		if sqlText == "" {
			continue
		}
		analysis, err := modelsql.Analyze(context.Background(), sqlText)
		if err != nil {
			return &modelSQLDependencyError{ModelName: tableName, Err: err}
		}
		for _, source := range analysis.SourceRefs {
			if _, ok := model.Sources[source]; !ok {
				return &modelSQLDependencyError{ModelName: tableName, Err: fmt.Errorf("SQL references unknown source %q", source)}
			}
		}
		for _, dependency := range analysis.ModelRefs {
			if dependency == tableName || (table.ModelName != "" && dependency == table.ModelName) {
				return &modelSQLDependencyError{ModelName: tableName, Err: fmt.Errorf("cannot read itself")}
			}
			if _, err := resolveCompilerModelDependency(model, dependency); err != nil {
				return &modelSQLDependencyError{ModelName: tableName, Err: fmt.Errorf("SQL references %w", err)}
			}
		}
		table.SourceDependencies = append([]string(nil), analysis.SourceRefs...)
		table.ModelDependencies = append([]string(nil), analysis.ModelRefs...)
		table.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: append([]string(nil), analysis.SourceRefs...), ModelRefs: append([]string(nil), analysis.ModelRefs...)}
		model.Tables[tableName] = table
	}
	return nil
}

func resolveCompilerModelDependency(model *semanticmodel.Model, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("unknown Model %q", name)
	}
	if len(model.Datasets) == 0 {
		if _, ok := model.Tables[name]; !ok {
			return "", fmt.Errorf("unknown Model %q", name)
		}
		return name, nil
	}
	for alias, spec := range model.Datasets {
		if strings.TrimSpace(spec.Model) != name {
			continue
		}
		table, ok := model.Tables[alias]
		if ok && (table.ModelName == "" || table.ModelName == name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("unknown Model %q", name)
}
