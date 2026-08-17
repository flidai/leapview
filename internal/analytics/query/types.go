package query

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

type Field struct {
	Field string
	Alias string
}

type resolvedAggregateMetric struct {
	Field         string
	Name          string
	Label         string
	Description   string
	Dataset       string
	Aggregation   string
	InputField    string
	Filters       []metricFilter
	NamedFilters  []CompiledNamedFilter
	Empty         string
	Unit          string
	Format        string
	TimeDimension string
}

type metricFilter struct {
	Field    string
	Operator string
	Values   []any
}

type Time struct {
	Field string
	Grain string
	Alias string
}

type Filter struct {
	Field        string
	Dataset      string
	Operator     string
	Values       []any
	Path         []string
	Not          bool
	RequireMatch bool
	MatchGuard   bool
	Groups       []FilterGroup
	Spatial      *SpatialFilter
}

type SpatialFilter struct {
	Kind           string
	LatitudeField  string
	LongitudeField string
	Dataset        string
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

type Request struct {
	Dataset     string
	Dimensions  []Field
	Metrics     []Field
	Time        Time
	Filters     []Filter
	Sort        []Sort
	ColumnMasks []ColumnMask
	Limit       int
	Offset      int
	// SpatialBucket replaces the selected coordinate dimensions with globally
	// aligned Web-Mercator cell indexes before semantic metric aggregation.
	// It is an internal governed planning primitive for vector tiles.
	SpatialBucket *SpatialBucket
}

type SpatialBucket struct {
	Latitude   Field
	Longitude  Field
	Zoom       int
	CellPixels int
}

type SpatialTileRequest struct {
	Dataset      string
	Metrics      []Field
	Filters      []Filter
	ColumnMasks  []ColumnMask
	Latitude     Field
	Longitude    Field
	Zoom         int
	TargetZoom   int
	MetatileX    int
	MetatileY    int
	MetatileSize int
	CellPixels   int
	Buffer       int
}

type SpatialTileRawRequest struct {
	Dataset      string
	Dimensions   []Field
	Metrics      []Field
	Identity     []Field
	Filters      []Filter
	ColumnMasks  []ColumnMask
	Time         Time
	Latitude     Field
	Longitude    Field
	Zoom         int
	MetatileX    int
	MetatileY    int
	MetatileSize int
	FeatureCap   int
	Buffer       int
}

type SpatialTileBudgetRequest struct {
	Dataset      string
	Dimensions   []Field
	Metrics      []Field
	Identity     []Field
	Filters      []Filter
	ColumnMasks  []ColumnMask
	Time         Time
	Latitude     Field
	Longitude    Field
	Zoom         int
	FeatureCap   int
	MaximumBytes int64
	Buffer       int
}

type SpatialMetadataRequest struct {
	Dataset        string
	Metrics        []Field
	Filters        []Filter
	ColumnMasks    []ColumnMask
	Latitude       Field
	Longitude      Field
	FeatureCap     int
	RawMinimumZoom int
	MaximumZoom    int
}

type RowRequest struct {
	Dataset     string
	Dimensions  []Field
	Metrics     []Field
	Filters     []Filter
	Sort        []Sort
	ColumnMasks []ColumnMask
	Limit       int
	Offset      int
}

type RawValueRequest struct {
	Dataset     string
	Dimensions  []Field
	Metric      Field
	Filters     []Filter
	Sort        []Sort
	ColumnMasks []ColumnMask
	Limit       int
}

type CountRequest struct {
	Dataset string
	Filters []Filter
}

type Plan struct {
	SQL                  string
	Args                 []any
	Columns              []string
	Mode                 string
	Datasets             []string
	StitchDimensions     []string
	PhysicalDependencies []string
	RelationshipPaths    []string
	// EffectiveOrdering is the total ordering applied to the result. Explicit
	// caller sorts are kept first; selected output columns complete ties.
	EffectiveOrdering []Sort
	// IR is the validated, renderer-independent graph used by every semantic
	// query path, including row, count, raw-value, aggregate, bundle, and
	// spatial planning boundaries.
	IR *planir.Graph
}

// Explain returns the deterministic typed plan explanation. SQL is included
// only as the renderer result; callers should use this for audit/debug output
// instead of depending on CTE naming or formatting details.
func (p Plan) Explain() (string, error) {
	if p.IR == nil {
		return "", fmt.Errorf("plan has no PlanIR")
	}
	return p.IR.Explain()
}

// BundleRequest is one independently shaped aggregate in a shared governed
// single-dataset scan. ID is an opaque consumer key and must be unique in a
// bundle.
type BundleRequest struct {
	ID      string
	Request Request
}

// BundlePlan is one physical statement containing independently shaped result
// branches over a common governed scan.
type BundlePlan struct {
	Plan     Plan
	Branches []BundleBranch
}

type BundleBranch struct {
	ID          string
	Ordinal     int
	Columns     []BundleColumn
	Fingerprint string
}

type BundleColumn struct {
	Output   string
	Physical string
}

const (
	BundleBranchColumn = "__bundle_branch"
	BundleRowColumn    = "__bundle_row"
)
