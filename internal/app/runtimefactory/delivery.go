package runtimefactory

// Composition helpers for the canonical candidate delivery adapter. The app
// owns target credentials and pool admission; this package only wires those
// capabilities into deployment/module without exposing them to HTTP handlers.

import (
	"context"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/release"
)

type CanonicalDeliveryConfig struct {
	Lifecycle      *deployment.DeliveryLifecycle
	Artifacts      release.CandidateArtifactPreparer
	Plan           func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error)
	PlanPreview    func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error)
	BuildRequest   func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error)
	ReadyCandidate func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet, deployment.DeliveryBuildResult) (deployment.Candidate, error)
	Publish        func(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
	Rollback       func(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
}

// NewCanonicalDeliveryAdapter returns the single production candidate path.
// BuildRequest is target-owned because it supplies the admitted pool,
// lease-checked candidatecatalog runner, create-only catalog object store,
// read-only verifier, and durable seal repository.
func NewCanonicalDeliveryAdapter(config CanonicalDeliveryConfig) *deploymentmodule.CanonicalDeliveryAdapter {
	return &deploymentmodule.CanonicalDeliveryAdapter{
		Lifecycle: config.Lifecycle, Artifacts: config.Artifacts, Plan: config.Plan, PlanPreview: config.PlanPreview,
		BuildRequest: config.BuildRequest, ReadyCandidate: config.ReadyCandidate, Publish: config.Publish, Rollback: config.Rollback,
	}
}
