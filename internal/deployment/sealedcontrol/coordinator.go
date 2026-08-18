// Package sealedcontrol coordinates target-owned publication and rollback.
//
// The coordinator has no DuckLake or object-store capability. It verifies that
// the caller supplied one exact verified seal, invokes the durable control
// plane, and relies on that store's compare-and-swap transaction for the
// active-generation pointer. This keeps retries and stale-base failures in
// SQLite rather than in process-local state.
package sealedcontrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

var (
	ErrInvalidRequest     = errors.New("sealed control request is invalid")
	ErrUnauthorized       = errors.New("sealed control authorization is required")
	ErrSealUnverified     = errors.New("sealed control requires verified catalog evidence")
	ErrActivationProtocol = errors.New("sealed control activation protocol violated")
)

// PublicationStore is implemented by the SQLite delivery repository. Both
// methods are durable, idempotent operations; ActivatePublication performs
// the target revision and active-generation CAS.
type PublicationStore interface {
	RequestPublication(context.Context, deployment.PublicationIntent, ...deployment.CatalogRoot) (deployment.PublicationIntent, error)
	ActivatePublication(context.Context, string, time.Time) (deployment.PublicationIntent, error)
}

type committedPublicationReader interface {
	DeliveryPublicationByRequest(context.Context, string, string) (deployment.PublicationIntent, error)
}

// RollbackStore performs one SQLite-only rollback transaction. It must select
// the exact retained generation named by RollbackRequest and compare-and-swap
// both its expected active generation and target revision.
type RollbackStore interface {
	Rollback(context.Context, deployment.RollbackRequest) (deployment.RollbackResult, error)
}

// SealBinding is the exact control-plane root being acted on. Candidate and
// generation IDs are part of verification, preventing a verified seal with
// identical bytes but different qualification evidence from being substituted.
type SealBinding struct {
	Seal              deployment.VerifiedSeal
	DeploymentID      string
	ProjectID         string
	Environment       string
	TargetID          string
	CandidateID       string
	GenerationID      string
	PlanDigest        string
	ServingArtifactID string
	ApprovalReleaseID string
	ActorID           string
	Operation         string
}

// VerifiedSealVerifier is target-owned evidence validation. Implementations
// normally resolve the seal from SQLite, verify it is verified, and verify the
// exact object bytes/metadata before this coordinator is called. No storage
// credentials are passed through the coordinator.
type VerifiedSealVerifier func(context.Context, SealBinding) error

// Authorization is deliberately required at this boundary. Ownership of a
// candidate or generation is not a serving authorization decision.
type Authorization func(context.Context, SealBinding) error

// ApprovalVerifier re-reads durable approval evidence immediately before
// publication. Implementations must bind deployment/candidate, plan request
// digest, serving-artifact release identity, scope, status, and expiry; a
// verified seal alone is not approval evidence.
type ApprovalVerifier func(context.Context, SealBinding, deployment.PublicationIntent) error

// ActivationApprovalAuthorizer is the narrow durable approval contract used
// by the sealed publication boundary. Keeping this as an interface lets the
// production deployment module expose its SQLite-backed service without
// making the coordinator depend on module internals.
type ActivationApprovalAuthorizer interface {
	AuthorizeActivation(context.Context, deployment.ApprovalActivation) (deployment.Approval, error)
}

// DurableApprovalVerifier adapts the deployment approval service to this
// boundary. Approval is looked up by the exact candidate deployment ID and
// checked against the publication plan digest and immutable serving-artifact
// release identity on every retry, so replacement candidates/replans cannot
// reuse an earlier approval.
func DurableApprovalVerifier(service ActivationApprovalAuthorizer) ApprovalVerifier {
	return func(ctx context.Context, binding SealBinding, publication deployment.PublicationIntent) error {
		if service == nil {
			return deployment.ErrApprovalRequired
		}
		if binding.DeploymentID == "" || binding.CandidateID == "" || binding.GenerationID == "" || binding.PlanDigest == "" || binding.ServingArtifactID == "" || binding.ApprovalReleaseID == "" {
			return deployment.ErrApprovalScope
		}
		if publication.CandidateID != binding.CandidateID || publication.GenerationID != binding.GenerationID || publication.PlanDigest != binding.PlanDigest || binding.ServingArtifactID != binding.Seal.ServingArtifactID {
			return deployment.ErrApprovalScope
		}
		_, err := service.AuthorizeActivation(ctx, deployment.ApprovalActivation{
			ProjectID: publication.ProjectID.String(), DeploymentID: binding.DeploymentID,
			Environment: publication.Environment, RequestDigest: publication.RequestDigest,
			ReleaseID: binding.ApprovalReleaseID,
		})
		return err
	}
}

type Coordinator struct {
	Publications     PublicationStore
	Rollbacks        RollbackStore
	VerifySeal       VerifiedSealVerifier
	Authorize        Authorization
	ApprovalVerifier ApprovalVerifier
	Now              func() time.Time
}

// PublicationActivation wraps the final durable CAS with a prepared runtime
// cutover. The callback must invoke commit exactly when the runtime is ready;
// returning an error aborts the prepared runtime and leaves the target pointer
// unchanged. An already committed retry receives a no-op commit callback so
// the caller can reconcile its in-process runtime without repeating the CAS.
type PublicationActivation func(context.Context, func() error) error

func invokePublicationActivation(ctx context.Context, activate PublicationActivation, commit func() error) error {
	if activate == nil {
		return nil
	}
	if commit == nil {
		return fmt.Errorf("%w: commit callback is required", ErrActivationProtocol)
	}
	calls := 0
	var commitErr error
	var protocolErr error
	wrapped := func() error {
		if calls != 0 {
			protocolErr = fmt.Errorf("%w: commit callback invoked more than once", ErrActivationProtocol)
			return protocolErr
		}
		calls++
		commitErr = commit()
		return commitErr
	}
	if err := activate(ctx, wrapped); err != nil {
		return err
	}
	if calls != 1 {
		return fmt.Errorf("%w: commit callback invoked %d times", ErrActivationProtocol, calls)
	}
	if protocolErr != nil {
		return protocolErr
	}
	if commitErr != nil {
		return commitErr
	}
	return nil
}

type PublishRequest struct {
	Publication       deployment.PublicationIntent
	Generation        deployment.CatalogRoot
	Seal              deployment.VerifiedSeal
	ApprovalReleaseID string
	ActorID           string
}

func (c *Coordinator) Publish(ctx context.Context, request PublishRequest) (deployment.PublicationIntent, error) {
	return c.PublishWithActivation(ctx, request, nil)
}

// PublishWithActivation performs publication validation, durable pending-row
// creation, approval, and seal verification before handing the final target
// CAS to activation. This lets a caller prepare the exact serving runtime
// after RequestPublication has created its generation row while still keeping
// the active pointer and in-process cutover in one serialized callback.
func (c *Coordinator) PublishWithActivation(ctx context.Context, request PublishRequest, activate PublicationActivation) (deployment.PublicationIntent, error) {
	if c == nil || c.Publications == nil || c.VerifySeal == nil || c.Authorize == nil {
		return deployment.PublicationIntent{}, fmt.Errorf("%w: publication store, seal verifier, and authorization are required", ErrInvalidRequest)
	}
	if err := request.Seal.Validate(); err != nil {
		return deployment.PublicationIntent{}, err
	}
	if err := request.Generation.Validate(); err != nil {
		return deployment.PublicationIntent{}, err
	}
	if request.Generation.CatalogDigest != request.Seal.CatalogDigest || request.Generation.CatalogObjectKey != request.Seal.CatalogObjectKey || request.Generation.PhysicalPoolID != request.Seal.PhysicalPoolID || request.Generation.ServingArtifactID != request.Seal.ServingArtifactID || request.Generation.ServingArtifactDigest != request.Seal.ServingArtifactDigest {
		return deployment.PublicationIntent{}, fmt.Errorf("%w: generation does not point to exact verified seal", ErrSealUnverified)
	}
	if err := request.Publication.Validate(); err != nil {
		return deployment.PublicationIntent{}, err
	}
	if request.Publication.CandidateID != request.Generation.CandidateID || request.Publication.GenerationID != request.Generation.ID || request.Publication.PlanID != request.Generation.PlanID || request.Publication.PlanDigest != request.Generation.PlanDigest || request.Publication.TargetID != request.Generation.TargetID || request.Publication.ProjectID != request.Generation.ProjectID || request.Publication.Environment != request.Generation.Environment {
		return deployment.PublicationIntent{}, fmt.Errorf("%w: publication does not bind exact candidate/generation", ErrSealUnverified)
	}
	binding := SealBinding{Seal: request.Seal, DeploymentID: request.Publication.ID, ProjectID: request.Publication.ProjectID.String(), Environment: request.Publication.Environment, TargetID: request.Publication.TargetID, CandidateID: request.Generation.CandidateID, GenerationID: request.Generation.ID, PlanDigest: request.Generation.PlanDigest, ServingArtifactID: request.Generation.ServingArtifactID, ApprovalReleaseID: request.ApprovalReleaseID, ActorID: request.ActorID, Operation: "publish"}
	// A committed retry may skip only the fresh approval check.  Authorization
	// remains live on every request so revocation takes effect immediately.
	if err := c.Authorize(ctx, binding); err != nil {
		return deployment.PublicationIntent{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if reader, ok := c.Publications.(committedPublicationReader); ok {
		if committed, err := reader.DeliveryPublicationByRequest(ctx, request.Publication.TargetID, request.Publication.RequestDigest); err == nil && committed.Status == deployment.DeliveryPublicationCommitted {
			if committed.ID != request.Publication.ID || committed.CandidateID != request.Publication.CandidateID || committed.GenerationID != request.Publication.GenerationID || committed.PlanID != request.Publication.PlanID || committed.PlanDigest != request.Publication.PlanDigest || committed.ProjectID != request.Publication.ProjectID || committed.Environment != request.Publication.Environment || committed.ExpectedBaseGenerationID != request.Publication.ExpectedBaseGenerationID || committed.ExpectedTargetRevision != request.Publication.ExpectedTargetRevision {
				return deployment.PublicationIntent{}, fmt.Errorf("%w: committed publication identity differs", ErrSealUnverified)
			}
			if err := c.VerifySeal(ctx, binding); err != nil {
				return deployment.PublicationIntent{}, fmt.Errorf("%w: %v", ErrSealUnverified, err)
			}
			if err := invokePublicationActivation(ctx, activate, func() error { return nil }); err != nil {
				return committed, err
			}
			return committed, nil
		}
	}
	// Persist/reconcile the exact pending publication before approval or remote
	// seal preflight. This leaves a durable operator-visible request when
	// approval is missing or a provider check is indeterminate, and retries can
	// continue from the same immutable identity without reissuing work.
	publication, err := c.Publications.RequestPublication(ctx, request.Publication, request.Generation)
	if err != nil {
		return deployment.PublicationIntent{}, err
	}
	if publication.Status == deployment.DeliveryPublicationCommitted {
		// RequestPublication may reconcile a committed retry even when the
		// optional committed-publication reader is unavailable.  Authorization
		// and the exact remote seal must still be checked for this request; only
		// a fresh approval check is intentionally skipped for a committed retry.
		if err := c.VerifySeal(ctx, binding); err != nil {
			return publication, fmt.Errorf("%w: %w", ErrSealUnverified, err)
		}
		if err := invokePublicationActivation(ctx, activate, func() error { return nil }); err != nil {
			return publication, err
		}
		return publication, nil
	}
	if c.ApprovalVerifier != nil {
		if err := c.ApprovalVerifier(ctx, binding, publication); err != nil {
			return publication, fmt.Errorf("%w: %w", ErrUnauthorized, err)
		}
	}
	// Verify the exact remote bytes/provider state immediately before the CAS;
	// the durable pending request above is the recovery point if this fails.
	if err := c.VerifySeal(ctx, binding); err != nil {
		return publication, fmt.Errorf("%w: %w", ErrSealUnverified, err)
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	// The store owns the CAS and reconciles an indeterminate/completed
	// publication by request identity. Do not retry here with a new generation.
	var committed deployment.PublicationIntent
	commit := func() error {
		var err error
		committed, err = c.Publications.ActivatePublication(ctx, publication.ID, now)
		return err
	}
	if err := invokePublicationActivation(ctx, activate, commit); err != nil {
		return publication, err
	}
	if activate != nil {
		if committed.ID == "" {
			committed = publication
		}
		return committed, nil
	}
	return c.Publications.ActivatePublication(ctx, publication.ID, now)
}

type RollbackRequest struct {
	Request deployment.RollbackRequest
	ActorID string
}

func (c *Coordinator) Rollback(ctx context.Context, request RollbackRequest) (deployment.RollbackResult, error) {
	return c.RollbackWithActivation(ctx, request, nil)
}

// RollbackWithActivation applies the same prepared-runtime/CAS fence as
// PublishWithActivation while retaining the rollback store's idempotent
// request/result recovery behavior.
func (c *Coordinator) RollbackWithActivation(ctx context.Context, request RollbackRequest, activate PublicationActivation) (deployment.RollbackResult, error) {
	if c == nil || c.Rollbacks == nil || c.VerifySeal == nil || c.Authorize == nil {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback store, seal verifier, and authorization are required", ErrInvalidRequest)
	}
	if err := request.Request.Validate(); err != nil {
		return deployment.RollbackResult{}, err
	}
	binding := SealBinding{Seal: request.Request.VerifiedSeal, DeploymentID: request.Request.ID, ProjectID: request.Request.ProjectID.String(), Environment: request.Request.Environment, TargetID: request.Request.TargetID, CandidateID: request.Request.CandidateID, GenerationID: request.Request.GenerationID, ActorID: request.ActorID, Operation: "rollback"}
	if err := c.Authorize(ctx, binding); err != nil {
		return deployment.RollbackResult{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if err := c.VerifySeal(ctx, binding); err != nil {
		return deployment.RollbackResult{}, fmt.Errorf("%w: %v", ErrSealUnverified, err)
	}
	request.Request.ActorID = request.ActorID
	var result deployment.RollbackResult
	commit := func() error {
		var err error
		result, err = c.Rollbacks.Rollback(ctx, request.Request)
		return err
	}
	if err := invokePublicationActivation(ctx, activate, commit); err != nil {
		return result, err
	}
	if activate != nil {
		return result, nil
	}
	return c.Rollbacks.Rollback(ctx, request.Request)
}
