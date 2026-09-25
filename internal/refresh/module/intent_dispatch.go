package module

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
)

// ManualPipelineIntentCommand is a generation-independent request to run an
// authored pipeline. Identity supplies only the stable project/environment
// scope; the queued run receives its serving generation during dispatch.
type ManualPipelineIntentCommand struct {
	Identity       projectgraph.ServingIdentity
	PipelineID     string
	PrincipalID    string
	IdempotencyKey string
	RequestID      string
	CorrelationID  string
	RetryOf        string
}

// ManualIntentView is the product-owned read projection for accepted manual
// requests. RunID is populated after the reserved run is attached.
type ManualIntentView struct {
	IntentID      string
	ReservedRunID string
	RunID         string
	PipelineID    string
	Status        string
	Reason        string
	CreatedAt     time.Time
	QueuePosition int
	CancelAllowed bool
}

type manualIntentRepository interface {
	InTx(context.Context, func(refreshpostgres.Tx) error) error
	CreateManualIntentWithAudit(context.Context, refreshpostgres.ManualIntentInput, func(context.Context, refreshpostgres.Tx, refreshpostgres.ManualIntent) error) (refreshpostgres.ManualIntent, bool, error)
	ClaimNextManualIntent(context.Context, refreshpostgres.Scope, string, time.Duration) (refreshpostgres.ManualIntent, bool, error)
	AttachManualIntentTx(context.Context, refreshpostgres.Tx, string, string, int64, string) error
	ReleaseManualIntentTx(context.Context, refreshpostgres.Tx, string, string, int64) error
	MarkManualIntentStaleTx(context.Context, refreshpostgres.Tx, string, string, int64) error
	GetManualIntent(context.Context, refreshpostgres.Scope, string) (refreshpostgres.ManualIntent, error)
	GetManualIntentByIdempotency(context.Context, refreshpostgres.Scope, string, string) (refreshpostgres.ManualIntent, error)
	ListManualIntents(context.Context, refreshpostgres.Scope, string, int) ([]refreshpostgres.ManualIntent, error)
	ListRecentStaleManualIntents(context.Context, refreshpostgres.Scope, string, int) ([]refreshpostgres.ManualIntent, error)
	CancelManualIntentWithAudit(context.Context, refreshpostgres.Scope, string, func(context.Context, refreshpostgres.Tx, refreshpostgres.ManualIntent) error) (refreshpostgres.ManualIntent, error)
}

func (m *Module) manualIntentReady() bool {
	return m != nil && m.manualIntents != nil && m.runs != nil && m.manualTargetID != "" && m.manualIntentOwner != ""
}

// QueueManualPipelineIntent durably records a browser Run now request without
// binding it to the generation active when the request arrives.
func (m *Module) QueueManualPipelineIntent(ctx context.Context, command ManualPipelineIntentCommand) (ManualIntentView, error) {
	if !m.manualIntentReady() {
		return ManualIntentView{}, errors.New("durable manual refresh intent persistence is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := command.Identity.Validate(); err != nil {
		return ManualIntentView{}, err
	}
	pipelineID, err := projectgraph.NewResourceID(command.PipelineID)
	if err != nil || pipelineID.String() != command.PipelineID {
		return ManualIntentView{}, errors.New("manual refresh pipeline id is invalid")
	}
	principalID := strings.TrimSpace(command.PrincipalID)
	if principalID == "" {
		return ManualIntentView{}, errors.New("manual refresh principal is required")
	}
	key := strings.TrimSpace(command.IdempotencyKey)
	if key == "" {
		return ManualIntentView{}, errors.New("manual refresh idempotency key is required")
	}
	if key != command.IdempotencyKey {
		return ManualIntentView{}, errors.New("manual refresh idempotency key is not canonical")
	}
	requestDigest, err := manualIntentRequestDigest(command.Identity, principalID, pipelineID, m.manualTargetID, command.RetryOf)
	if err != nil {
		return ManualIntentView{}, err
	}
	existing, replayErr := m.manualIntents.GetManualIntentByIdempotency(ctx, refreshpostgres.Scope{
		ProjectID: command.Identity.ProjectID.String(), Environment: command.Identity.Environment,
	}, principalID, key)
	if replayErr == nil {
		if existing.TargetID != m.manualTargetID || existing.PipelineID != pipelineID.String() || existing.RequestDigest != requestDigest {
			return ManualIntentView{}, apigenfailure.New("conflict", "This request key was already used for a different refresh request.")
		}
		return m.manualIntentView(ctx, existing, 0), nil
	}
	if !errors.Is(replayErr, refreshpostgres.ErrNotFound) {
		return ManualIntentView{}, fmt.Errorf("read manual refresh intent replay: %w", replayErr)
	}
	activeIdentity, err := m.ActiveServingIdentity(ctx)
	if err != nil {
		return ManualIntentView{}, fmt.Errorf("resolve active refresh identity for manual intent: %w", err)
	}
	if err := activeIdentity.Validate(); err != nil {
		return ManualIntentView{}, err
	}
	if activeIdentity.ProjectID != command.Identity.ProjectID || activeIdentity.Environment != command.Identity.Environment {
		return ManualIntentView{}, errors.New("manual refresh identity does not match the active project/environment")
	}
	if m.service.Artifacts == nil {
		return ManualIntentView{}, errors.New("refresh artifact loader is unavailable")
	}
	active, err := m.activeRefreshState(ctx, activeIdentity)
	if err != nil {
		return ManualIntentView{}, fmt.Errorf("resolve active refresh definition for manual intent: %w", err)
	}
	loaded, err := m.service.Artifacts.Load(ctx, active.Artifact)
	if err != nil {
		return ManualIntentView{}, fmt.Errorf("load active refresh definition for manual intent: %w", err)
	}
	if loaded.Definition == nil {
		return ManualIntentView{}, errors.New("compiled project definition is required")
	}
	if _, ok := loaded.Definition.Pipelines[pipelineID.String()]; !ok {
		return ManualIntentView{}, fmt.Errorf("unknown refresh pipeline %q", pipelineID)
	}
	if m.service.ResolveSourceDigest == nil {
		return ManualIntentView{}, errors.New("canonical refresh source digest resolver is required")
	}
	sourceDigest, err := m.service.ResolveSourceDigest(ctx, activeIdentity)
	if err != nil {
		return ManualIntentView{}, fmt.Errorf("resolve source digest for manual intent: %w", err)
	}
	if err := refreshschedule.ValidateArtifactDigest(sourceDigest); err != nil {
		return ManualIntentView{}, fmt.Errorf("resolve source digest for manual intent: %w", err)
	}
	requestID := strings.TrimSpace(command.RequestID)
	if requestID == "" {
		requestID = strings.TrimPrefix(key, "ui:")
	}
	correlationID := strings.TrimSpace(command.CorrelationID)
	if correlationID == "" {
		correlationID = requestID
	}
	auditIntent, err := buildRefreshAuditIntent(ctx, CreateRefreshRunOperationID, principalID, activeIdentity.ProjectID.String(), requestID, correlationID)
	if err != nil {
		return ManualIntentView{}, fmt.Errorf("build manual refresh audit intent: %w", err)
	}
	if auditIntent == nil {
		return ManualIntentView{}, errors.New("manual refresh audit intent is unavailable")
	}
	auditIntent.RequestDigest = requestDigest
	auditIntentJSON, err := json.Marshal(auditIntent)
	if err != nil {
		return ManualIntentView{}, fmt.Errorf("encode manual refresh audit intent: %w", err)
	}
	if m.manualIntentCreateAuditWriter == nil {
		return ManualIntentView{}, errors.New("manual refresh acceptance audit writer is unavailable")
	}
	intent, _, err := m.manualIntents.CreateManualIntentWithAudit(ctx, refreshpostgres.ManualIntentInput{
		IdempotencyKey: key, RequestDigest: requestDigest,
		ProjectID: activeIdentity.ProjectID.String(), Environment: activeIdentity.Environment,
		PipelineID: pipelineID.String(), TargetID: m.manualTargetID, PrincipalID: principalID,
		SourceDigest: sourceDigest, AuditIntentJSON: auditIntentJSON,
	}, func(auditCtx context.Context, tx refreshpostgres.Tx, accepted refreshpostgres.ManualIntent) error {
		acceptanceAudit := *auditIntent
		acceptanceAudit.Action = "refresh.request.accepted"
		acceptanceAudit.EventID = deriveAuditEventID("refresh-intent-accepted", auditIntent.EventID)
		payload, marshalErr := json.Marshal(manualIntentAcceptedAuditPayload{
			IntentID: accepted.IntentID, ReservedRunID: accepted.ReservedRunID,
			PipelineID: accepted.PipelineID, Status: refreshpostgres.ManualIntentWaiting,
		})
		if marshalErr != nil {
			return marshalErr
		}
		acceptanceAudit.MetadataJSON = string(payload)
		return m.manualIntentCreateAuditWriter.RecordRefreshAuditTx(auditCtx, tx, acceptanceAudit)
	})
	if err != nil {
		if errors.Is(err, refreshpostgres.ErrManualIntentQueueFull) {
			return ManualIntentView{}, apigenfailure.New("conflict", "The manual refresh queue is full. Try again after current runs finish.")
		}
		return ManualIntentView{}, fmt.Errorf("persist manual refresh intent: %w", err)
	}
	if err := m.verifyManualIntentCommand(ctx, CreateRefreshRunOperationID); err != nil {
		return ManualIntentView{}, err
	}
	return m.manualIntentView(ctx, intent, 0), nil
}

type manualIntentAcceptedAuditPayload struct {
	IntentID      string `json:"intentId"`
	ReservedRunID string `json:"reservedRunId"`
	PipelineID    string `json:"pipelineId"`
	Status        string `json:"status"`
}

func (m *Module) activeRefreshState(ctx context.Context, identity projectgraph.ServingIdentity) (refreshrun.ServingState, error) {
	var active refreshrun.ServingState
	var err error
	if m.service.ResolveActive != nil {
		active, err = m.service.ResolveActive(ctx, identity)
	} else {
		if m.service.ServingStates == nil {
			return refreshrun.ServingState{}, errors.New("refresh serving state reader is unavailable")
		}
		active, err = m.service.Active(ctx, identity.ProjectID, servingstate.Environment(identity.Environment))
	}
	if err != nil {
		return refreshrun.ServingState{}, err
	}
	if active.State.ProjectID != identity.ProjectID || string(active.State.Environment) != identity.Environment || string(active.State.ID) != identity.GenerationID || active.Artifact.ServingStateID != active.State.ID {
		return refreshrun.ServingState{}, errors.New("resolved active refresh definition does not match its serving identity")
	}
	return active, nil
}

func manualIntentRequestDigest(identity projectgraph.ServingIdentity, principalID string, pipelineID projectgraph.ResourceID, targetID, retryOf string) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	if err := pipelineID.Validate(); err != nil {
		return "", err
	}
	principalID = strings.TrimSpace(principalID)
	if principalID == "" || strings.TrimSpace(targetID) == "" {
		return "", errors.New("manual refresh principal and target are required")
	}
	payload, err := json.Marshal(struct {
		Project     string `json:"project"`
		Environment string `json:"environment"`
		Target      string `json:"target"`
		Pipeline    string `json:"pipeline"`
		Principal   string `json:"principal"`
		RetryOf     string `json:"retryOf"`
	}{identity.ProjectID.String(), identity.Environment, targetID, pipelineID.String(), principalID, strings.TrimSpace(retryOf)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func deriveAuditEventID(label, sourceID string) string {
	digest := sha256.Sum256([]byte(label + "\x00" + sourceID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (m *Module) verifyManualIntentCommand(ctx context.Context, operationID string) error {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID, apigencommand.Execution{
		// The intent and access audit row were accepted atomically by the native
		// refresh transaction; the generated executor verifies the command's
		// declared durable guarantee without emitting a second audit event.
		Transactional: func(context.Context, apigencommand.Contract) error { return nil },
	})
}

func (m *Module) manualIntentView(ctx context.Context, intent refreshpostgres.ManualIntent, queuePosition int) ManualIntentView {
	view := ManualIntentView{
		IntentID: intent.IntentID, ReservedRunID: intent.ReservedRunID,
		RunID: intent.AttachedRunID, PipelineID: intent.PipelineID,
		Status: intent.Status, CreatedAt: intent.CreatedAt,
		CancelAllowed: intent.Status == refreshpostgres.ManualIntentWaiting,
	}
	if intent.Status == refreshpostgres.ManualIntentStale {
		view.Reason = "Pipeline definition changed while waiting; start a new request"
	}
	if queuePosition > 0 {
		view.QueuePosition = queuePosition
		return view
	}
	if intent.Status != refreshpostgres.ManualIntentWaiting || m == nil || m.manualIntents == nil {
		return view
	}
	rows, err := m.manualIntents.ListManualIntents(ctx, refreshpostgres.Scope{ProjectID: intent.ProjectID, Environment: intent.Environment}, m.manualTargetID, refreshpostgres.MaxPageSize)
	if err != nil {
		return view
	}
	position := 0
	for _, queued := range rows {
		if queued.Status != refreshpostgres.ManualIntentWaiting {
			continue
		}
		position++
		if queued.IntentID == intent.IntentID {
			view.QueuePosition = position
			break
		}
	}
	return view
}

// ListManualPipelineIntents exposes this native target's live acceptance
// queue to the project browser. The persistence read is bounded and already
// FIFO ordered.
func (m *Module) ListManualPipelineIntents(ctx context.Context, scope refreshrun.ReadScope) ([]ManualIntentView, error) {
	if !m.manualIntentReady() {
		return nil, errors.New("durable manual refresh intent persistence is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	rows, err := m.manualIntents.ListManualIntents(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, m.manualTargetID, refreshpostgres.MaxPageSize)
	if err != nil {
		return nil, err
	}
	views := make([]ManualIntentView, 0, len(rows))
	waitingPosition := 0
	for _, intent := range rows {
		if intent.Status == refreshpostgres.ManualIntentWaiting {
			waitingPosition++
		}
		position := 0
		if intent.Status == refreshpostgres.ManualIntentWaiting {
			position = waitingPosition
		}
		views = append(views, m.manualIntentView(ctx, intent, position))
	}
	stale, err := m.manualIntents.ListRecentStaleManualIntents(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, m.manualTargetID, 20)
	if err != nil {
		return nil, err
	}
	for _, intent := range stale {
		views = append(views, m.manualIntentView(ctx, intent, 0))
	}
	return views, nil
}

// CancelManualPipelineIntentForUI cancels a request only while it is still
// waiting, and fences the mutation to its project/environment, pipeline, and
// requesting principal.
func (m *Module) CancelManualPipelineIntentForUI(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID, intentID, principalID, idempotencyKey string) error {
	if !m.manualIntentReady() {
		return errors.New("durable manual refresh intent persistence is unavailable")
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	pipeline, err := projectgraph.NewResourceID(pipelineID)
	if err != nil || pipeline.String() != pipelineID {
		return errors.New("manual refresh intent not found")
	}
	scope := refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment}
	intent, err := m.manualIntents.GetManualIntent(ctx, scope, intentID)
	if err != nil || intent.TargetID != m.manualTargetID || intent.PipelineID != pipeline.String() || intent.PrincipalID != strings.TrimSpace(principalID) {
		return errors.New("manual refresh intent is not cancellable")
	}
	if intent.Status != refreshpostgres.ManualIntentWaiting && intent.Status != refreshpostgres.ManualIntentClaimed && intent.Status != refreshpostgres.ManualIntentCancelled {
		return apigenfailure.New("conflict", "This refresh request has already started and can no longer be cancelled here.")
	}
	key := strings.TrimSpace(idempotencyKey)
	if key == "" || key != idempotencyKey {
		return errors.New("manual refresh cancellation idempotency key is required")
	}
	requestID := strings.TrimPrefix(key, "ui:")
	correlationID := requestID
	cancelAudit, err := buildRefreshAuditIntent(ctx, CancelRefreshRunOperationID, strings.TrimSpace(principalID), identity.ProjectID.String(), requestID, correlationID)
	if err != nil || cancelAudit == nil {
		if err == nil {
			err = errors.New("manual refresh cancellation audit intent is unavailable")
		}
		return err
	}
	cancelAudit.Action = "refresh.request.cancelled"
	cancelAudit.EventID = deriveAuditEventID("refresh-intent-cancelled", cancelAudit.EventID)
	cancelAudit.RequestDigest = manualIntentCancelDigest(identity, strings.TrimSpace(principalID), intentID, key)
	if m.manualIntentCancelAuditWriter == nil {
		return errors.New("manual refresh cancellation audit writer is unavailable")
	}
	cancelled, err := m.manualIntents.CancelManualIntentWithAudit(ctx, scope, intentID, func(auditCtx context.Context, tx refreshpostgres.Tx, cancelled refreshpostgres.ManualIntent) error {
		payload, marshalErr := json.Marshal(manualIntentCancelledAuditPayload{
			IntentID: cancelled.IntentID, ReservedRunID: cancelled.ReservedRunID,
			PipelineID: cancelled.PipelineID, Status: refreshpostgres.ManualIntentCancelled,
		})
		if marshalErr != nil {
			return marshalErr
		}
		intent := *cancelAudit
		intent.MetadataJSON = string(payload)
		return m.manualIntentCancelAuditWriter.RecordRefreshCancelAuditTx(auditCtx, tx, intent)
	})
	if err != nil {
		if errors.Is(err, refreshpostgres.ErrConflict) {
			return apigenfailure.New("conflict", "This refresh request has already started and can no longer be cancelled here.")
		}
		return err
	}
	if cancelled.Status != refreshpostgres.ManualIntentCancelled {
		return errors.New("manual refresh intent is not cancellable")
	}
	return m.verifyManualIntentCommand(ctx, CancelRefreshRunOperationID)
}

type manualIntentCancelledAuditPayload struct {
	IntentID      string `json:"intentId"`
	ReservedRunID string `json:"reservedRunId"`
	PipelineID    string `json:"pipelineId"`
	Status        string `json:"status"`
}

func manualIntentCancelDigest(identity projectgraph.ServingIdentity, principalID, intentID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(identity.ProjectID.String() + "\x00" + identity.Environment + "\x00" + principalID + "\x00" + intentID + "\x00" + idempotencyKey))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (m *Module) runManualIntentDispatcher(ctx context.Context) {
	defer m.wg.Done()
	dispatch := func() {
		if err := m.dispatchOneManualIntent(ctx); err != nil && ctx.Err() == nil {
			m.logger.WarnContext(ctx, "dispatch deferred manual refresh intent failed", "error", RedactFailure(err))
		}
	}
	dispatch()
	ticker := time.NewTicker(m.manualIntentInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatch()
		}
	}
}

func (m *Module) dispatchOneManualIntent(ctx context.Context) error {
	if !m.manualIntentReady() || m.resolveIdentity == nil {
		return nil
	}
	activeIdentity, err := m.resolveIdentity(ctx)
	if err != nil {
		return fmt.Errorf("resolve active refresh identity for intent dispatch: %w", err)
	}
	if err := activeIdentity.Validate(); err != nil {
		return err
	}
	scope := refreshpostgres.Scope{ProjectID: activeIdentity.ProjectID.String(), Environment: activeIdentity.Environment}
	intent, ok, err := m.manualIntents.ClaimNextManualIntent(ctx, scope, m.manualIntentOwner, m.leaseTimeout)
	if err != nil || !ok {
		return err
	}
	if intent.TargetID != m.manualTargetID || intent.ProjectID != scope.ProjectID || intent.Environment != scope.Environment {
		return m.releaseManualIntent(ctx, intent, errors.New("claimed manual refresh intent is outside the configured target scope"))
	}
	readScope := refreshrun.ReadScope{ProjectID: activeIdentity.ProjectID, Environment: activeIdentity.Environment}
	// A crash can occur after the immutable run transaction commits but before
	// AttachManualIntentTx. Recover that exact reserved run first; never resolve
	// a newer generation or rebase this accepted request.
	existing, err := m.runs.GetRun(ctx, readScope, intent.ReservedRunID)
	if err == nil {
		if existing.ID != intent.ReservedRunID || existing.TargetType != refreshrun.TargetRefreshPipeline || existing.PipelineID.String() != intent.PipelineID || existing.ParentRunID != "" || existing.PrincipalID != intent.PrincipalID || existing.TriggerType != refreshrun.TriggerManual {
			return m.releaseManualIntent(ctx, intent, errors.New("reserved manual refresh run identity conflicts with the durable intent"))
		}
		return m.attachManualIntent(ctx, intent, existing.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("read reserved manual refresh run: %w", err))
	}
	activeIdentity, err = m.resolveIdentity(ctx)
	if err != nil {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("resolve active serving generation for manual intent: %w", err))
	}
	if err := activeIdentity.Validate(); err != nil {
		return m.releaseManualIntent(ctx, intent, err)
	}
	if activeIdentity.ProjectID.String() != intent.ProjectID || activeIdentity.Environment != intent.Environment {
		return m.releaseManualIntent(ctx, intent, errors.New("active serving scope does not match manual refresh intent"))
	}
	currentDigest, err := m.service.ResolveSourceDigest(ctx, activeIdentity)
	if err != nil {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("resolve current source digest for manual intent: %w", err))
	}
	if err := refreshschedule.ValidateArtifactDigest(intent.SourceDigest); err != nil {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("pinned source digest is invalid: %w", err))
	}
	if err := refreshschedule.ValidateArtifactDigest(currentDigest); err != nil {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("current source digest is invalid: %w", err))
	}
	if currentDigest != intent.SourceDigest {
		if err := m.manualIntents.InTx(ctx, func(tx refreshpostgres.Tx) error {
			return m.manualIntents.MarkManualIntentStaleTx(ctx, tx, intent.IntentID, m.manualIntentOwner, intent.FenceGeneration)
		}); err != nil {
			return fmt.Errorf("mark stale manual refresh intent: %w", err)
		}
		return nil
	}
	pipeline, err := projectgraph.NewResourceID(intent.PipelineID)
	if err != nil {
		return m.releaseManualIntent(ctx, intent, err)
	}
	var auditIntent *access.AuditIntent
	if len(intent.AuditIntentJSON) == 0 {
		return m.releaseManualIntent(ctx, intent, errors.New("manual refresh audit intent is missing"))
	}
	var decoded access.AuditIntent
	if err := json.Unmarshal(intent.AuditIntentJSON, &decoded); err != nil {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("decode manual refresh audit intent: %w", err))
	}
	auditIntent = &decoded
	// The accepted-request audit has a distinct identity from the later run
	// admission audit. Refresh admission commits this latter event atomically
	// with its immutable run and job.
	auditIntent.EventID = deriveAuditEventID("refresh-intent-run-queued", intent.IntentID)
	auditIntent.Action = refreshQueuedAuditAction
	auditIntent.RequestDigest, err = refreshrun.RequestDigest(activeIdentity, intent.PrincipalID, pipeline)
	if err != nil {
		return m.releaseManualIntent(ctx, intent, err)
	}
	payload, marshalErr := json.Marshal(struct {
		ID                  string   `json:"id"`
		PipelineID          string   `json:"pipelineId"`
		SemanticModel       string   `json:"semanticModel"`
		InvocationSource    string   `json:"invocationSource"`
		MatchingScheduleIDs []string `json:"matchingScheduleIds"`
		PlanDigest          string   `json:"planDigest"`
		Status              string   `json:"status"`
	}{intent.ReservedRunID, pipeline.String(), "", refreshrun.TriggerManual, []string{}, "", refreshrun.RunStatusQueued})
	if marshalErr != nil {
		return m.releaseManualIntent(ctx, intent, marshalErr)
	}
	auditIntent.MetadataJSON = string(payload)
	if m.service.ResolveSourceDigest == nil {
		return m.releaseManualIntent(ctx, intent, errors.New("canonical refresh source digest resolver is unavailable"))
	}
	queued, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
		RunID: intent.ReservedRunID, Identity: activeIdentity, PrincipalID: intent.PrincipalID,
		EstimatedMemoryBytes: 1, PipelineID: pipeline, TriggerType: refreshrun.TriggerManual,
		InvocationSource: refreshrun.TriggerManual, IdempotencyKey: "manual-intent:" + intent.IntentID,
		AuditIntent: auditIntent,
	})
	if err != nil {
		return m.releaseManualIntent(ctx, intent, fmt.Errorf("queue reserved manual refresh run: %w", err))
	}
	if queued.Run.ID != intent.ReservedRunID {
		return m.releaseManualIntent(ctx, intent, errors.New("refresh admission returned a different reserved run id"))
	}
	// QueuePipelineRefresh commits independently. If the process dies before
	// this link commits, the next claimant discovers ReservedRunID above.
	if err := m.attachManualIntent(ctx, intent, queued.Run.ID); err != nil {
		return err
	}
	if err := m.verifyRunCreated(ctx, queued.Run); err != nil {
		return err
	}
	return nil
}

func (m *Module) attachManualIntent(ctx context.Context, intent refreshpostgres.ManualIntent, runID string) error {
	err := m.manualIntents.InTx(ctx, func(tx refreshpostgres.Tx) error {
		return m.manualIntents.AttachManualIntentTx(ctx, tx, intent.IntentID, m.manualIntentOwner, intent.FenceGeneration, runID)
	})
	if err != nil {
		return fmt.Errorf("attach manual refresh intent to reserved run: %w", err)
	}
	return nil
}

func (m *Module) releaseManualIntent(ctx context.Context, intent refreshpostgres.ManualIntent, cause error) error {
	releaseErr := m.manualIntents.InTx(ctx, func(tx refreshpostgres.Tx) error {
		return m.manualIntents.ReleaseManualIntentTx(ctx, tx, intent.IntentID, m.manualIntentOwner, intent.FenceGeneration)
	})
	if releaseErr != nil {
		return errors.Join(cause, fmt.Errorf("release manual refresh intent claim: %w", releaseErr))
	}
	return cause
}
