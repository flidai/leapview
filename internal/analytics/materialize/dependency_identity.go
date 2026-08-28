package materialize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

const (
	resultDependencySettingsVersion = 1
	materializedResultFormatName    = "arrow-result"
	materializedResultFormatVersion = 1
)

func (r *Runtime) dependencyPlanInput(projection semanticquery.DependencyProjection) resultidentity.PlanInput {
	limits := r.queryResultLimits()
	encoded, _ := json.Marshal(struct {
		Version  int   `json:"version"`
		MaxRows  int   `json:"maxRows"`
		MaxBytes int64 `json:"maxBytes"`
	}{Version: resultDependencySettingsVersion, MaxRows: limits.MaxRows, MaxBytes: limits.MaxBytes})
	digest := sha256.Sum256(encoded)
	return resultidentity.PlanInput{
		Datasets: projection.Datasets, PlannerDigest: projection.PlannerDigest,
		SettingsDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ResultFormat: resultidentity.ResultFormat{
			Name: materializedResultFormatName, Version: materializedResultFormatVersion,
		},
	}
}
