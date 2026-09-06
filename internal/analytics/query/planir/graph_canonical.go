package planir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type canonicalGraph struct {
	Meta   NodeMeta        `json:"meta"`
	Roots  []string        `json:"roots,omitempty"`
	Output string          `json:"output"`
	Nodes  []canonicalNode `json:"nodes"`
}

type canonicalNode struct {
	Kind Kind            `json:"kind"`
	Meta NodeMeta        `json:"meta"`
	Data json.RawMessage `json:"data"`
}

func (g *Graph) Canonical() ([]byte, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g.canonicalValidated()
}

// canonicalValidated serializes a graph whose caller has already validated
// it. Keeping validation outside this helper lets one request derive several
// identity projections without walking the same immutable graph repeatedly.
func (g *Graph) canonicalValidated() ([]byte, error) {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := canonicalGraph{Meta: canonicalMeta(g.NodeMeta), Roots: sortedStrings(g.Roots), Output: g.Output}
	for _, id := range ids {
		node := g.Nodes[id]
		data, err := canonicalData(node)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", id, err)
		}
		out.Nodes = append(out.Nodes, canonicalNode{Kind: node.Kind(), Meta: canonicalMeta(node.Meta()), Data: data})
	}
	return json.Marshal(out)
}

// DependencyCanonical returns the canonical logical plan used for result
// dependency identity. Execution targets are deliberately removed: their
// revisions are represented separately by relation evidence.
func (g *Graph) DependencyCanonical() ([]byte, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g.dependencyCanonicalValidated()
}

func (g *Graph) dependencyCanonicalValidated() ([]byte, error) {
	canonical, err := g.canonicalValidated()
	if err != nil {
		return nil, err
	}
	var projection any
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if err := decoder.Decode(&projection); err != nil {
		return nil, fmt.Errorf("decode canonical dependency plan: %w", err)
	}
	removeExecutionTargets(projection)
	return json.Marshal(projection)
}

func removeExecutionTargets(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "relation")
		delete(typed, "from_relation")
		delete(typed, "to_relation")
		for _, child := range typed {
			removeExecutionTargets(child)
		}
	case []any:
		for _, child := range typed {
			removeExecutionTargets(child)
		}
	}
}

func canonicalData(node Node) (json.RawMessage, error) {
	var value any
	switch n := node.(type) {
	case *ScanDataset:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *TraverseRelationship:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *FilterRows:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *AggregateMetrics:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *StitchAggregates:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *ComputeRatio:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *ComputeDerived:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *SortLimit:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *TotalRows:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *BundleBranches:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *SpatialEnvelope:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case *AnalyticalEnvelope:
		if n == nil {
			return nil, fmt.Errorf("node is nil")
		}
		return canonicalData(*n)
	case ScanDataset:
		value = struct {
			Dataset  string `json:"dataset"`
			Relation string `json:"relation,omitempty"`
		}{n.Dataset, n.Relation}
	case TraverseRelationship:
		value = struct {
			Input string           `json:"input"`
			Path  RelationshipPath `json:"path"`
		}{n.Input, canonicalPath(n.Path)}
	case FilterRows:
		predicate := canonicalPredicate(n.Predicate)
		fieldRoutes := canonicalFieldRoutes(n.FieldRoutes)
		value = struct {
			Input       string                         `json:"input"`
			Predicate   Predicate                      `json:"predicate"`
			Source      FilterSource                   `json:"source"`
			Name        string                         `json:"name,omitempty"`
			Fields      []string                       `json:"fields,omitempty"`
			MatchGuard  bool                           `json:"match_guard,omitempty"`
			FieldRoutes map[string][]RelationshipRoute `json:"field_routes,omitempty"`
		}{n.Input, predicate, n.Source, n.Name, sortedStrings(n.Fields), n.MatchGuard, fieldRoutes}
	case AggregateMetrics:
		metrics := append([]MetricSpec(nil), n.Metrics...)
		for i := range metrics {
			metrics[i] = canonicalMetricSpec(metrics[i])
		}
		sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
		value = struct {
			Input       string         `json:"input"`
			GroupBy     []string       `json:"group_by,omitempty"`
			TimeBuckets []TimeBucket   `json:"time_buckets,omitempty"`
			Spatial     *SpatialBucket `json:"spatial,omitempty"`
			Metrics     []MetricSpec   `json:"metrics"`
		}{n.Input, append([]string(nil), n.GroupBy...), append([]TimeBucket(nil), n.TimeBuckets...), n.Spatial, metrics}
	case StitchAggregates:
		value = struct {
			Inputs []string `json:"inputs"`
			Keys   []string `json:"keys,omitempty"`
		}{sortedStrings(n.InputsList), append([]string(nil), n.Keys...)}
	case ComputeRatio:
		value = struct {
			Input       string `json:"input"`
			Numerator   string `json:"numerator"`
			Denominator string `json:"denominator"`
			Output      string `json:"output"`
		}{n.Input, n.Numerator, n.Denominator, n.Output}
	case ComputeDerived:
		expression := canonicalScalar(n.Expression)
		value = struct {
			Input      string     `json:"input"`
			Output     string     `json:"output"`
			Expression ScalarExpr `json:"expression"`
		}{n.Input, n.Output, expression}
	case SortLimit:
		value = struct {
			Input      string       `json:"input"`
			Sort       []SortKey    `json:"sort,omitempty"`
			Projection []Projection `json:"projection,omitempty"`
			Limit      int          `json:"limit,omitempty"`
			Offset     int          `json:"offset,omitempty"`
		}{n.Input, append([]SortKey(nil), n.Sort...), append([]Projection(nil), n.Projection...), n.Limit, n.Offset}
	case TotalRows:
		value = struct {
			Input      string `json:"input"`
			TotalField string `json:"total_field"`
		}{n.Input, n.TotalField}
	case BundleBranches:
		value = struct {
			Branches []BundleBranch `json:"branches"`
		}{append([]BundleBranch(nil), n.Branches...)}
	case SpatialEnvelope:
		value = struct {
			Operation        SpatialEnvelopeOperation `json:"operation"`
			Input            string                   `json:"input,omitempty"`
			Inputs           []string                 `json:"inputs,omitempty"`
			Latitude         string                   `json:"latitude,omitempty"`
			Longitude        string                   `json:"longitude,omitempty"`
			Metrics          []string                 `json:"metrics,omitempty"`
			MetricProperties []SpatialProperty        `json:"metric_properties,omitempty"`
			Properties       []SpatialProperty        `json:"properties,omitempty"`
			Identity         []string                 `json:"identity,omitempty"`
			Zoom             int                      `json:"zoom,omitempty"`
			TargetZoom       int                      `json:"target_zoom,omitempty"`
			CellPixels       int                      `json:"cell_pixels,omitempty"`
			Buffer           int                      `json:"buffer,omitempty"`
			FeatureCap       int                      `json:"feature_cap,omitempty"`
			MaximumBytes     int64                    `json:"maximum_bytes,omitempty"`
			RawMinimumZoom   int                      `json:"raw_minimum_zoom,omitempty"`
			MaximumZoom      int                      `json:"maximum_zoom,omitempty"`
		}{n.Operation, n.Input, append([]string(nil), n.InputsList...), n.Latitude, n.Longitude, append([]string(nil), n.Metrics...), append([]SpatialProperty(nil), n.MetricProperties...), append([]SpatialProperty(nil), n.Properties...), append([]string(nil), n.Identity...), n.Zoom, n.TargetZoom, n.CellPixels, n.Buffer, n.FeatureCap, n.MaximumBytes, n.RawMinimumZoom, n.MaximumZoom}
	case AnalyticalEnvelope:
		sortKeys := append([]SortKey(nil), n.Sort...)
		value = struct {
			Operation           AnalyticalEnvelopeOperation `json:"operation"`
			Input               string                      `json:"input"`
			Value               string                      `json:"value"`
			ValueType           string                      `json:"value_type"`
			Group               string                      `json:"group,omitempty"`
			BinCount            int                         `json:"bin_count,omitempty"`
			DomainMinimum       *float64                    `json:"domain_minimum,omitempty"`
			DomainMaximum       *float64                    `json:"domain_maximum,omitempty"`
			NullPolicy          string                      `json:"null_policy,omitempty"`
			Approximation       string                      `json:"approximation,omitempty"`
			Quantiles           []float64                   `json:"quantiles,omitempty"`
			WhiskerLower        *float64                    `json:"whisker_lower,omitempty"`
			WhiskerUpper        *float64                    `json:"whisker_upper,omitempty"`
			Outliers            string                      `json:"outliers,omitempty"`
			DistributionColumns []string                    `json:"distribution_columns,omitempty"`
			Sort                []SortKey                   `json:"sort,omitempty"`
			Limit               int                         `json:"limit,omitempty"`
		}{n.Operation, n.Input, n.Value, n.ValueType, n.Group, n.BinCount, n.DomainMinimum, n.DomainMaximum, n.NullPolicy, n.Approximation, append([]float64(nil), n.Quantiles...), n.WhiskerLower, n.WhiskerUpper, n.Outliers, append([]string(nil), n.DistributionColumns...), sortKeys, n.Limit}
	default:
		return nil, fmt.Errorf("unsupported node kind %q", node.Kind())
	}
	return json.Marshal(value)
}

func canonicalMetricSpec(metric MetricSpec) MetricSpec {
	out := metric
	out.Inputs = append([]string(nil), metric.Inputs...)
	out.Filters = append([]AggregateFilter(nil), metric.Filters...)
	for i := range out.Filters {
		out.Filters[i].Predicate = canonicalPredicate(out.Filters[i].Predicate)
		out.Filters[i].Fields = sortedStrings(out.Filters[i].Fields)
		out.Filters[i].RelationshipRoutes = canonicalRelationshipRoutes(out.Filters[i].RelationshipRoutes)
	}
	sort.Slice(out.Filters, func(i, j int) bool {
		if out.Filters[i].Name != out.Filters[j].Name {
			return out.Filters[i].Name < out.Filters[j].Name
		}
		return out.Filters[i].Phase < out.Filters[j].Phase
	})
	return out
}

func canonicalRelationshipRoutes(routes []RelationshipRoute) []RelationshipRoute {
	out := append([]RelationshipRoute(nil), routes...)
	for i := range out {
		out[i].Edges = append([]RelationshipPath(nil), out[i].Edges...)
		for j := range out[i].Edges {
			out[i].Edges[j] = canonicalPath(out[i].Edges[j])
		}
	}
	sort.Slice(out, func(i, j int) bool { return relationshipRouteKey(out[i]) < relationshipRouteKey(out[j]) })
	return out
}

func canonicalFieldRoutes(values map[string][]RelationshipRoute) map[string][]RelationshipRoute {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string][]RelationshipRoute, len(values))
	for _, key := range keys {
		out[key] = canonicalRelationshipRoutes(values[key])
	}
	return out
}

func canonicalMetricSpecs(metrics []MetricSpec) []MetricSpec {
	out := make([]MetricSpec, len(metrics))
	for i, metric := range metrics {
		out[i] = canonicalMetricSpec(metric)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func canonicalPredicate(predicate Predicate) Predicate {
	out := predicate
	if predicate.Spatial != nil {
		spatial := *predicate.Spatial
		spatial.Points = append([]SpatialPoint(nil), predicate.Spatial.Points...)
		out.Spatial = &spatial
	}
	out.Values = append([]Literal(nil), predicate.Values...)
	if predicate.Kind == PredicateIn {
		sort.Slice(out.Values, func(i, j int) bool { return literalKey(out.Values[i]) < literalKey(out.Values[j]) })
	}
	out.Children = make([]Predicate, len(predicate.Children))
	for i, child := range predicate.Children {
		out.Children[i] = canonicalPredicate(child)
	}
	if predicate.Kind == PredicateAnd || predicate.Kind == PredicateOr {
		sort.Slice(out.Children, func(i, j int) bool {
			left, _ := json.Marshal(out.Children[i])
			right, _ := json.Marshal(out.Children[j])
			return string(left) < string(right)
		})
	}
	return out
}

func canonicalScalar(expression ScalarExpr) ScalarExpr {
	out := expression
	out.Children = make([]ScalarExpr, len(expression.Children))
	for i, child := range expression.Children {
		out.Children[i] = canonicalScalar(child)
	}
	return out
}

func literalKey(value Literal) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func canonicalMeta(meta NodeMeta) NodeMeta {
	out := meta
	out.AvailableFields = append([]Field(nil), meta.AvailableFields...)
	sort.Slice(out.AvailableFields, func(i, j int) bool {
		if out.AvailableFields[i].Name == out.AvailableFields[j].Name {
			return out.AvailableFields[i].Type < out.AvailableFields[j].Type
		}
		return out.AvailableFields[i].Name < out.AvailableFields[j].Name
	})
	out.AvailableMetrics = append([]Metric(nil), meta.AvailableMetrics...)
	sort.Slice(out.AvailableMetrics, func(i, j int) bool {
		if out.AvailableMetrics[i].Name == out.AvailableMetrics[j].Name {
			if out.AvailableMetrics[i].Type == out.AvailableMetrics[j].Type {
				return out.AvailableMetrics[i].Empty < out.AvailableMetrics[j].Empty
			}
			return out.AvailableMetrics[i].Type < out.AvailableMetrics[j].Type
		}
		return out.AvailableMetrics[i].Name < out.AvailableMetrics[j].Name
	})
	out.RootDatasets = sortedStrings(meta.RootDatasets)
	out.PhysicalLineage = append([]PhysicalLineage(nil), meta.PhysicalLineage...)
	for i := range out.PhysicalLineage {
		out.PhysicalLineage[i].Route = append([]string(nil), out.PhysicalLineage[i].Route...)
	}
	sort.Slice(out.PhysicalLineage, func(i, j int) bool {
		a, b := out.PhysicalLineage[i], out.PhysicalLineage[j]
		if a.Logical != b.Logical {
			return a.Logical < b.Logical
		}
		if a.Dataset != b.Dataset {
			return a.Dataset < b.Dataset
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		return strings.Join(a.Route, "/") < strings.Join(b.Route, "/")
	})
	out.RelationshipRoutes = append([]RelationshipRoute(nil), meta.RelationshipRoutes...)
	for i := range out.RelationshipRoutes {
		out.RelationshipRoutes[i].Edges = append([]RelationshipPath(nil), out.RelationshipRoutes[i].Edges...)
		for j := range out.RelationshipRoutes[i].Edges {
			out.RelationshipRoutes[i].Edges[j] = canonicalPath(out.RelationshipRoutes[i].Edges[j])
		}
	}
	sort.Slice(out.RelationshipRoutes, func(i, j int) bool {
		return relationshipRouteKey(out.RelationshipRoutes[i]) < relationshipRouteKey(out.RelationshipRoutes[j])
	})
	return out
}

func canonicalPath(path RelationshipPath) RelationshipPath {
	out := path
	out.JoinKeys = append([]JoinKey(nil), path.JoinKeys...)
	return out
}

func relationshipRouteKey(route RelationshipRoute) string {
	parts := make([]string, len(route.Edges))
	for i, edge := range route.Edges {
		parts[i] = edge.Name + ":" + edge.FromDataset + ">" + edge.ToDataset
	}
	return route.RootDataset + ":" + strings.Join(parts, "/")
}

// Fingerprint returns the SHA-256 digest of Canonical in lowercase hex.
func (g *Graph) Fingerprint() (string, error) {
	canonical, err := g.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// DependencyFingerprint returns the target-independent SHA-256 fingerprint
// used by result identity. Fingerprint remains the executable-plan identity.
func (g *Graph) DependencyFingerprint() (string, error) {
	canonical, err := g.DependencyCanonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// Dependencies is the complete physical scope proven by a validated graph.
// PhysicalFields use the stable dataset.field spelling consumed by serving
// state and relationship paths use root:edge,edge signatures. All collections
// are sorted and de-duplicated, so callers can safely use them as cache keys
// or authorization inputs without performing a second semantic resolution.
type Dependencies struct {
	Datasets          []string `json:"datasets,omitempty"`
	PhysicalFields    []string `json:"physical_fields,omitempty"`
	RelationshipPaths []string `json:"relationship_paths,omitempty"`
}

// Dependencies derives lineage from the graph itself. Validation runs first;
// a renderer or caller must never infer scope from an unvalidated node graph.
