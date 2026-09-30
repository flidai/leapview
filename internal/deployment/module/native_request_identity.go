package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
)

// NativeDeliveryPlanRequestDigest computes the canonical operation identity
// for a native plan request. CreatePlan and durable replay share this helper
// so their request projections cannot drift independently.
func NativeDeliveryPlanRequestDigest(request NativeDeliveryPlanRequest) (string, error) {
	var pipelinePlan *projectpipelineplan.Plan
	if request.PipelinePlan != nil {
		canonical := request.PipelinePlan.Canonical()
		pipelinePlan = &canonical
	}
	canonical := struct {
		ProjectID, TargetID, Environment, PrincipalID, SourceOwnerID, Operation, SourceDigest, SourceAttestationDigest, IdempotencyKey string
		PipelinePlan                                                                                                                   *projectpipelineplan.Plan `json:"pipelinePlan,omitempty"`
	}{request.ProjectID.String(), request.TargetID, request.Environment, request.PrincipalID, request.SourceOwnerID, request.Operation, request.SourceDigest, request.SourceAttestationDigest, request.IdempotencyKey, pipelinePlan}
	return nativeRequestDigest(canonical)
}

// NativeDeliveryBuildRequestDigest computes the canonical operation identity
// for a native build request. BuildPlan and durable replay use the same fixed
// field projection.
func NativeDeliveryBuildRequestDigest(request NativeDeliveryBuildRequest) (string, error) {
	canonical := struct {
		ProjectID, TargetID, Environment, PlanID, PrincipalID, IdempotencyKey string
	}{
		ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment,
		PlanID: request.PlanID.String(), PrincipalID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey,
	}
	return nativeRequestDigest(canonical)
}

func nativeRequestDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
