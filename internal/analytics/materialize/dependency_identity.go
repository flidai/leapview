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
	return r.dependencyPlanInputProtected(projection, nil)
}

func (r *Runtime) dependencyPlanInputProtected(projection semanticquery.DependencyProjection, semanticAccess *resultidentity.SemanticAccessIdentity) resultidentity.PlanInput {
	limits := r.queryResultLimits()
	encoded, _ := json.Marshal(struct {
		Version  int   `json:"version"`
		MaxRows  int   `json:"maxRows"`
		MaxBytes int64 `json:"maxBytes"`
	}{Version: resultDependencySettingsVersion, MaxRows: limits.MaxRows, MaxBytes: limits.MaxBytes})
	digest := sha256.Sum256(encoded)
	return resultidentity.PlanInput{
		SemanticAccess: semanticAccess,
		Datasets:       projection.Datasets, PlannerDigest: projection.PlannerDigest,
		SettingsDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ResultFormat: resultidentity.ResultFormat{
			Name: materializedResultFormatName, Version: materializedResultFormatVersion,
		},
	}
}

// dependencyForProtectedProjection derives a query dependency from
// activation evidence and the request-bound protected-consumer identity. The
// identity is optional so public result dependencies retain their historical
// representation; protected cache admission calls this method only after a
// complete consumer identity has been obtained.
func (r *Runtime) dependencyForProtectedProjection(projection semanticquery.DependencyProjection, semanticAccess *resultidentity.SemanticAccessIdentity) (resultidentity.Dependency, bool) {
	if r == nil || !r.dependencyEvidence.Available() {
		return resultidentity.Dependency{}, false
	}
	dependency, err := r.dependencyEvidence.Dependency(r.dependencyPlanInputProtected(projection, semanticAccess))
	return dependency, err == nil
}
