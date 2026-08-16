package http

import (
	"context"
	nethttp "net/http"

	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/navigation"
	"github.com/flidai/leapview/internal/project/ui"
)

type Metrics interface {
	Catalog() navigation.Catalog
	DataExplorerModel(modelID string) (DataExplorerModel, bool)
	ExecuteDataPreview(ctx context.Context, request DataPreviewRequest) (DataPreviewResult, error)
	ExecuteDataExplore(ctx context.Context, request DataExploreRequest) (DataExploreResult, error)
}

type DataExploreRequest struct {
	ProjectID  string
	ModelID    string
	DatasetID  string
	Dimensions []string
	Measures   []string
	Time       DataExploreTime
	Filters    []DataExploreFilter
	Sort       []DataExploreSort
	Limit      int
}

type DataExploreTime struct {
	Field string
	Grain string
	Alias string
}

type DataExploreFilter struct {
	Field    string
	Fact     string
	Operator string
	Values   []string
}

type DataExploreSort struct {
	Field     string
	Direction string
}

type DataExploreResult struct {
	Columns      []string
	Rows         []map[string]any
	SQL          string
	Plan         string
	DurationMS   int64
	RowsReturned int
	Truncated    bool
	Warnings     []string
}

type DataPreviewRequest struct {
	ProjectID    string
	ObjectKey    string
	Layer        string
	ModelID      string
	Table        string
	Columns      []string
	SortColumn   string
	Direction    string
	Offset       int
	Limit        int
	IncludeTotal bool
}

type DataPreviewResult struct {
	Rows           []map[string]any
	TotalRows      int
	TotalRowsKnown bool
	SQL            string
}

type DataExplorerModel struct {
	ID            string
	Title         string
	Description   string
	Sources       map[string]DataExplorerSource
	Tables        map[string]DataExplorerTable
	Measures      map[string]DataExplorerMeasure
	Relationships []DataExplorerRelationship
}

type DataExplorerSource struct {
	Fields  map[string]DataExplorerField
	Columns []DataExplorerColumn
}

type DataExplorerTable struct {
	Description string
	Grain       string
	Dimensions  map[string]DataExplorerField
	Columns     map[string]DataExplorerField
	Schema      []DataExplorerColumn
}

type DataExplorerMeasure struct {
	Name        string
	Label       string
	Description string
	Fact        string
	Type        string
	Hidden      bool
}

type DataExplorerRelationship struct {
	ID          string
	Description string
	From        string
	To          string
	Cardinality string
}

type DataExplorerField struct {
	Name        string
	Label       string
	Type        string
	Description string
}

type DataExplorerColumn struct {
	Name         string
	PhysicalType string
	Ordinal      int
	Nullable     *bool
	Default      string
	Comment      string
	PrimaryKey   bool
}

type DevelopCatalogReader interface {
	ActiveAssetCatalog(ctx context.Context, projectID projectgraph.ResourceID, environment string) (project.DevelopCatalog, bool, error)
}

type RefreshStateProvider interface {
	AssetRefreshState(ctx context.Context, projectID, environment string, asset project.DevelopAssetView) (ui.AssetRefreshState, error)
	AssetVersionsState(ctx context.Context, projectID, environment string, asset project.DevelopAssetView, section string) (ui.AssetVersionsState, error)
}

type AssetRefreshRunner interface {
	RefreshAsset(ctx context.Context, input AssetRefreshInput) error
	RetryAsset(ctx context.Context, input AssetRefreshInput, retryOf string) error
	CancelRefreshRun(ctx context.Context, input PipelineRunCancelInput) error
}

type AssetRefreshInput struct {
	Request   *nethttp.Request
	ProjectID string
	Asset     project.DevelopAssetView
	Assets    []project.DevelopAssetView
	Edges     []project.DevelopEdgeView
}

type PipelineRunCancelInput struct {
	Request    *nethttp.Request
	ProjectID  string
	PipelineID string
	RunID      string
}
