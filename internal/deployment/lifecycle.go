package deployment

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// DeliveryCandidateBuildInput is the transport-neutral hand-off used by the
// candidate synchronization endpoint and native plan construction. It keeps
// source provenance and compiler evidence together while target-owned native
// persistence performs plan/build orchestration.
type DeliveryCandidateBuildInput struct {
	ProjectID      projectgraph.ResourceID
	OwnerID        string
	ArtifactDigest string
	Operation      DeliveryOperationKind
	CandidateKey   string
	Candidate      Candidate
	Source         project.CandidateSourceSnapshot
	Plan           *DeliveryPlan
	// PipelinePlan is immutable refresh selection evidence. It is optional for
	// ordinary code-change candidate delivery and required by canonical
	// pipeline restatements.
	PipelinePlan *PipelinePlan
}

// DeliveryTarget is the read-only target fence used by native planning and
// build stale checks. TargetRevision is the sole publication/build authority;
// the active generation and publication IDs are the durable serving pointers.
type DeliveryTarget struct {
	TargetID            string
	ProjectID           string
	Environment         string
	TargetRevision      int64
	ActiveGenerationID  string
	ActivePublicationID string
}

// DeliveryTargetResolver never acquires writer credentials or touches object
// storage. Implementations read the authoritative target revision and active
// generation/publication pointers from the control plane.
type DeliveryTargetResolver interface {
	ResolveDeliveryTarget(context.Context, string) (DeliveryTarget, error)
}

// DeliveryPlanRequest is the explicit input to native plan construction.
// ServingArtifactDigest and SourceAttestationDigest bind the immutable
// compiler/source evidence used by the target-owned PostgreSQL authority.
type DeliveryPlanRequest struct {
	ID           string
	ActorID      string
	TargetID     string
	ProjectID    string
	Environment  string
	Operation    DeliveryOperationKind
	SourceDigest string
	// ServingArtifactDigest is the deterministic packed serving-bundle identity
	// selected during native planning. Legacy callers may leave it empty; the
	// repository then retains SourceDigest for backwards compatibility.
	ServingArtifactDigest string
	// SourceAttestationDigest is an opaque target-issued identity for the exact
	// retained source revision. It participates in provenance/plan identity,
	// never execution identity.
	SourceAttestationDigest string
	Execution               DeliveryExecutionInputs
	Provenance              DeliveryProvenance
	Governance              DeliveryGovernance
	Evidence                DeliveryPlanEvidence
	PipelinePlan            *PipelinePlan
	CreatedAt               time.Time
	Persist                 bool
}
