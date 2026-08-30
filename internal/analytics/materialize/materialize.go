package materialize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type Executor interface {
	Exec(ctx context.Context, statement string) error
}

type ModelTablePlanner interface {
	PlanModelTable(ctx context.Context, model *semanticmodel.Model, tableName string, table semanticmodel.Table) (ModelTablePlan, error)
}

// NamespacedModelTablePlanner is the candidate-write extension of
// ModelTablePlanner. The relation namespace is value-only and is validated by
// the caller before it reaches a planner. Keeping this as an optional
// capability preserves the active/legacy planner contract while ensuring
// candidate writes cannot silently fall back to the shared model schema.
type NamespacedModelTablePlanner interface {
	PlanModelTableInNamespace(ctx context.Context, model *semanticmodel.Model, tableName string, table semanticmodel.Table, relationNamespace string) (ModelTablePlan, error)
}

type PreparedSources interface {
	ModelTablePlanner
	Close() error
}

// SourceObservation is captured from the resolved source session before that
// session (and its credentials) is closed. Runtime gate evaluation consumes
// this value after detachment; it never re-opens authored paths or relations.
// For revision freshness, RevisionObserved is the canonical UTC timestamp
// selected by the authored revision contract; adapters may replace it with
// target metadata when a connector exposes a stronger equivalent.
type SourceObservation struct {
	ID                 string
	Schema             []semanticmodel.ColumnSchema
	Revision           string
	RevisionObserved   time.Time
	FreshnessObserved  time.Time
	FreshnessEmpty     bool
	SchemaFailure      ObservationFailure
	FreshnessFailure   ObservationFailure
	ObservationQueries int
	ObservationRows    int64
	ObservationMillis  int64
}

type ObservationFailure string

const (
	ObservationUnavailable ObservationFailure = "unavailable"
	ObservationTimeout     ObservationFailure = "timeout"
	ObservationBounds      ObservationFailure = "bounds"
)

type observationBudgetKey struct{}

// ObservationBudget limits live source evidence queries. It is carried only
// through the in-process materialization context and never enters artifacts.
type ObservationBudget struct {
	MaxQueries int
	MaxMillis  int64
}

func WithObservationBudget(ctx context.Context, budget ObservationBudget) context.Context {
	return context.WithValue(ctx, observationBudgetKey{}, budget)
}

func ObservationBudgetFromContext(ctx context.Context) ObservationBudget {
	if ctx == nil {
		return ObservationBudget{}
	}
	if budget, ok := ctx.Value(observationBudgetKey{}).(ObservationBudget); ok {
		return budget
	}
	return ObservationBudget{}
}

// SourceObservationProvider is implemented by source preparers that can
// capture target-owned freshness and schema evidence while their live session
// is still held. It is optional for test preparers; production source runtime
// implements it.
type SourceObservationProvider interface {
	SourceObservations(context.Context) ([]SourceObservation, error)
}

type SourcePreparer interface {
	Prepare(context.Context, *semanticmodel.Model) (PreparedSources, error)
}

type ModelTablePlan struct {
	Mode string
	SQL  string
}

const (
	PlanModeDirectSourceRead      = "direct_source_read"
	PlanModeProjectedSourceInline = "projected_source_inline"
	PlanModeModelSQL              = "model_sql"
)

type SourcePathResolver interface {
	ResolveSourcePath(model *semanticmodel.Model, source semanticmodel.Source) (string, error)
}

type MissingDataError struct {
	Missing []string
}

func (e *MissingDataError) Error() string {
	return fmt.Sprintf("managed source files are missing: %s", strings.Join(e.Missing, ", "))
}

func (e *MissingDataError) SetupRequired() bool {
	return true
}

func Refresh(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model) (time.Time, error) {
	if executor == nil {
		return time.Time{}, fmt.Errorf("materialization executor is required")
	}
	if sources == nil {
		return time.Time{}, fmt.Errorf("model table planner is required")
	}
	if err := ModelTables(ctx, executor, sources, model); err != nil {
		return time.Time{}, err
	}
	return time.Now(), nil
}

func RefreshModelTables(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model, tableNames []string) (time.Time, error) {
	if executor == nil {
		return time.Time{}, fmt.Errorf("materialization executor is required")
	}
	if sources == nil {
		return time.Time{}, fmt.Errorf("model table planner is required")
	}
	if err := ModelTablesNamed(ctx, executor, sources, model, tableNames); err != nil {
		return time.Time{}, err
	}
	return time.Now(), nil
}

func ValidateFiles(model *semanticmodel.Model) error {
	return ValidateFilesWithResolver(model, defaultSourcePathResolver{})
}

func ValidateFilesWithResolver(model *semanticmodel.Model, resolver SourcePathResolver) error {
	if resolver == nil {
		return fmt.Errorf("source path resolver is required")
	}
	var missing []string
	for name, source := range model.Sources {
		if source.Path == "" {
			continue
		}
		connection := model.Connections[source.Connection]
		if connection.Kind != "managed" {
			continue
		}
		file, err := resolver.ResolveSourcePath(model, source)
		if err != nil {
			return fmt.Errorf("resolving managed source %s: %w", name, err)
		}
		if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, file)
		} else if err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &MissingDataError{Missing: missing}
	}
	return nil
}

func ResolveSourcePath(model *semanticmodel.Model, source semanticmodel.Source) (string, error) {
	return defaultSourcePathResolver{}.ResolveSourcePath(model, source)
}

type defaultSourcePathResolver struct{}

func (defaultSourcePathResolver) ResolveSourcePath(model *semanticmodel.Model, source semanticmodel.Source) (string, error) {
	connection := model.Connections[source.Connection]
	switch connection.Kind {
	case "managed":
		root := strings.TrimSpace(connection.Root)
		if root == "" {
			return "", fmt.Errorf("managed connection %q has no active revision", source.Connection)
		}
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("managed connection %q revision root must be absolute", source.Connection)
		}
		if filepath.IsAbs(source.Path) {
			return "", fmt.Errorf("managed connection %q source path must be relative", source.Connection)
		}
		target := filepath.Clean(filepath.Join(root, source.Path))
		relative, err := filepath.Rel(filepath.Clean(root), target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("managed connection %q source path escapes its active revision", source.Connection)
		}
		return target, nil
	default:
		if connection.Scope == "" {
			return source.Path, nil
		}
		if connectors.IsLocalPath(source.Path) {
			return connectors.JoinScope(connection.Scope, source.Path), nil
		}
		if !connectors.WithinScope(connection.Scope, source.Path) {
			return "", fmt.Errorf("path %q is outside connection %q scope %q", source.Path, source.Connection, connection.Scope)
		}
		return source.Path, nil
	}
}

func ModelTables(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model) error {
	if executor == nil {
		return fmt.Errorf("materialization executor is required")
	}
	if sources == nil {
		return fmt.Errorf("model table planner is required")
	}
	order, err := ModelTableOrder(model)
	if err != nil {
		return err
	}
	return ModelTablesNamed(ctx, executor, sources, model, order)
}

// ModelTablesInNamespace is the full-refresh counterpart to
// ModelTablesNamedInNamespace.
func ModelTablesInNamespace(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model, relationNamespace string) error {
	if executor == nil {
		return fmt.Errorf("materialization executor is required")
	}
	if sources == nil {
		return fmt.Errorf("model table planner is required")
	}
	order, err := ModelTableOrder(model)
	if err != nil {
		return err
	}
	return ModelTablesNamedInNamespace(ctx, executor, sources, model, order, relationNamespace)
}

func ModelTablesNamed(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model, tableNames []string) error {
	return ModelTablesNamedInNamespace(ctx, executor, sources, model, tableNames, "model")
}

// ModelTablesNamedInNamespace materializes selected model tables into the
// validated relation namespace. Native candidate requests use this entrypoint
// so every DDL statement is scoped to the candidate's authority-derived
// schema. Legacy callers should continue using ModelTablesNamed.
func ModelTablesNamedInNamespace(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model, tableNames []string, relationNamespace string) error {
	if executor == nil {
		return fmt.Errorf("materialization executor is required")
	}
	if sources == nil {
		return fmt.Errorf("model table planner is required")
	}
	if model == nil {
		return fmt.Errorf("semantic model is required")
	}
	if err := validateRelationNamespace(relationNamespace); err != nil {
		return fmt.Errorf("relation namespace: %w", err)
	}
	if relationNamespace != "model" {
		if _, ok := sources.(NamespacedModelTablePlanner); !ok {
			return fmt.Errorf("materialization planner does not support relation namespace %q", relationNamespace)
		}
	}
	ordered, err := selectedModelTableOrder(model, tableNames)
	if err != nil {
		return err
	}
	if err := executor.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+relationNamespaceSQLName(relationNamespace)); err != nil {
		return err
	}
	for _, name := range ordered {
		if err := materializeModelTableInNamespace(ctx, executor, sources, model, name, relationNamespace); err != nil {
			return err
		}
	}
	return nil
}

// selectedModelTableOrder topologically orders only the requested tables.
// Partial refreshes must not rematerialize unchanged dependencies, but every
// dependency that is part of the selected set must be rebuilt first so model
// SQL never observes a stale or missing prerequisite.
func selectedModelTableOrder(model *semanticmodel.Model, tableNames []string) ([]string, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	selected := make(map[string]struct{}, len(tableNames))
	for _, name := range tableNames {
		if err := validateIdentifier(name); err != nil {
			return nil, err
		}
		if _, ok := model.Tables[name]; !ok {
			return nil, fmt.Errorf("unknown model table %q", name)
		}
		selected[name] = struct{}{}
	}
	orderedNames := make([]string, 0, len(selected))
	for name := range selected {
		orderedNames = append(orderedNames, name)
	}
	sort.Strings(orderedNames)

	visiting := make(map[string]bool, len(selected))
	visited := make(map[string]bool, len(selected))
	ordered := make([]string, 0, len(selected))
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("model table dependency cycle includes %q", name)
		}
		visiting[name] = true
		dependencies := append([]string(nil), model.Tables[name].ModelDependencies...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			if _, needed := selected[dependency]; !needed {
				continue
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		ordered = append(ordered, name)
		return nil
	}
	for _, name := range orderedNames {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func ModelTableDependencyOrder(model *semanticmodel.Model, selectedTable string) ([]string, error) {
	selectedTable = strings.TrimSpace(selectedTable)
	if selectedTable == "" {
		return nil, fmt.Errorf("model table is required")
	}
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	temporary := map[string]bool{}
	permanent := map[string]bool{}
	order := []string{}
	var visit func(string) error
	visit = func(name string) error {
		if permanent[name] {
			return nil
		}
		if temporary[name] {
			return fmt.Errorf("model table dependency cycle includes %q", name)
		}
		table, ok := model.Tables[name]
		if !ok {
			return fmt.Errorf("unknown model table %q", name)
		}
		temporary[name] = true
		for _, dependency := range table.ModelDependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		temporary[name] = false
		permanent[name] = true
		order = append(order, name)
		return nil
	}
	if err := visit(selectedTable); err != nil {
		return nil, err
	}
	return order, nil
}

func materializeModelTable(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model, name string) error {
	return materializeModelTableInNamespace(ctx, executor, sources, model, name, "model")
}

func materializeModelTableInNamespace(ctx context.Context, executor Executor, sources ModelTablePlanner, model *semanticmodel.Model, name, relationNamespace string) error {
	table := model.Tables[name]
	var plan ModelTablePlan
	var err error
	if relationNamespace != "model" {
		namespaced, ok := sources.(NamespacedModelTablePlanner)
		if !ok {
			return fmt.Errorf("materialization planner does not support relation namespace %q", relationNamespace)
		}
		plan, err = namespaced.PlanModelTableInNamespace(ctx, model, name, table, relationNamespace)
	} else {
		plan, err = sources.PlanModelTable(ctx, model, name, table)
	}
	if err != nil {
		return err
	}
	if plan.SQL == "" {
		return fmt.Errorf("model table %q produced empty materialization SQL", name)
	}
	if err := executor.Exec(ctx, plan.SQL); err != nil {
		return fmt.Errorf("materializing %s.%s: %w", relationNamespace, name, err)
	}
	return nil
}

func ModelTableOrder(model *semanticmodel.Model) ([]string, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	temporary := map[string]bool{}
	permanent := map[string]bool{}
	order := []string{}
	var visit func(string) error
	visit = func(name string) error {
		if permanent[name] {
			return nil
		}
		if temporary[name] {
			return fmt.Errorf("model table dependency cycle includes %q", name)
		}
		table, ok := model.Tables[name]
		if !ok {
			return fmt.Errorf("unknown model table %q", name)
		}
		temporary[name] = true
		for _, dependency := range table.ModelDependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		temporary[name] = false
		permanent[name] = true
		order = append(order, name)
		return nil
	}
	for _, name := range model.TableNames() {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func validateIdentifier(value string) error {
	for i, r := range value {
		if i == 0 {
			if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && r != '_' {
				return fmt.Errorf("invalid identifier %q", value)
			}
			continue
		}
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return fmt.Errorf("invalid identifier %q", value)
		}
	}
	if value == "" {
		return fmt.Errorf("invalid identifier %q", value)
	}
	return nil
}

func validateRelationNamespace(value string) error {
	if err := validateIdentifier(value); err != nil {
		return err
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("relation namespace %q must be lowercase canonical", value)
	}
	if len(value) > 63 {
		return fmt.Errorf("relation namespace %q exceeds 63 bytes", value)
	}
	return nil
}

func relationNamespaceSQLName(value string) string {
	// validateRelationNamespace has already constrained this value to a plain
	// SQL identifier, so emitting it directly preserves the canonical DDL text
	// for both the legacy and authority-derived schemas.
	return value
}
