// Package planir contains the typed, renderer-independent intermediate
// representation used by the governed query planner.  The representation is
// deliberately small: it describes relational intent and physical lineage,
// but does not attempt to be a general SQL AST.
package planir

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind is the closed set of operations in the initial plan IR.  Keeping the
// set closed makes it possible for validation and renderers to reject a plan
// they do not understand instead of silently changing its meaning.
type Kind string

const (
	KindScanDataset          Kind = "ScanDataset"
	KindTraverseRelationship Kind = "TraverseRelationship"
	KindFilterRows           Kind = "FilterRows"
	KindAggregateMetrics     Kind = "AggregateMetrics"
	KindStitchAggregates     Kind = "StitchAggregates"
	KindComputeRatio         Kind = "ComputeRatio"
	KindComputeDerived       Kind = "ComputeDerived"
	KindSortLimit            Kind = "SortLimit"
	KindBundleBranches       Kind = "BundleBranches"
)

// FilterPhase identifies where a filter is evaluated.  A filter may not move
// across an aggregate boundary, since that can change both cardinality and
// metric semantics.
type FilterPhase string

const (
	FilterPhaseUnspecified   FilterPhase = ""
	FilterPhaseScan          FilterPhase = "scan"
	FilterPhaseRelationship  FilterPhase = "relationship"
	FilterPhaseAggregate     FilterPhase = "aggregate"
	FilterPhasePostAggregate FilterPhase = "post_aggregate"
)

func (p FilterPhase) valid() bool {
	switch p {
	case FilterPhaseUnspecified, FilterPhaseScan, FilterPhaseRelationship,
		FilterPhaseAggregate, FilterPhasePostAggregate:
		return true
	default:
		return false
	}
}

func (p FilterPhase) rank() int {
	switch p {
	case FilterPhaseScan:
		return 1
	case FilterPhaseRelationship:
		return 2
	case FilterPhaseAggregate:
		return 3
	case FilterPhasePostAggregate:
		return 4
	default:
		return 0
	}
}

// FilterSource identifies the governed origin of a row predicate. Named
// filters are semantic-model members and retain their authored name; request
// filters are ad-hoc constraints supplied for one query and have no member
// name. Keeping this as a closed type prevents a renderer from treating an
// unrecognised provenance value as governed input.
type FilterSource string

const (
	FilterSourceNamed   FilterSource = "named"
	FilterSourceRequest FilterSource = "request"
)

func (s FilterSource) valid() bool {
	return s == FilterSourceNamed || s == FilterSourceRequest
}

// Grain is the identity at which a node emits rows. Fields are ordered: a
// composite grain's order is part of its meaning. TimeGrain is optional and
// records a time bucket in addition to Fields.
type Grain struct {
	Fields    []string `json:"fields,omitempty"`
	TimeGrain string   `json:"time_grain,omitempty"`
}

func (g Grain) empty() bool { return len(g.Fields) == 0 && g.TimeGrain == "" }

func (g Grain) equal(other Grain) bool {
	if g.TimeGrain != other.TimeGrain || len(g.Fields) != len(other.Fields) {
		return false
	}
	for i := range g.Fields {
		if g.Fields[i] != other.Fields[i] {
			return false
		}
	}
	return true
}

// Field and Metric are the typed names available after a node. Type is a
// semantic datatype (for example "string", "date", or "decimal"), not a
// database-specific type.
type Field struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type Metric struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// Literal is the only value that may enter a predicate or scalar expression.
// Its tagged representation avoids interface{} and keeps serialization and
// renderer parameter binding deterministic.
type LiteralKind string

const (
	LiteralString LiteralKind = "string"
	LiteralNumber LiteralKind = "number"
	LiteralBool   LiteralKind = "bool"
	LiteralNull   LiteralKind = "null"
)

type Literal struct {
	Kind       LiteralKind `json:"kind"`
	String     string      `json:"string,omitempty"`
	NumberText string      `json:"number_text,omitempty"`
	Bool       bool        `json:"bool,omitempty"`
}

var exactNumber = regexp.MustCompile(`^[+-]?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func (l Literal) valid() bool {
	switch l.Kind {
	case LiteralString, LiteralBool, LiteralNull:
		return true
	case LiteralNumber:
		return exactNumber.MatchString(l.NumberText)
	default:
		return false
	}
}

// PredicateKind is a closed Boolean predicate algebra. Field names are
// references, never SQL snippets.
type PredicateKind string

const (
	PredicateCompare PredicateKind = "compare"
	PredicateIsNull  PredicateKind = "is_null"
	PredicateIn      PredicateKind = "in"
	PredicateAnd     PredicateKind = "and"
	PredicateOr      PredicateKind = "or"
	PredicateNot     PredicateKind = "not"
)

type Predicate struct {
	Kind     PredicateKind `json:"kind"`
	Field    string        `json:"field,omitempty"`
	Operator string        `json:"operator,omitempty"`
	Value    Literal       `json:"value,omitempty"`
	Values   []Literal     `json:"values,omitempty"`
	Children []Predicate   `json:"children,omitempty"`
	Negated  bool          `json:"negated,omitempty"` // only used by is_null
}

// ScalarKind is a closed scalar expression algebra for derived metrics.
type ScalarKind string

const (
	ScalarMetricRef ScalarKind = "metric_ref"
	ScalarLiteral   ScalarKind = "literal"
	ScalarNeg       ScalarKind = "neg"
	ScalarPos       ScalarKind = "pos"
	ScalarAdd       ScalarKind = "add"
	ScalarSub       ScalarKind = "sub"
	ScalarMul       ScalarKind = "mul"
	ScalarDiv       ScalarKind = "div"
	ScalarSafeDiv   ScalarKind = "safe_divide"
	ScalarFunction  ScalarKind = "function"
)

type ScalarExpr struct {
	Kind     ScalarKind   `json:"kind"`
	Metric   string       `json:"metric,omitempty"`
	Literal  Literal      `json:"literal,omitempty"`
	Function string       `json:"function,omitempty"`
	Children []ScalarExpr `json:"children,omitempty"`
}

var comparisonOperators = map[string]struct{}{
	"=": {}, "!=": {}, "<>": {}, "<": {}, "<=": {}, ">": {}, ">=": {},
	"LIKE": {}, "ILIKE": {},
}

func (p Predicate) validate(fields, metrics map[string]bool) error {
	available := func(name string) bool { return fields[name] || metrics[name] }
	if p.Kind == "" {
		return fmt.Errorf("predicate kind is required")
	}
	switch p.Kind {
	case PredicateCompare:
		if p.Field == "" || !available(p.Field) {
			return fmt.Errorf("predicate field %q is unavailable", p.Field)
		}
		if _, ok := comparisonOperators[strings.ToUpper(p.Operator)]; !ok {
			return fmt.Errorf("unsupported comparison operator %q", p.Operator)
		}
		if !p.Value.valid() || p.Value.Kind == LiteralNull {
			return fmt.Errorf("comparison requires a non-null typed value")
		}
	case PredicateIsNull:
		if p.Field == "" || !available(p.Field) {
			return fmt.Errorf("predicate field %q is unavailable", p.Field)
		}
		if p.Operator != "" || p.Value.Kind != "" || len(p.Values) != 0 || len(p.Children) != 0 {
			return fmt.Errorf("is-null predicate has unexpected operands")
		}
	case PredicateIn:
		if p.Field == "" || !available(p.Field) {
			return fmt.Errorf("predicate field %q is unavailable", p.Field)
		}
		if len(p.Values) == 0 {
			return fmt.Errorf("in predicate requires values")
		}
		for _, value := range p.Values {
			if !value.valid() || value.Kind == LiteralNull {
				return fmt.Errorf("in predicate values must be non-null typed values")
			}
		}
	case PredicateAnd, PredicateOr:
		if len(p.Children) == 0 {
			return fmt.Errorf("%s predicate requires children", p.Kind)
		}
		for _, child := range p.Children {
			if err := child.validate(fields, metrics); err != nil {
				return err
			}
		}
	case PredicateNot:
		if len(p.Children) != 1 {
			return fmt.Errorf("not predicate requires one child")
		}
		if err := p.Children[0].validate(fields, metrics); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported predicate kind %q", p.Kind)
	}
	return nil
}

func (e ScalarExpr) validate(metrics map[string]bool) error {
	if e.Kind == "" {
		return fmt.Errorf("scalar expression kind is required")
	}
	switch e.Kind {
	case ScalarMetricRef:
		if e.Metric == "" || !metrics[e.Metric] {
			return fmt.Errorf("scalar metric %q is unavailable", e.Metric)
		}
	case ScalarLiteral:
		if !e.Literal.valid() || e.Literal.Kind == LiteralNull {
			return fmt.Errorf("scalar literal must be a non-null typed value")
		}
	case ScalarNeg, ScalarPos:
		if len(e.Children) != 1 {
			return fmt.Errorf("%s expression requires one child", e.Kind)
		}
		if err := e.Children[0].validate(metrics); err != nil {
			return err
		}
	case ScalarAdd, ScalarSub, ScalarMul, ScalarDiv, ScalarSafeDiv:
		if len(e.Children) != 2 {
			return fmt.Errorf("%s expression requires two children", e.Kind)
		}
		for _, child := range e.Children {
			if err := child.validate(metrics); err != nil {
				return err
			}
		}
	case ScalarFunction:
		if e.Function == "" {
			return fmt.Errorf("scalar function name is required")
		}
		arity := map[string][2]int{"coalesce": {2, 8}, "nullif": {2, 2}, "abs": {1, 1}, "round": {1, 2}, "safe_divide": {2, 2}}
		bounds, ok := arity[strings.ToLower(e.Function)]
		if !ok {
			return fmt.Errorf("unsupported scalar function %q", e.Function)
		}
		if len(e.Children) < bounds[0] || len(e.Children) > bounds[1] {
			return fmt.Errorf("scalar function %q requires %d-%d children", e.Function, bounds[0], bounds[1])
		}
		for _, child := range e.Children {
			if err := child.validate(metrics); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported scalar expression kind %q", e.Kind)
	}
	return nil
}

func (p Predicate) fields() []string {
	seen := map[string]bool{}
	var walk func(Predicate)
	walk = func(value Predicate) {
		if value.Field != "" {
			seen[value.Field] = true
		}
		for _, child := range value.Children {
			walk(child)
		}
	}
	walk(p)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MetricSpec is an aggregate or computed metric introduced by a node.
// Expression is a closed scalar expression; it is intentionally not a
// generic SQL AST or an arbitrary SQL escape hatch.
type MetricSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
	Input       string `json:"input,omitempty"`
	// Inputs carries the ordered operands for aggregate forms that consume
	// more than one governed field (currently COUNT_DISTINCT_PAIR).  Keeping
	// these as field references, rather than an SQL tuple, preserves the closed
	// PlanIR boundary while still expressing coordinate cardinality exactly.
	Inputs  []string          `json:"inputs,omitempty"`
	Empty   string            `json:"empty,omitempty"`
	Filters []AggregateFilter `json:"filters,omitempty"`
}

type JoinKey struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RelationshipPath describes a governed relationship traversal. Name is the
// semantic relationship name; the endpoint datasets and key tuple are kept
// in the IR so renderers never have to rediscover lineage.
type RelationshipPath struct {
	Name         string    `json:"name"`
	FromDataset  string    `json:"from_dataset"`
	ToDataset    string    `json:"to_dataset"`
	FromRelation string    `json:"from_relation,omitempty"`
	ToRelation   string    `json:"to_relation,omitempty"`
	JoinKeys     []JoinKey `json:"join_keys,omitempty"`
}

// RelationshipRoute is an ordered, root-relative safe traversal. Keeping the
// route explicit prevents sibling role-playing edges from being accidentally
// concatenated into one path during lineage derivation.
type RelationshipRoute struct {
	RootDataset string             `json:"root_dataset"`
	Edges       []RelationshipPath `json:"edges"`
}

// PhysicalLineage links a logical field or metric to its physical origin.
type PhysicalLineage struct {
	Logical string `json:"logical"`
	Dataset string `json:"dataset"`
	Field   string `json:"field"`
	// Route identifies the ordered relationship edge names used to reach the
	// physical dataset. It disambiguates role-playing paths that share a table
	// and column while retaining a closed, typed lineage contract.
	Route []string `json:"route,omitempty"`
}

// NodeMeta is common to every node and to Graph. The slices represent sets,
// except OutputGrain.Fields (whose order is meaningful), and are canonicalized
// by Canonical and Fingerprint.
type NodeMeta struct {
	NodeID             string              `json:"id"`
	OutputGrain        Grain               `json:"output_grain"`
	AvailableFields    []Field             `json:"available_fields,omitempty"`
	AvailableMetrics   []Metric            `json:"available_metrics,omitempty"`
	RootDatasets       []string            `json:"root_datasets,omitempty"`
	FilterPhase        FilterPhase         `json:"filter_phase"`
	PhysicalLineage    []PhysicalLineage   `json:"physical_lineage,omitempty"`
	RelationshipRoutes []RelationshipRoute `json:"relationship_routes,omitempty"`
}

func (m NodeMeta) validate(where string) error {
	if m.NodeID == "" {
		return fmt.Errorf("%s: node id is required", where)
	}
	if !m.FilterPhase.valid() {
		return fmt.Errorf("%s: invalid filter phase %q", where, m.FilterPhase)
	}
	if m.OutputGrain.TimeGrain != "" && strings.TrimSpace(m.OutputGrain.TimeGrain) == "" {
		return fmt.Errorf("%s: empty time grain", where)
	}
	seen := map[string]struct{}{}
	availableNames := map[string]struct{}{}
	for _, field := range m.AvailableFields {
		if field.Name == "" {
			return fmt.Errorf("%s: available field has empty name", where)
		}
		if _, ok := seen[field.Name]; ok {
			return fmt.Errorf("%s: duplicate available field %q", where, field.Name)
		}
		seen[field.Name] = struct{}{}
		availableNames[field.Name] = struct{}{}
	}
	seen = map[string]struct{}{}
	for _, metric := range m.AvailableMetrics {
		if metric.Name == "" {
			return fmt.Errorf("%s: available metric has empty name", where)
		}
		if _, ok := seen[metric.Name]; ok {
			return fmt.Errorf("%s: duplicate available metric %q", where, metric.Name)
		}
		seen[metric.Name] = struct{}{}
		availableNames[metric.Name] = struct{}{}
	}
	for _, lineage := range m.PhysicalLineage {
		if _, found := availableNames[lineage.Logical]; !found {
			return fmt.Errorf("%s: physical lineage logical name %q is unavailable", where, lineage.Logical)
		}
	}
	for _, dataset := range m.RootDatasets {
		if strings.TrimSpace(dataset) == "" {
			return fmt.Errorf("%s: empty root dataset", where)
		}
	}
	for _, lineage := range m.PhysicalLineage {
		if lineage.Logical == "" || lineage.Dataset == "" || lineage.Field == "" {
			return fmt.Errorf("%s: incomplete physical lineage for %q", where, lineage.Logical)
		}
	}
	seenRoutes := map[string]struct{}{}
	for _, route := range m.RelationshipRoutes {
		routeKey := route.RootDataset
		for _, edge := range route.Edges {
			routeKey += "|" + edge.Name + ":" + edge.FromDataset + ">" + edge.ToDataset
		}
		if _, exists := seenRoutes[routeKey]; exists {
			return fmt.Errorf("%s: duplicate relationship route %q", where, routeKey)
		}
		seenRoutes[routeKey] = struct{}{}
		if route.RootDataset == "" || len(route.Edges) == 0 {
			return fmt.Errorf("%s: incomplete relationship route from %q", where, route.RootDataset)
		}
		current := route.RootDataset
		for _, path := range route.Edges {
			if path.Name == "" || path.FromDataset == "" || path.ToDataset == "" {
				return fmt.Errorf("%s: incomplete relationship path %q", where, path.Name)
			}
			if path.FromDataset != current {
				return fmt.Errorf("%s: relationship route %q is not contiguous at %q", where, route.RootDataset, path.Name)
			}
			if len(path.JoinKeys) == 0 {
				return fmt.Errorf("%s: relationship path %q has no join keys", where, path.Name)
			}
			for _, key := range path.JoinKeys {
				if key.From == "" || key.To == "" {
					return fmt.Errorf("%s: relationship path %q has incomplete join key", where, path.Name)
				}
			}
			current = path.ToDataset
		}
	}
	return nil
}

// Node is the common typed node contract.
type Node interface {
	Kind() Kind
	Meta() NodeMeta
	Inputs() []string
	nodeMarker()
}

type ScanDataset struct {
	NodeMeta
	Dataset  string `json:"dataset"`
	Relation string `json:"relation,omitempty"`
}

func (ScanDataset) Kind() Kind       { return KindScanDataset }
func (n ScanDataset) Meta() NodeMeta { return n.NodeMeta }
func (ScanDataset) Inputs() []string { return nil }
func (ScanDataset) nodeMarker()      {}

type TraverseRelationship struct {
	NodeMeta
	Input string           `json:"input"`
	Path  RelationshipPath `json:"path"`
}

func (TraverseRelationship) Kind() Kind         { return KindTraverseRelationship }
func (n TraverseRelationship) Meta() NodeMeta   { return n.NodeMeta }
func (n TraverseRelationship) Inputs() []string { return []string{n.Input} }
func (TraverseRelationship) nodeMarker()        {}

type FilterRows struct {
	NodeMeta
	Input     string       `json:"input"`
	Predicate Predicate    `json:"predicate"`
	Source    FilterSource `json:"source"`
	Name      string       `json:"name,omitempty"`
	Fields    []string     `json:"fields,omitempty"` // optional explicit dependency declaration
	// MatchGuard applies relationship match guards to predicate leaves while
	// preserving authored AND/OR/NOT grouping. FieldRoutes identifies the
	// joined route for each governed field; local fields intentionally have no
	// entry and therefore receive no guard.
	MatchGuard  bool                           `json:"match_guard,omitempty"`
	FieldRoutes map[string][]RelationshipRoute `json:"field_routes,omitempty"`
}

func (FilterRows) Kind() Kind         { return KindFilterRows }
func (n FilterRows) Meta() NodeMeta   { return n.NodeMeta }
func (n FilterRows) Inputs() []string { return []string{n.Input} }
func (FilterRows) nodeMarker()        {}

// AggregateFilter is a named governed predicate attached to one aggregate
// metric. It remains metric-local so two metrics rooted in one dataset can
// intentionally have different populations without a branch-wide filter.
type AggregateFilter struct {
	Source             FilterSource                   `json:"source"`
	Name               string                         `json:"name"`
	Predicate          Predicate                      `json:"predicate"`
	Phase              FilterPhase                    `json:"phase"`
	Fields             []string                       `json:"fields,omitempty"`
	RelationshipRoutes []RelationshipRoute            `json:"relationship_routes,omitempty"`
	MatchGuard         bool                           `json:"match_guard,omitempty"`
	FieldRoutes        map[string][]RelationshipRoute `json:"field_routes,omitempty"`
}

type AggregateMetrics struct {
	NodeMeta
	Input       string         `json:"input"`
	GroupBy     []string       `json:"group_by,omitempty"`
	TimeBuckets []TimeBucket   `json:"time_buckets,omitempty"`
	Spatial     *SpatialBucket `json:"spatial,omitempty"`
	Metrics     []MetricSpec   `json:"metrics"`
}

// SpatialBucket is the typed, renderer-independent Web-Mercator bucketing
// operation used by spatial aggregate plans. Latitude and Longitude are
// logical field references available on the aggregate input; the renderer
// lowers them to globally aligned cell indexes at the requested zoom.
type SpatialBucket struct {
	Latitude   string `json:"latitude"`
	Longitude  string `json:"longitude"`
	Zoom       int    `json:"zoom"`
	CellPixels int    `json:"cell_pixels"`
}

type TimeBucket struct {
	Field      string `json:"field"`
	Grain      string `json:"grain"`
	Timezone   string `json:"timezone,omitempty"`
	WeekStart  string `json:"week_start,omitempty"`
	DateTimeTZ bool   `json:"datetime_tz,omitempty"`
}

func (AggregateMetrics) Kind() Kind         { return KindAggregateMetrics }
func (n AggregateMetrics) Meta() NodeMeta   { return n.NodeMeta }
func (n AggregateMetrics) Inputs() []string { return []string{n.Input} }
func (AggregateMetrics) nodeMarker()        {}

// StitchAggregates combines independently reduced roots at a conformed grain.
// Keys are normatively null-safe (equivalent to SQL IS NOT DISTINCT FROM),
// including null-valued dimension members and the synthetic scalar key.
type StitchAggregates struct {
	NodeMeta
	InputsList []string `json:"inputs"`
	Keys       []string `json:"keys,omitempty"`
}

func (StitchAggregates) Kind() Kind         { return KindStitchAggregates }
func (n StitchAggregates) Meta() NodeMeta   { return n.NodeMeta }
func (n StitchAggregates) Inputs() []string { return append([]string(nil), n.InputsList...) }
func (StitchAggregates) nodeMarker()        {}

type ComputeRatio struct {
	NodeMeta
	Input       string `json:"input"`
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Output      string `json:"output"`
}

func (ComputeRatio) Kind() Kind         { return KindComputeRatio }
func (n ComputeRatio) Meta() NodeMeta   { return n.NodeMeta }
func (n ComputeRatio) Inputs() []string { return []string{n.Input} }
func (ComputeRatio) nodeMarker()        {}

type ComputeDerived struct {
	NodeMeta
	Input      string     `json:"input"`
	Output     string     `json:"output"`
	Expression ScalarExpr `json:"expression"`
}

func (ComputeDerived) Kind() Kind         { return KindComputeDerived }
func (n ComputeDerived) Meta() NodeMeta   { return n.NodeMeta }
func (n ComputeDerived) Inputs() []string { return []string{n.Input} }
func (ComputeDerived) nodeMarker()        {}

type SortKey struct {
	Field      string `json:"field"`
	Descending bool   `json:"descending,omitempty"`
}

// Projection is the final typed output mapping applied by SortLimit. Source
// names refer only to fields/metrics exposed by the input node; arbitrary SQL
// expressions are intentionally not representable here.
type Projection struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Mask   string `json:"mask,omitempty"`
}

type SortLimit struct {
	NodeMeta
	Input      string       `json:"input"`
	Sort       []SortKey    `json:"sort,omitempty"`
	Projection []Projection `json:"projection,omitempty"`
	Limit      int          `json:"limit,omitempty"`
	Offset     int          `json:"offset,omitempty"`
}

func (SortLimit) Kind() Kind         { return KindSortLimit }
func (n SortLimit) Meta() NodeMeta   { return n.NodeMeta }
func (n SortLimit) Inputs() []string { return []string{n.Input} }
func (SortLimit) nodeMarker()        {}

type BundleBranches struct {
	NodeMeta
	// Branches retain consumer identity and authored order. Their metadata
	// remains on each input node; this node is an explicit heterogeneous
	// envelope and therefore has no homogeneous grain or projection.
	Branches []BundleBranch `json:"branches"`
}

type BundleBranch struct {
	ID      string `json:"id"`
	Ordinal int    `json:"ordinal"`
	Input   string `json:"input"`
}

func (BundleBranches) Kind() Kind       { return KindBundleBranches }
func (n BundleBranches) Meta() NodeMeta { return n.NodeMeta }
func (n BundleBranches) Inputs() []string {
	inputs := make([]string, len(n.Branches))
	for i, branch := range n.Branches {
		inputs[i] = branch.Input
	}
	return inputs
}
func (BundleBranches) nodeMarker() {}

// Graph is a DAG of typed nodes. Nodes is keyed by NodeMeta.NodeID. Output is
// the node whose result is returned; Roots is optional and, when present,
// documents the intended source roots.
type Graph struct {
	NodeMeta
	Nodes  map[string]Node `json:"nodes"`
	Roots  []string        `json:"roots,omitempty"`
	Output string          `json:"output"`
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
