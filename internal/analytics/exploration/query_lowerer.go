package exploration

import (
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// QueryLowerer is the contract exposed by the analytics capability for
// translating an authored exploration into governed data-query requests.
// Adapters depend on this port rather than the lowering use-case package.
type QueryLowerer interface {
	QueryForModel(ExplorationSpec, *semanticmodel.Model) (dataquery.Query, error)
	Filters(ExplorationSpec) ([]dataquery.Filter, error)
}
