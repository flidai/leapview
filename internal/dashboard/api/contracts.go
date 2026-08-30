package api

import (
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type PageInfo struct {
	NextCursor string `json:"nextCursor"`
}

type PublicationStatus string

const (
	PublicationStatusActive       PublicationStatus = "active"
	PublicationStatusSuspended    PublicationStatus = "suspended"
	PublicationStatusUnconfigured PublicationStatus = "unconfigured"
)

type PublicationResponse struct {
	ActiveServingStateID *string           `json:"activeServingStateId,omitempty"`
	AllowedOrigins       []string          `json:"allowedOrigins"`
	Configured           bool              `json:"configured"`
	ConfiguredAt         *string           `json:"configuredAt,omitempty"`
	CreatedAt            string            `json:"createdAt"`
	Dashboard            string            `json:"dashboard"`
	DefaultPage          string            `json:"defaultPage"`
	DisabledAt           *string           `json:"disabledAt,omitempty"`
	EmbedURL             string            `json:"embedUrl"`
	IFrameSnippet        string            `json:"iframeSnippet"`
	Name                 string            `json:"name"`
	ProjectID            string            `json:"projectId"`
	Revision             int64             `json:"revision"`
	PublicURL            string            `json:"publicUrl"`
	RotatedAt            *string           `json:"rotatedAt,omitempty"`
	Status               PublicationStatus `json:"status"`
	SuspendedAt          *string           `json:"suspendedAt,omitempty"`
	SuspendedBy          *string           `json:"suspendedBy,omitempty"`
	UpdatedAt            string            `json:"updatedAt"`
}

type PublicationListResponse struct {
	Items []PublicationResponse `json:"items"`
}

type DashboardSummary struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	SemanticModel string   `json:"semanticModel"`
	Tags          []string `json:"tags"`
	PageCount     int      `json:"pageCount"`
}

type DashboardListResponse struct {
	Items []DashboardSummary `json:"items"`
	Page  PageInfo           `json:"page"`
}

type SemanticModelSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type SemanticModelListResponse struct {
	Items []SemanticModelSummary `json:"items"`
	Page  PageInfo               `json:"page"`
}

type ModelRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type DashboardManifestResponse struct {
	ID            string                  `json:"id"`
	Title         string                  `json:"title"`
	Description   string                  `json:"description,omitempty"`
	SemanticModel string                  `json:"semantic_model,omitempty"`
	Model         *ModelRef               `json:"model,omitempty"`
	Counts        DashboardManifestCounts `json:"counts"`
	Pages         []DashboardManifestPage `json:"pages"`
	DetailTools   map[string]string       `json:"detail_tools"`
}

type DashboardManifestCounts struct {
	Pages   int `json:"pages"`
	Visuals int `json:"visuals"`
	Filters int `json:"filters"`
}

type DashboardManifestPage struct {
	ID          string                       `json:"id"`
	Title       string                       `json:"title"`
	Description string                       `json:"description,omitempty"`
	Components  []DashboardManifestComponent `json:"components"`
}

type DashboardManifestComponent struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Title string `json:"title,omitempty"`
}

type DashboardComponentPlacement struct {
	Col     int `json:"col,omitempty"`
	Row     int `json:"row,omitempty"`
	ColSpan int `json:"colSpan,omitempty"`
	RowSpan int `json:"rowSpan,omitempty"`
}

type DashboardComponentResponse struct {
	ID          string                       `json:"id"`
	Kind        string                       `json:"kind"`
	Ref         string                       `json:"ref,omitempty"`
	Title       string                       `json:"title,omitempty"`
	Description string                       `json:"description,omitempty"`
	Placement   *DashboardComponentPlacement `json:"placement,omitempty"`
	X           float64                      `json:"x,omitempty"`
	Y           float64                      `json:"y,omitempty"`
	Width       float64                      `json:"width,omitempty"`
	Height      float64                      `json:"height,omitempty"`
	VisualID    string                       `json:"visualId,omitempty"`
	FilterID    string                       `json:"filterId,omitempty"`
}

type DashboardComponentListResponse struct {
	Items []DashboardComponentResponse `json:"items"`
	Page  PageInfo                     `json:"page"`
}

type DashboardPageResponse struct {
	ID          string                       `json:"id"`
	Title       string                       `json:"title"`
	Description string                       `json:"description,omitempty"`
	Components  []DashboardComponentResponse `json:"components"`
}

type DashboardTableColumn struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
}

type DashboardFilterPredicatePolicy struct {
	Kind      string   `json:"kind"`
	Operators []string `json:"operators"`
}

type DashboardFilterStaticOption struct {
	Value map[string]any `json:"value"`
	Label string         `json:"label"`
}

type DashboardFilterOptionSource struct {
	Kind   string                        `json:"kind"`
	Limit  int32                         `json:"limit"`
	Values []DashboardFilterStaticOption `json:"values"`
}

type DashboardCompiledFilterDefinition struct {
	ID            string                           `json:"id"`
	Label         string                           `json:"label"`
	Description   string                           `json:"description,omitempty"`
	Field         string                           `json:"field"`
	Dataset       string                           `json:"dataset,omitempty"`
	ValueKind     string                           `json:"valueKind"`
	Predicates    []DashboardFilterPredicatePolicy `json:"predicates"`
	Options       DashboardFilterOptionSource      `json:"options"`
	FormatPattern string                           `json:"formatPattern,omitempty"`
	FormatUnit    string                           `json:"formatUnit,omitempty"`
	Timezone      string                           `json:"timezone"`
	Calendar      string                           `json:"calendar"`
	WeekStart     string                           `json:"weekStart"`
}

type DashboardFilterBindingRef struct {
	Scope string `json:"scope"`
	ID    string `json:"id"`
}

type DashboardCompiledFilterBinding struct {
	Key                string                      `json:"key"`
	ID                 string                      `json:"id"`
	Filter             string                      `json:"filter"`
	Scope              string                      `json:"scope"`
	PageID             string                      `json:"pageID,omitempty"`
	Default            map[string]any              `json:"default"`
	SelectionMode      string                      `json:"selectionMode"`
	MaxSelectedValues  int32                       `json:"maxSelectedValues"`
	ReaderEditable     bool                        `json:"readerEditable"`
	URLParam           string                      `json:"urlParam,omitempty"`
	PaneVisible        bool                        `json:"paneVisible"`
	PaneOrder          int32                       `json:"paneOrder"`
	PaneLabel          string                      `json:"paneLabel,omitempty"`
	Targets            []string                    `json:"targets"`
	OptionDependencies []DashboardFilterBindingRef `json:"optionDependencies"`
}

type DashboardFilterPresentation struct {
	Style       string `json:"style"`
	Search      bool   `json:"search"`
	SelectAll   bool   `json:"selectAll"`
	ShowCounts  bool   `json:"showCounts"`
	ShowSummary bool   `json:"showSummary"`
	Compact     bool   `json:"compact"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	AriaLabel   string `json:"ariaLabel,omitempty"`
}

type DashboardFilterDescribeResponse struct {
	Definition   DashboardCompiledFilterDefinition `json:"definition"`
	Binding      DashboardCompiledFilterBinding    `json:"binding"`
	ComponentID  string                            `json:"componentId,omitempty"`
	Presentation *DashboardFilterPresentation      `json:"presentation,omitempty"`
	Placement    *DashboardComponentPlacement      `json:"placement,omitempty"`
}

type DashboardVisualDescribeResponse struct {
	ID           string                            `json:"id"`
	ComponentID  string                            `json:"componentId,omitempty"`
	RendererID   string                            `json:"rendererID"`
	SpecRevision string                            `json:"specRevision"`
	Spec         visualizationir.VisualizationSpec `json:"spec"`
	Placement    *DashboardComponentPlacement      `json:"placement,omitempty"`
	X            float64                           `json:"x,omitempty"`
	Y            float64                           `json:"y,omitempty"`
	Width        float64                           `json:"width,omitempty"`
	Height       float64                           `json:"height,omitempty"`
}

type SemanticModelDescriptionResponse struct {
	ID          string                   `json:"id"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Dashboards  []ModelDashboardUsage    `json:"dashboards"`
	Counts      *SemanticModelCounts     `json:"counts,omitempty"`
	Datasets    []SemanticDatasetSummary `json:"datasets,omitempty"`
}

type SemanticModelCounts struct {
	Datasets      int `json:"datasets"`
	Fields        int `json:"fields"`
	Dimensions    int `json:"dimensions"`
	Metrics       int `json:"metrics"`
	Filters       int `json:"filters"`
	Relationships int `json:"relationships"`
}

type SemanticDatasetSummary struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	Description string `json:"description"`
	FieldCount  int    `json:"fieldCount"`
	MetricCount int    `json:"metricCount"`
}

type SemanticDatasetListResponse struct {
	Items []SemanticDatasetSummary `json:"items"`
	Page  PageInfo                 `json:"page"`
}

type SemanticDatasetResponse struct {
	ID          string                           `json:"id"`
	Model       string                           `json:"model"`
	Description string                           `json:"description"`
	GrainEntity string                           `json:"grainEntity,omitempty"`
	Entities    map[string]SemanticEntitySummary `json:"entities"`
	FieldCount  int                              `json:"fieldCount"`
	MetricCount int                              `json:"metricCount"`
}

type SemanticEntitySummary struct {
	Type   string   `json:"type"`
	Fields []string `json:"fields"`
}

type SemanticFieldResponse struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Dataset     string   `json:"dataset"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Datatype    string   `json:"datatype,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Format      string   `json:"format,omitempty"`
	Grain       string   `json:"grain,omitempty"`
	Time        string   `json:"time,omitempty"`
	Grains      []string `json:"grains,omitempty"`
}

type SemanticFieldListResponse struct {
	Items []SemanticFieldResponse `json:"items"`
	Page  PageInfo                `json:"page"`
}

type SemanticRelationshipResponse struct {
	ID          string   `json:"id"`
	FromDataset string   `json:"fromDataset"`
	FromFields  []string `json:"fromFields"`
	ToDataset   string   `json:"toDataset"`
	ToFields    []string `json:"toFields"`
	Cardinality string   `json:"cardinality"`
	Active      bool     `json:"active"`
}

type SemanticRelationshipListResponse struct {
	Items []SemanticRelationshipResponse `json:"items"`
	Page  PageInfo                       `json:"page"`
}

type SemanticSourceResponse struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Connection  string `json:"connection,omitempty"`
	Table       string `json:"table,omitempty"`
	Path        string `json:"path,omitempty"`
	Description string `json:"description,omitempty"`
}

type SemanticSourceListResponse struct {
	Items []SemanticSourceResponse `json:"items"`
	Page  PageInfo                 `json:"page"`
}

type SemanticFieldRef struct {
	Field string `json:"field"`
	Alias string `json:"alias,omitempty"`
}

type SemanticTimeRef struct {
	Field string `json:"field"`
	Grain string `json:"grain,omitempty"`
	Alias string `json:"alias,omitempty"`
}

type SemanticSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction,omitempty"`
}

type SemanticFilter struct {
	Field    string                `json:"field,omitempty"`
	Dataset  string                `json:"dataset,omitempty"`
	Operator string                `json:"operator,omitempty"`
	Values   []any                 `json:"values,omitempty"`
	Groups   []SemanticFilterGroup `json:"groups,omitempty"`
}

type SemanticFilterGroup struct {
	Filters []SemanticFilter `json:"filters"`
}

type SemanticQueryRequest struct {
	Dimensions []SemanticFieldRef `json:"dimensions,omitempty"`
	Metrics    []SemanticFieldRef `json:"metrics,omitempty"`
	Time       *SemanticTimeRef   `json:"time,omitempty"`
	Filters    []SemanticFilter   `json:"filters,omitempty"`
	Sort       []SemanticSort     `json:"sort,omitempty"`
	Limit      int                `json:"limit,omitempty"`
	PageToken  string             `json:"pageToken,omitempty"`
}

type SemanticPreviewRequest struct {
	Dimensions []SemanticFieldRef `json:"dimensions,omitempty"`
	Metrics    []SemanticFieldRef `json:"metrics,omitempty"`
	Filters    []SemanticFilter   `json:"filters,omitempty"`
	Sort       []SemanticSort     `json:"sort,omitempty"`
	Limit      int                `json:"limit,omitempty"`
	PageToken  string             `json:"pageToken,omitempty"`
}

type QueryColumn struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Nullable bool           `json:"nullable"`
	FieldRef *QueryFieldRef `json:"fieldRef,omitempty"`
	Label    string         `json:"label,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Unit     string         `json:"unit,omitempty"`
	Format   string         `json:"format,omitempty"`
}

type QueryFieldRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type QueryFreshness struct {
	LastSuccessfulRefreshAt string `json:"lastSuccessfulRefreshAt"`
	SnapshotID              string `json:"snapshotId"`
	ServingStateID          string `json:"servingStateId"`
	Source                  string `json:"source"`
	Status                  string `json:"status"`
}

type QueryCompleteness struct {
	ReturnedRows int  `json:"returnedRows"`
	HasMore      bool `json:"hasMore"`
}

type SemanticQueryResponse struct {
	QueryID         string            `json:"queryId"`
	ServingSnapshot string            `json:"servingSnapshot"`
	Freshness       *QueryFreshness   `json:"freshness,omitempty"`
	Completeness    QueryCompleteness `json:"completeness"`
	Columns         []QueryColumn     `json:"columns"`
	Rows            [][]any           `json:"rows"`
	Page            PageInfo          `json:"page"`
}

type SemanticExplainResponse struct {
	Mode                 string           `json:"mode"`
	Datasets             []string         `json:"datasets"`
	StitchDimensions     []string         `json:"stitchDimensions"`
	PhysicalDependencies []string         `json:"physicalDependencies"`
	RelationshipPaths    []string         `json:"relationshipPaths"`
	SQL                  string           `json:"sql"`
	Args                 []map[string]any `json:"args"`
	Columns              []string         `json:"columns"`
	Warnings             []string         `json:"warnings"`
	EffectiveOrdering    []SemanticSort   `json:"effectiveOrdering"`
}

type ModelDashboardUsage struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	SemanticModel string `json:"semantic_model"`
	Pages         int    `json:"pages"`
}

type DashboardPageQueryRequest struct {
	FilterState           map[string]any   `json:"filterState"`
	InteractionSelections []map[string]any `json:"interactionSelections"`
	SpatialSelections     []map[string]any `json:"spatialSelections"`
}

type DashboardVisualQueryRequest struct {
	Limit                 int              `json:"limit"`
	PageToken             string           `json:"pageToken"`
	FilterState           map[string]any   `json:"filterState"`
	InteractionSelections []map[string]any `json:"interactionSelections"`
	SpatialSelections     []map[string]any `json:"spatialSelections"`
}

type DashboardTableQueryResponse struct {
	ID              string        `json:"id"`
	Type            string        `json:"type"`
	QueryID         string        `json:"queryId"`
	ServingSnapshot string        `json:"servingSnapshot"`
	Title           string        `json:"title"`
	Columns         []QueryColumn `json:"columns"`
	Rows            [][]string    `json:"rows"`
	AvailableRows   int           `json:"availableRows"`
	Page            PageInfo      `json:"page"`
}

type DashboardFilterOptionResponse struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type DashboardFilterOptionListResponse struct {
	Items []DashboardFilterOptionResponse `json:"items"`
	Page  PageInfo                        `json:"page"`
}
