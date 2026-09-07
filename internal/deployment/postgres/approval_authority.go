package postgres

// This file implements the native approval authority for canonical delivery
// publications. It deliberately does not activate a publication: approval is
// immutable request/append-only decision evidence consumed by the publication
// worker's separate activation fence.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrApprovalInvalid          = errors.New("invalid delivery approval request")
	ErrApprovalNotFound         = errors.New("delivery approval request not found")
	ErrApprovalConflict         = errors.New("delivery approval request conflict")
	ErrApprovalRequired         = errors.New("delivery approval is required")
	ErrApprovalExpired          = errors.New("delivery approval request expired")
	ErrApprovalUnauthorized     = errors.New("delivery approval authorization denied")
	ErrApprovalSeparationOfDuty = errors.New("delivery approval separation of duty violated")
)

const (
	approvalMaxLifetime = 24 * time.Hour
	approvalMaxEvidence = 32768
)

// ApprovalAction identifies an authorization and audit operation. Revoke is a
// decision row, never an in-place mutation of an earlier approval.
type ApprovalAction string

const (
	ApprovalActionRequest ApprovalAction = "request"
	ApprovalActionApprove ApprovalAction = "approve"
	ApprovalActionDeny    ApprovalAction = "deny"
	ApprovalActionRevoke  ApprovalAction = "revoke"
)

// ApprovalActor is bounded credential evidence supplied by the access
// authority. Credential validation remains an authorization seam; the
// database additionally rejects expired decision credentials.
type ApprovalActor struct {
	PrincipalID         string
	CredentialClass     string
	CredentialID        string
	CredentialExpiresAt time.Time
}

func (a ApprovalActor) validate(now time.Time) error {
	if a.PrincipalID == "" || a.PrincipalID != strings.TrimSpace(a.PrincipalID) ||
		a.CredentialID == "" || a.CredentialID != strings.TrimSpace(a.CredentialID) ||
		a.CredentialExpiresAt.IsZero() || !now.Before(a.CredentialExpiresAt.UTC()) {
		return fmt.Errorf("%w: actor identity or credential is invalid", ErrApprovalInvalid)
	}
	switch a.CredentialClass {
	case "human", "workload", "api_token", "session":
		return nil
	default:
		return fmt.Errorf("%w: credential class is invalid", ErrApprovalInvalid)
	}
}

// ApprovalEvidence contains references to the operation, event and audit
// records committed with the mutation. The approval tables retain these IDs
// as immutable evidence; record appenders are invoked through caller-owned tx.
type ApprovalEvidence struct {
	OperationID string
	EventID     string
	AuditID     string
	Metadata    json.RawMessage
}

func (e ApprovalEvidence) canonical() (ApprovalEvidence, error) {
	op, err := uuidID(e.OperationID, "approval operation id", false)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	ev, err := uuidID(e.EventID, "approval event id", false)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	au, err := uuidID(e.AuditID, "approval audit id", false)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	metadata, err := canonicalObject(e.Metadata, approvalMaxEvidence, true)
	if err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: evidence: %v", ErrApprovalInvalid, err)
	}
	return ApprovalEvidence{OperationID: op, EventID: ev, AuditID: au, Metadata: metadata}, nil
}

// ApprovalRequestInput is the exact immutable scope that a reviewer sees and
// approves. Publication identity fields are repeated intentionally so a
// malformed cross-target request cannot be interpreted by a later worker.
type ApprovalRequestInput struct {
	RequestID, PublicationID, TargetID string
	CandidateID, GenerationID          string
	RequestDigest                      string
	ExpectedTargetRevision             int64
	PolicyRevision                     int64
	RequestedBy                        ApprovalActor
	ExpiresAt                          time.Time
	Evidence                           ApprovalEvidence
}

// ApprovalRequest is immutable request evidence plus its latest append-only
// decision (when one exists).
type ApprovalRequest struct {
	RequestID, PublicationID, TargetID string
	CandidateID, GenerationID          string
	RequestDigest                      string
	ExpectedTargetRevision             int64
	PolicyRevision                     int64
	RequestedBy                        ApprovalActor
	RequestedAt, ExpiresAt             time.Time
	Evidence                           ApprovalEvidence
	LatestDecision                     *ApprovalDecision
}

type ApprovalDecision struct {
	DecisionID, RequestID string
	Revision              int64
	Decision              ApprovalAction
	DecidedBy             ApprovalActor
	DecidedAt             time.Time
	Evidence              ApprovalEvidence
}

// ApprovalAuthorizationInput is the explicit seam to the access authority.
// Implementations must check the generated operation privilege and project/
// environment scope; this package never infers authorization from IDs.
type ApprovalAuthorizationInput struct {
	Action  ApprovalAction
	Request ApprovalRequestInput
	Current *ApprovalRequest
	Actor   ApprovalActor
}

type ApprovalAuthorizer interface {
	AuthorizeApproval(context.Context, ApprovalAuthorizationInput) error
}

type ApprovalAuthorizerFunc func(context.Context, ApprovalAuthorizationInput) error

func (f ApprovalAuthorizerFunc) AuthorizeApproval(ctx context.Context, input ApprovalAuthorizationInput) error {
	if f == nil {
		return ErrApprovalUnauthorized
	}
	return f(ctx, input)
}

// Operation, event and audit appenders receive the caller-owned transaction.
// They must not begin, commit, or roll back it. This keeps all evidence atomic
// with the approval mutation while leaving capability ownership in composition.
type ApprovalOperation struct {
	Action   ApprovalAction
	Request  ApprovalRequest
	Decision *ApprovalDecision
	Evidence ApprovalEvidence
}
type ApprovalEvent struct {
	Action   ApprovalAction
	Request  ApprovalRequest
	Decision *ApprovalDecision
	Evidence ApprovalEvidence
}
type ApprovalAudit struct {
	Action   ApprovalAction
	Request  ApprovalRequest
	Decision *ApprovalDecision
	Evidence ApprovalEvidence
}

type ApprovalOperationAppender interface {
	AppendApprovalOperation(context.Context, Tx, ApprovalOperation) error
}
type ApprovalEventAppender interface {
	AppendApprovalEvent(context.Context, Tx, ApprovalEvent) error
}
type ApprovalAuditAppender interface {
	AppendApprovalAudit(context.Context, Tx, ApprovalAudit) error
}

// ApprovalActivationAppender enqueues the native activation intent in the
// approval transaction. Implementations must use the supplied transaction and
// preserve deterministic replay identity; they never commit or roll it back.
type ApprovalActivationAppender interface {
	EnqueueApprovalActivation(context.Context, Tx, ApprovalRequest, ApprovalDecision) error
}

type ApprovalAuthorityOptions struct {
	Authorize  ApprovalAuthorizer
	Operation  ApprovalOperationAppender
	Event      ApprovalEventAppender
	Audit      ApprovalAuditAppender
	Activation ApprovalActivationAppender
}

type ApprovalAuthority struct {
	repository *Repository
	authorize  ApprovalAuthorizer
	operation  ApprovalOperationAppender
	event      ApprovalEventAppender
	audit      ApprovalAuditAppender
	activation ApprovalActivationAppender
}

// newLowLevelApprovalAuthority constructs the approval state machine for
// repository-focused tests and non-production adapters. It intentionally
// omits the activation consequence and is package-private so app production
// composition cannot bypass the strict constructor.
func newLowLevelApprovalAuthority(repository *Repository, options ApprovalAuthorityOptions) (*ApprovalAuthority, error) {
	if repository == nil || !repository.Configured() || !approvalPortPresent(options.Authorize) || !approvalPortPresent(options.Operation) || !approvalPortPresent(options.Event) || !approvalPortPresent(options.Audit) {
		return nil, ErrInvalid
	}
	return &ApprovalAuthority{repository: repository, authorize: options.Authorize, operation: options.Operation, event: options.Event, audit: options.Audit, activation: options.Activation}, nil
}

// NewApprovalAuthority is the fail-closed constructor used by application
// composition. Approval grants without an activation enqueue consequence are
// unsafe in production and are rejected before the authority is published.
func NewApprovalAuthority(repository *Repository, options ApprovalAuthorityOptions) (*ApprovalAuthority, error) {
	if !approvalPortPresent(options.Activation) {
		return nil, ErrInvalid
	}
	return newLowLevelApprovalAuthority(repository, options)
}

// NewProductionApprovalAuthority is retained as an explicit composition
// alias for callers that prefer the production intent in the name.
func NewProductionApprovalAuthority(repository *Repository, options ApprovalAuthorityOptions) (*ApprovalAuthority, error) {
	return NewApprovalAuthority(repository, options)
}

// Interfaces supplied by composition may hold typed nil pointers/functions.
// Treat those as absent so a fail-closed authority cannot panic while writing
// an approval mutation.
func approvalPortPresent(port any) bool {
	if port == nil {
		return false
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (a *ApprovalAuthority) Request(ctx context.Context, input ApprovalRequestInput) (ApprovalRequest, error) {
	tx, err := a.repository.begin(ctx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	request, err := a.RequestTx(ctx, tx, input)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ApprovalRequest{}, err
	}
	committed = true
	return request, nil
}

func (a *ApprovalAuthority) RequestTx(ctx context.Context, tx Tx, input ApprovalRequestInput) (ApprovalRequest, error) {
	if a == nil || a.repository == nil || tx == nil {
		return ApprovalRequest{}, ErrInvalid
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	normalized, err := normalizeApprovalRequestAt(input, now)
	if err != nil {
		return ApprovalRequest{}, err
	}
	publication, err := depdb.New(tx).LockPublicationForApproval(ctx, dbUUID(normalized.PublicationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrApprovalNotFound
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	if a.authorize != nil {
		if err := a.authorize.AuthorizeApproval(ctx, ApprovalAuthorizationInput{Action: ApprovalActionRequest, Request: normalized, Actor: normalized.RequestedBy}); err != nil {
			return ApprovalRequest{}, fmt.Errorf("%w: %v", ErrApprovalUnauthorized, err)
		}
	}
	if existing, getErr := loadApprovalRequest(ctx, tx, normalized.RequestID); getErr == nil {
		if !sameApprovalRequest(existing, normalized) {
			return ApprovalRequest{}, ErrApprovalConflict
		}
		// Idempotent request retries return the original immutable record even
		// after its publication reaches a terminal state.
		return existing, nil
	} else if !errors.Is(getErr, ErrApprovalNotFound) {
		return ApprovalRequest{}, getErr
	}
	if publication.State != "pending" || publication.TargetID != normalized.TargetID ||
		publication.GenerationID != normalized.GenerationID || publication.CandidateID != normalized.CandidateID ||
		publication.RequestDigest != normalized.RequestDigest || publication.ExpectedTargetRevision != normalized.ExpectedTargetRevision {
		return ApprovalRequest{}, ErrApprovalConflict
	}
	durablePolicyRevision, err := depdb.New(tx).GetGenerationApprovalPolicyRevision(ctx, dbUUID(normalized.GenerationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrApprovalNotFound
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	if durablePolicyRevision != normalized.PolicyRevision {
		return ApprovalRequest{}, ErrApprovalConflict
	}
	if err := depdb.New(tx).InsertApprovalRequest(ctx, depdb.InsertApprovalRequestParams{
		RequestID: dbUUID(normalized.RequestID), PublicationID: dbUUID(normalized.PublicationID), TargetID: normalized.TargetID,
		CandidateID: dbUUID(normalized.CandidateID), GenerationID: dbUUID(normalized.GenerationID), RequestDigest: normalized.RequestDigest,
		ExpectedTargetRevision: normalized.ExpectedTargetRevision, PolicyRevision: normalized.PolicyRevision, RequestedBy: normalized.RequestedBy.PrincipalID,
		RequestCredentialClass: normalized.RequestedBy.CredentialClass, RequestCredentialID: normalized.RequestedBy.CredentialID, RequestCredentialExpiresAt: pgTime(normalized.RequestedBy.CredentialExpiresAt),
		ExpiresAt: pgTime(normalized.ExpiresAt), OperationID: dbUUID(normalized.Evidence.OperationID), EventID: dbUUID(normalized.Evidence.EventID),
		AuditID: dbUUID(normalized.Evidence.AuditID), Evidence: normalized.Evidence.Metadata,
	}); err != nil {
		return ApprovalRequest{}, normalizeApprovalError(err)
	}
	request, err := loadApprovalRequest(ctx, tx, normalized.RequestID)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if err := a.appendEvidence(ctx, tx, ApprovalActionRequest, request, nil, normalized.Evidence); err != nil {
		return ApprovalRequest{}, err
	}
	return request, nil
}

func (a *ApprovalAuthority) Approve(ctx context.Context, input ApprovalDecisionInput) (ApprovalRequest, error) {
	return a.decide(ctx, input, ApprovalActionApprove)
}
func (a *ApprovalAuthority) Deny(ctx context.Context, input ApprovalDecisionInput) (ApprovalRequest, error) {
	return a.decide(ctx, input, ApprovalActionDeny)
}
func (a *ApprovalAuthority) Revoke(ctx context.Context, input ApprovalDecisionInput) (ApprovalRequest, error) {
	return a.decide(ctx, input, ApprovalActionRevoke)
}

type ApprovalDecisionInput struct {
	RequestID, DecisionID string
	ExpectedRevision      int64
	Actor                 ApprovalActor
	Evidence              ApprovalEvidence
}

func (a *ApprovalAuthority) decide(ctx context.Context, input ApprovalDecisionInput, action ApprovalAction) (ApprovalRequest, error) {
	tx, err := a.repository.begin(ctx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	request, err := a.DecideTx(ctx, tx, input, action)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ApprovalRequest{}, err
	}
	committed = true
	return request, nil
}

func (a *ApprovalAuthority) DecideTx(ctx context.Context, tx Tx, input ApprovalDecisionInput, action ApprovalAction) (ApprovalRequest, error) {
	if a == nil || a.repository == nil || tx == nil {
		return ApprovalRequest{}, ErrInvalid
	}
	requestID, err := uuidID(input.RequestID, "approval request id", false)
	if err != nil {
		return ApprovalRequest{}, err
	}
	decisionID, err := uuidID(input.DecisionID, "approval decision id", false)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if input.ExpectedRevision < 0 || (action != ApprovalActionApprove && action != ApprovalActionDeny && action != ApprovalActionRevoke) {
		return ApprovalRequest{}, ErrApprovalInvalid
	}
	input.Actor.CredentialExpiresAt = normalizeApprovalTime(input.Actor.CredentialExpiresAt)
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if err := input.Actor.validate(now); err != nil {
		return ApprovalRequest{}, err
	}
	evidence, err := input.Evidence.canonical()
	if err != nil {
		return ApprovalRequest{}, err
	}
	identity, err := depdb.New(tx).GetApprovalRequest(ctx, dbUUID(requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrApprovalNotFound
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	publication, err := depdb.New(tx).LockPublicationForApproval(ctx, dbUUID(identity.PublicationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrApprovalNotFound
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	if publication.TargetID != identity.TargetID || publication.GenerationID != identity.GenerationID || publication.CandidateID != identity.CandidateID || publication.RequestDigest != identity.RequestDigest || publication.ExpectedTargetRevision != identity.ExpectedTargetRevision {
		return ApprovalRequest{}, ErrApprovalConflict
	}
	if _, err := depdb.New(tx).LockApprovalRequest(ctx, dbUUID(requestID)); errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrApprovalNotFound
	} else if err != nil {
		return ApprovalRequest{}, err
	}
	request, err := loadApprovalRequest(ctx, tx, requestID)
	if err != nil {
		return ApprovalRequest{}, err
	}
	scope := ApprovalRequestInput{RequestID: request.RequestID, PublicationID: request.PublicationID, TargetID: request.TargetID, CandidateID: request.CandidateID, GenerationID: request.GenerationID, RequestDigest: request.RequestDigest, ExpectedTargetRevision: request.ExpectedTargetRevision, PolicyRevision: request.PolicyRevision, RequestedBy: request.RequestedBy, ExpiresAt: request.ExpiresAt, Evidence: request.Evidence}
	if a.authorize != nil {
		if err := a.authorize.AuthorizeApproval(ctx, ApprovalAuthorizationInput{Action: action, Request: scope, Current: &request, Actor: input.Actor}); err != nil {
			return ApprovalRequest{}, fmt.Errorf("%w: %v", ErrApprovalUnauthorized, err)
		}
	}
	if (action == ApprovalActionApprove || action == ApprovalActionDeny) && input.Actor.PrincipalID == request.RequestedBy.PrincipalID {
		return ApprovalRequest{}, ErrApprovalSeparationOfDuty
	}
	if existing, getErr := depdb.New(tx).GetApprovalDecision(ctx, dbUUID(decisionID)); getErr == nil {
		stored, valid := storedApprovalDecision(action)
		if !valid || existing.RequestID != requestID || existing.Decision != stored || existing.DecidedBy != input.Actor.PrincipalID || existing.DecisionCredentialClass != input.Actor.CredentialClass || existing.DecisionCredentialID != input.Actor.CredentialID || !dbTime(existing.DecisionCredentialExpiresAt).Equal(input.Actor.CredentialExpiresAt) || !sameCanonical(existing.Evidence, evidence.Metadata) || existing.OperationID != evidence.OperationID || existing.EventID != evidence.EventID || existing.AuditID != evidence.AuditID {
			return ApprovalRequest{}, ErrApprovalConflict
		}
		// An exact operation retry is idempotent even when its original
		// compare-and-swap revision has since advanced.
		return request, nil
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return ApprovalRequest{}, getErr
	}
	if publication.State != "pending" {
		return ApprovalRequest{}, ErrApprovalConflict
	}
	if !now.Before(request.ExpiresAt) {
		return ApprovalRequest{}, ErrApprovalExpired
	}
	// loadApprovalRequest includes the latest immutable decision, so this
	// compare-and-swap revision is safe while the request row is locked.
	if input.ExpectedRevision != requestRevision(request) {
		return ApprovalRequest{}, ErrApprovalConflict
	}
	revision, err := depdb.New(tx).NextApprovalDecisionRevision(ctx, dbUUID(requestID))
	if err != nil {
		return ApprovalRequest{}, err
	}
	storedDecision, ok := storedApprovalDecision(action)
	if !ok {
		return ApprovalRequest{}, ErrApprovalInvalid
	}
	decision := ApprovalDecision{DecisionID: decisionID, RequestID: requestID, Revision: revision, Decision: action, DecidedBy: input.Actor, DecidedAt: now, Evidence: evidence}
	if err := depdb.New(tx).InsertApprovalDecision(ctx, depdb.InsertApprovalDecisionParams{
		DecisionID: dbUUID(decisionID), RequestID: dbUUID(requestID), DecisionRevision: revision, Decision: storedDecision, DecidedBy: input.Actor.PrincipalID,
		DecisionCredentialClass: input.Actor.CredentialClass, DecisionCredentialID: input.Actor.CredentialID, DecisionCredentialExpiresAt: pgTime(input.Actor.CredentialExpiresAt),
		DecidedAt: pgTime(now), OperationID: dbUUID(evidence.OperationID), EventID: dbUUID(evidence.EventID), AuditID: dbUUID(evidence.AuditID), Evidence: evidence.Metadata,
	}); err != nil {
		return ApprovalRequest{}, normalizeApprovalError(err)
	}
	persisted, err := depdb.New(tx).GetApprovalDecision(ctx, dbUUID(decisionID))
	if err != nil {
		return ApprovalRequest{}, err
	}
	decision = ApprovalDecision{DecisionID: persisted.DecisionID, RequestID: persisted.RequestID, Revision: persisted.DecisionRevision, Decision: approvalActionFromStored(persisted.Decision), DecidedBy: ApprovalActor{PrincipalID: persisted.DecidedBy, CredentialClass: persisted.DecisionCredentialClass, CredentialID: persisted.DecisionCredentialID, CredentialExpiresAt: dbTime(persisted.DecisionCredentialExpiresAt)}, DecidedAt: dbTime(persisted.DecidedAt), Evidence: ApprovalEvidence{OperationID: persisted.OperationID, EventID: persisted.EventID, AuditID: persisted.AuditID, Metadata: append([]byte(nil), persisted.Evidence...)}}
	request.LatestDecision = &decision
	if err := a.appendEvidence(ctx, tx, action, request, &decision, evidence); err != nil {
		return ApprovalRequest{}, err
	}
	if action == ApprovalActionApprove && a.activation != nil {
		if err := a.activation.EnqueueApprovalActivation(ctx, tx, request, decision); err != nil {
			return ApprovalRequest{}, err
		}
	}
	return request, nil
}

func (a *ApprovalAuthority) Effective(ctx context.Context, requestID string) (ApprovalRequest, error) {
	db, err := requireDB(a.repository)
	if err != nil {
		return ApprovalRequest{}, err
	}
	return effectiveApproval(ctx, db, requestID)
}

func (a *ApprovalAuthority) EffectiveTx(ctx context.Context, tx Tx, requestID string) (ApprovalRequest, error) {
	if tx == nil {
		return ApprovalRequest{}, ErrInvalid
	}
	return effectiveApproval(ctx, tx, requestID)
}

func effectiveApproval(ctx context.Context, db DBTX, requestID string) (ApprovalRequest, error) {
	now, err := databaseNow(ctx, db)
	if err != nil {
		return ApprovalRequest{}, err
	}
	id, err := uuidID(requestID, "approval request id", false)
	if err != nil {
		return ApprovalRequest{}, err
	}
	row, err := depdb.New(db).GetEffectiveApproval(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		request, reqErr := loadApprovalRequest(ctx, db, id)
		if errors.Is(reqErr, ErrApprovalNotFound) {
			return ApprovalRequest{}, ErrApprovalNotFound
		}
		if reqErr != nil {
			return ApprovalRequest{}, reqErr
		}
		if !now.Before(request.ExpiresAt) {
			return ApprovalRequest{}, ErrApprovalExpired
		}
		return ApprovalRequest{}, ErrApprovalRequired
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	request := approvalRequestFromEffective(row)
	return request, nil
}

func (a *ApprovalAuthority) RequestByID(ctx context.Context, requestID string) (ApprovalRequest, error) {
	db, err := requireDB(a.repository)
	if err != nil {
		return ApprovalRequest{}, err
	}
	id, err := uuidID(requestID, "approval request id", false)
	if err != nil {
		return ApprovalRequest{}, err
	}
	return loadApprovalRequest(ctx, db, id)
}

func normalizeApprovalRequest(input ApprovalRequestInput) (ApprovalRequestInput, error) {
	now := time.Now().UTC()
	return normalizeApprovalRequestAt(input, now)
}

func normalizeApprovalRequestAt(input ApprovalRequestInput, now time.Time) (ApprovalRequestInput, error) {
	requestID, err := uuidID(input.RequestID, "approval request id", false)
	if err != nil {
		return ApprovalRequestInput{}, err
	}
	publicationID, err := uuidID(input.PublicationID, "publication id", false)
	if err != nil {
		return ApprovalRequestInput{}, err
	}
	target, err := textID(input.TargetID, "target id")
	if err != nil {
		return ApprovalRequestInput{}, err
	}
	candidateID, err := uuidID(input.CandidateID, "candidate id", false)
	if err != nil {
		return ApprovalRequestInput{}, err
	}
	generationID, err := uuidID(input.GenerationID, "generation id", false)
	if err != nil {
		return ApprovalRequestInput{}, err
	}
	if _, err := digest(input.RequestDigest, "request digest"); err != nil {
		return ApprovalRequestInput{}, err
	}
	input.ExpiresAt = normalizeApprovalTime(input.ExpiresAt)
	input.RequestedBy.CredentialExpiresAt = normalizeApprovalTime(input.RequestedBy.CredentialExpiresAt)
	if input.ExpectedTargetRevision <= 0 || input.PolicyRevision <= 0 || input.ExpiresAt.IsZero() {
		return ApprovalRequestInput{}, ErrApprovalInvalid
	}
	now = normalizeApprovalTime(now)
	if !input.ExpiresAt.UTC().After(now) || input.ExpiresAt.UTC().After(now.Add(approvalMaxLifetime)) || input.ExpiresAt.UTC().After(input.RequestedBy.CredentialExpiresAt.UTC()) {
		return ApprovalRequestInput{}, ErrApprovalExpired
	}
	if err := input.RequestedBy.validate(now); err != nil {
		return ApprovalRequestInput{}, err
	}
	evidence, err := input.Evidence.canonical()
	if err != nil {
		return ApprovalRequestInput{}, err
	}
	input.RequestID, input.PublicationID, input.TargetID = requestID, publicationID, target
	input.CandidateID, input.GenerationID, input.Evidence = candidateID, generationID, evidence
	return input, nil
}

// PostgreSQL timestamptz stores microsecond precision. Canonicalizing before
// persistence makes idempotent retries compare the same values they read back.
func normalizeApprovalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func loadApprovalRequest(ctx context.Context, db DBTX, id string) (ApprovalRequest, error) {
	row, err := depdb.New(db).GetApprovalRequest(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrApprovalNotFound
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	request := ApprovalRequest{
		RequestID: row.RequestID, PublicationID: row.PublicationID, TargetID: row.TargetID, CandidateID: row.CandidateID, GenerationID: row.GenerationID,
		RequestDigest: row.RequestDigest, ExpectedTargetRevision: row.ExpectedTargetRevision, PolicyRevision: row.PolicyRevision,
		RequestedBy: ApprovalActor{PrincipalID: row.RequestedBy, CredentialClass: row.RequestCredentialClass, CredentialID: row.RequestCredentialID, CredentialExpiresAt: dbTime(row.RequestCredentialExpiresAt)},
		RequestedAt: dbTime(row.RequestedAt), ExpiresAt: dbTime(row.ExpiresAt),
		Evidence: ApprovalEvidence{OperationID: row.OperationID, EventID: row.EventID, AuditID: row.AuditID, Metadata: append([]byte(nil), row.Evidence...)},
	}
	latest, err := depdb.New(db).GetLatestApprovalDecision(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return request, nil
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	request.LatestDecision = &ApprovalDecision{
		DecisionID: latest.DecisionID, RequestID: latest.RequestID, Revision: latest.DecisionRevision,
		Decision:  approvalActionFromStored(latest.Decision),
		DecidedBy: ApprovalActor{PrincipalID: latest.DecidedBy, CredentialClass: latest.DecisionCredentialClass, CredentialID: latest.DecisionCredentialID, CredentialExpiresAt: dbTime(latest.DecisionCredentialExpiresAt)},
		DecidedAt: dbTime(latest.DecidedAt), Evidence: ApprovalEvidence{OperationID: latest.OperationID, EventID: latest.EventID, AuditID: latest.AuditID, Metadata: append([]byte(nil), latest.Evidence...)},
	}
	return request, nil
}

func approvalRequestFromEffective(row depdb.GetEffectiveApprovalRow) ApprovalRequest {
	decision := &ApprovalDecision{DecisionID: row.DecisionID, RequestID: row.RequestID, Revision: row.DecisionRevision, Decision: approvalActionFromStored(row.Decision), DecidedBy: ApprovalActor{PrincipalID: row.DecidedBy, CredentialClass: row.DecisionCredentialClass, CredentialID: row.DecisionCredentialID, CredentialExpiresAt: dbTime(row.DecisionCredentialExpiresAt)}, DecidedAt: dbTime(row.DecidedAt), Evidence: ApprovalEvidence{OperationID: row.DecisionOperationID, EventID: row.DecisionEventID, AuditID: row.DecisionAuditID, Metadata: append([]byte(nil), row.DecisionEvidence...)}}
	return ApprovalRequest{RequestID: row.RequestID, PublicationID: row.PublicationID, TargetID: row.TargetID, CandidateID: row.CandidateID, GenerationID: row.GenerationID, RequestDigest: row.RequestDigest, ExpectedTargetRevision: row.ExpectedTargetRevision, PolicyRevision: row.PolicyRevision, RequestedBy: ApprovalActor{PrincipalID: row.RequestedBy, CredentialClass: row.RequestCredentialClass, CredentialID: row.RequestCredentialID, CredentialExpiresAt: dbTime(row.RequestCredentialExpiresAt)}, RequestedAt: dbTime(row.RequestedAt), ExpiresAt: dbTime(row.ExpiresAt), Evidence: ApprovalEvidence{OperationID: row.RequestOperationID, EventID: row.RequestEventID, AuditID: row.RequestAuditID, Metadata: append([]byte(nil), row.RequestEvidence...)}, LatestDecision: decision}
}

func approvalActionFromStored(decision string) ApprovalAction {
	switch decision {
	case "approved":
		return ApprovalActionApprove
	case "denied":
		return ApprovalActionDeny
	case "revoked":
		return ApprovalActionRevoke
	default:
		return ApprovalAction(decision)
	}
}

func storedApprovalDecision(action ApprovalAction) (string, bool) {
	switch action {
	case ApprovalActionApprove:
		return "approved", true
	case ApprovalActionDeny:
		return "denied", true
	case ApprovalActionRevoke:
		return "revoked", true
	default:
		return "", false
	}
}

func requestRevision(request ApprovalRequest) int64 {
	if request.LatestDecision == nil {
		return 0
	}
	return request.LatestDecision.Revision
}

func sameApprovalRequest(existing ApprovalRequest, input ApprovalRequestInput) bool {
	return existing.RequestID == input.RequestID && existing.PublicationID == input.PublicationID && existing.TargetID == input.TargetID && existing.CandidateID == input.CandidateID && existing.GenerationID == input.GenerationID && existing.RequestDigest == input.RequestDigest && existing.ExpectedTargetRevision == input.ExpectedTargetRevision && existing.PolicyRevision == input.PolicyRevision && existing.RequestedBy.PrincipalID == input.RequestedBy.PrincipalID && existing.RequestedBy.CredentialClass == input.RequestedBy.CredentialClass && existing.RequestedBy.CredentialID == input.RequestedBy.CredentialID && existing.RequestedBy.CredentialExpiresAt.Equal(input.RequestedBy.CredentialExpiresAt) && existing.ExpiresAt.Equal(input.ExpiresAt) && sameCanonical(existing.Evidence.Metadata, input.Evidence.Metadata) && existing.Evidence.OperationID == input.Evidence.OperationID && existing.Evidence.EventID == input.Evidence.EventID && existing.Evidence.AuditID == input.Evidence.AuditID
}

func (a *ApprovalAuthority) appendEvidence(ctx context.Context, tx Tx, action ApprovalAction, request ApprovalRequest, decision *ApprovalDecision, evidence ApprovalEvidence) error {
	if a.operation != nil {
		if err := a.operation.AppendApprovalOperation(ctx, tx, ApprovalOperation{Action: action, Request: request, Decision: decision, Evidence: evidence}); err != nil {
			return err
		}
	}
	if a.event != nil {
		if err := a.event.AppendApprovalEvent(ctx, tx, ApprovalEvent{Action: action, Request: request, Decision: decision, Evidence: evidence}); err != nil {
			return err
		}
	}
	if a.audit != nil {
		if err := a.audit.AppendApprovalAudit(ctx, tx, ApprovalAudit{Action: action, Request: request, Decision: decision, Evidence: evidence}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeApprovalError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %v", ErrApprovalConflict, err)
		case "23503":
			return fmt.Errorf("%w: %v", ErrApprovalNotFound, err)
		case "23514", "P0001":
			msg := strings.ToLower(pgErr.Message)
			switch {
			case strings.Contains(msg, "policy revision"):
				return fmt.Errorf("%w: %v", ErrApprovalConflict, err)
			case strings.Contains(msg, "expired"):
				return fmt.Errorf("%w: %v", ErrApprovalExpired, err)
			case strings.Contains(msg, "separation"):
				return fmt.Errorf("%w: %v", ErrApprovalSeparationOfDuty, err)
			default:
				return fmt.Errorf("%w: %v", ErrApprovalInvalid, err)
			}
		}
	}
	return err
}
