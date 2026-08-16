package deployment

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	defaultCandidateLifetime       = 8 * time.Hour
	defaultCandidateQuota          = 4
	CandidateBaseGenerationEmpty   = "empty"
	CandidateAuditStarted          = "candidate.started"
	CandidateAuditArtifactReplaced = "candidate.artifact_replaced"
	CandidateAuditReady            = "candidate.ready"
	CandidateAuditFailed           = "candidate.failed"
	CandidateAuditRetried          = "candidate.retried"
	CandidateAuditCancelled        = "candidate.cancelled"
	CandidateAuditExpired          = "candidate.expired"
)

type CandidateRepository interface {
	ActiveCandidateBaseScope(context.Context, projectgraph.ResourceID, string) (CandidateScope, error)
	StartCandidate(context.Context, Candidate, int) (Candidate, bool, error)
	ActiveCandidate(context.Context, string, projectgraph.ResourceID, string, string) (Candidate, error)
	CandidateByID(context.Context, string) (Candidate, error)
	SaveCandidate(context.Context, Candidate, int64) (Candidate, error)
	ExpireCandidates(context.Context, string, time.Time) (int64, error)
}

type CandidateServiceConfig struct {
	TargetID          string
	CanonicalOrigin   string
	Environment       string
	Lifetime          time.Duration
	MaxActivePerOwner int
	Now               func() time.Time
	NewID             func() (string, error)
	Audit             func(context.Context, CandidateEvent) error
	Logger            *slog.Logger
	RuntimeLifecycle  CandidateRuntimeLifecycle
}

// CandidateRuntimeLifecycle retires private generations after durable
// candidate transitions. Implementations must make both operations idempotent.
type CandidateRuntimeLifecycle interface {
	RetireCandidate(string) int
	ReapExpiredCandidates(time.Time) int
}

type CandidateService struct {
	repository        CandidateRepository
	targetID          string
	canonicalOrigin   string
	environment       string
	lifetime          time.Duration
	maxActivePerOwner int
	now               func() time.Time
	newID             func() (string, error)
	audit             func(context.Context, CandidateEvent) error
	logger            *slog.Logger
	runtimeLifecycle  CandidateRuntimeLifecycle
}

type StartCandidateRequest struct {
	ProjectID      projectgraph.ResourceID
	OwnerID        string
	ArtifactDigest string
	Key            string
}

type CandidateStartResult struct {
	Candidate  Candidate
	PreviewURL string
	Resumed    bool
}

type CandidateEvent struct {
	Action       string
	CandidateID  string
	ProjectID    projectgraph.ResourceID
	BaseIdentity *projectgraph.ServingIdentity
	TargetID     string
	PrincipalID  string
	Status       CandidateStatus
	MetadataJSON string
}

func NewCandidateService(repository CandidateRepository, config CandidateServiceConfig) (*CandidateService, error) {
	if repository == nil {
		return nil, fmt.Errorf("candidate repository is required")
	}
	if config.Audit == nil {
		return nil, fmt.Errorf("%w: recorder is required", ErrCandidateAuditUnavailable)
	}
	if config.TargetID != strings.TrimSpace(config.TargetID) || config.Environment != strings.TrimSpace(config.Environment) {
		return nil, fmt.Errorf("candidate target and environment must be canonical")
	}
	origin, err := canonicalCandidateOrigin(config.CanonicalOrigin)
	if err != nil {
		return nil, err
	}
	if config.TargetID == "" || config.Environment == "" {
		return nil, fmt.Errorf("candidate target and environment are required")
	}
	if config.Lifetime <= 0 {
		config.Lifetime = defaultCandidateLifetime
	}
	if config.MaxActivePerOwner <= 0 {
		config.MaxActivePerOwner = defaultCandidateQuota
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewID == nil {
		config.NewID = newCandidateID
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &CandidateService{
		repository: repository, targetID: config.TargetID, canonicalOrigin: origin,
		environment: config.Environment, lifetime: config.Lifetime,
		maxActivePerOwner: config.MaxActivePerOwner, now: config.Now, newID: config.NewID, audit: config.Audit,
		logger:           config.Logger,
		runtimeLifecycle: config.RuntimeLifecycle,
	}, nil
}

func (service *CandidateService) Start(ctx context.Context, request StartCandidateRequest) (CandidateStartResult, error) {
	now := service.now().UTC()
	if request.ProjectID.Validate() != nil || request.ProjectID.String() != strings.TrimSpace(request.ProjectID.String()) {
		return CandidateStartResult{}, fmt.Errorf("%w: project id must be canonical", ErrCandidateInvalid)
	}
	baseScope, err := service.repository.ActiveCandidateBaseScope(ctx, request.ProjectID, service.environment)
	if err != nil {
		return CandidateStartResult{}, fmt.Errorf("resolve candidate base generation: %w", err)
	}
	id, err := service.newID()
	if err != nil {
		return CandidateStartResult{}, fmt.Errorf("generate candidate id: %w", err)
	}
	candidate, err := NewCandidate(CandidateStartInput{
		ID: id, TargetID: service.targetID,
		OwnerID: request.OwnerID, Scope: CandidateScope{ProjectID: baseScope.ProjectID, Environment: baseScope.Environment, BaseGenerationID: baseScope.BaseGenerationID}, ArtifactDigest: request.ArtifactDigest,
		Key: request.Key, ExpiresAt: now.Add(service.lifetime), Now: now,
	})
	if err != nil {
		return CandidateStartResult{}, err
	}
	candidate, resumed, err := service.repository.StartCandidate(ctx, candidate, service.maxActivePerOwner)
	if err != nil {
		return CandidateStartResult{}, err
	}
	// StartCandidate is one stable operation regardless of whether storage
	// creates a candidate or resumes the author's existing candidate. Preserve
	// that distinction as metadata instead of changing the audit action.
	service.recordBestEffort(ctx, CandidateAuditStarted, candidate, map[string]any{"resumed": resumed})
	return CandidateStartResult{Candidate: candidate, PreviewURL: service.PreviewURL(candidate.ID), Resumed: resumed}, nil
}

func (service *CandidateService) CancelActive(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	ownerID,
	key string,
) (Candidate, error) {
	if projectID.Validate() != nil || ownerID != strings.TrimSpace(ownerID) || key != strings.TrimSpace(key) {
		return Candidate{}, ErrCandidateInvalid
	}
	candidate, err := service.repository.ActiveCandidate(
		ctx,
		service.targetID,
		projectID,
		ownerID,
		normalizeCandidateKey(key),
	)
	if err != nil {
		return Candidate{}, err
	}
	return service.Cancel(ctx, candidateScopeForService(candidate))
}

func candidateScopeForService(candidate Candidate) CandidateAccessScope {
	return CandidateAccessScope{
		ProjectID: candidate.Scope.ProjectID, CandidateID: candidate.ID,
		OwnerID: candidate.OwnerID, TargetID: candidate.TargetID,
	}
}

func (service *CandidateService) Get(ctx context.Context, scope CandidateAccessScope) (Candidate, error) {
	if scope.ProjectID.Validate() != nil || scope.CandidateID == "" || scope.CandidateID != strings.TrimSpace(scope.CandidateID) || scope.OwnerID != strings.TrimSpace(scope.OwnerID) {
		return Candidate{}, ErrCandidateInvalid
	}
	candidate, err := service.repository.CandidateByID(ctx, scope.CandidateID)
	if err != nil {
		return Candidate{}, err
	}
	if !service.owns(candidate, scope) {
		return Candidate{}, ErrCandidateNotFound
	}
	return service.expireOnRead(ctx, candidate)
}

func (service *CandidateService) GetOwned(ctx context.Context, candidateID, ownerID string) (Candidate, error) {
	if candidateID == "" || candidateID != strings.TrimSpace(candidateID) || ownerID != strings.TrimSpace(ownerID) {
		return Candidate{}, ErrCandidateInvalid
	}
	candidate, err := service.repository.CandidateByID(ctx, candidateID)
	if err != nil {
		return Candidate{}, err
	}
	if candidate.TargetID != service.targetID || candidate.OwnerID != ownerID {
		return Candidate{}, ErrCandidateNotFound
	}
	return service.expireOnRead(ctx, candidate)
}

func (service *CandidateService) Review(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	candidateID string,
) (Candidate, error) {
	if projectID.Validate() != nil || candidateID == "" || candidateID != strings.TrimSpace(candidateID) {
		return Candidate{}, ErrCandidateInvalid
	}
	candidate, err := service.repository.CandidateByID(
		ctx,
		candidateID,
	)
	if err != nil {
		return Candidate{}, err
	}
	if candidate.TargetID != service.targetID ||
		candidate.Scope.ProjectID != projectID {
		return Candidate{}, ErrCandidateNotFound
	}
	return service.expireOnRead(ctx, candidate)
}

func (service *CandidateService) ReplaceArtifact(ctx context.Context, scope CandidateAccessScope, expectedDigest, nextDigest string) (Candidate, error) {
	return service.mutate(ctx, scope, CandidateAuditArtifactReplaced, func(candidate Candidate) (Candidate, error) {
		now := service.now().UTC()
		return candidate.ReplaceArtifact(expectedDigest, nextDigest, now, now.Add(service.lifetime))
	})
}

func (service *CandidateService) MarkReady(
	ctx context.Context,
	scope CandidateAccessScope,
	artifactDigest,
	provenanceDigest string,
) (Candidate, error) {
	return service.mutate(ctx, scope, CandidateAuditReady, func(candidate Candidate) (Candidate, error) {
		return candidate.MarkReady(
			artifactDigest,
			provenanceDigest,
			service.now().UTC(),
		)
	})
}

func (service *CandidateService) MarkFailed(ctx context.Context, scope CandidateAccessScope, artifactDigest, failureCode string) (Candidate, error) {
	return service.mutate(ctx, scope, CandidateAuditFailed, func(candidate Candidate) (Candidate, error) {
		return candidate.MarkFailed(artifactDigest, failureCode, service.now().UTC())
	})
}

func (service *CandidateService) Retry(ctx context.Context, scope CandidateAccessScope) (Candidate, error) {
	return service.mutate(ctx, scope, CandidateAuditRetried, func(candidate Candidate) (Candidate, error) {
		now := service.now().UTC()
		return candidate.Retry(now, now.Add(service.lifetime))
	})
}

func (service *CandidateService) Cancel(ctx context.Context, scope CandidateAccessScope) (Candidate, error) {
	candidate, err := service.mutate(ctx, scope, CandidateAuditCancelled, func(candidate Candidate) (Candidate, error) {
		return candidate.Cancel(service.now().UTC())
	})
	if err == nil && service.runtimeLifecycle != nil {
		service.runtimeLifecycle.RetireCandidate(candidate.ID)
	}
	return candidate, err
}

func (service *CandidateService) Reconcile(ctx context.Context) (int64, error) {
	now := service.now().UTC()
	count, err := service.repository.ExpireCandidates(ctx, service.targetID, now)
	if err != nil {
		return count, err
	}
	if service.runtimeLifecycle != nil {
		service.runtimeLifecycle.ReapExpiredCandidates(now)
	}
	return count, nil
}

func (service *CandidateService) PreviewURL(candidateID string) string {
	return service.canonicalOrigin + "/candidates/" + url.PathEscape(candidateID)
}

func (service *CandidateService) mutate(
	ctx context.Context,
	scope CandidateAccessScope,
	action string,
	transition func(Candidate) (Candidate, error),
) (Candidate, error) {
	current, err := service.Get(ctx, scope)
	if err != nil {
		return Candidate{}, err
	}
	next, err := transition(current)
	if err != nil {
		return Candidate{}, err
	}
	if next == current {
		return current, nil
	}
	saved, err := service.repository.SaveCandidate(ctx, next, current.Revision)
	if err != nil {
		return Candidate{}, err
	}
	service.recordBestEffort(ctx, action, saved, nil)
	return saved, nil
}

func (service *CandidateService) owns(candidate Candidate, scope CandidateAccessScope) bool {
	if scope.CandidateID != strings.TrimSpace(scope.CandidateID) || scope.OwnerID != strings.TrimSpace(scope.OwnerID) || scope.TargetID != strings.TrimSpace(scope.TargetID) || scope.ProjectID.Validate() != nil {
		return false
	}
	targetID := scope.TargetID
	if targetID == "" {
		targetID = service.targetID
	}
	return scope.CandidateID == candidate.ID &&
		scope.ProjectID == candidate.Scope.ProjectID &&
		scope.OwnerID == candidate.OwnerID &&
		targetID == service.targetID && candidate.TargetID == service.targetID
}

func (service *CandidateService) expireOnRead(ctx context.Context, candidate Candidate) (Candidate, error) {
	now := service.now().UTC()
	for attempt := 0; attempt < 2; attempt++ {
		expired, changed, err := candidate.Expire(now)
		if err != nil || !changed {
			return expired, err
		}
		saved, err := service.repository.SaveCandidate(ctx, expired, candidate.Revision)
		if err == nil {
			service.recordBestEffort(ctx, CandidateAuditExpired, saved, nil)
			if service.runtimeLifecycle != nil {
				service.runtimeLifecycle.RetireCandidate(saved.ID)
			}
			return saved, nil
		}
		if !errors.Is(err, ErrCandidateConflict) {
			return Candidate{}, err
		}
		candidate, err = service.repository.CandidateByID(ctx, candidate.ID)
		if err != nil {
			return Candidate{}, err
		}
	}
	return Candidate{}, ErrCandidateConflict
}

func (service *CandidateService) record(ctx context.Context, operationID, action string, candidate Candidate, metadata map[string]any) error {
	if service.audit == nil {
		return ErrCandidateAuditUnavailable
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["environment"] = candidate.Scope.Environment
	metadata["baseGeneration"] = candidate.Scope.BaseGenerationID
	metadata["projectId"] = candidate.Scope.ProjectID.String()
	metadata["candidateKey"] = candidate.Key
	resumed, _ := metadata["resumed"].(bool)
	payload := deploymentgen.GenSchemaCandidateAuditPayload{
		OperationId: operationID, CandidateId: candidate.ID, ProjectId: candidate.Scope.ProjectID.String(),
		TargetId: candidate.TargetID, Environment: candidate.Scope.Environment,
		BaseGeneration: candidate.Scope.BaseGenerationID, CandidateKey: candidate.Key,
		Status: string(candidate.Status), Resumed: resumed,
	}
	var encoded string
	var err error
	switch operationID {
	case string(deploymentgen.GenOperationStartProjectCandidate):
		encoded, err = deploymentgen.EncodeGenStartProjectCandidateAuditPayload(payload)
	case string(deploymentgen.GenOperationReplaceProjectCandidateArtifact):
		encoded, err = deploymentgen.EncodeGenReplaceProjectCandidateArtifactAuditPayload(payload)
	case string(deploymentgen.GenOperationRetryProjectCandidate):
		encoded, err = deploymentgen.EncodeGenRetryProjectCandidateAuditPayload(payload)
	case string(deploymentgen.GenOperationCancelProjectCandidate):
		encoded, err = deploymentgen.EncodeGenCancelProjectCandidateAuditPayload(payload)
	case string(deploymentgen.GenOperationCancelProjectCandidateByKey):
		encoded, err = deploymentgen.EncodeGenCancelProjectCandidateByKeyAuditPayload(payload)
	case string(deploymentgen.GenOperationCommitProjectCandidateSynchronization):
		encoded, err = deploymentgen.EncodeGenCommitProjectCandidateSynchronizationAuditPayload(payload)
	default:
		encodedBytes, marshalErr := json.Marshal(metadata)
		if marshalErr != nil {
			return marshalErr
		}
		encoded = string(encodedBytes)
	}
	if err != nil {
		return err
	}
	baseIdentity, _ := candidate.Scope.BaseIdentity()
	return service.audit(ctx, CandidateEvent{
		Action: action, CandidateID: candidate.ID, ProjectID: candidate.Scope.ProjectID, BaseIdentity: baseIdentity, TargetID: candidate.TargetID,
		PrincipalID: candidate.OwnerID, Status: candidate.Status, MetadataJSON: string(encoded),
	})
}

func (service *CandidateService) recordBestEffort(
	ctx context.Context,
	action string,
	candidate Candidate,
	metadata map[string]any,
) {
	logger := service.logger
	if logger == nil {
		logger = slog.Default()
	}
	operationID, command := apigencommand.OperationID(ctx)
	if command {
		contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(operationID)
		if !ok || contract.AuditAction != action {
			operationID, command = candidateOperationID(action)
		}
	} else {
		operationID, command = candidateOperationID(action)
	}
	if !command {
		if err := service.record(ctx, "", action, candidate, metadata); err != nil {
			logger.ErrorContext(ctx, "candidate audit failed", "audit_action", action, "candidate_id", candidate.ID, "project_id", candidate.Scope.ProjectID.String(), "principal_id", candidate.OwnerID, "error", err)
		}
		return
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		logger.ErrorContext(ctx, "candidate command executor is unavailable", "operation_id", operationID, "error", err)
		return
	}
	err = executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if action != contract.AuditAction {
				return fmt.Errorf("candidate audit action %q does not match generated action %q", action, contract.AuditAction)
			}
			return service.record(ctx, contract.OperationID, contract.AuditAction, candidate, metadata)
		},
		LogMessage: "candidate audit failed",
		LogAttributes: []slog.Attr{
			slog.String("candidate_id", candidate.ID),
			slog.String("project_id", candidate.Scope.ProjectID.String()),
			slog.String("principal_id", candidate.OwnerID),
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "candidate command contract execution failed", "operation_id", operationID, "error", err)
	}
}

func candidateOperationID(action string) (string, bool) {
	switch action {
	case CandidateAuditStarted:
		return string(deploymentgen.GenOperationStartProjectCandidate), true
	case CandidateAuditArtifactReplaced:
		return string(deploymentgen.GenOperationReplaceProjectCandidateArtifact), true
	case CandidateAuditReady:
		return string(deploymentgen.GenOperationCommitProjectCandidateSynchronization), true
	case CandidateAuditRetried:
		return string(deploymentgen.GenOperationRetryProjectCandidate), true
	case CandidateAuditCancelled:
		return string(deploymentgen.GenOperationCancelProjectCandidate), true
	default:
		return "", false
	}
}

func canonicalCandidateOrigin(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	origin, err := url.Parse(value)
	if err != nil || (origin.Scheme != "https" && origin.Scheme != "http") || origin.Host == "" ||
		(origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
		return "", fmt.Errorf("candidate canonical origin must be an HTTP(S) origin")
	}
	return origin.Scheme + "://" + origin.Host, nil
}

func newCandidateID() (string, error) {
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return "cand_" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}
