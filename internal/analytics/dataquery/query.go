package dataquery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Kind string

const (
	KindSemanticAggregate         Kind = "semantic_aggregate"
	KindSemanticRows              Kind = "semantic_rows"
	KindModelTableRows            Kind = "model_table_rows"
	KindSemanticHistogram         Kind = "semantic_histogram"
	KindSemanticDistribution      Kind = "semantic_distribution"
	KindSemanticSpatialTile       Kind = "semantic_spatial_tile"
	KindSemanticSpatialTileBudget Kind = "semantic_spatial_tile_budget"
	KindSemanticSpatialMetadata   Kind = "semantic_spatial_metadata"
)

type Query struct {
	ProjectID     projectgraph.ResourceID
	Surface       string
	Operation     string
	PrincipalID   string
	RequestID     string
	ObjectType    string
	ObjectID      string
	CorrelationID string
	CandidateID   string
	// EffectivePolicyFingerprint identifies the complete authorized policy
	// boundary after principal, candidate restrictions, RLS, and masking are
	// resolved. It is intentionally distinct from request/audit identity.
	EffectivePolicyFingerprint string
	ModelID                    string
	Kind                       Kind
	Target                     string
	Fields                     []Field
	Measures                   []Field
	// AuthorizationFields preserves the logical projection used to authorize a
	// physical query whose result shape intentionally omits those fields (for
	// example, an exact COUNT for a governed dashboard table). Executors and
	// planners must not project these fields.
	AuthorizationFields []Field
	Value               Field
	Time                Time
	Filters             []Filter
	Sort                []Sort
	ColumnMasks         []ColumnMask
	Offset              int
	Limit               int
	BinCount            int
	IncludeTotal        bool
	SpatialTile         *SpatialTile
	SpatialTileBudget   *SpatialTileBudget
	SpatialMetadata     *SpatialMetadata
}

type SpatialTilePrecision string

const (
	SpatialTilePrecisionRaw        SpatialTilePrecision = "raw"
	SpatialTilePrecisionAggregated SpatialTilePrecision = "aggregated"
)

type SpatialTile struct {
	Latitude     Field
	Longitude    Field
	Identity     []Field
	Zoom         int
	TargetZoom   int
	MetatileX    int
	MetatileY    int
	MetatileSize int
	CellPixels   int
	Buffer       int
	FeatureCap   int
	Precision    SpatialTilePrecision
}

// SpatialTileBudget describes one revision-wide raw-precision probe. Unlike a
// metatile query, it evaluates every governed XYZ tile at Zoom and returns only
// the maximum feature count and encoded MVT byte length.
type SpatialTileBudget struct {
	Latitude     Field
	Longitude    Field
	Identity     []Field
	Zoom         int
	Buffer       int
	FeatureCap   int
	MaximumBytes int64
}

type SpatialMetadata struct {
	Latitude       Field
	Longitude      Field
	FeatureCap     int
	RawMinimumZoom int
	MaximumZoom    int
}

type Field struct {
	Field string
	Alias string
}

type Time struct {
	Field string
	Grain string
	Alias string
}

type Filter struct {
	Field    string
	Fact     string
	Operator string
	Values   []any
	Groups   []FilterGroup
	Spatial  *SpatialFilter
}

type SpatialFilter struct {
	Kind           string
	LatitudeField  string
	LongitudeField string
	Fact           string
	West           float64
	South          float64
	East           float64
	North          float64
	Points         []SpatialPoint
	Center         SpatialPoint
	RadiusMeters   float64
}

type SpatialPoint struct {
	Longitude float64
	Latitude  float64
}

type FilterGroup struct {
	Filters []Filter
}

type Sort struct {
	Field     string
	Direction string
}

type ColumnMask struct {
	Field string
	Mask  string
}

type Column struct {
	Name string
}

type Row map[string]any

type Result struct {
	Columns          []Column
	Rows             []Row
	TotalRows        int
	TotalRowsKnown   bool
	SQL              string
	PlanText         string
	DurationMS       int64
	QueueWaitMS      int64
	PlanningMS       int64
	ConnectionWaitMS int64
	DatabaseMS       int64
	ExecutionMS      int64
	ExecutionState   string
	CacheOutcome     string
	RowsReturned     int
	BytesEstimate    int64
	Status           string
	Error            string
	Warnings         []string
}

// BundleRequest identifies one independently decoded aggregate consumer. A
// bundle executor must govern every Query before deciding whether the requests
// are physically compatible.
type BundleRequest struct {
	ID    string
	Query Query
}

type BundleResult struct {
	Results map[string]Result
	SQL     string
}

type BundleExecutor interface {
	ExecuteDataQueryBundle(context.Context, []BundleRequest) (BundleResult, error)
}

type BundleIncompatibleError struct{ Err error }

func (e *BundleIncompatibleError) Error() string {
	return "data query bundle is incompatible: " + e.Err.Error()
}
func (e *BundleIncompatibleError) Unwrap() error { return e.Err }

func IsBundleIncompatible(err error) bool {
	var target *BundleIncompatibleError
	return errors.As(err, &target)
}

type BundleBranchError struct {
	ID  string
	Err error
}

func (e *BundleBranchError) Error() string {
	return fmt.Sprintf("data query bundle branch %q: %v", e.ID, e.Err)
}
func (e *BundleBranchError) Unwrap() error { return e.Err }

const (
	SurfaceDashboard       = "dashboard"
	SurfaceAPI             = "api"
	SurfaceAgent           = "agent"
	SurfaceCLI             = "cli"
	SurfaceDataExplorer    = "data_explorer"
	SurfacePublicDashboard = "public_dashboard"

	OperationDashboardAggregate         = "dashboard_aggregate"
	OperationDashboardRows              = "dashboard_rows"
	OperationDashboardCount             = "dashboard_count"
	OperationDashboardHistogram         = "dashboard_histogram"
	OperationDashboardDistribution      = "dashboard_distribution"
	OperationDashboardFilterOptions     = "dashboard_filter_options"
	OperationDashboardSpatialTile       = "dashboard_spatial_tile"
	OperationDashboardSpatialTileBudget = "dashboard_spatial_tile_budget"
	OperationDashboardSpatialMetadata   = "dashboard_spatial_metadata"
	OperationAPIQuery                   = "api_query"
	OperationAPIPreview                 = "api_preview"
	OperationAgentQuery                 = "agent_query"
	OperationPreviewWindow              = "preview_window"
	OperationSemanticExplore            = "semantic_explore"

	StatusSuccess  = "success"
	StatusError    = "error"
	StatusCanceled = "canceled"
	StatusTimeout  = "timeout"

	ExecutionStarted   = "started"
	ExecutionRejected  = "rejected"
	ExecutionCanceled  = "canceled"
	ExecutionTimeout   = "timeout"
	ExecutionSucceeded = "succeeded"
	ExecutionFailed    = "failed"

	CacheHit       = "hit"
	CacheMiss      = "miss"
	CacheCoalesced = "coalesced"
	CacheError     = "error"
)

type Metadata struct {
	ProjectID     projectgraph.ResourceID
	Surface       string
	Operation     string
	PrincipalID   string
	RequestID     string
	ObjectType    string
	ObjectID      string
	CorrelationID string
	StreamID      string
}

type metadataContextKey struct{}

func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	return context.WithValue(ctx, metadataContextKey{}, metadata)
}

func MetadataFromContext(ctx context.Context) Metadata {
	metadata, _ := ctx.Value(metadataContextKey{}).(Metadata)
	return metadata
}

func (q Query) WithMetadata(metadata Metadata) Query {
	if metadata.ProjectID != "" {
		q.ProjectID = metadata.ProjectID
	}
	if metadata.Surface != "" {
		q.Surface = metadata.Surface
	}
	if metadata.Operation != "" {
		q.Operation = metadata.Operation
	}
	if metadata.PrincipalID != "" {
		q.PrincipalID = metadata.PrincipalID
	}
	if metadata.RequestID != "" {
		q.RequestID = metadata.RequestID
	}
	if metadata.ObjectType != "" {
		q.ObjectType = metadata.ObjectType
	}
	if metadata.ObjectID != "" {
		q.ObjectID = metadata.ObjectID
	}
	if metadata.CorrelationID != "" {
		q.CorrelationID = metadata.CorrelationID
	}
	return q
}

func SemanticAggregate(modelID, target string, fields, measures []Field, filters []Filter, sort []Sort, offset, limit int) Query {
	return Query{ModelID: modelID, Kind: KindSemanticAggregate, Target: target, Fields: fields, Measures: measures, Filters: filters, Sort: sort, Offset: offset, Limit: limit}
}

func SemanticRows(modelID, target string, fields, measures []Field, filters []Filter, sort []Sort, offset, limit int, includeTotal bool) Query {
	return Query{ModelID: modelID, Kind: KindSemanticRows, Target: target, Fields: fields, Measures: measures, Filters: filters, Sort: sort, Offset: offset, Limit: limit, IncludeTotal: includeTotal}
}

func ModelTableRows(modelID, table string, columns []string, sort []Sort, offset, limit int, includeTotal bool) Query {
	return Query{ModelID: modelID, Kind: KindModelTableRows, Target: table, Fields: fieldsFromNames(columns), Sort: sort, Offset: offset, Limit: limit, IncludeTotal: includeTotal}
}

func SemanticHistogram(modelID, target string, dimensions []Field, measure Field, filters []Filter, binCount int) Query {
	return Query{ModelID: modelID, Kind: KindSemanticHistogram, Target: target, Fields: dimensions, Value: measure, Filters: filters, BinCount: binCount}
}

func SemanticDistribution(modelID, target string, dimensions []Field, measure Field, filters []Filter, sort []Sort, limit int) Query {
	return Query{ModelID: modelID, Kind: KindSemanticDistribution, Target: target, Fields: dimensions, Value: measure, Filters: filters, Sort: sort, Limit: limit}
}

func (q Query) Validate() error {
	if strings.TrimSpace(q.ModelID) == "" {
		return fmt.Errorf("data query requires model id")
	}
	if q.Offset < 0 {
		return fmt.Errorf("data query offset must be non-negative")
	}
	if q.Limit < 0 {
		return fmt.Errorf("data query limit must be non-negative")
	}
	for _, sort := range q.Sort {
		if strings.TrimSpace(sort.Field) == "" {
			return fmt.Errorf("data query sort field is required")
		}
		direction := strings.ToLower(strings.TrimSpace(sort.Direction))
		if direction != "" && direction != "asc" && direction != "desc" {
			return fmt.Errorf("unsupported sort direction %q", sort.Direction)
		}
	}
	switch q.Kind {
	case KindSemanticAggregate, KindSemanticRows:
		if len(q.Fields) == 0 && len(q.Measures) == 0 && q.Time.Field == "" && !(q.Kind == KindSemanticRows && q.IncludeTotal) {
			return fmt.Errorf("%s query requires at least one selected field", q.Kind)
		}
	case KindModelTableRows:
		if strings.TrimSpace(q.Target) == "" {
			return fmt.Errorf("%s query requires target", q.Kind)
		}
	case KindSemanticHistogram, KindSemanticDistribution:
		if strings.TrimSpace(q.Target) == "" {
			return fmt.Errorf("%s query requires target", q.Kind)
		}
		if strings.TrimSpace(q.Value.Field) == "" {
			return fmt.Errorf("%s query requires a value field", q.Kind)
		}
		if q.Kind == KindSemanticHistogram && q.BinCount <= 0 {
			return fmt.Errorf("semantic histogram query requires a positive bin count")
		}
	case KindSemanticSpatialTile:
		if len(q.Fields) == 0 || q.SpatialTile == nil {
			return fmt.Errorf("semantic spatial tile query requires selected fields and tile coordinates")
		}
		tile := q.SpatialTile
		if strings.TrimSpace(tile.Latitude.Field) == "" || strings.TrimSpace(tile.Longitude.Field) == "" || tile.MetatileSize <= 0 || tile.FeatureCap <= 0 {
			return fmt.Errorf("semantic spatial tile query requires coordinates, metatile size, and feature cap")
		}
		if tile.Precision != SpatialTilePrecisionRaw && tile.Precision != SpatialTilePrecisionAggregated {
			return fmt.Errorf("unsupported semantic spatial tile precision %q", tile.Precision)
		}
	case KindSemanticSpatialTileBudget:
		if len(q.Fields) == 0 || q.SpatialTileBudget == nil {
			return fmt.Errorf("semantic spatial tile budget query requires selected fields and a budget probe")
		}
		budget := q.SpatialTileBudget
		if strings.TrimSpace(budget.Latitude.Field) == "" || strings.TrimSpace(budget.Longitude.Field) == "" || budget.Zoom < 0 || budget.Zoom > 18 || budget.Buffer < 0 || budget.FeatureCap <= 0 || budget.MaximumBytes <= 0 {
			return fmt.Errorf("semantic spatial tile budget query requires coordinates, zoom, buffer, and feature cap")
		}
	case KindSemanticSpatialMetadata:
		if len(q.Fields) == 0 || q.SpatialMetadata == nil || strings.TrimSpace(q.SpatialMetadata.Latitude.Field) == "" || strings.TrimSpace(q.SpatialMetadata.Longitude.Field) == "" {
			return fmt.Errorf("semantic spatial metadata query requires selected fields and coordinates")
		}
		metadata := q.SpatialMetadata
		if metadata.FeatureCap <= 0 || metadata.RawMinimumZoom < 0 || metadata.MaximumZoom < metadata.RawMinimumZoom || metadata.MaximumZoom > 18 {
			return fmt.Errorf("semantic spatial metadata requires valid global raw-tile budgets")
		}
	default:
		return fmt.Errorf("unsupported data query kind %q", q.Kind)
	}
	return nil
}

func FieldNames(fields []Field) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		name := strings.TrimSpace(field.Field)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func ColumnsFromNames(names []string) []Column {
	out := make([]Column, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, Column{Name: name})
		}
	}
	return out
}

func fieldsFromNames(names []string) []Field {
	out := make([]Field, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, Field{Field: name})
		}
	}
	return out
}
