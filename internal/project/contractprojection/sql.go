package contractprojection

import "github.com/flidai/leapview/internal/analytics/modelsql"

func canonicalModelSQL(sqlText string) (*string, error) {
	return modelsql.CanonicalProjection(sqlText)
}
