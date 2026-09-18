package api

// SearchKind is the transport-neutral resource kind accepted by the project
// search contract. The generated API adapter converts its wire enum into this
// value before dispatching to the capability that owns search execution.
type SearchKind string

const (
	SearchKindProject       SearchKind = "project"
	SearchKindConnection    SearchKind = "connection"
	SearchKindSource        SearchKind = "source"
	SearchKindModel         SearchKind = "model"
	SearchKindSemanticModel SearchKind = "semantic_model"
	SearchKindPipeline      SearchKind = "pipeline"
	SearchKindDashboard     SearchKind = "dashboard"
)

// SearchParams is the narrow project-search application contract. It is kept
// separate from the generated HTTP adapter so capability modules do not need
// to import generated transport types.
type SearchParams struct {
	Q      string
	Kind   *[]SearchKind
	Domain *string
	Limit  *int32
	Cursor *string
}

// ResourceCreateReceipt is the bounded response shared by resource creation
// handlers. Creation returns only the allocated identity and lifecycle status;
// callers must perform an independently authorized read for any other fields.
type ResourceCreateReceipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type ProjectResponse struct {
	ActiveDeploymentID *string `json:"activeDeploymentId,omitempty"`
	CreatedAt          string  `json:"createdAt"`
	ID                 string  `json:"id"`
	LatestReleaseID    *string `json:"latestReleaseId,omitempty"`
	Title              string  `json:"title"`
	UpdatedAt          string  `json:"updatedAt"`
}
