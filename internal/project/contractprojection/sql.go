package contractprojection

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/modelsql"
)

func canonicalModelSQL(sqlText string, resolver *ReferenceContext) (*string, error) {
	analysis, err := modelsql.Analyze(context.Background(), sqlText)
	if err != nil {
		return nil, err
	}
	if len(analysis.SourceRefs) > 0 || len(analysis.ModelRefs) > 0 {
		if resolver == nil {
			return nil, fmt.Errorf("Model SQL references governed resources but no validated project reference context was supplied")
		}
		return modelsql.CanonicalProjectionWithReferences(sqlText, resolver)
	}
	return modelsql.CanonicalProjection(sqlText)
}
