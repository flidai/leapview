package module

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
)

// NativeDeliveryApprovalPort is the publication-scoped approval boundary
// used by production HTTP routes. Candidate-wide legacy approval services do
// not implement this interface and are never selected when it is present.
type NativeDeliveryApprovalPort interface {
	RequestPublicationApproval(context.Context, NativeApprovalRequest) (depauth.ApprovalRequest, error)
	GetPublicationApproval(context.Context, NativeApprovalLookup) (depauth.ApprovalRequest, error)
	ApprovePublicationApproval(context.Context, NativeApprovalDecision) (depauth.ApprovalRequest, error)
	DenyPublicationApproval(context.Context, NativeApprovalDecision) (depauth.ApprovalRequest, error)
	RevokePublicationApproval(context.Context, NativeApprovalDecision) (depauth.ApprovalRequest, error)
}

type NativeApprovalRequest struct {
	ProjectID, TargetID, Environment string
	PublicationID                    uuid.UUID
	// PrincipalID is the authenticated requester; Actor carries the same
	// principal plus bounded credential evidence and is checked for equality.
	PrincipalID    string
	IdempotencyKey string
	Actor          ApprovalActor
}

type NativeApprovalLookup struct {
	ProjectID, TargetID, Environment string
	PublicationID, RequestID         string
}

type NativeApprovalDecision struct {
	ProjectID, TargetID, Environment string
	PublicationID, RequestID         string
	ExpectedRevision                 int64
	IdempotencyKey                   string
	Actor                            ApprovalActor
}

// ApprovalActivationJob is the immutable payload persisted with an approval
// grant. The worker validates every publication fence and decision identity
// before delegating to the native activation transaction.
type ApprovalActivationJob struct {
	RequestID              string `json:"request_id"`
	PublicationID          string `json:"publication_id"`
	TargetID               string `json:"target_id"`
	GenerationID           string `json:"generation_id"`
	CandidateID            string `json:"candidate_id"`
	RequestDigest          string `json:"request_digest"`
	PublicationActorID     string `json:"publication_actor_id"` // immutable publication owner
	RequestedBy            string `json:"requested_by"`         // approval requester
	DecidedBy              string `json:"decided_by"`           // reviewer who granted approval
	IdempotencyKey         string `json:"idempotency_key"`
	ExpectedTargetRevision int64  `json:"expected_target_revision"`
	PolicyRevision         int64  `json:"policy_revision"`
	DecisionRevision       int64  `json:"decision_revision"`
	DecisionID             string `json:"decision_id"`
}

type nativeApprovalCoordinator struct {
	repository  *depauth.Repository
	authority   *depauth.ApprovalAuthority
	targetID    string
	environment string
}

func newNativeApprovalCoordinator(repository *depauth.Repository, authority *depauth.ApprovalAuthority, targetID, environment string) (NativeDeliveryApprovalPort, error) {
	if repository == nil || !repository.Configured() || authority == nil || strings.TrimSpace(targetID) == "" || targetID != strings.TrimSpace(targetID) || strings.TrimSpace(environment) == "" || environment != strings.TrimSpace(environment) {
		return nil, fmt.Errorf("native PostgreSQL approval authority is not configured")
	}
	return &nativeApprovalCoordinator{repository: repository, authority: authority, targetID: targetID, environment: environment}, nil
}

func (c *nativeApprovalCoordinator) RequestPublicationApproval(ctx context.Context, input NativeApprovalRequest) (depauth.ApprovalRequest, error) {
	if c == nil || input.TargetID != c.targetID || input.Environment != c.environment || input.ProjectID == "" || input.PrincipalID == "" || input.PrincipalID != input.Actor.PrincipalID || strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey || input.IdempotencyKey == "" {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalInvalid
	}
	publication, err := c.repository.Publication(ctx, input.PublicationID.String())
	if err != nil {
		return depauth.ApprovalRequest{}, err
	}
	target, err := c.repository.Target(ctx, c.targetID)
	if err != nil || target.ProjectID != input.ProjectID || target.Environment != c.environment || publication.TargetID != c.targetID {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalConflict
	}
	generation, err := c.repository.Generation(ctx, publication.GenerationID)
	if err != nil {
		return depauth.ApprovalRequest{}, err
	}
	plan, err := c.repository.Plan(ctx, generation.PlanID)
	if err != nil {
		return depauth.ApprovalRequest{}, err
	}
	requestID := deterministicApprovalUUID("request:" + publication.PublicationID + ":" + input.IdempotencyKey)
	now, err := c.repository.DatabaseNow(ctx)
	if err != nil {
		return depauth.ApprovalRequest{}, err
	}
	expires := now.Add(time.Hour)
	if !input.Actor.CredentialExpiresAt.IsZero() && input.Actor.CredentialExpiresAt.Before(expires) {
		expires = input.Actor.CredentialExpiresAt
	}
	evidence := approvalEvidenceFor(requestID, depauth.ApprovalActionRequest)
	return c.authority.Request(ctx, depauth.ApprovalRequestInput{RequestID: requestID, PublicationID: publication.PublicationID, TargetID: c.targetID, CandidateID: publication.CandidateID, GenerationID: publication.GenerationID, RequestDigest: publication.RequestDigest, ExpectedTargetRevision: publication.ExpectedTargetRevision, PolicyRevision: plan.ApprovalPolicyRevision, RequestedBy: nativeActor(input.Actor), ExpiresAt: expires, Evidence: evidence})
}

func (c *nativeApprovalCoordinator) GetPublicationApproval(ctx context.Context, input NativeApprovalLookup) (depauth.ApprovalRequest, error) {
	if c == nil || input.TargetID != c.targetID || input.Environment != c.environment || input.RequestID == "" {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalInvalid
	}
	request, err := c.authority.RequestByID(ctx, input.RequestID)
	if err != nil {
		return depauth.ApprovalRequest{}, err
	}
	if request.PublicationID != input.PublicationID || request.TargetID != c.targetID {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalNotFound
	}
	publication, err := c.repository.Publication(ctx, input.PublicationID)
	if err != nil || publication.TargetID != c.targetID || publication.RequestDigest != request.RequestDigest {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalNotFound
	}
	target, err := c.repository.Target(ctx, c.targetID)
	if err != nil || target.ProjectID != input.ProjectID {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalNotFound
	}
	return request, nil
}

func (c *nativeApprovalCoordinator) decide(ctx context.Context, input NativeApprovalDecision, action depauth.ApprovalAction) (depauth.ApprovalRequest, error) {
	if c == nil || input.TargetID != c.targetID || input.Environment != c.environment || input.IdempotencyKey == "" || strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey || input.ExpectedRevision < 0 {
		return depauth.ApprovalRequest{}, depauth.ErrApprovalInvalid
	}
	request, err := c.GetPublicationApproval(ctx, NativeApprovalLookup{ProjectID: input.ProjectID, TargetID: input.TargetID, Environment: input.Environment, PublicationID: input.PublicationID, RequestID: input.RequestID})
	if err != nil {
		return depauth.ApprovalRequest{}, err
	}
	decisionID := deterministicApprovalUUID(string(action) + ":" + request.RequestID + ":" + input.IdempotencyKey)
	decision := depauth.ApprovalDecisionInput{RequestID: request.RequestID, DecisionID: decisionID, ExpectedRevision: input.ExpectedRevision, Actor: nativeActor(input.Actor), Evidence: approvalEvidenceFor(decisionID, action)}
	switch action {
	case depauth.ApprovalActionApprove:
		return c.authority.Approve(ctx, decision)
	case depauth.ApprovalActionDeny:
		return c.authority.Deny(ctx, decision)
	case depauth.ApprovalActionRevoke:
		return c.authority.Revoke(ctx, decision)
	default:
		return depauth.ApprovalRequest{}, depauth.ErrApprovalInvalid
	}
}

func nativeActor(actor ApprovalActor) depauth.ApprovalActor {
	return depauth.ApprovalActor{PrincipalID: actor.PrincipalID, CredentialClass: string(actor.CredentialClass), CredentialID: actor.CredentialID, CredentialExpiresAt: actor.CredentialExpiresAt}
}

func (c *nativeApprovalCoordinator) ApprovePublicationApproval(ctx context.Context, input NativeApprovalDecision) (depauth.ApprovalRequest, error) {
	return c.decide(ctx, input, depauth.ApprovalActionApprove)
}
func (c *nativeApprovalCoordinator) DenyPublicationApproval(ctx context.Context, input NativeApprovalDecision) (depauth.ApprovalRequest, error) {
	return c.decide(ctx, input, depauth.ApprovalActionDeny)
}
func (c *nativeApprovalCoordinator) RevokePublicationApproval(ctx context.Context, input NativeApprovalDecision) (depauth.ApprovalRequest, error) {
	return c.decide(ctx, input, depauth.ApprovalActionRevoke)
}

func approvalEvidenceFor(seed string, action depauth.ApprovalAction) depauth.ApprovalEvidence {
	return depauth.ApprovalEvidence{OperationID: deterministicApprovalUUID("operation:" + string(action) + ":" + seed), EventID: deterministicApprovalUUID("event:" + string(action) + ":" + seed), AuditID: deterministicApprovalUUID("audit:" + string(action) + ":" + seed), Metadata: []byte(`{"source":"native-http"}`)}
}

// deterministicApprovalUUID creates a stable UUIDv7-shaped identity from an
// idempotency seed. The high timestamp bits are fixed; all entropy comes from
// SHA-256, so retries and independent workers derive the same evidence IDs.
func deterministicApprovalUUID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	var b [16]byte
	copy(b[:], h[:16])
	b[0] = 0x01
	b[1] = 0x98
	b[2] = 0xf2
	b[3] = 0xc0
	// UUID version 7 and RFC 9562 variant.
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return uuid.UUID(b).String()
}
