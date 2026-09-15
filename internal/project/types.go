package project

type AssetType string

const (
	AssetTypeSemanticModel   AssetType = "semantic_model"
	AssetTypeConnection      AssetType = "connection"
	AssetTypeSource          AssetType = "source"
	AssetTypeModel           AssetType = "model"
	AssetTypeDashboard       AssetType = "dashboard"
	AssetTypeRefreshPipeline AssetType = "refresh_pipeline"
)

type AssetEdgeType string

const (
	AssetEdgeUsesConnection AssetEdgeType = "uses_connection"
)
