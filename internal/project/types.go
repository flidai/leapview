package project

type AssetType string

const (
	AssetTypeCatalog         AssetType = "catalog"
	AssetTypeSemanticModel   AssetType = "semantic_model"
	AssetTypeConnection      AssetType = "connection"
	AssetTypeSource          AssetType = "source"
	AssetTypeModelTable      AssetType = "model_table"
	AssetTypeRelationship    AssetType = "relationship"
	AssetTypeField           AssetType = "field"
	AssetTypeMetric          AssetType = "metric"
	AssetTypeDashboard       AssetType = "dashboard"
	AssetTypePage            AssetType = "page"
	AssetTypePageItem        AssetType = "page_item"
	AssetTypeFilter          AssetType = "filter"
	AssetTypeVisual          AssetType = "visual"
	AssetTypeRefreshPipeline AssetType = "refresh_pipeline"
)

type AssetEdgeType string

const (
	AssetEdgeContains               AssetEdgeType = "contains"
	AssetEdgeUsesConnection         AssetEdgeType = "uses_connection"
	AssetEdgeReadsSource            AssetEdgeType = "reads_source"
	AssetEdgeUsesSemanticModel      AssetEdgeType = "uses_semantic_model"
	AssetEdgeUsesModelTable         AssetEdgeType = "uses_model_table"
	AssetEdgeUsesMetric             AssetEdgeType = "uses_metric"
	AssetEdgeUsesField              AssetEdgeType = "uses_field"
	AssetEdgeUsesVisual             AssetEdgeType = "uses_visual"
	AssetEdgeUsesFilter             AssetEdgeType = "uses_filter"
	AssetEdgeFiltersField           AssetEdgeType = "filters_field"
	AssetEdgeRefreshesSemanticModel AssetEdgeType = "refreshes_semantic_model"
)
