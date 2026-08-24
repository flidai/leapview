package deployment

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

var (
	ErrApprovalNotFound          = apigenfailure.New("approval_not_found", "deployment approval not found")
	ErrApprovalConflict          = apigenfailure.New("approval_conflict", "deployment approval conflict")
	ErrApprovalInvalid           = apigenfailure.New("approval_invalid", "deployment approval invalid")
	ErrApprovalRequired          = apigenfailure.New("approval_conflict", "deployment approval required")
	ErrApprovalExpired           = apigenfailure.New("approval_conflict", "deployment approval expired")
	ErrApprovalCredentialExpired = apigenfailure.New("approval_credential_expired", "deployment approval credential expired")
	ErrApprovalSeparationOfDuty  = apigenfailure.New("separation_of_duty", "deployment approval separation of duty violated")
	ErrApprovalScope             = apigenfailure.New("approval_not_found", "deployment approval scope mismatch")
)

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalRevoked  ApprovalStatus = "revoked"
	ApprovalExpired  ApprovalStatus = "expired"
)

type CredentialClass string

const (
	CredentialClassHuman    CredentialClass = "human"
	CredentialClassWorkload CredentialClass = "workload"
	CredentialClassAPIToken CredentialClass = "api_token"
	CredentialClassSession  CredentialClass = "session"
)

type ApprovalActor struct {
	PrincipalID         string
	CredentialClass     CredentialClass
	CredentialID        string
	CredentialExpiresAt time.Time
}

func (actor ApprovalActor) validate(now time.Time) error {
	if strings.TrimSpace(actor.PrincipalID) == "" ||
		strings.TrimSpace(actor.CredentialID) == "" {
		return fmt.Errorf("%w: principal and credential are required", ErrApprovalInvalid)
	}
	switch actor.CredentialClass {
	case CredentialClassHuman, CredentialClassWorkload,
		CredentialClassAPIToken, CredentialClassSession:
	default:
		return fmt.Errorf("%w: credential class is invalid", ErrApprovalInvalid)
	}
	if actor.CredentialExpiresAt.IsZero() ||
		!now.Before(actor.CredentialExpiresAt.UTC()) {
		return ErrApprovalCredentialExpired
	}
	return nil
}

type Approval struct {
	ID                          string
	ProjectID                   string
	DeploymentID                string
	Environment                 string
	RequestDigest               string
	ReleaseID                   string
	Status                      ApprovalStatus
	RequestedBy                 string
	RequestCredentialClass      CredentialClass
	RequestCredentialID         string
	RequestedAt                 time.Time
	ApprovedBy                  string
	ApprovalCredentialClass     CredentialClass
	ApprovalCredentialID        string
	ApprovalCredentialExpiresAt time.Time
	ApprovedAt                  time.Time
	RevokedBy                   string
	RevokedAt                   time.Time
	ExpiresAt                   time.Time
	Revision                    int64
}

func (approval Approval) Validate() error {
	if strings.TrimSpace(approval.ID) == "" ||
		strings.TrimSpace(approval.ProjectID) == "" ||
		strings.TrimSpace(approval.DeploymentID) == "" ||
		strings.TrimSpace(approval.Environment) == "" ||
		strings.TrimSpace(approval.RequestDigest) == "" ||
		strings.TrimSpace(approval.ReleaseID) == "" ||
		strings.TrimSpace(approval.RequestedBy) == "" ||
		strings.TrimSpace(approval.RequestCredentialID) == "" ||
		approval.RequestedAt.IsZero() ||
		approval.ExpiresAt.IsZero() ||
		approval.Revision < 1 {
		return fmt.Errorf("%w: approval identity is incomplete", ErrApprovalInvalid)
	}
	switch approval.RequestCredentialClass {
	case CredentialClassHuman, CredentialClassWorkload,
		CredentialClassAPIToken, CredentialClassSession:
	default:
		return fmt.Errorf("%w: request credential class is invalid", ErrApprovalInvalid)
	}
	switch approval.Status {
	case ApprovalPending:
		if approval.hasDecisionEvidence() ||
			approval.RevokedBy != "" || !approval.RevokedAt.IsZero() {
			return fmt.Errorf("%w: pending approval contains transition evidence", ErrApprovalInvalid)
		}
	case ApprovalApproved:
		if err := approval.validateDecisionEvidence(true); err != nil {
			return err
		}
		if approval.RevokedBy != "" || !approval.RevokedAt.IsZero() {
			return fmt.Errorf("%w: approval decision contains revocation evidence", ErrApprovalInvalid)
		}
	case ApprovalDenied:
		if err := approval.validateDecisionEvidence(true); err != nil {
			return err
		}
		if approval.RevokedBy != "" || !approval.RevokedAt.IsZero() {
			return fmt.Errorf("%w: denied decision contains revocation evidence", ErrApprovalInvalid)
		}
	case ApprovalRevoked:
		if approval.RevokedBy == "" || approval.RevokedAt.IsZero() {
			return fmt.Errorf("%w: revoked decision evidence is incomplete", ErrApprovalInvalid)
		}
		if err := approval.validateDecisionEvidence(false); err != nil {
			return err
		}
	case ApprovalExpired:
		if approval.RevokedBy != "" || !approval.RevokedAt.IsZero() {
			return fmt.Errorf("%w: expired approval contains revocation evidence", ErrApprovalInvalid)
		}
		if err := approval.validateDecisionEvidence(false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: approval status is invalid", ErrApprovalInvalid)
	}
	return nil
}

func (approval Approval) hasDecisionEvidence() bool {
	return approval.ApprovedBy != "" ||
		approval.ApprovalCredentialClass != "" ||
		approval.ApprovalCredentialID != "" ||
		!approval.ApprovalCredentialExpiresAt.IsZero() ||
		!approval.ApprovedAt.IsZero()
}

func (approval Approval) validateDecisionEvidence(required bool) error {
	if !approval.hasDecisionEvidence() {
		if required {
			return fmt.Errorf("%w: approval decision evidence is missing", ErrApprovalInvalid)
		}
		return nil
	}
	if approval.ApprovedBy == "" ||
		approval.ApprovalCredentialID == "" ||
		approval.ApprovalCredentialExpiresAt.IsZero() ||
		approval.ApprovedAt.IsZero() ||
		approval.ApprovedBy == approval.RequestedBy {
		return fmt.Errorf("%w: approval decision evidence is invalid", ErrApprovalInvalid)
	}
	switch approval.ApprovalCredentialClass {
	case CredentialClassHuman, CredentialClassWorkload,
		CredentialClassAPIToken, CredentialClassSession:
		return nil
	default:
		return fmt.Errorf("%w: approval credential class is invalid", ErrApprovalInvalid)
	}
}

type ApprovalRequest struct {
	ProjectID     string
	DeploymentID  string
	Environment   string
	RequestDigest string
	ReleaseID     string
	RequestedBy   ApprovalActor
}

type ApprovalTransition struct {
	ProjectID        string
	DeploymentID     string
	ApprovalID       string
	ExpectedRevision int64
	Actor            ApprovalActor
}

type ApprovalActivation struct {
	ProjectID     string
	DeploymentID  string
	Environment   string
	RequestDigest string
	ReleaseID     string
}

type ApprovalRepository interface {
	CreateApproval(context.Context, Approval) (Approval, error)
	ApprovalByDeployment(context.Context, string) (Approval, error)
	SaveApproval(context.Context, Approval, int64) (Approval, error)
}

type ApprovalServiceConfig struct {
	Lifetime time.Duration
	Now      func() time.Time
	NewID    func() (string, error)
	Random   io.Reader
}

type ApprovalService struct {
	repository ApprovalRepository
	lifetime   time.Duration
	now        func() time.Time
	newID      func() (string, error)
}

func NewApprovalService(
	repository ApprovalRepository,
	config ApprovalServiceConfig,
) (*ApprovalService, error) {
	if repository == nil {
		return nil, fmt.Errorf("deployment approval repository is required")
	}
	if config.Lifetime <= 0 {
		config.Lifetime = 30 * time.Minute
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewID == nil {
		random := config.Random
		if random == nil {
			random = rand.Reader
		}
		config.NewID = func() (string, error) {
			var entropy [18]byte
			if _, err := io.ReadFull(random, entropy[:]); err != nil {
				return "", err
			}
			return "approval_" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
		}
	}
	return &ApprovalService{
		repository: repository,
		lifetime:   config.Lifetime,
		now:        config.Now,
		newID:      config.NewID,
	}, nil
}

func (service *ApprovalService) Request(
	ctx context.Context,
	request ApprovalRequest,
) (Approval, error) {
	now := service.now().UTC()
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.DeploymentID = strings.TrimSpace(request.DeploymentID)
	request.Environment = strings.TrimSpace(request.Environment)
	request.RequestDigest = strings.TrimSpace(request.RequestDigest)
	request.ReleaseID = strings.TrimSpace(request.ReleaseID)
	if request.ProjectID == "" || request.DeploymentID == "" ||
		request.Environment == "" || request.RequestDigest == "" ||
		request.ReleaseID == "" {
		return Approval{}, fmt.Errorf("%w: approval scope is incomplete", ErrApprovalInvalid)
	}
	if err := request.RequestedBy.validate(now); err != nil {
		return Approval{}, err
	}
	existing, err := service.repository.ApprovalByDeployment(
		ctx,
		request.DeploymentID,
	)
	if err == nil && existing.Status != ApprovalDenied &&
		existing.Status != ApprovalRevoked && existing.Status != ApprovalExpired {
		if existing.ProjectID != request.ProjectID ||
			existing.Environment != request.Environment ||
			existing.RequestDigest != request.RequestDigest ||
			existing.ReleaseID != request.ReleaseID ||
			existing.RequestedBy != strings.TrimSpace(
				request.RequestedBy.PrincipalID,
			) {
			return Approval{}, ErrApprovalConflict
		}
		if !now.Before(existing.ExpiresAt) {
			existing.Status = ApprovalExpired
			existing.Revision++
			if err := existing.Validate(); err != nil {
				return Approval{}, err
			}
			if _, err := service.repository.SaveApproval(
				WithoutAuditIntent(ctx),
				existing,
				existing.Revision-1,
			); err != nil {
				return Approval{}, err
			}
		} else {
			return existing, nil
		}
	}
	if err != nil && !errors.Is(err, ErrApprovalNotFound) {
		return Approval{}, err
	}
	id, err := service.newID()
	if err != nil {
		return Approval{}, fmt.Errorf("generate deployment approval id: %w", err)
	}
	approval := Approval{
		ID: id, ProjectID: request.ProjectID,
		DeploymentID:           request.DeploymentID,
		Environment:            request.Environment,
		RequestDigest:          request.RequestDigest,
		ReleaseID:              request.ReleaseID,
		Status:                 ApprovalPending,
		RequestedBy:            strings.TrimSpace(request.RequestedBy.PrincipalID),
		RequestCredentialClass: request.RequestedBy.CredentialClass,
		RequestCredentialID:    strings.TrimSpace(request.RequestedBy.CredentialID),
		RequestedAt:            now,
		ExpiresAt:              now.Add(service.lifetime),
		Revision:               1,
	}
	if err := approval.Validate(); err != nil {
		return Approval{}, err
	}
	return service.repository.CreateApproval(ctx, approval)
}

func (service *ApprovalService) ValidateActor(actor ApprovalActor) error {
	if service == nil {
		return fmt.Errorf("deployment approval service is unavailable")
	}
	return actor.validate(service.now().UTC())
}

func (service *ApprovalService) Current(
	ctx context.Context,
	deploymentID string,
) (Approval, error) {
	if service == nil {
		return Approval{}, ErrApprovalNotFound
	}
	current, err := service.repository.ApprovalByDeployment(
		ctx,
		strings.TrimSpace(deploymentID),
	)
	if err != nil {
		return Approval{}, err
	}
	now := service.now().UTC()
	if (current.Status == ApprovalPending || current.Status == ApprovalApproved) &&
		!now.Before(current.ExpiresAt) {
		expectedRevision := current.Revision
		current.Status = ApprovalExpired
		current.Revision++
		if err := current.Validate(); err != nil {
			return Approval{}, err
		}
		saved, err := service.repository.SaveApproval(WithoutAuditIntent(ctx), current, expectedRevision)
		if errors.Is(err, ErrApprovalConflict) {
			return service.repository.ApprovalByDeployment(
				ctx,
				strings.TrimSpace(deploymentID),
			)
		}
		return saved, err
	}
	return current, nil
}

func (service *ApprovalService) Approve(
	ctx context.Context,
	transition ApprovalTransition,
) (Approval, error) {
	return service.decide(ctx, transition, ApprovalApproved)
}

func (service *ApprovalService) Deny(
	ctx context.Context,
	transition ApprovalTransition,
) (Approval, error) {
	return service.decide(ctx, transition, ApprovalDenied)
}

func (service *ApprovalService) decide(
	ctx context.Context,
	transition ApprovalTransition,
	status ApprovalStatus,
) (Approval, error) {
	now := service.now().UTC()
	current, err := service.loadTransition(ctx, transition, now)
	if err != nil {
		return Approval{}, err
	}
	if current.Status != ApprovalPending {
		return Approval{}, fmt.Errorf("%w: approval is %s", ErrApprovalConflict, current.Status)
	}
	if strings.TrimSpace(transition.Actor.PrincipalID) == current.RequestedBy {
		return Approval{}, ErrApprovalSeparationOfDuty
	}
	if status != ApprovalApproved && status != ApprovalDenied {
		return Approval{}, fmt.Errorf("%w: approval decision is invalid", ErrApprovalInvalid)
	}
	current.Status = status
	current.ApprovedBy = strings.TrimSpace(transition.Actor.PrincipalID)
	current.ApprovalCredentialClass = transition.Actor.CredentialClass
	current.ApprovalCredentialID = strings.TrimSpace(transition.Actor.CredentialID)
	current.ApprovalCredentialExpiresAt = transition.Actor.CredentialExpiresAt.UTC()
	current.ApprovedAt = now
	if expires := transition.Actor.CredentialExpiresAt.UTC(); expires.Before(current.ExpiresAt) {
		current.ExpiresAt = expires
	}
	current.Revision++
	if err := current.Validate(); err != nil {
		return Approval{}, err
	}
	return service.repository.SaveApproval(ctx, current, transition.ExpectedRevision)
}

func (service *ApprovalService) Revoke(
	ctx context.Context,
	transition ApprovalTransition,
) (Approval, error) {
	now := service.now().UTC()
	current, err := service.loadTransition(ctx, transition, now)
	if err != nil {
		return Approval{}, err
	}
	if current.Status != ApprovalPending && current.Status != ApprovalApproved {
		return Approval{}, fmt.Errorf("%w: approval is %s", ErrApprovalConflict, current.Status)
	}
	current.Status = ApprovalRevoked
	current.RevokedBy = strings.TrimSpace(transition.Actor.PrincipalID)
	current.RevokedAt = now
	current.Revision++
	if err := current.Validate(); err != nil {
		return Approval{}, err
	}
	return service.repository.SaveApproval(ctx, current, transition.ExpectedRevision)
}

func (service *ApprovalService) AuthorizeActivation(
	ctx context.Context,
	request ApprovalActivation,
) (Approval, error) {
	approval, err := service.repository.ApprovalByDeployment(
		ctx,
		strings.TrimSpace(request.DeploymentID),
	)
	if err != nil {
		if errors.Is(err, ErrApprovalNotFound) {
			return Approval{}, ErrApprovalRequired
		}
		return Approval{}, err
	}
	if approval.ProjectID != strings.TrimSpace(request.ProjectID) ||
		approval.DeploymentID != strings.TrimSpace(request.DeploymentID) ||
		approval.Environment != strings.TrimSpace(request.Environment) ||
		approval.RequestDigest != strings.TrimSpace(request.RequestDigest) ||
		approval.ReleaseID != strings.TrimSpace(request.ReleaseID) {
		return Approval{}, ErrApprovalScope
	}
	if approval.Status != ApprovalApproved {
		return Approval{}, ErrApprovalRequired
	}
	if !service.now().UTC().Before(approval.ExpiresAt) {
		return Approval{}, ErrApprovalExpired
	}
	if err := approval.Validate(); err != nil {
		return Approval{}, err
	}
	return approval, nil
}

func (service *ApprovalService) loadTransition(
	ctx context.Context,
	transition ApprovalTransition,
	now time.Time,
) (Approval, error) {
	if err := transition.Actor.validate(now); err != nil {
		return Approval{}, err
	}
	current, err := service.repository.ApprovalByDeployment(
		ctx,
		strings.TrimSpace(transition.DeploymentID),
	)
	if err != nil {
		return Approval{}, err
	}
	if current.ProjectID != strings.TrimSpace(transition.ProjectID) ||
		current.DeploymentID != strings.TrimSpace(transition.DeploymentID) ||
		current.ID != strings.TrimSpace(transition.ApprovalID) {
		return Approval{}, ErrApprovalScope
	}
	if current.Revision != transition.ExpectedRevision {
		return Approval{}, ErrApprovalConflict
	}
	if !now.Before(current.ExpiresAt) {
		return Approval{}, ErrApprovalExpired
	}
	return current, nil
}
