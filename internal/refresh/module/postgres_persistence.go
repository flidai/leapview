package module

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshoperation "github.com/flidai/leapview/internal/refresh/operation"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
)

// PostgresQueueWriter is the transaction-aware bridge to the canonical
// platform jobs authority. It receives the same pgx transaction used for the
// refresh run and must enqueue/link the job without committing it.
type PostgresQueueWriter interface {
	EnqueueRefreshTx(context.Context, refreshpostgres.Tx, refreshrun.RunInput, string) (string, error)
}

// PostgresRefreshEventWriter appends the initial refresh lifecycle event in
// the caller-owned transaction. Implementations are optional only for
// legacy/non-native fixtures; native production composition supplies one.
type PostgresRefreshEventWriter interface {
	AppendRefreshQueuedEventTx(context.Context, refreshpostgres.Tx, string, string, string) error
}

// PostgresQueue is the module-owned queue capability. It intentionally
// repeats the queue surface instead of exposing another package's repository
// type from this public adapter configuration.
type PostgresQueue interface {
	ListExecutableJobs(context.Context, refreshrun.ReadScope, int) ([]refreshrun.JobRecord, error)
	ClaimExecutableJob(context.Context, refreshrun.JobRecord, string, time.Duration) (refreshrun.JobRecord, bool, error)
	RenewJobLease(context.Context, refreshrun.JobRecord, time.Duration) error
	JobQueueStats(context.Context, refreshrun.ReadScope) (refreshrun.JobQueueStats, error)
}

// PostgresCanonicalVerifier verifies the exact committed delivery evidence for
// a canonical refresh while the refresh authority transaction is open.
type PostgresCanonicalVerifier interface {
	VerifyCanonicalRefreshTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord, refreshrun.CanonicalRefreshResult) (refreshpostgres.PublicationInput, error)
}

// PostgresNativeRefreshFinalizer is the transaction-aware native delivery
// publication seam for canonical refresh completion. Implementations must use
// the caller-owned transaction: they may create/replay the exact native
// publication, acquire and fence a delivery lease, and CAS activation, but
// must not commit or roll back tx.
//
// The finalizer is optional while native refresh composition is being rolled
// out. When configured, CompleteCanonicalRefresh invokes it before writing
// refresh data-version/run/job terminal evidence, so every authority commits
// or rolls back together.
type PostgresNativeRefreshFinalizer interface {
	FinalizeCanonicalRefreshTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord, refreshrun.CanonicalRefreshResult, refreshpostgres.PublicationInput) error
}

// PostgresNativeRefreshFinalizerFunc adapts a function to the finalizer seam.
type PostgresNativeRefreshFinalizerFunc func(context.Context, refreshpostgres.Tx, refreshrun.JobRecord, refreshrun.CanonicalRefreshResult, refreshpostgres.PublicationInput) error

func (f PostgresNativeRefreshFinalizerFunc) FinalizeCanonicalRefreshTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult, publication refreshpostgres.PublicationInput) error {
	if f == nil {
		return errors.New("native refresh finalizer is unavailable")
	}
	return f(ctx, tx, job, result, publication)
}

// PostgresCancelAuditWriter writes a final cancellation audit row through the
// caller-owned authority transaction.
type PostgresCancelAuditWriter interface {
	RecordRefreshCancelAuditTx(context.Context, refreshpostgres.Tx, access.AuditIntent) error
}

// PostgresRefreshAuditWriter records the generated create intent through the
// same caller-owned transaction as operation/run admission.
type PostgresRefreshAuditWriter interface {
	RecordRefreshAuditTx(context.Context, refreshpostgres.Tx, access.AuditIntent) error
}

// PostgresPersistenceConfig supplies the transaction-aware physical
// publication identity resolver. The refresh authority records the resolved
// pair as provenance; it never accepts an unqualified snapshot-only data
// version.
type PostgresPersistenceConfig struct {
	SchedulerOwner              string
	PublicationIdentityResolver PostgresPublicationIdentityResolver
	// Jobs is the complete canonical platform-jobs authority. Reads, enqueue,
	// lifecycle, and recovery must all come from this one adapter.
	Jobs              PostgresJobsAuthority
	CanonicalVerifier PostgresCanonicalVerifier
	// NativeFinalizer is deliberately separate from the verifier. The
	// verifier proves the refresh result; the finalizer composes native
	// publication/activation into the same caller-owned transaction. It is
	// required by production module admission; evaluation fixtures may omit it.
	NativeFinalizer PostgresNativeRefreshFinalizer
	// Operations is the shared platform idempotency authority. Native keyed
	// create admission composes it with refresh rows and canonical jobs in the
	// same caller-owned transaction.
	Operations        refreshoperation.Authority
	CancelAuditWriter PostgresCancelAuditWriter
	CreateAuditWriter PostgresRefreshAuditWriter
}

// NewPostgresPersistence adapts the clean-slate PostgreSQL authority to the
// module's domain contracts. It does not create a queue or a second workflow
// authority; callers should provide job queue behavior separately when they
// enable the dispatcher.
func NewPostgresPersistence(repository *refreshpostgres.Repository, config PostgresPersistenceConfig) (Persistence, error) {
	if repository == nil || !repository.Configured() {
		return Persistence{}, errors.New("configured refresh PostgreSQL repository is required")
	}
	if strings.TrimSpace(config.SchedulerOwner) == "" {
		return Persistence{}, errors.New("PostgreSQL scheduler owner is required")
	}
	if config.PublicationIdentityResolver == nil {
		return Persistence{}, ErrPublicationIdentityUnavailable
	}
	if isNilPostgresCapability(config.Jobs) {
		return Persistence{}, errors.New("PostgreSQL canonical jobs authority is required")
	}
	queueAuthority, queueOK := config.Jobs.(postgresQueueAuthority)
	if !queueOK {
		return Persistence{}, errors.New("PostgreSQL canonical jobs authority provenance is required")
	}
	if !queueAuthority.Configured() {
		return Persistence{}, errors.New("configured PostgreSQL canonical jobs authority is required")
	}
	if !queueAuthority.MatchesRefreshRepository(repository) {
		return Persistence{}, errors.New("PostgreSQL canonical jobs authority does not match refresh repository")
	}
	if config.CanonicalVerifier == nil {
		return Persistence{}, errors.New("PostgreSQL canonical refresh verifier is required")
	}
	if config.CancelAuditWriter == nil {
		return Persistence{}, errors.New("PostgreSQL cancellation audit writer is required")
	}
	lifecycle := config.Jobs
	recoveryQueue := config.Jobs
	terminalRecovery, err := NewPostgresTerminalRecovery(repository, recoveryQueue)
	if err != nil {
		return Persistence{}, err
	}
	return Persistence{
		Runs:             &postgresRunPersistence{repository: repository, jobs: config.Jobs, operations: config.Operations, cancelAuditWriter: config.CancelAuditWriter, createAuditWriter: config.CreateAuditWriter},
		Schedules:        &postgresSchedulePersistence{repository: repository, schedulerOwner: config.SchedulerOwner, identityResolver: config.PublicationIdentityResolver},
		Publication:      &postgresPublicationPersistence{repository: repository, identityResolver: config.PublicationIdentityResolver, canonicalVerifier: config.CanonicalVerifier, nativeFinalizer: config.NativeFinalizer, cancelAuditWriter: config.CancelAuditWriter, queueLifecycle: lifecycle, queueRecovery: recoveryQueue},
		TerminalRecovery: terminalRecovery, nativeRepository: repository,
	}, nil
}

var _ refreshschedule.Repository = (*postgresSchedulePersistence)(nil)
var _ RunPersistence = (*postgresRunPersistence)(nil)
var _ refreshrun.CanonicalPublicationUnitOfWork = (*postgresPublicationPersistence)(nil)

type postgresSchedulePersistence struct {
	repository       *refreshpostgres.Repository
	schedulerOwner   string
	identityResolver PostgresPublicationIdentityResolver
}

func (p *postgresSchedulePersistence) Reconcile(ctx context.Context, input refreshschedule.ReconcileInput) error {
	if p == nil || p.repository == nil {
		return errors.New("refresh PostgreSQL schedule persistence is unavailable")
	}
	if err := input.Identity.Validate(); err != nil {
		return err
	}
	if err := refreshschedule.ValidateArtifactDigest(input.ArtifactDigest); err != nil {
		return err
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	values := make([]refreshpostgres.ScheduleInput, 0)
	for _, pipeline := range input.Pipelines {
		if err := pipeline.Validate(); err != nil {
			return err
		}
		for _, schedule := range pipeline.Schedules {
			parsed, err := refreshschedule.ParseSchedule(schedule.Expression, pipeline.Timezone)
			if err != nil {
				return err
			}
			next := parsed.Next(now)
			if next.IsZero() {
				return fmt.Errorf("refresh pipeline %q schedule %q has no next occurrence", pipeline.ID, schedule.ID)
			}
			values = append(values, refreshpostgres.ScheduleInput{
				ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment,
				PipelineID: pipeline.ID.String(), ScheduleID: schedule.ID, SemanticModelID: pipeline.SemanticModelID.String(),
				GenerationID: input.Identity.GenerationID, ArtifactDigest: input.ArtifactDigest,
				Cron: schedule.Expression, Timezone: pipeline.Timezone, StartingDeadline: time.Duration(pipeline.StartingDeadlineSeconds) * time.Second,
				ConcurrencyPolicy: pipeline.ConcurrencyPolicy, ScheduleDigest: digestSchedule(pipeline, schedule), NextRunAt: next,
			})
		}
	}
	if len(values) == 0 {
		return p.repository.ReconcileScope(ctx, refreshpostgres.Scope{ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, GenerationID: input.Identity.GenerationID}, input.Identity.GenerationID, nil)
	}
	return p.repository.Reconcile(ctx, values)
}

func digestSchedule(pipeline refreshschedule.Definition, schedule refreshschedule.Schedule) string {
	b, _ := json.Marshal(struct {
		Pipeline, Schedule, Timezone, Policy string
	}{pipeline.ID.String(), schedule.ID, pipeline.Timezone, pipeline.ConcurrencyPolicy})
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func (p *postgresSchedulePersistence) ClaimDue(ctx context.Context, identity projectgraph.ServingIdentity, now time.Time) ([]refreshschedule.Occurrence, error) {
	if p == nil || p.repository == nil {
		return nil, errors.New("refresh PostgreSQL schedule persistence is unavailable")
	}
	claimed, err := p.repository.ClaimDue(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID}, now, p.schedulerOwner, 5*time.Minute, refreshpostgres.MaxPageSize)
	if err != nil {
		return nil, err
	}
	out := make([]refreshschedule.Occurrence, 0, len(claimed))
	for _, occurrence := range claimed {
		schedule, scheduleErr := p.repository.Schedule(ctx, occurrence.ScheduleRevisionID)
		if scheduleErr != nil {
			return nil, scheduleErr
		}
		projectID, projectErr := projectgraph.NewResourceID(occurrence.ProjectID)
		if projectErr != nil {
			return nil, projectErr
		}
		storedIdentity, identityErr := projectgraph.NewServingIdentity(projectID, occurrence.Environment, occurrence.GenerationID)
		if identityErr != nil {
			return nil, identityErr
		}
		out = append(out, refreshschedule.Occurrence{
			OccurrenceID: occurrence.OccurrenceID, LeaseOwner: occurrence.LeaseOwner, LeaseRevision: occurrence.FenceGeneration, LeaseExpiresAt: occurrence.LeaseExpiresAt,
			Identity: storedIdentity, PipelineID: projectgraph.ResourceID(occurrence.PipelineID), MatchingScheduleIDs: append([]string(nil), occurrence.MatchingScheduleIDs...),
			SemanticModelID: projectgraph.ResourceID(occurrence.SemanticModelID), ArtifactDigest: occurrence.ArtifactDigest,
			Timezone: schedule.Timezone, ScheduledAt: occurrence.NominalTime,
		})
	}
	return out, nil
}

func (p *postgresSchedulePersistence) ReleaseOccurrence(ctx context.Context, occurrence refreshschedule.Occurrence) error {
	if p == nil || p.repository == nil {
		return errors.New("refresh PostgreSQL schedule persistence is unavailable")
	}
	if err := occurrence.Identity.Validate(); err != nil {
		return err
	}
	if occurrence.OccurrenceID == "" || occurrence.LeaseOwner == "" || occurrence.LeaseRevision <= 0 {
		return errors.New("refresh occurrence claim fence is required")
	}
	return p.repository.ReleaseOccurrence(ctx, refreshpostgres.Occurrence{
		OccurrenceID: occurrence.OccurrenceID, ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
		PipelineID: occurrence.PipelineID.String(), NominalTime: occurrence.ScheduledAt.UTC(), LeaseOwner: occurrence.LeaseOwner, FenceGeneration: occurrence.LeaseRevision,
	})
}

func occurrenceIDForDomain(occurrence refreshschedule.Occurrence) string {
	h := sha256.Sum256([]byte(occurrence.Identity.ProjectID.String() + "\x00" + occurrence.Identity.Environment + "\x00" + occurrence.PipelineID.String() + "\x00" + occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano)))
	return "occurrence-" + hex.EncodeToString(h[:])
}

func (p *postgresSchedulePersistence) NextRun(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID) (time.Time, bool, error) {
	return p.repository.NextRun(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID}, pipelineID.String())
}

func (p *postgresSchedulePersistence) SaveDataVersion(ctx context.Context, version refreshschedule.DataVersion) error {
	if p == nil || p.repository == nil {
		return errors.New("refresh PostgreSQL schedule persistence is unavailable")
	}
	if err := version.Identity.Validate(); err != nil {
		return err
	}
	if version.SnapshotID <= 0 {
		return errors.New("refresh data version snapshot is required")
	}
	if version.Source == refreshschedule.DataVersionSourcePublish && strings.TrimSpace(version.RunID) == "" {
		return errors.New("PostgreSQL published data versions require deployment publication provenance")
	}
	request := PostgresPublicationIdentityRequest{
		ProjectID: version.Identity.ProjectID.String(), Environment: version.Identity.Environment,
		GenerationID: version.Identity.GenerationID, SemanticModelID: version.SemanticModelID.String(),
		PipelineID: version.PipelineID.String(), RunID: version.RunID, SnapshotID: version.SnapshotID,
		Source: version.Source, TargetRevision: version.TargetRevision,
	}
	return p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		identity, err := resolvePublicationIdentityTx(ctx, tx, p.identityResolver, request)
		if err != nil {
			return err
		}
		return p.repository.SaveDataVersionTx(ctx, tx, refreshpostgres.DataVersion{
			ProjectID: version.Identity.ProjectID.String(), Environment: version.Identity.Environment,
			SemanticModelID: version.SemanticModelID.String(), GenerationID: version.Identity.GenerationID,
			SnapshotID: version.SnapshotID, RefreshedAt: version.RefreshedAt.UTC(), Source: version.Source,
			PipelineID: version.PipelineID.String(), RunID: version.RunID, PhysicalPoolID: identity.PhysicalPoolID,
			CatalogID: identity.CatalogID, TargetRevision: version.TargetRevision, LeaseOwner: version.LeaseOwner,
			LeaseRevision: version.LeaseRevision,
		})
	})
}

func (p *postgresSchedulePersistence) DataVersion(ctx context.Context, identity projectgraph.ServingIdentity, semanticModelID projectgraph.ResourceID) (refreshschedule.DataVersion, bool, error) {
	v, found, err := p.repository.DataVersion(ctx, identity.ProjectID.String(), identity.Environment, semanticModelID.String(), identity.GenerationID)
	if err != nil || !found {
		return refreshschedule.DataVersion{}, found, err
	}
	return refreshschedule.DataVersion{Identity: identity, SemanticModelID: semanticModelID, SnapshotID: v.SnapshotID, RefreshedAt: v.RefreshedAt, Source: v.Source, PipelineID: projectgraph.ResourceID(v.PipelineID), RunID: v.RunID, TargetRevision: v.TargetRevision, LeaseOwner: v.LeaseOwner, LeaseRevision: v.LeaseRevision}, true, nil
}

type postgresRunPersistence struct {
	repository        *refreshpostgres.Repository
	jobs              PostgresJobsAuthority
	operations        refreshoperation.Authority
	cancelAuditWriter PostgresCancelAuditWriter
	createAuditWriter PostgresRefreshAuditWriter
}

func (p *postgresRunPersistence) queueLifecycle() (PostgresQueueLifecycle, error) {
	if p == nil || p.jobs == nil {
		return nil, errors.New("canonical platform jobs queue is not configured")
	}
	return p.jobs, nil
}

// LookupIdempotentRun is the preflight replay fast-path for keyed native
// commands. It reads only the shared operation scope/digest and terminal
// {runId} evidence; a missing operation is reported as a fresh admission,
// while a digest/type mismatch remains a durable conflict.
func (p *postgresRunPersistence) LookupIdempotentRun(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID, key, digest string) (refreshrun.RunRecord, []refreshrun.RunRecord, bool, error) {
	if p == nil || p.repository == nil || p.operations == nil {
		return refreshrun.RunRecord{}, nil, false, errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	op, err := p.operations.Get(ctx, refreshOperationScope(identity.ProjectID.String(), identity.Environment), key)
	if errors.Is(err, refreshpostgres.ErrNotFound) {
		return refreshrun.RunRecord{}, nil, false, nil
	}
	if err != nil {
		return refreshrun.RunRecord{}, nil, false, err
	}
	if op.OperationType != "refresh_pipeline" || op.RequestDigest != digest {
		return refreshrun.RunRecord{}, nil, false, refreshpostgres.ErrConflict
	}
	runID, ok := operationRunID(op)
	if !ok {
		// An in-flight or malformed terminal operation is not a replayable
		// admission. Let the final reserve classify the durable disposition.
		if op.State != "completed" {
			return refreshrun.RunRecord{}, nil, false, nil
		}
		return refreshrun.RunRecord{}, nil, false, refreshpostgres.ErrConflict
	}
	root, err := p.repository.GetRun(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment}, runID)
	if err != nil {
		return refreshrun.RunRecord{}, nil, false, err
	}
	if root.OperationID != op.OperationID || root.PipelineID != pipelineID.String() {
		return refreshrun.RunRecord{}, nil, false, refreshpostgres.ErrConflict
	}
	children, err := p.repository.ListChildRuns(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment}, runID, refreshpostgres.MaxPageSize)
	if err != nil {
		return refreshrun.RunRecord{}, nil, false, err
	}
	rootRecord, err := fromPostgresRun(root)
	if err != nil {
		return refreshrun.RunRecord{}, nil, false, err
	}
	childRecords := make([]refreshrun.RunRecord, 0, len(children))
	for _, child := range children {
		record, convertErr := fromPostgresRun(child)
		if convertErr != nil {
			return refreshrun.RunRecord{}, nil, false, convertErr
		}
		childRecords = append(childRecords, record)
	}
	return rootRecord, childRecords, true, nil
}

func refreshOperationScope(projectID, environment string) string {
	// Platform operation scopes are bounded to 255 bytes while authored
	// project/environment identities may each approach their own maxima. Hash
	// the canonical pair to preserve separation without truncation collisions.
	digest := sha256.Sum256([]byte(projectID + "\x00" + environment))
	return "refresh:" + hex.EncodeToString(digest[:])
}

// operationRunID accepts only the exact canonical terminal outcome object
// emitted by keyed refresh admission: {"runId":"..."}. Unknown or duplicate
// fields are rejected so a replay cannot trust ambiguous evidence.
func operationRunID(op refreshoperation.Record) (string, bool) {
	if op.State != "completed" || len(op.Outcome) == 0 {
		return "", false
	}
	var fields map[string]json.RawMessage
	if err := strictjson.DecodeWithOptions(op.Outcome, &fields, strictjson.Options{MaxBytes: 32768, MaxDepth: 4, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil || len(fields) != 1 {
		return "", false
	}
	raw, ok := fields["runId"]
	if !ok {
		return "", false
	}
	var runID string
	if err := strictjson.DecodeWithOptions(raw, &runID, strictjson.Options{MaxBytes: 256, MaxDepth: 2}); err != nil || runID == "" || runID != strings.TrimSpace(runID) {
		return "", false
	}
	return runID, true
}

// operationCancelOutcome accepts only the exact terminal evidence emitted by
// keyed cancellation. Both the run identity and cancelled status are needed
// to prevent a replay from trusting an ambiguous or incomplete result.
func operationCancelOutcome(op refreshoperation.Record) (string, string, bool) {
	if op.State != "completed" || len(op.Outcome) == 0 {
		return "", "", false
	}
	var fields map[string]json.RawMessage
	if err := strictjson.DecodeWithOptions(op.Outcome, &fields, strictjson.Options{MaxBytes: 32768, MaxDepth: 4, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil || len(fields) != 2 {
		return "", "", false
	}
	rawRunID, runOK := fields["runId"]
	rawStatus, statusOK := fields["status"]
	if !runOK || !statusOK {
		return "", "", false
	}
	var runID, status string
	if err := strictjson.DecodeWithOptions(rawRunID, &runID, strictjson.Options{MaxBytes: 256, MaxDepth: 2}); err != nil || runID == "" || runID != strings.TrimSpace(runID) {
		return "", "", false
	}
	if err := strictjson.DecodeWithOptions(rawStatus, &status, strictjson.Options{MaxBytes: 64, MaxDepth: 2}); err != nil || status != refreshrun.RunStatusCancelled {
		return "", "", false
	}
	return runID, status, true
}

// CreateRunTree is the PostgreSQL atomic admission boundary used by
// QueuePipelineRefresh. It constructs deterministic dependency identities and
// delegates one transaction containing root insertion, Replace supersession,
// canonical job enqueue/link, occurrence close, and every child insert.
func (p *postgresRunPersistence) CreateRunTree(ctx context.Context, tree refreshrun.RunTreeInput) (refreshrun.RunRecord, []refreshrun.RunRecord, error) {
	if p == nil || p.repository == nil {
		return refreshrun.RunRecord{}, nil, errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	rootInput := tree.Root
	if rootInput.ParentRunID != "" || rootInput.TargetType != refreshrun.TargetRefreshPipeline {
		return refreshrun.RunRecord{}, nil, errors.New("refresh run tree requires a root pipeline input")
	}
	if tree.Occurrence != nil {
		occurrence := tree.Occurrence
		if rootInput.TriggerType != refreshrun.TriggerSchedule || occurrence.Identity != rootInput.Identity || occurrence.PipelineID != rootInput.TargetID || occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano) != rootInput.NominalTime || occurrence.OccurrenceID == "" || occurrence.LeaseOwner == "" || occurrence.LeaseRevision <= 0 {
			return refreshrun.RunRecord{}, nil, errors.New("scheduled refresh run does not match claimed occurrence")
		}
		if !sameStringSlice(rootInput.MatchingScheduleIDs, occurrence.MatchingScheduleIDs) {
			return refreshrun.RunRecord{}, nil, errors.New("scheduled refresh run does not match occurrence schedule evidence")
		}
		rootInput.OccurrenceID = occurrence.OccurrenceID
	}
	root, err := toPostgresRunInput(rootInput)
	if err != nil {
		return refreshrun.RunRecord{}, nil, err
	}
	childInputs := make([]refreshpostgres.RunInput, 0, len(tree.DependencyTargets))
	seenTargets := make(map[string]struct{}, len(tree.DependencyTargets))
	for _, targetID := range tree.DependencyTargets {
		if err := targetID.Validate(); err != nil {
			return refreshrun.RunRecord{}, nil, err
		}
		if _, exists := seenTargets[targetID.String()]; exists {
			return refreshrun.RunRecord{}, nil, fmt.Errorf("duplicate refresh dependency target %q", targetID)
		}
		seenTargets[targetID.String()] = struct{}{}
		child := rootInput
		child.RunID = deterministicChildRunID(root.RunID, targetID.String())
		child.ParentRunID = root.RunID
		child.TargetType = refreshrun.TargetModel
		child.TargetID = targetID
		child.TriggerType = refreshrun.TriggerDependency
		child.JobKind = refreshrun.JobKindChildRun
		child.OccurrenceID = ""
		child.AuditIntent = nil
		converted, convertErr := toPostgresRunInput(child)
		if convertErr != nil {
			return refreshrun.RunRecord{}, nil, convertErr
		}
		childInputs = append(childInputs, converted)
	}
	if p.jobs == nil {
		return refreshrun.RunRecord{}, nil, errors.New("canonical platform jobs queue writer is required")
	}
	createHook := func(hookCtx context.Context, tx refreshpostgres.Tx, created refreshpostgres.Run) (string, error) {
		jobID, enqueueErr := p.jobs.EnqueueRefreshTx(hookCtx, tx, rootInput, created.RunID)
		if enqueueErr == nil && jobID == "" {
			return "", errors.New("canonical platform jobs queue writer returned an empty job id")
		}
		return jobID, enqueueErr
	}
	supersedeHook := func(hookCtx context.Context, tx refreshpostgres.Tx, jobIDs []string) error {
		lifecycle, lifecycleErr := p.queueLifecycle()
		if lifecycleErr != nil {
			return lifecycleErr
		}
		return lifecycle.SupersedeJobsTx(hookCtx, tx, jobIDs)
	}
	occurrenceID, owner, fence := "", "", int64(0)
	if tree.Occurrence != nil {
		occurrenceID, owner, fence = tree.Occurrence.OccurrenceID, tree.Occurrence.LeaseOwner, tree.Occurrence.LeaseRevision
	}
	if tree.IdempotencyKey != "" && tree.RequestDigest == "" {
		return refreshrun.RunRecord{}, nil, errors.New("refresh request digest is required with idempotency key")
	}
	if tree.IdempotencyKey != "" && p.operations == nil {
		return refreshrun.RunRecord{}, nil, errors.New("PostgreSQL operation authority is required")
	}
	if tree.IdempotencyKey != "" && rootInput.AuditIntent != nil && p.createAuditWriter == nil {
		return refreshrun.RunRecord{}, nil, errors.New("refresh create audit writer is required")
	}
	if tree.IdempotencyKey != "" {
		if _, ok := p.jobs.(PostgresRefreshEventWriter); !ok {
			return refreshrun.RunRecord{}, nil, errors.New("refresh lifecycle event writer is required")
		}
	}
	var createdRoot refreshpostgres.Run
	var createdChildren []refreshpostgres.Run
	var createErr error
	if tree.IdempotencyKey != "" {
		auditHook := func(hookCtx context.Context, tx refreshpostgres.Tx, created refreshpostgres.Run) error {
			if p.createAuditWriter == nil || rootInput.AuditIntent == nil {
				return nil
			}
			intent := *rootInput.AuditIntent
			intent.RequestDigest = tree.RequestDigest
			if intent.MetadataJSON != "" {
				var metadata map[string]any
				if json.Unmarshal([]byte(intent.MetadataJSON), &metadata) == nil {
					metadata["id"] = created.RunID
					metadata["pipelineId"] = created.PipelineID
					metadata["semanticModel"] = created.SemanticModelID
					metadata["invocationSource"] = created.InvocationSource
					metadata["matchingScheduleIds"] = created.MatchingScheduleIDs
					metadata["planDigest"] = created.PlanDigest
					metadata["status"] = "queued"
					if encoded, marshalErr := json.Marshal(metadata); marshalErr == nil {
						intent.MetadataJSON = string(encoded)
					}
				}
			}
			return p.createAuditWriter.RecordRefreshAuditTx(hookCtx, tx, intent)
		}
		eventHook := func(hookCtx context.Context, tx refreshpostgres.Tx, created refreshpostgres.Run) error {
			writer, ok := p.jobs.(PostgresRefreshEventWriter)
			if !ok {
				return nil
			}
			payload, marshalErr := json.Marshal(struct {
				ID                  string   `json:"id"`
				PipelineID          string   `json:"pipelineId"`
				SemanticModel       string   `json:"semanticModel"`
				InvocationSource    string   `json:"invocationSource"`
				MatchingScheduleIDs []string `json:"matchingScheduleIds"`
				PlanDigest          string   `json:"planDigest"`
				Status              string   `json:"status"`
			}{created.RunID, created.PipelineID, created.SemanticModelID, created.InvocationSource, created.MatchingScheduleIDs, created.PlanDigest, "queued"})
			if marshalErr != nil {
				return marshalErr
			}
			return writer.AppendRefreshQueuedEventTx(hookCtx, tx, created.RunID, string(payload), "refresh.queued")
		}
		createErr = p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
			acquired, acquireErr := p.operations.AcquireTx(ctx, tx, refreshoperation.AcquireInput{
				Scope: refreshOperationScope(root.ProjectID, root.Environment), OperationType: "refresh_pipeline",
				IdempotencyKey: tree.IdempotencyKey, RequestDigest: tree.RequestDigest, OwnerID: root.PrincipalID,
				Lease: 2 * time.Minute, Retention: 24 * time.Hour,
			})
			if acquireErr != nil {
				return acquireErr
			}
			if acquired.Operation.Scope != refreshOperationScope(root.ProjectID, root.Environment) || acquired.Operation.OperationType != "refresh_pipeline" || acquired.Operation.IdempotencyKey != tree.IdempotencyKey || acquired.Operation.RequestDigest != tree.RequestDigest {
				return refreshpostgres.ErrConflict
			}
			if acquired.Status == refreshoperation.Replay || acquired.Replay {
				runID, replayOK := operationRunID(acquired.Operation)
				if !replayOK {
					return refreshpostgres.ErrConflict
				}
				replayRoot, replayErr := p.repository.GetRunTx(ctx, tx, refreshpostgres.Scope{ProjectID: root.ProjectID, Environment: root.Environment}, runID)
				if replayErr != nil {
					return replayErr
				}
				if replayRoot.OperationID != acquired.Operation.OperationID || replayRoot.PipelineID != root.PipelineID {
					return refreshpostgres.ErrConflict
				}
				replayChildren, replayErr := p.repository.WithTx(tx).ListChildRuns(ctx, refreshpostgres.Scope{ProjectID: root.ProjectID, Environment: root.Environment}, runID, refreshpostgres.MaxPageSize)
				if replayErr != nil {
					return replayErr
				}
				createdRoot, createdChildren = replayRoot, replayChildren
				return nil
			}
			if acquired.Status != refreshoperation.Acquired {
				return refreshpostgres.ErrConflict
			}
			root.OperationID = acquired.Operation.OperationID
			createdRoot, createdChildren, createErr = p.repository.CreateRunTreeTx(ctx, tx, root, childInputs, occurrenceID, owner, fence, createHook, supersedeHook)
			if createErr != nil {
				return createErr
			}
			if err := auditHook(ctx, tx, createdRoot); err != nil {
				return err
			}
			if err := eventHook(ctx, tx, createdRoot); err != nil {
				return err
			}
			outcome, marshalErr := json.Marshal(map[string]string{"runId": createdRoot.RunID})
			if marshalErr != nil {
				return marshalErr
			}
			if err := p.operations.CompleteTx(ctx, tx, acquired.Lease, outcome); err != nil {
				return err
			}
			return nil
		})
	} else {
		createdRoot, createdChildren, createErr = p.repository.CreateRunTreeWithSupersedeHook(ctx, root, childInputs, occurrenceID, owner, fence, createHook, supersedeHook)
	}
	if createErr != nil {
		return refreshrun.RunRecord{}, nil, createErr
	}
	rootRecord, err := fromPostgresRun(createdRoot)
	if err != nil {
		return refreshrun.RunRecord{}, nil, err
	}
	childRecords := make([]refreshrun.RunRecord, 0, len(createdChildren))
	for _, child := range createdChildren {
		record, convertErr := fromPostgresRun(child)
		if convertErr != nil {
			return refreshrun.RunRecord{}, nil, convertErr
		}
		childRecords = append(childRecords, record)
	}
	return rootRecord, childRecords, nil
}

func deterministicChildRunID(rootID, targetID string) string {
	digest := sha256.Sum256([]byte(rootID + "\x00" + targetID))
	return rootID + "-child-" + hex.EncodeToString(digest[:12])
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (p *postgresRunPersistence) CreateRun(ctx context.Context, input refreshrun.RunInput) (refreshrun.RunRecord, error) {
	_ = ctx
	_ = input
	return refreshrun.RunRecord{}, errors.New("standalone PostgreSQL refresh run admission is disabled; use CreateRunTree")
}

func toPostgresRunInput(input refreshrun.RunInput) (refreshpostgres.RunInput, error) {
	if err := input.Validate(); err != nil {
		return refreshpostgres.RunInput{}, err
	}
	if input.PipelinePlan == nil {
		return refreshpostgres.RunInput{}, errors.New("refresh pipeline plan is required")
	}
	runID := input.RunID
	if runID == "" {
		// Scheduled runs are keyed by their durable occurrence token. Manual
		// requests without an explicit command identity are fresh invocations;
		// generate an opaque UUID instead of collapsing identical payloads.
		if input.OccurrenceID != "" {
			runID = "run-occurrence-" + input.OccurrenceID
		} else {
			generated, err := refreshpostgres.NewUUIDv7()
			if err != nil {
				return refreshpostgres.RunInput{}, err
			}
			runID = generated
		}
	}
	nominal := time.Time{}
	if input.NominalTime != "" {
		parsed, err := time.Parse(time.RFC3339Nano, input.NominalTime)
		if err != nil {
			return refreshpostgres.RunInput{}, err
		}
		nominal = parsed.UTC()
	}
	return refreshpostgres.RunInput{
		RunID:     runID,
		ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, GenerationID: input.Identity.GenerationID,
		ParentRunID: input.ParentRunID,
		PipelineID:  input.PipelineID.String(), SemanticModelID: input.SemanticModelID.String(), TargetType: input.TargetType, TargetID: input.TargetID.String(), TargetRevision: input.TargetRevision,
		TriggerType: input.TriggerType, InvocationSource: input.InvocationSource, TriggerID: input.TriggerID, ConcurrencyPolicy: input.ConcurrencyPolicy,
		OccurrenceID: input.OccurrenceID, NominalTime: nominal, PlanDigest: input.PipelinePlan.Digest, ArtifactDigest: input.PipelinePlan.ArtifactDigest,
		MatchingScheduleIDs: append([]string(nil), input.MatchingScheduleIDs...), MaterializationScope: append([]string(nil), input.PipelinePlan.MaterializationScope...), PrincipalID: input.PrincipalID,
	}, nil
}

func fromPostgresRun(run refreshpostgres.Run) (refreshrun.RunRecord, error) {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(run.ProjectID), run.Environment, run.GenerationID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	out := refreshrun.RunRecord{
		ID: run.RunID, Identity: identity, SemanticModelID: projectgraph.ResourceID(run.SemanticModelID), PipelineID: projectgraph.ResourceID(run.PipelineID),
		InvocationSource: run.InvocationSource, MatchingScheduleIDs: append([]string(nil), run.MatchingScheduleIDs...), TriggerID: run.TriggerID,
		PlanDigest: run.PlanDigest, MaterializationScope: append([]string(nil), run.MaterializationScope...), PrincipalID: run.PrincipalID,
		TargetType: run.TargetType, TargetID: projectgraph.ResourceID(run.TargetID), TargetRevision: run.TargetRevision, TriggerType: run.TriggerType,
		Status: run.Status, Error: run.Error,
		ParentRunID: run.ParentRunID,
	}
	if !run.NominalTime.IsZero() {
		out.NominalTime = run.NominalTime.UTC().Format(time.RFC3339Nano)
	}
	out.CreatedAt = run.CreatedAt.UTC().Format(time.RFC3339Nano)
	out.UpdatedAt = run.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if !run.StartedAt.IsZero() {
		out.StartedAt = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !run.FinishedAt.IsZero() {
		out.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	return out, nil
}

func (p *postgresRunPersistence) GetRun(ctx context.Context, scope refreshrun.ReadScope, runID string) (refreshrun.RunRecord, error) {
	if p == nil || p.repository == nil {
		return refreshrun.RunRecord{}, errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	return p.getRun(ctx, scope, runID)
}

func (p *postgresRunPersistence) getRun(ctx context.Context, scope refreshrun.ReadScope, runID string) (refreshrun.RunRecord, error) {
	run, err := p.repository.GetRun(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, runID)
	if errors.Is(err, refreshpostgres.ErrNotFound) {
		return refreshrun.RunRecord{}, sql.ErrNoRows
	}
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return fromPostgresRun(run)
}

func (p *postgresRunPersistence) ListRuns(ctx context.Context, scope refreshrun.ReadScope, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	runs, err := p.repository.ListRuns(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment, GenerationID: ""}, limit, page.After)
	if err != nil {
		return nil, err
	}
	return mapPostgresRuns(runs)
}

func mapPostgresRuns(runs []refreshpostgres.Run) ([]refreshrun.RunRecord, error) {
	out := make([]refreshrun.RunRecord, 0, len(runs))
	for _, run := range runs {
		mapped, err := fromPostgresRun(run)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (p *postgresRunPersistence) ListTargetRuns(ctx context.Context, scope refreshrun.ReadScope, targetType string, targetID projectgraph.ResourceID, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	runs, err := p.repository.ListRunsFiltered(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, targetType, targetID.String(), "", false, limit, page.After)
	if err != nil {
		return nil, err
	}
	mapped, err := mapPostgresRuns(runs)
	if err != nil {
		return nil, err
	}
	return mapped, nil
}

func (p *postgresRunPersistence) LatestTargetRun(ctx context.Context, scope refreshrun.ReadScope, targetType string, targetID projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	runs, err := p.ListTargetRuns(ctx, scope, targetType, targetID, refreshrun.RunPage{Limit: 1})
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if len(runs) == 0 {
		return refreshrun.RunRecord{}, false, nil
	}
	return runs[0], true, nil
}

func (p *postgresRunPersistence) LatestSuccessfulTargetRun(ctx context.Context, scope refreshrun.ReadScope, targetType string, targetID projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	rows, err := p.repository.ListRunsFiltered(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, targetType, targetID.String(), "", true, 1, "")
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if len(rows) == 1 {
		run, mapErr := fromPostgresRun(rows[0])
		return run, mapErr == nil, mapErr
	}
	return refreshrun.RunRecord{}, false, nil
}

func (p *postgresRunPersistence) ListSemanticModelRuns(ctx context.Context, scope refreshrun.ReadScope, model projectgraph.ResourceID, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	runs, err := p.repository.ListRunsFiltered(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, "", "", model.String(), false, limit, page.After)
	if err != nil {
		return nil, err
	}
	return mapPostgresRuns(runs)
}

func (p *postgresRunPersistence) LatestSuccessfulSemanticModelRun(ctx context.Context, scope refreshrun.ReadScope, model projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	runs, err := p.repository.ListRunsFiltered(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, "", "", model.String(), true, 1, "")
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if len(runs) == 1 {
		run, mapErr := fromPostgresRun(runs[0])
		return run, mapErr == nil, mapErr
	}
	return refreshrun.RunRecord{}, false, nil
}

func (p *postgresRunPersistence) ListChildRuns(ctx context.Context, scope refreshrun.ReadScope, parentRunID string) ([]refreshrun.RunRecord, error) {
	rows, err := p.repository.ListChildRuns(ctx, refreshpostgres.Scope{ProjectID: scope.ProjectID.String(), Environment: scope.Environment}, parentRunID, 100)
	if err != nil {
		return nil, err
	}
	return mapPostgresRuns(rows)
}

func (p *postgresRunPersistence) MarkRunRunning(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, errors.New("PostgreSQL refresh runs require an explicit worker lease claim")
}

func (p *postgresRunPersistence) MarkRunSucceeded(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, errors.New("PostgreSQL refresh runs require an explicit worker lease fence")
}

func (p *postgresRunPersistence) MarkRunFailed(ctx context.Context, identity projectgraph.ServingIdentity, runID, message string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, errors.New("PostgreSQL refresh runs require an explicit worker lease fence")
}

func (p *postgresRunPersistence) MarkQueuedRunFailed(ctx context.Context, identity projectgraph.ServingIdentity, runID, message string) (refreshrun.RunRecord, error) {
	if p == nil || p.repository == nil {
		return refreshrun.RunRecord{}, errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	if err := identity.Validate(); err != nil || strings.TrimSpace(runID) == "" {
		return refreshrun.RunRecord{}, errors.New("queued root identity is required")
	}
	prior, err := p.repository.GetRun(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment}, runID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if prior.ParentRunID != "" {
		return refreshrun.RunRecord{}, errors.New("only queued root runs may be failed by the producer")
	}
	if prior.JobID == "" {
		return refreshrun.RunRecord{}, errors.New("queued root has no canonical platform job")
	}
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	failed, err := p.repository.FailQueuedRunWithHook(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID}, runID, message, func(hookCtx context.Context, tx refreshpostgres.Tx) error {
		return lifecycle.CancelJobTx(hookCtx, tx, prior.JobID)
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return fromPostgresRun(failed)
}

func (p *postgresRunPersistence) MarkRunPrepared(ctx context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	if job.LeaseOwner == "" || job.LeaseRevision <= 0 {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	return p.prepareClaimed(ctx, job)
}

func (p *postgresRunPersistence) prepareClaimed(ctx context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	current, err := p.repository.GetRun(ctx, refreshpostgres.Scope{ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment}, job.RunID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	// PostgreSQL claims the canonical jobs lease and creates the refresh
	// attempt in one transaction before this method is called. A second,
	// delayed ClaimAttempt would permit a stale worker to create a new refresh
	// fence after losing the platform lease.
	if current.Status != "running" && current.Status != "prepared" {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	run, err := p.repository.PrepareRun(ctx, job.RunID, job.LeaseOwner, job.LeaseRevision)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return fromPostgresRun(run)
}

func (p *postgresRunPersistence) RunMayPublish(ctx context.Context, job refreshrun.JobRecord) (bool, error) {
	return p.repository.RunMayPublish(ctx, job.RunID, job.LeaseOwner, job.LeaseRevision)
}

func (p *postgresRunPersistence) MarkRunSucceededClaimed(ctx context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	err = p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		if err := p.repository.CompleteRunTreeTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision, json.RawMessage(`{"module":true}`)); err != nil {
			return err
		}
		return lifecycle.CompleteJobTx(ctx, tx, job)
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return p.getRun(ctx, refreshrun.ReadScope{ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment}, job.RunID)
}

func (p *postgresRunPersistence) MarkRunFailedClaimed(ctx context.Context, job refreshrun.JobRecord, message string) (refreshrun.RunRecord, error) {
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	err = p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		message = safeWorkerFailureMessage(message)
		if err := p.repository.FailAttemptTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision, message, json.RawMessage(`{"module":true}`)); err != nil {
			return err
		}
		return lifecycle.FailJobTx(ctx, tx, job, message)
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return p.getRun(ctx, refreshrun.ReadScope{ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment}, job.RunID)
}

func (p *postgresRunPersistence) MarkRunTreeFailedClaimed(ctx context.Context, job refreshrun.JobRecord, message string) error {
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return err
	}
	return p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		message = safeWorkerFailureMessage(message)
		if err := p.repository.FailRunTreeTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision, message, json.RawMessage(`{"module":true}`)); err != nil {
			return err
		}
		// The root job is the worker's fenced claim. Child jobs are failed by
		// the tree transition above when they have active attempts; terminally
		// close the claimed root job in the same transaction.
		return lifecycle.FailJobTx(ctx, tx, job, message)
	})
}

func safeWorkerFailureMessage(_ string) string { return "refresh execution failed" }

func (p *postgresRunPersistence) MarkRunTreeSupersededClaimed(ctx context.Context, job refreshrun.JobRecord, message string) error {
	if p == nil || p.repository == nil {
		return errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return err
	}
	return p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		jobIDs, err := p.repository.SupersedeRunTreeTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision, message)
		if err != nil {
			return err
		}
		if err := lifecycle.CancelClaimedJobTx(ctx, tx, job); err != nil {
			return err
		}
		// The exact worker root job has just been terminalized through its fence;
		// supersede only descendant links returned by the refresh authority.
		remaining := make([]string, 0, len(jobIDs))
		for _, jobID := range jobIDs {
			if jobID != job.ID {
				remaining = append(remaining, jobID)
			}
		}
		if len(remaining) > 0 {
			return lifecycle.SupersedeJobsTx(ctx, tx, remaining)
		}
		return nil
	})
}

func (p *postgresRunPersistence) CancelRun(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	prior, err := p.repository.LookupRun(ctx, runID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if prior.JobID == "" {
		return refreshrun.RunRecord{}, errors.New("queued root has no canonical platform job")
	}
	run, err := p.repository.CancelRunWithAudit(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID}, runID, func(auditCtx context.Context, tx refreshpostgres.Tx) error {
		return lifecycle.CancelJobTx(auditCtx, tx, prior.JobID)
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return fromPostgresRun(run)
}

func (p *postgresRunPersistence) CancelRunWithAudit(ctx context.Context, identity projectgraph.ServingIdentity, runID string, intent *access.AuditIntent) (refreshrun.RunRecord, error) {
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	prior, err := p.repository.LookupRun(ctx, runID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if intent == nil {
		if fromContext, ok := refreshrun.AuditIntentFromContext(ctx); ok {
			intent = &fromContext
		}
	}
	if intent != nil && p.cancelAuditWriter == nil {
		return refreshrun.RunRecord{}, errors.New("refresh cancellation audit writer is required")
	}
	if prior.JobID == "" {
		return refreshrun.RunRecord{}, errors.New("queued root has no canonical platform job")
	}
	run, err := p.repository.CancelRunWithAudit(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID}, runID, func(auditCtx context.Context, tx refreshpostgres.Tx) error {
		if intent != nil {
			if err := p.cancelAuditWriter.RecordRefreshCancelAuditTx(auditCtx, tx, *intent); err != nil {
				return err
			}
		}
		return lifecycle.CancelJobTx(auditCtx, tx, prior.JobID)
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return fromPostgresRun(run)
}

// CancelRunWithAuditKeyed reserves the shared platform operation for a native
// cancellation and composes its terminal outcome with the refresh run, the
// canonical platform job, and the cancellation audit in one caller-owned
// PostgreSQL transaction. Replays read the exact {runId,status} evidence and
// return the original cancelled run without invoking either callback.
func (p *postgresRunPersistence) CancelRunWithAuditKeyed(ctx context.Context, identity projectgraph.ServingIdentity, runID, actorID, idempotencyKey, requestDigest string, intent *access.AuditIntent) (refreshrun.RunRecord, bool, error) {
	if p == nil || p.repository == nil || p.operations == nil {
		return refreshrun.RunRecord{}, false, errors.New("refresh PostgreSQL keyed cancellation is unavailable")
	}
	if err := identity.Validate(); err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	actorID = strings.TrimSpace(actorID)
	rawKey, rawDigest := idempotencyKey, requestDigest
	idempotencyKey = strings.TrimSpace(rawKey)
	requestDigest = strings.TrimSpace(rawDigest)
	if actorID == "" || idempotencyKey == "" || requestDigest == "" || rawKey != idempotencyKey || rawDigest != requestDigest {
		return refreshrun.RunRecord{}, false, errors.New("refresh keyed cancellation identity is required")
	}
	if intent == nil {
		if fromContext, ok := refreshrun.AuditIntentFromContext(ctx); ok {
			intent = &fromContext
		}
	}
	if intent != nil && p.cancelAuditWriter == nil {
		return refreshrun.RunRecord{}, false, errors.New("refresh cancellation audit writer is required")
	}
	lifecycle, err := p.queueLifecycle()
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	scope := refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID}
	var cancelled refreshpostgres.Run
	replayed := false
	err = p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		acquired, acquireErr := p.operations.AcquireTx(ctx, tx, refreshoperation.AcquireInput{
			Scope: refreshOperationScope(identity.ProjectID.String(), identity.Environment), OperationType: "refresh_pipeline_cancel",
			IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, OwnerID: actorID,
			Lease: 2 * time.Minute, Retention: 24 * time.Hour,
		})
		if acquireErr != nil {
			return acquireErr
		}
		if acquired.Operation.Scope != refreshOperationScope(identity.ProjectID.String(), identity.Environment) || acquired.Operation.OperationType != "refresh_pipeline_cancel" || acquired.Operation.IdempotencyKey != idempotencyKey || acquired.Operation.RequestDigest != requestDigest {
			return refreshpostgres.ErrConflict
		}
		if acquired.Status == refreshoperation.Replay || acquired.Replay {
			evidenceRunID, evidenceStatus, ok := operationCancelOutcome(acquired.Operation)
			if !ok || evidenceRunID != runID || evidenceStatus != refreshrun.RunStatusCancelled {
				return refreshpostgres.ErrConflict
			}
			replay, replayErr := p.repository.GetRunTx(ctx, tx, scope, runID)
			if replayErr != nil {
				return replayErr
			}
			if replay.Status != evidenceStatus {
				return refreshpostgres.ErrConflict
			}
			cancelled = replay
			replayed = true
			return nil
		}
		if acquired.Status != refreshoperation.Acquired {
			return refreshpostgres.ErrConflict
		}
		prior, priorErr := p.repository.LookupRunTx(ctx, tx, runID)
		if priorErr != nil {
			return priorErr
		}
		if prior.ProjectID != identity.ProjectID.String() || prior.Environment != identity.Environment || prior.GenerationID != identity.GenerationID || prior.ParentRunID != "" || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.JobID == "" {
			return refreshpostgres.ErrNotFound
		}
		cancelled, err = p.repository.CancelRunWithAuditTx(ctx, tx, scope, runID, func(auditCtx context.Context, auditTx refreshpostgres.Tx) error {
			if intent != nil {
				auditIntent := *intent
				auditIntent.RequestDigest = requestDigest
				if err := p.cancelAuditWriter.RecordRefreshCancelAuditTx(auditCtx, auditTx, auditIntent); err != nil {
					return err
				}
			}
			return lifecycle.CancelJobTx(auditCtx, auditTx, prior.JobID)
		})
		if err != nil {
			return err
		}
		outcome, marshalErr := json.Marshal(struct {
			RunID  string `json:"runId"`
			Status string `json:"status"`
		}{cancelled.RunID, cancelled.Status})
		if marshalErr != nil {
			return marshalErr
		}
		return p.operations.CompleteTx(ctx, tx, acquired.Lease, outcome)
	})
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	row, err := fromPostgresRun(cancelled)
	return row, replayed, err
}

func (p *postgresRunPersistence) CheckInvocationAdmission(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID, source string) error {
	if p == nil || p.repository == nil {
		return errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	return p.repository.CheckInvocationAdmission(ctx, refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment}, pipelineID.String(), source)
}

func (p *postgresRunPersistence) CheckScheduledInvocationAdmission(ctx context.Context, occurrence refreshschedule.Occurrence) error {
	if p == nil || p.repository == nil {
		return errors.New("refresh PostgreSQL run persistence is unavailable")
	}
	return p.repository.CheckScheduledInvocationAdmission(ctx, refreshpostgres.Scope{ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment}, occurrence.PipelineID.String())
}

func (p *postgresRunPersistence) ListExecutableJobs(ctx context.Context, scope refreshrun.ReadScope, limit int) ([]refreshrun.JobRecord, error) {
	if p.jobs == nil {
		return nil, errors.New("canonical platform jobs queue is not configured")
	}
	return p.jobs.ListExecutableJobs(ctx, scope, limit)
}

func (p *postgresRunPersistence) ClaimExecutableJob(ctx context.Context, candidate refreshrun.JobRecord, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	if p.jobs == nil {
		return refreshrun.JobRecord{}, false, errors.New("canonical platform jobs queue is not configured")
	}
	return p.jobs.ClaimExecutableJob(ctx, candidate, owner, lease)
}

func (p *postgresRunPersistence) RenewJobLease(ctx context.Context, job refreshrun.JobRecord, lease time.Duration) error {
	if p.jobs == nil {
		return errors.New("canonical platform jobs queue is not configured")
	}
	return p.jobs.RenewJobLease(ctx, job, lease)
}

func (p *postgresRunPersistence) JobQueueStats(ctx context.Context, scope refreshrun.ReadScope) (refreshrun.JobQueueStats, error) {
	if p.jobs == nil {
		return refreshrun.JobQueueStats{}, errors.New("canonical platform jobs queue is not configured")
	}
	return p.jobs.JobQueueStats(ctx, scope)
}

// postgresPublicationPersistence is deliberately small: publication state,
// data-version provenance, and terminal run completion are all delegated to
// the PostgreSQL authority. The adapter never opens a database transaction or
// reaches into pgx directly, so callers retain ownership of any transaction
// paths exposed by the authority itself.
type postgresPublicationPersistence struct {
	repository        *refreshpostgres.Repository
	identityResolver  PostgresPublicationIdentityResolver
	canonicalVerifier PostgresCanonicalVerifier
	nativeFinalizer   PostgresNativeRefreshFinalizer
	cancelAuditWriter PostgresCancelAuditWriter
	queueLifecycle    PostgresQueueLifecycle
	queueRecovery     PostgresQueueRecovery
}

func (p *postgresPublicationPersistence) Publish(ctx context.Context, identity projectgraph.ServingIdentity, servingStateID servingstate.ID, version refreshschedule.DataVersion) error {
	_ = ctx
	_ = identity
	_ = servingStateID
	_ = version
	return errors.New("legacy PostgreSQL refresh publication is disabled; use CompleteCanonicalRefresh")
}

// CompleteCanonicalRefresh verifies delivery-owned canonical evidence and
// commits publication, data-version provenance, and refresh completion in one
// authority transaction. A verifier is required because delivery tables are a
// separate capability; omitting it fails closed instead of assuming a
// snapshot-only publication.
func (p *postgresPublicationPersistence) CompleteCanonicalRefresh(ctx context.Context, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) error {
	if p == nil || p.repository == nil || p.canonicalVerifier == nil {
		return errors.New("canonical refresh verifier is required")
	}
	if err := job.Validate(); err != nil || job.LeaseOwner == "" || job.LeaseRevision <= 0 || result.PlanID == "" || result.ServingStateID == "" || result.SnapshotID <= 0 {
		return refreshrun.ErrLeaseLost
	}
	evidence, _ := json.Marshal(struct {
		PlanID         string `json:"plan_id"`
		ServingStateID string `json:"serving_state_id"`
		SnapshotID     int64  `json:"snapshot_id"`
	}{result.PlanID, result.ServingStateID, result.SnapshotID})
	return p.repository.InTx(ctx, func(tx refreshpostgres.Tx) error {
		publicationID := "publication-canonical-" + job.RunID
		// A committed refresh publication is durable completion evidence. Resolve
		// the currently admitted physical identity before accepting that replay;
		// an old physical tuple must never be trusted when the admission has
		// changed since the original completion.
		publication, publicationErr := p.repository.PublicationTx(ctx, tx, publicationID)
		if publicationErr == nil {
			if publication.State != "committed" {
				// Preserve the poison check for a partial refresh publication. A
				// pending link is not a fresh completion and must fail closed.
				_, replayErr := p.replayCanonicalCompletionTx(ctx, tx, job, result, evidence, PostgresPublicationIdentity{})
				return replayErr
			}
			identity, identityErr := resolvePublicationIdentityTx(ctx, tx, p.identityResolver, PostgresPublicationIdentityRequest{
				ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment,
				GenerationID: result.ServingStateID, SemanticModelID: job.SemanticModelID.String(),
				PipelineID: job.PipelineID.String(), RunID: job.RunID, SnapshotID: result.SnapshotID,
				Source: string(refreshschedule.DataVersionSourceRefresh), TargetRevision: job.TargetRevision,
			})
			if identityErr != nil {
				return identityErr
			}
			replayed, replayErr := p.replayCanonicalCompletionTx(ctx, tx, job, result, evidence, identity)
			if replayErr != nil {
				return replayErr
			}
			if replayed {
				return nil
			}
			return refreshpostgres.ErrConflict
		}
		if !errors.Is(publicationErr, refreshpostgres.ErrNotFound) {
			return publicationErr
		}
		// No deterministic publication exists yet. Run the partial-evidence
		// poison checks before entering the first-time path; this prevents a
		// missing link from masking a marker, data-version, terminal run, or
		// terminal queue outcome left by an interrupted completion.
		replayed, replayErr := p.replayCanonicalCompletionTx(ctx, tx, job, result, evidence, PostgresPublicationIdentity{})
		if replayErr != nil {
			return replayErr
		}
		if replayed {
			return refreshpostgres.ErrConflict
		}
		mayPublish, fenceErr := p.repository.RunMayPublishTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision)
		if fenceErr != nil {
			return fenceErr
		}
		if !mayPublish {
			return refreshpostgres.ErrStaleFence
		}
		pubInput, err := p.canonicalVerifier.VerifyCanonicalRefreshTx(ctx, tx, job, result)
		if err != nil {
			return err
		}
		if pubInput.RunID != job.RunID || pubInput.BaseGenerationID != job.Identity.GenerationID || pubInput.ResultGenerationID != result.ServingStateID || pubInput.ExpectedTargetRevision != job.TargetRevision || pubInput.ResultTargetRevision <= pubInput.ExpectedTargetRevision || pubInput.SnapshotID != 0 {
			return errors.New("canonical publication evidence identity differs")
		}
		if pubInput.PublicationID == "" {
			pubInput.PublicationID = publicationID
		}
		pubInput.PlanDigest = job.PipelinePlan.Digest
		pubInput.ArtifactDigest = job.PipelinePlan.ArtifactDigest
		pubInput.OwnerID = job.LeaseOwner
		pubInput.FenceGeneration = job.LeaseRevision
		pubInput.Evidence = evidence
		if p.nativeFinalizer != nil {
			if err := p.nativeFinalizer.FinalizeCanonicalRefreshTx(ctx, tx, job, result, pubInput); err != nil {
				return fmt.Errorf("finalize native canonical refresh: %w", err)
			}
		}
		// Native finalization may create and activate the result generation.
		// Resolve its physical identity only after that step, then bind all
		// refresh provenance to the exact admitted tuple before linking or
		// committing any refresh evidence.
		identity, identityErr := resolvePublicationIdentityTx(ctx, tx, p.identityResolver, PostgresPublicationIdentityRequest{
			ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment,
			GenerationID: result.ServingStateID, SemanticModelID: job.SemanticModelID.String(),
			PipelineID: job.PipelineID.String(), RunID: job.RunID, SnapshotID: result.SnapshotID,
			Source: string(refreshschedule.DataVersionSourceRefresh), TargetRevision: job.TargetRevision,
		})
		if identityErr != nil {
			return identityErr
		}
		if pubInput.PhysicalPoolID != identity.PhysicalPoolID || pubInput.CatalogID != identity.CatalogID {
			return publicationIdentityMismatchf("canonical publication physical identity differs from resolved runtime")
		}
		publication, err = p.repository.LinkPublicationTx(ctx, tx, pubInput)
		if err != nil {
			return fmt.Errorf("link canonical publication: %w", err)
		}
		if err := p.repository.CommitPublicationTx(ctx, tx, publication.PublicationID, job.RunID, job.LeaseOwner, job.LeaseRevision, result.SnapshotID, evidence, pubInput.PhysicalPoolID, pubInput.CatalogID); err != nil {
			return fmt.Errorf("commit canonical publication: %w", err)
		}
		if err := p.repository.SaveDataVersionTx(ctx, tx, refreshpostgres.DataVersion{
			ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment, SemanticModelID: job.SemanticModelID.String(), GenerationID: result.ServingStateID,
			SnapshotID: result.SnapshotID, Source: refreshschedule.DataVersionSourceRefresh, PipelineID: job.PipelineID.String(), RunID: job.RunID,
			PhysicalPoolID: identity.PhysicalPoolID, CatalogID: identity.CatalogID, TargetRevision: pubInput.ResultTargetRevision, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
		}); err != nil {
			return fmt.Errorf("save canonical data version: %w", err)
		}
		if err := p.repository.CompleteRunTreeTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision, evidence); err != nil {
			return fmt.Errorf("complete canonical run tree: %w", err)
		}
		if p.queueLifecycle == nil {
			return errors.New("canonical platform jobs queue lifecycle is required")
		}
		return p.queueLifecycle.CompleteJobTx(ctx, tx, job)
	})
}

func (p *postgresPublicationPersistence) replayCanonicalCompletionTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult, evidence []byte, identity PostgresPublicationIdentity) (bool, error) {
	publicationID := "publication-canonical-" + job.RunID
	publication, err := p.repository.PublicationTx(ctx, tx, publicationID)
	if errors.Is(err, refreshpostgres.ErrNotFound) {
		marker, markerErr := p.repository.PublicationLinkMarkerTx(ctx, tx, job.RunID, result.ServingStateID)
		if markerErr != nil {
			return false, markerErr
		}
		_, foundVersion, versionErr := p.repository.DataVersionTx(ctx, tx, job.Identity.ProjectID.String(), job.Identity.Environment, job.SemanticModelID.String(), result.ServingStateID)
		if versionErr != nil {
			return false, versionErr
		}
		treeSucceeded, treeErr := p.repository.RunTreeSucceededTx(ctx, tx, job.RunID)
		if treeErr != nil {
			return false, treeErr
		}
		if marker || foundVersion || treeSucceeded {
			return false, refreshpostgres.ErrConflict
		}
		if p.queueRecovery != nil {
			queuedJob, queueErr := p.queueRecovery.GetJobTx(ctx, tx, job.ID)
			if queueErr == nil {
				if queuedJob.Status == jobs.StatusSucceeded || queuedJob.Status == jobs.StatusFailed || queuedJob.Status == jobs.StatusCancelled {
					return false, refreshpostgres.ErrConflict
				}
				if queuedJob.Attempts > 0 {
					attempt, foundAttempt, attemptErr := p.queueRecovery.LatestAttemptTx(ctx, tx, queuedJob.ID, int64(queuedJob.Attempts), queuedJob.LeaseGeneration)
					if attemptErr != nil {
						return false, attemptErr
					}
					if !foundAttempt || attempt.Outcome != "running" {
						return false, refreshpostgres.ErrConflict
					}
				}
			} else if !errors.Is(queueErr, jobs.ErrNotFound) {
				return false, queueErr
			}
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if publication.State != "committed" {
		return false, fmt.Errorf("%w: replay publication state=%s", refreshpostgres.ErrConflict, publication.State)
	}
	if publication.RunID != job.RunID || publication.BaseGenerationID != job.Identity.GenerationID || publication.ResultGenerationID != result.ServingStateID || publication.PlanDigest != job.PipelinePlan.Digest || publication.ArtifactDigest != job.PipelinePlan.ArtifactDigest || publication.ExpectedTargetRevision != job.TargetRevision || publication.SnapshotID != result.SnapshotID || publication.PhysicalPoolID != identity.PhysicalPoolID || publication.CatalogID != identity.CatalogID || publication.OwnerID != job.LeaseOwner || publication.FenceGeneration != job.LeaseRevision || !jsonEquivalent(publication.Evidence, evidence) {
		return false, publicationIdentityMismatchf("replay publication evidence differs from resolved identity")
	}
	version, found, err := p.repository.DataVersionTx(ctx, tx, job.Identity.ProjectID.String(), job.Identity.Environment, job.SemanticModelID.String(), result.ServingStateID)
	if err != nil {
		return false, err
	}
	if !found || version.ProjectID != job.Identity.ProjectID.String() || version.Environment != job.Identity.Environment || version.SemanticModelID != job.SemanticModelID.String() || version.GenerationID != result.ServingStateID || version.SnapshotID != result.SnapshotID || version.Source != refreshschedule.DataVersionSourceRefresh || version.PipelineID != job.PipelineID.String() || version.RunID != job.RunID || version.TargetRevision != publication.ResultTargetRevision || version.LeaseOwner != job.LeaseOwner || version.LeaseRevision != job.LeaseRevision || version.PhysicalPoolID != identity.PhysicalPoolID || version.CatalogID != identity.CatalogID {
		return false, publicationIdentityMismatchf("replay data-version evidence differs from resolved identity")
	}
	treeSucceeded, err := p.repository.RunTreeSucceededTx(ctx, tx, job.RunID)
	if err != nil {
		return false, err
	}
	if !treeSucceeded || p.queueRecovery == nil {
		return false, refreshpostgres.ErrConflict
	}
	queuedJob, err := p.queueRecovery.GetJobTx(ctx, tx, job.ID)
	if err != nil {
		return false, err
	}
	inputPayload := strings.TrimSpace(job.PayloadJSON)
	if inputPayload == "" {
		inputPayload = "{}"
	}
	expectedPayload, payloadErr := json.Marshal(refreshJobPayload{PipelinePlan: job.PipelinePlan, Input: json.RawMessage(inputPayload)})
	if payloadErr != nil {
		return false, payloadErr
	}
	if queuedJob.ID != job.ID || queuedJob.Kind != job.Kind || queuedJob.WorkloadClass != jobpolicy.WorkloadClassBackground || queuedJob.PartitionKey != "refresh:"+job.Identity.ProjectID.String()+":"+job.Identity.Environment || queuedJob.PrincipalID != job.PrincipalID || !slices.Equal(queuedJob.GroupIDs, job.GroupIDs) || queuedJob.ResourceKind != refreshJobResourceKind || queuedJob.ResourceID != job.RunID || queuedJob.EstimatedMemoryBytes != job.EstimatedMemoryBytes || !jsonEquivalent(queuedJob.Payload, expectedPayload) || queuedJob.Attempts != job.AttemptCount || queuedJob.LeaseGeneration != job.LeaseRevision || queuedJob.Status != jobs.StatusSucceeded || queuedJob.FinishedAt == "" || queuedJob.LeaseOwner != "" || queuedJob.LeaseExpiresAt != "" || queuedJob.ErrorJSON != "{}" {
		return false, refreshpostgres.ErrConflict
	}
	attempt, foundAttempt, err := p.queueRecovery.LatestAttemptTx(ctx, tx, queuedJob.ID, int64(job.AttemptCount), job.LeaseRevision)
	if err != nil {
		return false, err
	}
	if !foundAttempt || attempt.JobID != queuedJob.ID || attempt.AttemptNumber != int64(job.AttemptCount) || attempt.FencingGeneration != job.LeaseRevision || attempt.Owner != job.LeaseOwner || attempt.Outcome != string(jobs.StatusSucceeded) || attempt.FinishedAt == nil || string(attempt.ErrorJSON) != "{}" {
		return false, refreshpostgres.ErrConflict
	}
	return true, nil
}

func jsonEquivalent(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
