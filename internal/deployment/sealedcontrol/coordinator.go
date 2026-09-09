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
	EvidenceDigest    string
	ServingArtifactID string
	ApprovalReleaseID string
	ActorID           string
	Operation         string
	// Bootstrap is set only by the activation worker after it has revalidated
	// the durable one-shot bootstrap policy. It allows that worker's context,
	// which is not the original HTTP request context, to cross the sealed
	// publication authorization boundary.
	Bootstrap bool
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
// publication. Implementations must bind deployment/candidate, exact
// plan/evidence digests, serving-artifact release identity, scope, status, and expiry; a
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
// checked against the publication plan/evidence digests and immutable serving-artifact
// release identity on every retry, so replacement candidates/replans cannot
// reuse an earlier approval.
func DurableApprovalVerifier(service ActivationApprovalAuthorizer) ApprovalVerifier {
	return func(ctx context.Context, binding SealBinding, publication deployment.PublicationIntent) error {
		if service == nil {
			return deployment.ErrApprovalRequired
		}
		if binding.DeploymentID == "" || binding.CandidateID == "" || binding.GenerationID == "" || binding.PlanDigest == "" || binding.EvidenceDigest == "" || binding.ServingArtifactID == "" || binding.ApprovalReleaseID == "" {
			return deployment.ErrApprovalScope
		}
		if deployment.ValidateDeliveryDigest(binding.PlanDigest) != nil || deployment.ValidateDeliveryDigest(binding.EvidenceDigest) != nil {
			return deployment.ErrApprovalScope
		}
		if publication.CandidateID != binding.CandidateID || publication.GenerationID != binding.GenerationID || publication.PlanDigest != binding.PlanDigest || binding.ServingArtifactID != binding.Seal.ServingArtifactID {
			return deployment.ErrApprovalScope
		}
		_, err := service.AuthorizeActivation(ctx, deployment.ApprovalActivation{
			ProjectID: publication.ProjectID.String(), DeploymentID: binding.DeploymentID,
			Environment: publication.Environment, RequestDigest: publication.RequestDigest,
			PlanDigest: binding.PlanDigest, EvidenceDigest: binding.EvidenceDigest,
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
	// BeforePublicationCommit is an optional qualification hook. It runs only
	// on a fresh pending publication, after approval and seal verification and
	// immediately before the durable activation CAS. Committed retries never
	// invoke it.
	BeforePublicationCommit func(context.Context, deployment.PublicationIntent) error
	Now                     func() time.Time
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
	// Bootstrap carries the worker's already-validated first-activation
	// decision into the sealed binding. Ordinary publication requests leave it
	// false and must carry the request-local APIGen marker instead.
	Bootstrap bool
}

// Validate checks the complete immutable publication tuple without reading or
// mutating a store. Outer lifecycle coordinators use this preflight before
// they advance their own durable state.
func (r PublishRequest) Validate() error {
	if err := r.Seal.Validate(); err != nil {
		return err
	}
	if err := r.Generation.Validate(); err != nil {
		return err
	}
	if r.Generation.CatalogDigest != r.Seal.CatalogDigest || r.Generation.CatalogObjectKey != r.Seal.CatalogObjectKey || r.Generation.PhysicalPoolID != r.Seal.PhysicalPoolID || r.Generation.ServingArtifactID != r.Seal.ServingArtifactID || r.Generation.ServingArtifactDigest != r.Seal.ServingArtifactDigest {
		return fmt.Errorf("%w: generation does not point to exact verified seal", ErrSealUnverified)
	}
	if err := r.Publication.Validate(); err != nil {
		return err
	}
	if r.Publication.CandidateID != r.Generation.CandidateID || r.Publication.GenerationID != r.Generation.ID || r.Publication.PlanID != r.Generation.PlanID || r.Publication.PlanDigest != r.Generation.PlanDigest || r.Publication.TargetID != r.Generation.TargetID || r.Publication.ProjectID != r.Generation.ProjectID || r.Publication.Environment != r.Generation.Environment {
		return fmt.Errorf("%w: publication does not bind exact candidate/generation", ErrSealUnverified)
	}
	if r.ActorID != "" {
		if err := deployment.ValidateDeliveryID(r.ActorID); err != nil {
			return fmt.Errorf("publication actor id: %w", err)
		}
	}
	return nil
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
	if err := request.Validate(); err != nil {
		return deployment.PublicationIntent{}, err
	}
	binding := SealBinding{Seal: request.Seal, DeploymentID: request.Publication.ID, ProjectID: request.Publication.ProjectID.String(), Environment: request.Publication.Environment, TargetID: request.Publication.TargetID, CandidateID: request.Generation.CandidateID, GenerationID: request.Generation.ID, PlanDigest: request.Generation.PlanDigest, ServingArtifactID: request.Generation.ServingArtifactID, ApprovalReleaseID: request.ApprovalReleaseID, ActorID: request.ActorID, Operation: "publish", Bootstrap: request.Bootstrap}
	// A committed retry may skip only the fresh approval check.  Authorization
	// remains live on every request so revocation takes effect immediately.
	if err := c.Authorize(ctx, binding); err != nil {
		return deployment.PublicationIntent{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if reader, ok := c.Publications.(committedPublicationReader); ok {
		if committed, err := reader.DeliveryPublicationByRequest(ctx, request.Publication.TargetID, request.Publication.RequestDigest); err == nil && committed.Status == deployment.DeliveryPublicationCommitted {
			if committed.ID != request.Publication.ID || committed.CandidateID != request.Publication.CandidateID || committed.GenerationID != request.Publication.GenerationID || committed.PlanID != request.Publication.PlanID || committed.PlanDigest != request.Publication.PlanDigest || committed.ProjectID != request.Publication.ProjectID || committed.Environment != request.Publication.Environment || committed.ExpectedBaseGenerationID != request.Publication.ExpectedBaseGenerationID || committed.ExpectedTargetRevision != request.Publication.ExpectedTargetRevision || committed.RefreshRunID != request.Publication.RefreshRunID || committed.RefreshLeaseOwner != request.Publication.RefreshLeaseOwner || committed.RefreshLeaseRevision != request.Publication.RefreshLeaseRevision || committed.RefreshTargetRevision != request.Publication.RefreshTargetRevision {
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
		if c.BeforePublicationCommit != nil {
			if err := c.BeforePublicationCommit(ctx, publication); err != nil {
				return err
			}
		}
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

// Validate checks rollback evidence before an outer identity lifecycle may
// change the active authored-resource bundle.
func (r RollbackRequest) Validate() error {
	request := r.Request
	if request.ActorID == "" {
		request.ActorID = r.ActorID
	}
	if r.ActorID != "" && request.ActorID != r.ActorID {
		return fmt.Errorf("%w: rollback actors differ", ErrInvalidRequest)
	}
	return request.Validate()
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
	if err := request.Validate(); err != nil {
		return deployment.RollbackResult{}, err
	}
	// Validate accepts the inner request actor when the wrapper actor is
	// omitted. Carry that same canonical actor into authorization and the
	// target store; otherwise this path silently clears a valid inner actor.
	actorID := request.ActorID
	if actorID == "" {
		actorID = request.Request.ActorID
	}
	request.Request.ActorID = actorID
	binding := SealBinding{Seal: request.Request.VerifiedSeal, DeploymentID: request.Request.ID, ProjectID: request.Request.ProjectID.String(), Environment: request.Request.Environment, TargetID: request.Request.TargetID, CandidateID: request.Request.CandidateID, GenerationID: request.Request.GenerationID, ActorID: actorID, Operation: "rollback"}
	if err := c.Authorize(ctx, binding); err != nil {
		return deployment.RollbackResult{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if err := c.VerifySeal(ctx, binding); err != nil {
		return deployment.RollbackResult{}, fmt.Errorf("%w: %v", ErrSealUnverified, err)
	}
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
