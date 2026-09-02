package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	refreshdb "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalid      = errors.New("invalid refresh authority input")
	ErrConflict     = errors.New("refresh authority identity conflict")
	ErrNotFound     = errors.New("refresh authority record not found")
	ErrBusy         = errors.New("refresh authority lease is owned by another worker")
	ErrStaleFence   = errors.New("refresh authority fence is stale")
	ErrLeaseExpired = errors.New("refresh authority lease is expired")
)

const (
	MaxPageSize  = 200
	MaxLease     = 24 * time.Hour
	MaxJSONBytes = 65536
)

// Scope is the stable read namespace. Generation remains a run identity but
// is intentionally not required to read historical runs.
type Scope struct{ ProjectID, Environment, GenerationID string }

type ScheduleInput struct {
	ScheduleRevisionID                             string
	ProjectID, Environment, PipelineID, ScheduleID string
	SemanticModelID, GenerationID, ArtifactDigest  string
	Cron, Timezone, ConcurrencyPolicy              string
	StartingDeadline                               time.Duration
	ScheduleDigest                                 string
	NextRunAt                                      time.Time
	Enabled                                        bool
}
type Schedule struct {
	ScheduleInput
	ValidFrom, ClosedAt, UpdatedAt time.Time
}

type Occurrence struct {
	OccurrenceID, ProjectID, Environment, PipelineID string
	NominalTime                                      time.Time
	ScheduleRevisionID                               string
	MatchingScheduleIDs                              []string
	SemanticModelID, GenerationID, ArtifactDigest    string
	Status, RunID, LeaseOwner                        string
	FenceGeneration                                  int64
	LeaseExpiresAt, ClaimedAt, FinishedAt, CreatedAt time.Time
	Outcome                                          json.RawMessage
}

type RunInput struct {
	RunID, OperationID, ProjectID, Environment, GenerationID string
	ParentRunID                                              string
	PipelineID, SemanticModelID, TargetType, TargetID        string
	TargetRevision                                           int64
	TriggerType, InvocationSource, TriggerID                 string
	ConcurrencyPolicy                                        string
	ScheduleRevisionID, OccurrenceID                         string
	NominalTime                                              time.Time
	PlanDigest, ArtifactDigest                               string
	MatchingScheduleIDs                                      []string
	MaterializationScope                                     []string
	PrincipalID, JobID                                       string
}
type Run struct {
	RunInput
	Status                                                      string
	AttemptCount, FenceGeneration                               int64
	LeaseOwner                                                  string
	LeaseExpiresAt, CreatedAt, UpdatedAt, StartedAt, FinishedAt time.Time
	Error                                                       string
}

type Attempt struct {
	RunID, OwnerID                                   string
	AttemptNumber, FenceGeneration                   int64
	LeaseExpiresAt, ClaimedAt, StartedAt, FinishedAt time.Time
	Status                                           string
	Evidence                                         json.RawMessage
	Error                                            string
}

type PublicationInput struct {
	PublicationID, RunID, BaseGenerationID, ResultGenerationID, PlanDigest, ArtifactDigest, OwnerID string
	PhysicalPoolID, CatalogID                                                                       string
	FenceGeneration                                                                                 int64
	ExpectedTargetRevision, ResultTargetRevision                                                    int64
	SnapshotID                                                                                      int64
	Evidence                                                                                        json.RawMessage
}
type Publication struct {
	PublicationInput
	State                  string
	CreatedAt, CommittedAt time.Time
}

type RecoveryInput struct {
	RunID, OwnerID, ExactExternalIdentity, LastError string
	FenceGeneration                                  int64
	Lease                                            time.Duration
	State                                            string
	Evidence                                         json.RawMessage
	NextReconcileAt                                  time.Time
}
type RecoveryState struct {
	RecoveryInput
	LeaseExpiresAt, UpdatedAt time.Time
}

type DataVersion struct {
	ProjectID, Environment, SemanticModelID, GenerationID string
	SnapshotID                                            int64
	RefreshedAt                                           time.Time
	Source, PipelineID, RunID, LeaseOwner                 string
	PhysicalPoolID, CatalogID                             string
	TargetRevision, LeaseRevision                         int64
}

type Repository struct{ db DBTX }

func stringPtr(v string) *string { return &v }
func int64Ptr(v int64) *int64    { return &v }

func New(db DBTX) *Repository { return &Repository{db: db} }

// DB exposes the configured native PostgreSQL handle to composition-owned
// adapters. The handle is only an identity/provenance value; callers must use
// the transaction passed to *Tx methods for mutations.
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

// Configured reports whether the repository has a native PostgreSQL query
// authority. Interface values can contain a typed nil pointer, so a plain
// interface comparison is not sufficient for admission.
func (r *Repository) Configured() bool { return r != nil && nativeDBConfigured(r.db) }

func nativeDBConfigured(db DBTX) bool {
	if db == nil {
		return false
	}
	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

// NewRepository is an explicit constructor alias used by composition roots
// that name capability repositories uniformly.
func NewRepository(db DBTX) *Repository { return New(db) }

// WithTx returns a repository bound to a caller-owned transaction. Methods
// ending in Tx never commit or roll back that transaction.
func (r *Repository) WithTx(tx Tx) *Repository { return New(tx) }

func (r *Repository) requireDB() error {
	if r == nil || !nativeDBConfigured(r.db) {
		return ErrInvalid
	}
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (r *Repository) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("refresh repository requires a transaction-capable pgx DB")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		_ = tx.Rollback(contextOrBackground(ctx))
		return err
	}
	return tx.Commit(contextOrBackground(ctx))
}

// InTx executes a bounded authority workflow in one transaction. If the
// repository is already bound to a caller-owned pgx transaction, ownership is
// retained and no commit/rollback is attempted here.
func (r *Repository) InTx(ctx context.Context, fn func(Tx) error) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	if tx, ok := r.db.(pgx.Tx); ok {
		return fn(tx)
	}
	return r.withTx(ctx, func(tx pgx.Tx) error { return fn(tx) })
}

func canonicalID(label, value string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must be canonical", label)
	}
	return nil
}
func digest(label, value string) error {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%s must be canonical sha256", label)
	}
	for _, c := range value[7:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("%s must be canonical sha256", label)
		}
	}
	return nil
}
func boundedObject(raw json.RawMessage, limit int) ([]byte, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var v any
	if err := strictjson.DecodeWithOptions(raw, &v, strictjson.Options{MaxBytes: int64(limit), MaxDepth: 32}); err != nil {
		return nil, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errors.New("JSON document must be an object")
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > limit {
		return nil, errors.New("JSON document exceeds bound")
	}
	return b, err
}
func jsonEqual(a, b []byte) bool {
	var av, bv any
	if strictjson.DecodeWithOptions(a, &av, strictjson.Options{MaxBytes: MaxJSONBytes, MaxDepth: 32}) != nil || strictjson.DecodeWithOptions(b, &bv, strictjson.Options{MaxBytes: MaxJSONBytes, MaxDepth: 32}) != nil {
		return bytes.Equal(a, b)
	}
	aa, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return bytes.Equal(aa, bb)
}
func digestJSON(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func newID(value string) (string, error) {
	if value != "" {
		if err := canonicalID("id", value, 256); err != nil {
			return "", err
		}
		return value, nil
	}
	v, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return v.String(), nil
}

// NewUUIDv7 returns a time-ordered opaque identity for operation, run and
// publication records. Authored resource identifiers remain validated text.
func NewUUIDv7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func validateScope(project, environment string) error {
	if err := canonicalID("project id", project, 255); err != nil {
		return err
	}
	return canonicalID("environment", environment, 128)
}
func validateGeneration(generation string) error {
	if generation == "" {
		return nil
	}
	return canonicalID("generation id", generation, 255)
}
func validateSchedule(in ScheduleInput) error {
	if err := validateScope(in.ProjectID, in.Environment); err != nil {
		return err
	}
	for label, value := range map[string]string{"pipeline id": in.PipelineID, "schedule id": in.ScheduleID, "semantic model id": in.SemanticModelID, "generation id": in.GenerationID, "cron": in.Cron, "timezone": in.Timezone} {
		if err := canonicalID(label, value, 255); err != nil {
			return err
		}
	}
	if err := digest("artifact digest", in.ArtifactDigest); err != nil {
		return err
	}
	if in.ScheduleDigest == "" {
		in.ScheduleDigest = digestJSON(in)
	}
	if err := digest("schedule digest", in.ScheduleDigest); err != nil {
		return err
	}
	if in.ConcurrencyPolicy != "Forbid" && in.ConcurrencyPolicy != "Replace" {
		return errors.New("concurrency policy must be Forbid or Replace")
	}
	if in.StartingDeadline < 0 || in.StartingDeadline > 366*24*time.Hour {
		return errors.New("starting deadline is outside bound")
	}
	if in.NextRunAt.IsZero() {
		return errors.New("next run time is required")
	}
	return nil
}
func validateRun(in RunInput) error {
	for label, value := range map[string]string{"run id": in.RunID, "project id": in.ProjectID, "environment": in.Environment, "generation id": in.GenerationID, "pipeline id": in.PipelineID, "semantic model id": in.SemanticModelID, "target type": in.TargetType, "target id": in.TargetID, "trigger type": in.TriggerType, "invocation source": in.InvocationSource} {
		if err := canonicalID(label, value, 256); err != nil {
			return err
		}
	}
	if in.TargetType != "refresh_pipeline" && in.TargetType != "model_table" {
		return errors.New("unsupported target type")
	}
	if in.TriggerType != "manual" && in.TriggerType != "schedule" && in.TriggerType != "dependency" {
		return errors.New("unsupported trigger type")
	}
	if in.InvocationSource != "manual" && in.InvocationSource != "schedule" && in.InvocationSource != "external" && in.InvocationSource != "backfill" && in.InvocationSource != "dependency" {
		return errors.New("unsupported invocation source")
	}
	if err := digest("plan digest", in.PlanDigest); err != nil {
		return err
	}
	if err := digest("artifact digest", in.ArtifactDigest); err != nil {
		return err
	}
	if in.ParentRunID != "" {
		if err := canonicalID("parent run id", in.ParentRunID, 256); err != nil {
			return err
		}
	}
	if in.TargetRevision < 0 {
		return errors.New("target revision cannot be negative")
	}
	if in.TriggerType == "schedule" && in.NominalTime.IsZero() {
		return errors.New("scheduled nominal time is required")
	}
	if in.ConcurrencyPolicy == "" {
		if in.TriggerType == "schedule" {
			return errors.New("scheduled run concurrency policy is required")
		}
		// Manual/dependency invocations do not apply scheduled overlap policy;
		// retain a concrete value for the immutable run record.
		in.ConcurrencyPolicy = "Forbid"
	}
	if in.ConcurrencyPolicy != "Forbid" && in.ConcurrencyPolicy != "Replace" {
		return errors.New("unsupported concurrency policy")
	}
	if in.ScheduleRevisionID != "" {
		if err := canonicalID("schedule revision id", in.ScheduleRevisionID, 256); err != nil {
			return err
		}
	}
	if in.OccurrenceID != "" {
		if err := canonicalID("occurrence id", in.OccurrenceID, 256); err != nil {
			return err
		}
	}
	if in.JobID != "" {
		if err := canonicalID("job id", in.JobID, 256); err != nil {
			return err
		}
	}
	for _, id := range in.MatchingScheduleIDs {
		if err := canonicalID("schedule id", id, 255); err != nil {
			return err
		}
	}
	return nil
}

// PutScheduleTx records an immutable schedule revision. Repeating the exact
// revision is a replay; changing its digest closes the previous revision and
// creates a new one. Timestamps and revision boundaries are assigned by the
// database clock.
func (r *Repository) PutScheduleTx(ctx context.Context, tx Tx, in ScheduleInput) (Schedule, error) {
	ctx = contextOrBackground(ctx)
	if tx == nil {
		return Schedule{}, ErrInvalid
	}
	if in.ScheduleDigest == "" {
		in.ScheduleDigest = digestJSON(struct{ ProjectID, Environment, PipelineID, ScheduleID, Cron, Timezone string }{in.ProjectID, in.Environment, in.PipelineID, in.ScheduleID, in.Cron, in.Timezone})
	}
	if err := validateSchedule(in); err != nil {
		return Schedule{}, err
	}
	if in.ScheduleRevisionID == "" {
		in.ScheduleRevisionID = digestJSON(struct{ P, E, Pi, S, G, D string }{in.ProjectID, in.Environment, in.PipelineID, in.ScheduleID, in.GenerationID, in.ScheduleDigest})
	}
	var existing Schedule
	var err error
	row, queryErr := refreshdb.New(tx).GetScheduleRevisionForUpdate(ctx, refreshdb.GetScheduleRevisionForUpdateParams{ProjectID: in.ProjectID, Environment: in.Environment, PipelineID: in.PipelineID, ScheduleID: in.ScheduleID, GenerationID: in.GenerationID})
	if queryErr == nil {
		existing = scheduleForUpdate(row)
	}
	err = queryErr
	if err == nil {
		if existing.ScheduleDigest == in.ScheduleDigest && existing.GenerationID == in.GenerationID && existing.ArtifactDigest == in.ArtifactDigest && existing.Cron == in.Cron && existing.Timezone == in.Timezone && existing.SemanticModelID == in.SemanticModelID && existing.ConcurrencyPolicy == in.ConcurrencyPolicy {
			return existing, nil
		}
		if _, err = refreshdb.New(tx).CloseScheduleRevision(ctx, existing.ScheduleRevisionID); err != nil {
			return Schedule{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, err
	}
	_, err = refreshdb.New(tx).InsertScheduleRevision(ctx, refreshdb.InsertScheduleRevisionParams{ScheduleRevisionID: in.ScheduleRevisionID, ProjectID: in.ProjectID, Environment: in.Environment, PipelineID: in.PipelineID, ScheduleID: in.ScheduleID, SemanticModelID: in.SemanticModelID, GenerationID: in.GenerationID, ArtifactDigest: in.ArtifactDigest, Cron: in.Cron, Timezone: in.Timezone, StartingDeadline: in.StartingDeadline, ConcurrencyPolicy: in.ConcurrencyPolicy, ScheduleDigest: in.ScheduleDigest, NextRunAt: in.NextRunAt})
	if err != nil {
		return Schedule{}, err
	}
	out, err := r.scheduleByRevision(ctx, tx, in.ScheduleRevisionID)
	if err != nil {
		return Schedule{}, err
	}
	if !out.ClosedAt.IsZero() || !out.Enabled {
		return Schedule{}, ErrConflict
	}
	return out, nil
}

func (r *Repository) scheduleByRevision(ctx context.Context, db DBTX, id string) (Schedule, error) {
	row, err := refreshdb.New(db).GetScheduleRevision(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, err
	}
	return scheduleFrom(row), nil
}

func (r *Repository) PutSchedule(ctx context.Context, in ScheduleInput) (Schedule, error) {
	var out Schedule
	err := r.withTx(ctx, func(tx pgx.Tx) error { var e error; out, e = r.PutScheduleTx(ctx, tx, in); return e })
	return out, err
}
func (r *Repository) CreateScheduleTx(ctx context.Context, tx Tx, in ScheduleInput) (Schedule, error) {
	return r.PutScheduleTx(ctx, tx, in)
}
func (r *Repository) CreateSchedule(ctx context.Context, in ScheduleInput) (Schedule, error) {
	return r.PutSchedule(ctx, in)
}

// Schedule returns one immutable schedule revision by identity.
func (r *Repository) Schedule(ctx context.Context, revisionID string) (Schedule, error) {
	if err := r.requireDB(); err != nil {
		return Schedule{}, err
	}
	if err := canonicalID("schedule revision id", revisionID, 256); err != nil {
		return Schedule{}, err
	}
	return r.scheduleByRevision(contextOrBackground(ctx), r.db, revisionID)
}

// NextRun returns the earliest active schedule cursor for a pipeline in a
// serving scope. It is a read projection used by the module presentation
// surface and does not infer a generation from mutable browser state.
func (r *Repository) NextRun(ctx context.Context, scope Scope, pipelineID string) (time.Time, bool, error) {
	if err := r.requireDB(); err != nil {
		return time.Time{}, false, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return time.Time{}, false, err
	}
	if err := canonicalID("pipeline id", pipelineID, 255); err != nil {
		return time.Time{}, false, err
	}
	next, err := refreshdb.New(r.db).GetNextScheduleRun(contextOrBackground(ctx), refreshdb.GetNextScheduleRunParams{ProjectID: scope.ProjectID, Environment: scope.Environment, PipelineID: pipelineID, GenerationID: scope.GenerationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return next, err == nil, err
}
func (r *Repository) Reconcile(ctx context.Context, values []ScheduleInput) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.ReconcileSchedulesTx(ctx, tx, values) })
}

// ReconcileScope reconciles one immutable serving generation, including the
// zero-schedule case where every currently active revision in the scope must
// be closed. It is a convenience boundary for capability adapters; callers
// that already own a transaction should use ReconcileScopeTx instead.
func (r *Repository) ReconcileScope(ctx context.Context, scope Scope, generation string, values []ScheduleInput) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		return r.ReconcileScopeTx(ctx, tx, scope, generation, values)
	})
}

// CreateRunTreeWithSupersedeHook atomically admits a refresh root, all of its
// dependency provenance rows, the canonical queue enqueue/link callback, and
// (when supplied) the scheduler occurrence claim transition. Any child
// constraint or callback failure rolls back the entire tree and occurrence.
func (r *Repository) CreateRunTreeWithSupersedeHook(ctx context.Context, root RunInput, children []RunInput, occurrenceID, owner string, fence int64, hook func(context.Context, Tx, Run) (string, error), supersedeHook func(context.Context, Tx, []string) error) (Run, []Run, error) {
	var out Run
	var outChildren []Run
	err := r.InTx(ctx, func(tx Tx) error {
		var err error
		out, outChildren, err = r.createRunTreeTx(ctx, tx, root, children, occurrenceID, owner, fence, hook, supersedeHook)
		return err
	})
	if err != nil {
		return Run{}, nil, err
	}
	return out, outChildren, nil
}

// CreateRunTreeTx admits a refresh run tree through a caller-owned
// transaction. External capabilities such as the shared platform operation
// authority, audit, and events can therefore compose their writes atomically.
// This method never begins, commits, or rolls back tx.
func (r *Repository) CreateRunTreeTx(ctx context.Context, tx Tx, root RunInput, children []RunInput, occurrenceID, owner string, fence int64, hook func(context.Context, Tx, Run) (string, error), supersedeHook func(context.Context, Tx, []string) error) (Run, []Run, error) {
	return r.createRunTreeTx(ctx, tx, root, children, occurrenceID, owner, fence, hook, supersedeHook)
}

// createRunTreeTx admits the root, scheduler occurrence, canonical queue
// link, dependency children and supersession transition on a caller-owned
// transaction. It deliberately performs no transaction control so keyed
// operation admission can add audit/event evidence before committing.
func (r *Repository) createRunTreeTx(ctx context.Context, tx Tx, root RunInput, children []RunInput, occurrenceID, owner string, fence int64, hook func(context.Context, Tx, Run) (string, error), supersedeHook func(context.Context, Tx, []string) error) (Run, []Run, error) {
	if tx == nil {
		return Run{}, nil, ErrInvalid
	}
	out, err := r.CreateRunTxWithSupersedeHook(ctx, tx, root, supersedeHook)
	if err != nil {
		return Run{}, nil, err
	}
	if occurrenceID != "" {
		if err := r.attachClaimedOccurrenceTx(ctx, tx, occurrenceID, out.RunID, owner, fence, out); err != nil {
			return Run{}, nil, err
		}
	}
	if hook != nil {
		jobID, hookErr := hook(contextOrBackground(ctx), tx, out)
		if hookErr != nil {
			return Run{}, nil, hookErr
		}
		if jobID == "" {
			return Run{}, nil, errors.New("canonical queue hook returned an empty job id")
		}
		if err := r.AttachJobTx(ctx, tx, out.RunID, jobID); err != nil {
			return Run{}, nil, err
		}
	}
	outChildren := make([]Run, 0, len(children))
	for _, child := range children {
		if child.ParentRunID == "" {
			child.ParentRunID = out.RunID
		}
		if child.ParentRunID != out.RunID || child.JobID != "" {
			return Run{}, nil, ErrInvalid
		}
		created, childErr := r.CreateRunTx(ctx, tx, child)
		if childErr != nil {
			return Run{}, nil, childErr
		}
		outChildren = append(outChildren, created)
	}
	out, err = r.runByID(contextOrBackground(ctx), tx, out.RunID)
	if err != nil {
		return Run{}, nil, err
	}
	return out, outChildren, nil
}

func (r *Repository) attachClaimedOccurrenceTx(ctx context.Context, tx Tx, occurrenceID, runID, owner string, fence int64, root Run) error {
	if tx == nil || occurrenceID == "" || runID == "" || owner == "" || fence <= 0 {
		return ErrInvalid
	}
	if root.OccurrenceID != occurrenceID || root.ProjectID == "" || root.Environment == "" || root.GenerationID == "" || root.PipelineID == "" || root.NominalTime.IsZero() {
		return ErrConflict
	}
	tag, err := refreshdb.New(tx).QueueClaimedOccurrence(contextOrBackground(ctx), refreshdb.QueueClaimedOccurrenceParams{RunID: stringPtr(runID), OccurrenceID: occurrenceID, ProjectID: root.ProjectID, Environment: root.Environment, GenerationID: root.GenerationID, PipelineID: root.PipelineID, NominalTime: root.NominalTime, LeaseOwner: owner, FenceGeneration: fence})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	return nil
}

// transitionRunOccurrenceTx mirrors a root run's terminal/claim transition in
// its attached scheduler occurrence. Manual and dependency runs have no
// occurrence and are therefore a no-op. The immutable run_id link is always
// part of the update predicate.
func (r *Repository) transitionRunOccurrenceTx(ctx context.Context, tx Tx, runID, status string, outcome json.RawMessage) error {
	if tx == nil || runID == "" {
		return ErrInvalid
	}
	if status != "running" && status != "succeeded" && status != "failed" && status != "cancelled" && status != "superseded" {
		return ErrInvalid
	}
	occurrenceID, err := refreshdb.New(tx).GetRunOccurrence(contextOrBackground(ctx), runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if occurrenceID == "" {
		return nil
	}
	ev := []byte(`{}`)
	if len(outcome) > 0 {
		bounded, err := boundedObject(outcome, MaxJSONBytes)
		if err != nil {
			return err
		}
		ev = bounded
	}
	tag, err := refreshdb.New(tx).TransitionOccurrence(contextOrBackground(ctx), refreshdb.TransitionOccurrenceParams{Status: status, Outcome: ev, OccurrenceID: occurrenceID, RunID: stringPtr(runID)})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	return nil
}

// ReconcileOccurrenceTerminalTx repairs an occurrence whose run was already
// terminalized before the occurrence transition committed (for example after
// a process crash). It is idempotent for an already-terminal occurrence.
func (r *Repository) ReconcileOccurrenceTerminalTx(ctx context.Context, tx Tx, runID, status string, outcome json.RawMessage) error {
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, status, outcome); err != nil {
		if errors.Is(err, ErrStaleFence) {
			if current, scanErr := refreshdb.New(tx).GetOccurrenceStatus(contextOrBackground(ctx), runID); scanErr == nil {
				if current == status {
					return nil
				}
				if current == "succeeded" || current == "failed" || current == "cancelled" || current == "superseded" {
					return ErrConflict
				}
			}
		}
		return err
	}
	return nil
}
func (r *Repository) ReconcileScopeTx(ctx context.Context, tx Tx, scope Scope, generation string, values []ScheduleInput) error {
	if tx == nil || len(values) > MaxPageSize {
		return ErrInvalid
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return err
	}
	if err := canonicalID("generation id", generation, 255); err != nil {
		return err
	}
	for _, in := range values {
		if in.ProjectID != scope.ProjectID || in.Environment != scope.Environment || in.GenerationID != generation {
			return ErrConflict
		}
		if _, err := r.PutScheduleTx(ctx, tx, in); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(values))
	pipelines := make([]string, 0, len(values))
	for _, in := range values {
		ids = append(ids, in.ScheduleID)
		pipelines = append(pipelines, in.PipelineID)
	}
	_, err := refreshdb.New(tx).CloseOmittedSchedules(contextOrBackground(ctx), refreshdb.CloseOmittedSchedulesParams{ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: generation, Pipelines: pipelines, ScheduleIds: ids})
	return err
}

// ReconcileSchedulesTx closes active revisions omitted from desired and puts
// each desired schedule. It is intentionally bounded by MaxPageSize.
func (r *Repository) ReconcileSchedulesTx(ctx context.Context, tx Tx, values []ScheduleInput) error {
	if len(values) > MaxPageSize {
		return fmt.Errorf("schedule reconciliation exceeds %d rows", MaxPageSize)
	}
	if tx == nil {
		return ErrInvalid
	}
	// Reconciliation is generation-scoped. Omitted active revisions in the
	// same project/environment/generation are closed; another generation is
	// never touched by a refresh deployment.
	var project, environment, generation string
	if len(values) > 0 {
		project, environment, generation = values[0].ProjectID, values[0].Environment, values[0].GenerationID
		if err := validateScope(project, environment); err != nil {
			return err
		}
	}
	for _, in := range values {
		if project == "" {
			project, environment, generation = in.ProjectID, in.Environment, in.GenerationID
		}
		if in.ProjectID != project || in.Environment != environment || in.GenerationID != generation {
			return ErrConflict
		}
		if _, err := r.PutScheduleTx(ctx, tx, in); err != nil {
			return err
		}
	}
	if project != "" {
		ids := make([]string, 0, len(values))
		pipelines := make([]string, 0, len(values))
		for _, in := range values {
			ids = append(ids, in.ScheduleID)
			pipelines = append(pipelines, in.PipelineID)
		}
		if _, err := refreshdb.New(tx).CloseOmittedSchedules(contextOrBackground(ctx), refreshdb.CloseOmittedSchedulesParams{ProjectID: project, Environment: environment, GenerationID: generation, Pipelines: pipelines, ScheduleIds: ids}); err != nil {
			return err
		}
	}
	return nil
}

// ClaimDue claims due schedule occurrences atomically for one explicit serving
// scope and scheduler lease owner.
func (r *Repository) ClaimDue(ctx context.Context, scope Scope, now time.Time, owner string, lease time.Duration, limit int) ([]Occurrence, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	var out []Occurrence
	var err error
	err = r.withTx(ctx, func(tx pgx.Tx) error { out, err = r.claimDueTx(ctx, tx, scope, now, owner, lease, limit); return err })
	return out, err
}
func (r *Repository) ClaimDueTx(ctx context.Context, tx Tx, scope Scope, now time.Time, owner string, lease time.Duration, limit int) ([]Occurrence, error) {
	return r.claimDueTx(ctx, tx, scope, now, owner, lease, limit)
}

func (r *Repository) claimDueTx(ctx context.Context, tx Tx, scope Scope, now time.Time, owner string, lease time.Duration, limit int) ([]Occurrence, error) {
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if err := validateGeneration(scope.GenerationID); err != nil {
		return nil, err
	}
	if err := canonicalID("owner", owner, 256); err != nil {
		return nil, err
	}
	if lease <= 0 || lease > MaxLease || limit < 1 || limit > MaxPageSize {
		return nil, errors.New("claim lease or limit is outside bound")
	}
	ctx = contextOrBackground(ctx)
	dbNow, err := refreshdb.New(tx).DatabaseClock(ctx)
	if err != nil {
		return nil, err
	}
	if now.IsZero() || now.After(dbNow) {
		now = dbNow
	}
	// Recover only abandoned claims selected under row locks. This statement is
	// bounded and safe when multiple schedulers run concurrently.
	if _, err := refreshdb.New(tx).RecoverStaleOccurrences(ctx, refreshdb.RecoverStaleOccurrencesParams{ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID, PageLimit: int32(limit)}); err != nil {
		return nil, err
	}
	rows, err := refreshdb.New(tx).ListDueSchedules(ctx, refreshdb.ListDueSchedulesParams{ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID, NextRunAt: now, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	type occurrenceGroup struct {
		schedule       Schedule
		ids, revisions []string
		retry          bool
		deadline       time.Duration
		scheduledAt    time.Time
	}
	groups := make(map[string]*occurrenceGroup)
	order := make([]string, 0, limit)
	dueSchedules := make([]Schedule, 0, limit)
	for _, row := range rows {
		dueSchedules = append(dueSchedules, scheduleFromDue(row))
	}
	for _, s := range dueSchedules {
		parsed, err := refreshschedule.ParseSchedule(s.Cron, s.Timezone)
		if err != nil {
			return nil, err
		}
		scheduledAt := s.NextRunAt
		retry := false
		ids := []string{s.ScheduleID}
		stored, lookupErr := occurrenceByNominal(ctx, tx, scope, s.PipelineID, scheduledAt)
		if lookupErr == nil && stored.Status == "pending" && stored.GenerationID == s.GenerationID {
			retry = true
			ids = stored.MatchingScheduleIDs
			if len(ids) == 0 {
				ids = []string{s.ScheduleID}
			}
		} else if lookupErr != nil && !errors.Is(lookupErr, ErrNotFound) {
			return nil, lookupErr
		}
		next := parsed.Next(scheduledAt)
		if retry {
			next = parsed.Next(now)
		} else {
			for !next.IsZero() && !next.After(now) {
				scheduledAt = next
				next = parsed.Next(next)
			}
		}
		if next.IsZero() {
			return nil, fmt.Errorf("schedule %q has no next occurrence", s.ScheduleID)
		}
		if _, err := refreshdb.New(tx).AdvanceScheduleCursor(ctx, refreshdb.AdvanceScheduleCursorParams{NextRunAt: next, ScheduleRevisionID: s.ScheduleRevisionID, GenerationID: s.GenerationID}); err != nil {
			return nil, err
		}
		// Keep coalescing pipeline-wide within one captured generation. When a
		// caller intentionally scans all generations, distinct generation groups
		// still converge on the one logical occurrence key at insertion, while
		// preserving the evidence of whichever generation won the claim.
		key := s.PipelineID + "\x00" + s.GenerationID + "\x00" + scheduledAt.UTC().Format(time.RFC3339Nano)
		g := groups[key]
		if g == nil {
			g = &occurrenceGroup{schedule: s, scheduledAt: scheduledAt, deadline: s.StartingDeadline}
			groups[key] = g
			order = append(order, key)
		}
		g.ids = append(g.ids, ids...)
		g.revisions = append(g.revisions, s.ScheduleRevisionID)
		g.retry = g.retry || retry
		if d := s.StartingDeadline; g.deadline > 0 && d > 0 && d < g.deadline {
			g.deadline = d
		}
	}
	type claimGroup struct {
		key   string
		group *occurrenceGroup
	}
	ordered := make([]claimGroup, 0, len(order))
	for _, k := range order {
		ordered = append(ordered, claimGroup{key: k, group: groups[k]})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].group, ordered[j].group
		if !a.scheduledAt.Equal(b.scheduledAt) {
			return a.scheduledAt.Before(b.scheduledAt)
		}
		ai, bi := append([]string{}, a.ids...), append([]string{}, b.ids...)
		sort.Strings(ai)
		sort.Strings(bi)
		return strings.Join(ai, "\x00") < strings.Join(bi, "\x00")
	})
	out := make([]Occurrence, 0, len(ordered))
	for _, item := range ordered {
		g, s := item.group, item.group.schedule
		g.ids = sortedUniqueStrings(g.ids)
		occID := occurrenceID(scope, s.PipelineID, g.scheduledAt)
		ids, _ := json.Marshal(g.ids)
		if !g.retry && !withinDeadline(now, g.scheduledAt, g.deadline) {
			if _, e := refreshdb.New(tx).InsertPendingOccurrence(ctx, refreshdb.InsertPendingOccurrenceParams{OccurrenceID: occID, ProjectID: scope.ProjectID, Environment: scope.Environment, PipelineID: s.PipelineID, NominalTime: g.scheduledAt, ScheduleRevisionID: s.ScheduleRevisionID, MatchingScheduleIds: ids, SemanticModelID: s.SemanticModelID, GenerationID: s.GenerationID, ArtifactDigest: s.ArtifactDigest}); e != nil {
				return nil, e
			}
			if _, e := refreshdb.New(tx).SkipOccurrence(ctx, occID); e != nil {
				return nil, e
			}
			continue
		}
		if _, e := refreshdb.New(tx).InsertOccurrence(ctx, refreshdb.InsertOccurrenceParams{OccurrenceID: occID, ProjectID: scope.ProjectID, Environment: scope.Environment, PipelineID: s.PipelineID, NominalTime: g.scheduledAt, ScheduleRevisionID: s.ScheduleRevisionID, MatchingScheduleIds: ids, SemanticModelID: s.SemanticModelID, GenerationID: s.GenerationID, ArtifactDigest: s.ArtifactDigest}); e != nil {
			return nil, e
		}
		_, e := refreshdb.New(tx).ClaimOccurrence(ctx, refreshdb.ClaimOccurrenceParams{OccurrenceID: occID, LeaseOwner: owner, Lease: lease, GenerationID: s.GenerationID})
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		o, e := occurrenceByID(ctx, tx, occID)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, nil
}

func occurrenceByNominal(ctx context.Context, db DBTX, scope Scope, pipeline string, nominal time.Time) (Occurrence, error) {
	id, err := refreshdb.New(db).GetOccurrenceByNominal(ctx, refreshdb.GetOccurrenceByNominalParams{ProjectID: scope.ProjectID, Environment: scope.Environment, PipelineID: pipeline, NominalTime: nominal})
	if errors.Is(err, pgx.ErrNoRows) {
		return Occurrence{}, ErrNotFound
	}
	if err != nil {
		return Occurrence{}, err
	}
	return occurrenceByID(ctx, db, id)
}
func withinDeadline(now, scheduled time.Time, d time.Duration) bool {
	late := now.Sub(scheduled)
	if late <= 0 {
		return true
	}
	// Argo's startingDeadlineSeconds=0 means that a missed nominal instant is
	// advanced past without a recovery execution. A nominal instant that is
	// exactly due (late == 0) still passes the check above.
	if d == 0 {
		return false
	}
	return late <= d
}
func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string{}, values...)
	sort.Strings(out)
	n := 1
	for i := 1; i < len(out); i++ {
		if out[i] != out[n-1] {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}

func occurrenceID(scope Scope, pipeline string, nominal time.Time) string {
	h := sha256.Sum256([]byte(scope.ProjectID + "\x00" + scope.Environment + "\x00" + pipeline + "\x00" + nominal.UTC().Format(time.RFC3339Nano)))
	return "occurrence-" + hex.EncodeToString(h[:])
}

func occurrenceByID(ctx context.Context, db DBTX, id string) (Occurrence, error) {
	row, err := refreshdb.New(db).GetOccurrence(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Occurrence{}, ErrNotFound
	}
	if err != nil {
		return Occurrence{}, err
	}
	return occurrenceFrom(row), nil
}

func (r *Repository) Occurrence(ctx context.Context, id string) (Occurrence, error) {
	if err := r.requireDB(); err != nil {
		return Occurrence{}, err
	}
	return occurrenceByID(contextOrBackground(ctx), r.db, id)
}
func (r *Repository) GetOccurrence(ctx context.Context, id string) (Occurrence, error) {
	return r.Occurrence(ctx, id)
}

// ReleaseOccurrence returns a claimed occurrence to pending only when the
// exact lease owner/fence still holds it. A stale worker cannot release a
// successor claim.
func (r *Repository) ReleaseOccurrence(ctx context.Context, o Occurrence) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.ReleaseOccurrenceTx(ctx, tx, o) })
}
func (r *Repository) ReleaseOccurrenceTx(ctx context.Context, tx Tx, o Occurrence) error {
	if tx == nil {
		return ErrInvalid
	}
	if o.OccurrenceID == "" || o.LeaseOwner == "" || o.FenceGeneration <= 0 {
		return ErrInvalid
	}
	tag, err := refreshdb.New(tx).ReleaseOccurrenceClaim(contextOrBackground(ctx), refreshdb.ReleaseOccurrenceClaimParams{OccurrenceID: o.OccurrenceID, LeaseOwner: o.LeaseOwner, FenceGeneration: o.FenceGeneration})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	// Requeue every schedule revision that contributed evidence so the next
	// dispatcher tick retries this nominal instant before later catch-up work.
	if _, err := refreshdb.New(tx).RequeueScheduleRevision(contextOrBackground(ctx), refreshdb.RequeueScheduleRevisionParams{NextRunAt: o.NominalTime, ProjectID: o.ProjectID, Environment: o.Environment, PipelineID: o.PipelineID, OccurrenceID: o.OccurrenceID}); err != nil {
		return err
	}
	return nil
}

// CreateRunTx stores the immutable execution identity. A duplicate run ID is
// an exact replay only when all identity fields (including request-linked
// operation and plan/artifact digests) match.
func (r *Repository) CreateRunTx(ctx context.Context, tx Tx, in RunInput) (Run, error) {
	return r.CreateRunTxWithSupersedeHook(ctx, tx, in, nil)
}

// CreateRunTxWithSupersedeHook is the scheduler admission transaction with an
// optional jobs-capability callback. Scheduled Replace closes prior refresh
// trees and invokes the callback with their linked job ids before inserting
// the replacement, keeping both capabilities atomic without cross-schema SQL.
func (r *Repository) CreateRunTxWithSupersedeHook(ctx context.Context, tx Tx, in RunInput, supersedeHook func(context.Context, Tx, []string) error) (Run, error) {
	ctx = contextOrBackground(ctx)
	if tx == nil {
		return Run{}, ErrInvalid
	}
	if in.RunID == "" {
		id, err := newID("")
		if err != nil {
			return Run{}, err
		}
		in.RunID = id
	}
	if in.ConcurrencyPolicy == "" && in.TriggerType != "schedule" {
		in.ConcurrencyPolicy = "Forbid"
	}
	if err := validateRun(in); err != nil {
		return Run{}, err
	}
	matchingIDs := in.MatchingScheduleIDs
	if matchingIDs == nil {
		matchingIDs = []string{}
	}
	materialization := in.MaterializationScope
	if materialization == nil {
		materialization = []string{}
	}
	matching, _ := json.Marshal(matchingIDs)
	material, _ := json.Marshal(materialization)
	if in.OperationID != "" {
		if err := canonicalID("operation id", in.OperationID, 256); err != nil {
			return Run{}, err
		}
		operationID, parseErr := uuid.Parse(in.OperationID)
		if parseErr != nil || operationID.String() != in.OperationID || operationID.Version() != 7 {
			return Run{}, errors.New("operation id must be a canonical UUIDv7")
		}
	}
	// The active-row SELECT below cannot lock a missing row. Serialize all
	// admissions for the immutable project/environment/target key so two
	// concurrent inserts cannot both pass the overlap fence.
	lockKey := fmt.Sprintf("%d:%s|%d:%s|%d:%s|%d:%s", len(in.ProjectID), in.ProjectID, len(in.Environment), in.Environment, len(in.TargetType), in.TargetType, len(in.TargetID), in.TargetID)
	if _, err := refreshdb.New(tx).AdvisoryLock(ctx, lockKey); err != nil {
		return Run{}, err
	}
	// Scheduled overlap policy is scoped strictly to scheduled invocations.
	// Manual/backfill/dependency admission always conflicts with any active
	// invocation and must never be superseded by Replace.
	rows, err := refreshdb.New(tx).ListActiveRuns(ctx, refreshdb.ListActiveRunsParams{ProjectID: in.ProjectID, Environment: in.Environment, TargetType: in.TargetType, TargetID: in.TargetID, RunID: in.RunID})
	if err != nil {
		return Run{}, err
	}
	var active, externalActive bool
	supersededJobIDs := make([]string, 0)
	for _, row := range rows {
		activeTrigger, jobID := row.TriggerType, row.JobID
		active = true
		if jobID != "" {
			supersededJobIDs = append(supersededJobIDs, jobID)
		}
		if activeTrigger != "schedule" {
			externalActive = true
		}
	}
	if active {
		if in.TriggerType != "schedule" || in.ConcurrencyPolicy == "Forbid" || externalActive {
			return Run{}, ErrConflict
		}
		if len(supersededJobIDs) > 0 && supersedeHook == nil {
			return Run{}, errors.New("scheduled replacement requires canonical jobs supersession hook")
		}
	}
	if in.TriggerType == "schedule" && in.ConcurrencyPolicy == "Replace" {
		if _, e := refreshdb.New(tx).FailSupersededAttempts(ctx, refreshdb.FailSupersededAttemptsParams{ProjectID: in.ProjectID, Environment: in.Environment, TargetType: in.TargetType, TargetID: in.TargetID, RunID: in.RunID}); e != nil {
			return Run{}, e
		}
		if _, e := refreshdb.New(tx).SupersedeRuns(ctx, refreshdb.SupersedeRunsParams{ProjectID: in.ProjectID, Environment: in.Environment, TargetType: in.TargetType, TargetID: in.TargetID, RunID: in.RunID}); e != nil {
			return Run{}, e
		}
		if _, e := refreshdb.New(tx).SupersedeOccurrences(ctx, refreshdb.SupersedeOccurrencesParams{ProjectID: in.ProjectID, Environment: in.Environment, TargetType: in.TargetType, TargetID: in.TargetID, RunID: in.RunID}); e != nil {
			return Run{}, e
		}
		if supersedeHook != nil {
			if err := supersedeHook(contextOrBackground(ctx), tx, supersededJobIDs); err != nil {
				return Run{}, err
			}
		}
	}
	_, err = refreshdb.New(tx).InsertRun(ctx, refreshdb.InsertRunParams{RunID: in.RunID, OperationID: in.OperationID, ProjectID: in.ProjectID, Environment: in.Environment, GenerationID: in.GenerationID, ParentRunID: in.ParentRunID, PipelineID: in.PipelineID, SemanticModelID: in.SemanticModelID, TargetType: in.TargetType, TargetID: in.TargetID, TargetRevision: in.TargetRevision, TriggerType: in.TriggerType, InvocationSource: in.InvocationSource, TriggerID: in.TriggerID, ConcurrencyPolicy: in.ConcurrencyPolicy, ScheduleRevisionID: in.ScheduleRevisionID, OccurrenceID: in.OccurrenceID, NominalTime: nullableTime(in.NominalTime), PlanDigest: in.PlanDigest, ArtifactDigest: in.ArtifactDigest, MatchingScheduleIds: matching, MaterializationScope: material, PrincipalID: in.PrincipalID, JobID: in.JobID})
	if err != nil {
		return Run{}, err
	}
	out, err := r.runByID(ctx, tx, in.RunID)
	if err != nil {
		return Run{}, err
	}
	if !sameRunIdentity(out, in) {
		return Run{}, ErrConflict
	}
	return out, nil
}
func nullableTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t
}
func sameRunIdentity(r Run, in RunInput) bool {
	if r.RunID != in.RunID || r.OperationID != in.OperationID || r.ProjectID != in.ProjectID || r.Environment != in.Environment || r.GenerationID != in.GenerationID || r.ParentRunID != in.ParentRunID || r.PipelineID != in.PipelineID || r.SemanticModelID != in.SemanticModelID || r.TargetType != in.TargetType || r.TargetID != in.TargetID || r.PlanDigest != in.PlanDigest || r.ArtifactDigest != in.ArtifactDigest || r.TriggerType != in.TriggerType || r.InvocationSource != in.InvocationSource || r.TriggerID != in.TriggerID || r.ConcurrencyPolicy != in.ConcurrencyPolicy || r.ScheduleRevisionID != in.ScheduleRevisionID || r.OccurrenceID != in.OccurrenceID || r.PrincipalID != in.PrincipalID || (in.JobID != "" && r.JobID != in.JobID) || r.TargetRevision != in.TargetRevision || !r.NominalTime.Equal(in.NominalTime) {
		return false
	}
	return slicesEqual(r.MatchingScheduleIDs, in.MatchingScheduleIDs) && slicesEqual(r.MaterializationScope, in.MaterializationScope)
}
func slicesEqual(a, b []string) bool {
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
func (r *Repository) CreateRun(ctx context.Context, in RunInput) (Run, error) {
	var out Run
	err := r.withTx(ctx, func(tx pgx.Tx) error { var e error; out, e = r.CreateRunTx(ctx, tx, in); return e })
	return out, err
}

// CreateRunWithHook composes run insertion with an external durable job
// enqueue/link operation in one caller-owned transaction. The hook must use
// the provided transaction and return the canonical job id; an empty id means
// that no queue integration was requested.
func (r *Repository) CreateRunWithHook(ctx context.Context, in RunInput, hook func(context.Context, Tx, Run) (string, error)) (Run, error) {
	return r.CreateRunWithSupersedeHook(ctx, in, hook, nil)
}

// CreateRunWithSupersedeHook extends CreateRunWithHook with a callback that
// terminalizes jobs linked to scheduled runs superseded by Replace.
func (r *Repository) CreateRunWithSupersedeHook(ctx context.Context, in RunInput, hook func(context.Context, Tx, Run) (string, error), supersedeHook func(context.Context, Tx, []string) error) (Run, error) {
	var out Run
	err := r.InTx(ctx, func(tx Tx) error {
		var err error
		out, err = r.CreateRunTxWithSupersedeHook(ctx, tx, in, supersedeHook)
		if err != nil {
			return err
		}
		if hook != nil {
			jobID, hookErr := hook(contextOrBackground(ctx), tx, out)
			if hookErr != nil {
				return hookErr
			}
			if jobID != "" {
				if err := r.AttachJobTx(ctx, tx, out.RunID, jobID); err != nil {
					return err
				}
				out, err = r.runByID(contextOrBackground(ctx), tx, out.RunID)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out, err
}

// AttachJobTx links an already-enqueued platform job to its run. This method
// never reads or writes the jobs capability; the caller-owned queue adapter
// proves enqueue success before handing the opaque canonical id here.
func (r *Repository) AttachJobTx(ctx context.Context, tx Tx, runID, jobID string) error {
	if tx == nil {
		return ErrInvalid
	}
	if err := canonicalID("run id", runID, 256); err != nil {
		return err
	}
	if err := canonicalID("job id", jobID, 256); err != nil {
		return err
	}
	existing, err := refreshdb.New(tx).GetRunJobForUpdate(contextOrBackground(ctx), runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if existing == jobID {
		// Exact replay is a true no-op. In particular, do not fire the deferred
		// root-job trigger again after the queue worker has advanced the job.
		return nil
	}
	if existing != "" {
		return ErrConflict
	}
	tag, err := refreshdb.New(tx).AttachRunJob(contextOrBackground(ctx), refreshdb.AttachRunJobParams{JobID: stringPtr(jobID), RunID: runID})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) runByID(ctx context.Context, db DBTX, id string) (Run, error) {
	row, err := refreshdb.New(db).GetRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	return runFrom(row), nil
}
func (r *Repository) GetRun(ctx context.Context, scope Scope, id string) (Run, error) {
	if err := r.requireDB(); err != nil {
		return Run{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return Run{}, err
	}
	out, err := r.runByID(contextOrBackground(ctx), r.db, id)
	if err != nil {
		return Run{}, err
	}
	if out.ProjectID != scope.ProjectID || out.Environment != scope.Environment {
		return Run{}, ErrNotFound
	}
	if scope.GenerationID != "" && out.GenerationID != scope.GenerationID {
		return Run{}, ErrNotFound
	}
	return out, nil
}

// LookupRun returns a run by durable identity without a generation filter.
// Queue adapters use this narrow projection to join the canonical jobs row
// back to its refresh run before applying the caller's stable project and
// environment scope.  It intentionally does not infer or mutate serving
// identity.
func (r *Repository) LookupRun(ctx context.Context, id string) (Run, error) {
	if err := r.requireDB(); err != nil {
		return Run{}, err
	}
	return r.runByID(contextOrBackground(ctx), r.db, id)
}

// GetRunTx reads a run through a caller-owned transaction while retaining the
// same scope fence as GetRun.
func (r *Repository) GetRunTx(ctx context.Context, tx Tx, scope Scope, id string) (Run, error) {
	if tx == nil {
		return Run{}, ErrInvalid
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return Run{}, err
	}
	out, err := r.runByID(contextOrBackground(ctx), tx, id)
	if err != nil {
		return Run{}, err
	}
	if out.ProjectID != scope.ProjectID || out.Environment != scope.Environment || (scope.GenerationID != "" && out.GenerationID != scope.GenerationID) {
		return Run{}, ErrNotFound
	}
	return out, nil
}

// LookupRunTx reads a run by id through a caller-owned transaction without a
// generation filter. Startup reconciliation uses the jobs resource id to
// discover terminal refresh rows while retaining the same snapshot.
func (r *Repository) LookupRunTx(ctx context.Context, tx Tx, id string) (Run, error) {
	if tx == nil || id == "" {
		return Run{}, ErrInvalid
	}
	return r.runByID(contextOrBackground(ctx), tx, id)
}

func (r *Repository) ListRuns(ctx context.Context, scope Scope, limit int, after string) ([]Run, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, errors.New("run page limit is outside bound")
	}
	if err := validateGeneration(scope.GenerationID); err != nil {
		return nil, err
	}
	ids, err := refreshdb.New(r.db).ListRunsPage(contextOrBackground(ctx), refreshdb.ListRunsPageParams{ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID, After: after, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, id := range ids {
		v, e := r.runByID(contextOrBackground(ctx), r.db, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// ListRunsFiltered is the projection used by module adapters. Filters are
// applied in SQL before pagination so latest/read surfaces never infer state
// from a truncated unrelated page.
func (r *Repository) ListRunsFiltered(ctx context.Context, scope Scope, targetType, targetID, semanticModelID string, successful bool, limit int, after string) ([]Run, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if err := validateGeneration(scope.GenerationID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	if targetType != "" {
		if err := canonicalID("target type", targetType, 64); err != nil {
			return nil, err
		}
	}
	if targetID != "" {
		if err := canonicalID("target id", targetID, 256); err != nil {
			return nil, err
		}
	}
	if semanticModelID != "" {
		if err := canonicalID("semantic model id", semanticModelID, 256); err != nil {
			return nil, err
		}
	}
	ids, err := refreshdb.New(r.db).ListRunsFilteredPage(contextOrBackground(ctx), refreshdb.ListRunsFilteredPageParams{ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID, TargetType: targetType, TargetID: targetID, SemanticModelID: semanticModelID, Successful: successful, After: after, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, limit)
	for _, id := range ids {
		v, err := r.runByID(contextOrBackground(ctx), r.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *Repository) ListChildRuns(ctx context.Context, scope Scope, parentRunID string, limit int) ([]Run, error) {
	if err := canonicalID("parent run id", parentRunID, 256); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	ids, err := refreshdb.New(r.db).ListChildRuns(contextOrBackground(ctx), refreshdb.ListChildRunsParams{ProjectID: scope.ProjectID, Environment: scope.Environment, ParentRunID: stringPtr(parentRunID), PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, limit)
	for _, id := range ids {
		run, err := r.GetRun(ctx, Scope{ProjectID: scope.ProjectID, Environment: scope.Environment}, id)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// SupersedeRunTree terminally fences a root and all of its dependency runs.
func (r *Repository) SupersedeRunTree(ctx context.Context, runID, owner string, fence int64, message string) error {
	if runID == "" || owner == "" || fence <= 0 {
		return ErrInvalid
	}
	return r.InTx(ctx, func(tx Tx) error {
		_, err := r.SupersedeRunTreeTx(ctx, tx, runID, owner, fence, message)
		return err
	})
}

// SupersedeRunTreeTx closes the exact worker-owned refresh root and all
// descendants through a caller-owned transaction. It returns linked canonical
// job ids so the jobs capability can terminalize them in the same transaction.
// No jobs tables are referenced here.
func (r *Repository) SupersedeRunTreeTx(ctx context.Context, tx Tx, runID, owner string, fence int64, message string) ([]string, error) {
	if tx == nil || runID == "" || owner == "" || fence <= 0 {
		return nil, ErrInvalid
	}
	if _, err := refreshdb.New(tx).GetLiveRunForUpdate(contextOrBackground(ctx), refreshdb.GetLiveRunForUpdateParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrStaleFence
		}
		return nil, err
	}
	rows, err := refreshdb.New(tx).ListRunTreeJobs(contextOrBackground(ctx), runID)
	if err != nil {
		return nil, err
	}
	jobIDs := make([]string, 0, 1)
	for _, id := range rows {
		if id != nil {
			jobIDs = append(jobIDs, *id)
		}
	}
	// Attempt evidence must close while each run still carries its live owner
	// and fence; the run guard then permits the terminal supersession.
	if _, err := refreshdb.New(tx).FailSupersededTreeAttempts(contextOrBackground(ctx), refreshdb.FailSupersededTreeAttemptsParams{Message: message, RunID: runID}); err != nil {
		return nil, err
	}
	if _, err := refreshdb.New(tx).SupersedeRunTree(contextOrBackground(ctx), refreshdb.SupersedeRunTreeParams{Message: message, RunID: runID}); err != nil {
		return nil, err
	}
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "superseded", json.RawMessage(`{"code":"REFRESH_SUPERSEDED"}`)); err != nil {
		return nil, err
	}
	return jobIDs, nil
}

// FailRunTree terminally fails a live root and every queued/running/prepared
// dependency run in the same immutable parent tree. The root transition is
// fenced by owner, generation and live lease; child attempts are closed before
// their run rows so the attempt guard observes the still-live child lease.
func (r *Repository) FailRunTree(ctx context.Context, runID, owner string, fence int64, message string, evidence json.RawMessage) error {
	if runID == "" || owner == "" || fence <= 0 {
		return ErrInvalid
	}
	ev, err := boundedObject(evidence, MaxJSONBytes)
	if err != nil {
		return err
	}
	return r.InTx(ctx, func(tx Tx) error { return r.FailRunTreeTx(ctx, tx, runID, owner, fence, message, ev) })
}

// FailRunTreeTx is the caller-owned transaction form of FailRunTree.
func (r *Repository) FailRunTreeTx(ctx context.Context, tx Tx, runID, owner string, fence int64, message string, evidence json.RawMessage) error {
	if tx == nil {
		return ErrInvalid
	}
	ev, err := boundedObject(evidence, MaxJSONBytes)
	if err != nil {
		return err
	}
	{
		if _, err := refreshdb.New(tx).GetLiveRunForUpdate(contextOrBackground(ctx), refreshdb.GetLiveRunForUpdateParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStaleFence
			}
			return err
		}
		if err := r.FailAttemptTx(ctx, tx, runID, owner, fence, message, ev); err != nil {
			return err
		}
		if _, err := refreshdb.New(tx).FailChildAttempts(contextOrBackground(ctx), refreshdb.FailChildAttemptsParams{Evidence: ev, Error: message, RunID: runID}); err != nil {
			return err
		}
		if _, err := refreshdb.New(tx).FailChildRuns(contextOrBackground(ctx), refreshdb.FailChildRunsParams{RunID: runID, Error: message}); err != nil {
			return err
		}
		return nil
	}
}

// CompleteRunTreeTx closes a root attempt and every active dependency run in
// one caller-owned transaction. Dependency rows are logical provenance (the
// dispatcher executes only the root platform job), so queued child rows are
// completed here rather than left as stranded runnable records.
func (r *Repository) CompleteRunTreeTx(ctx context.Context, tx Tx, runID, owner string, fence int64, evidence json.RawMessage) error {
	if tx == nil || runID == "" || owner == "" || fence <= 0 {
		return ErrInvalid
	}
	ev, err := boundedObject(evidence, MaxJSONBytes)
	if err != nil {
		return err
	}
	if _, err := refreshdb.New(tx).GetLiveRunForUpdate(contextOrBackground(ctx), refreshdb.GetLiveRunForUpdateParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleFence
		}
		return err
	}
	if err := r.CompleteAttemptTx(ctx, tx, runID, owner, fence, ev); err != nil {
		return err
	}
	if _, err := refreshdb.New(tx).CompleteChildAttempts(contextOrBackground(ctx), refreshdb.CompleteChildAttemptsParams{Evidence: ev, RunID: runID}); err != nil {
		return err
	}
	if _, err := refreshdb.New(tx).CompleteChildRuns(contextOrBackground(ctx), runID); err != nil {
		return err
	}
	return nil
}

// QuarantineQueuedRunTx atomically terminalizes a malformed queued run tree.
// The root is scoped to the opaque queue job link; dependency children remain
// jobless but are closed with the same poison evidence in this transaction.
func (r *Repository) QuarantineQueuedRunTx(ctx context.Context, tx Tx, runID, jobID string) (bool, error) {
	if tx == nil || runID == "" || jobID == "" {
		return false, ErrInvalid
	}
	const message = "refresh job payload rejected"
	poison, err := refreshdb.New(tx).GetRunPoisonState(contextOrBackground(ctx), runID)
	var status, existingJob string
	status, existingJob = poison.Status, poison.JobID
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	if existingJob != jobID {
		return false, ErrConflict
	}
	// A terminal replay is valid only when every row in the run tree carries
	// the exact poison message. Looking at the root alone would bless a partial
	// or tampered child tree after a crash.
	counts, err := refreshdb.New(tx).GetRunTreePoisonCounts(contextOrBackground(ctx), refreshdb.GetRunTreePoisonCountsParams{RunID: runID, Error: message})
	if err != nil {
		return false, err
	}
	var total, poisoned, queued int64
	total, poisoned, queued = counts.Total, counts.Poisoned, counts.Queued
	if total == 0 {
		return false, ErrNotFound
	}
	if status != "queued" {
		if total == poisoned {
			return true, nil
		}
		// A live owner may have won the race; let the caller treat this as a
		// no-op.  Terminal contradictions, however, must not be replayed.
		if status == "running" || status == "prepared" {
			return false, nil
		}
		return false, ErrConflict
	}
	if queued != total {
		return false, ErrConflict
	}
	tag, err := refreshdb.New(tx).FailQueuedTree(contextOrBackground(ctx), refreshdb.FailQueuedTreeParams{Error: message, RunID: runID})
	if err != nil {
		return false, err
	}
	if tag != total {
		return false, ErrStaleFence
	}
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "failed", json.RawMessage(`{"code":"REFRESH_POISON_PAYLOAD"}`)); err != nil {
		return false, err
	}
	return true, nil
}

// FailQueuedRunTreeTx terminalizes a queued root and any queued dependency
// provenance rows. It is used when a claimed platform job cannot be joined to
// a valid refresh payload; no refresh attempt exists yet, so the transition
// is intentionally limited to queued rows.
func (r *Repository) FailQueuedRunTreeTx(ctx context.Context, tx Tx, runID, message string) error {
	if tx == nil || runID == "" {
		return ErrInvalid
	}
	if strings.TrimSpace(message) == "" || len(message) > 256 {
		return ErrInvalid
	}
	tag, err := refreshdb.New(tx).FailQueuedTree(contextOrBackground(ctx), refreshdb.FailQueuedTreeParams{Error: message, RunID: runID})
	if err != nil {
		return err
	}
	if tag == 0 {
		return ErrStaleFence
	}
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "failed", json.RawMessage(`{"code":"REFRESH_FAILED"}`)); err != nil {
		return err
	}
	return nil
}

// FailRunTerminalEvidenceTx closes a run tree when its linked canonical job
// is demonstrably terminal or missing. The job evidence is the authority for
// this repair, so unlike worker transitions it does not rely on an old lease
// remaining live at process startup.
func (r *Repository) FailRunTerminalEvidenceTx(ctx context.Context, tx Tx, runID, message string, evidence json.RawMessage) error {
	if tx == nil || runID == "" || strings.TrimSpace(message) == "" || len(message) > 256 {
		return ErrInvalid
	}
	ev, err := boundedObject(evidence, MaxJSONBytes)
	if err != nil {
		return err
	}
	if _, err := refreshdb.New(tx).FailTerminalTreeAttempts(contextOrBackground(ctx), refreshdb.FailTerminalTreeAttemptsParams{Error: message, Evidence: ev, RunID: runID}); err != nil {
		return err
	}
	_, err = refreshdb.New(tx).FailTerminalTreeRuns(contextOrBackground(ctx), refreshdb.FailTerminalTreeRunsParams{Error: message, RunID: runID})
	if err != nil {
		return err
	}
	return r.transitionRunOccurrenceTx(ctx, tx, runID, "failed", ev)
}

// CheckInvocationAdmission is a read-only fast path. The CreateRunTx
// transaction remains the authoritative admission fence.
func (r *Repository) CheckInvocationAdmission(ctx context.Context, scope Scope, pipelineID, source string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return err
	}
	if err := canonicalID("pipeline id", pipelineID, 255); err != nil {
		return err
	}
	trigger, err := refreshdb.New(r.db).GetActiveInvocationTrigger(contextOrBackground(ctx), refreshdb.GetActiveInvocationTriggerParams{ProjectID: scope.ProjectID, Environment: scope.Environment, TargetID: pipelineID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if source != "schedule" || trigger != "schedule" {
		return ErrConflict
	}
	return nil
}

func (r *Repository) CheckScheduledInvocationAdmission(ctx context.Context, scope Scope, pipelineID string) error {
	return r.CheckInvocationAdmission(ctx, scope, pipelineID, "schedule")
}

// PrepareRun advances a live running run to the prepared publication phase
// while retaining its owner and fence. It is the module adapter's bridge for
// the run.Service workflow contract.
func (r *Repository) PrepareRun(ctx context.Context, runID, owner string, fence int64) (Run, error) {
	if err := r.requireDB(); err != nil {
		return Run{}, err
	}
	if err := canonicalID("run id", runID, 256); err != nil {
		return Run{}, err
	}
	if err := canonicalID("owner id", owner, 256); err != nil || fence <= 0 {
		return Run{}, ErrInvalid
	}
	tag, err := refreshdb.New(r.db).PrepareRun(contextOrBackground(ctx), refreshdb.PrepareRunParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence})
	if err != nil {
		return Run{}, err
	}
	if tag != 1 {
		return Run{}, ErrStaleFence
	}
	return r.runByID(contextOrBackground(ctx), r.db, runID)
}

// RunMayPublish reports whether the exact owner/fence still leases a run in
// the prepared publication phase. The check is intentionally read-only.
func (r *Repository) RunMayPublish(ctx context.Context, runID, owner string, fence int64) (bool, error) {
	if err := r.requireDB(); err != nil {
		return false, err
	}
	if runID == "" || owner == "" || fence <= 0 {
		return false, ErrInvalid
	}
	ok, err := refreshdb.New(r.db).RunLeaseExists(contextOrBackground(ctx), refreshdb.RunLeaseExistsParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence})
	return ok, err
}

// RunMayPublishTx performs the exact worker-fence check on a caller-owned
// transaction. Publication code uses this seam immediately before linking a
// pending publication so lease loss cannot race a later publish/activate
// decision. The transaction remains owned by the caller.
func (r *Repository) RunMayPublishTx(ctx context.Context, tx Tx, runID, owner string, fence int64) (bool, error) {
	if tx == nil {
		return false, ErrInvalid
	}
	if runID == "" || owner == "" || fence <= 0 {
		return false, ErrInvalid
	}
	_, err := refreshdb.New(tx).GetLiveRunForUpdate(contextOrBackground(ctx), refreshdb.GetLiveRunForUpdateParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CancelRun cancels a queued root run in its originating serving scope.
// Running/prepared runs remain worker-owned and are not cancellable here.
func (r *Repository) CancelRun(ctx context.Context, scope Scope, runID string) (Run, error) {
	if err := r.requireDB(); err != nil {
		return Run{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return Run{}, err
	}
	if err := validateGeneration(scope.GenerationID); err != nil || runID == "" {
		return Run{}, ErrInvalid
	}
	var out Run
	err := r.InTx(ctx, func(tx Tx) error {
		tag, err := refreshdb.New(tx).CancelQueuedRun(contextOrBackground(ctx), refreshdb.CancelQueuedRunParams{RunID: runID, ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID})
		if err != nil {
			return err
		}
		if tag != 1 {
			return ErrStaleFence
		}
		if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "cancelled", json.RawMessage(`{"code":"REFRESH_CANCELLED"}`)); err != nil {
			return err
		}
		var readErr error
		out, readErr = r.runByID(contextOrBackground(ctx), tx, runID)
		return readErr
	})
	return out, err
}

// FailQueuedRun records a producer-side admission failure before a worker
// lease exists. It is intentionally limited to queued roots; running work
// must use the exact lease-fenced attempt methods.
func (r *Repository) FailQueuedRun(ctx context.Context, scope Scope, runID, message string) (Run, error) {
	return r.FailQueuedRunWithHook(ctx, scope, runID, message, nil)
}

// FailQueuedRunWithHook performs a producer-side queued failure and optional
// caller-owned side effects (for example cancelling the linked platform job)
// in one transaction. Running work must use the exact worker fence methods.
func (r *Repository) FailQueuedRunWithHook(ctx context.Context, scope Scope, runID, message string, hook func(context.Context, Tx) error) (Run, error) {
	if err := r.requireDB(); err != nil {
		return Run{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil || runID == "" {
		return Run{}, ErrInvalid
	}
	var out Run
	err := r.InTx(ctx, func(tx Tx) error {
		tag, err := refreshdb.New(tx).FailQueuedRun(contextOrBackground(ctx), refreshdb.FailQueuedRunParams{Error: message, RunID: runID, ProjectID: scope.ProjectID, Environment: scope.Environment})
		if err != nil {
			return err
		}
		if tag != 1 {
			return ErrStaleFence
		}
		if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "failed", json.RawMessage(`{"code":"REFRESH_FAILED"}`)); err != nil {
			return err
		}
		if hook != nil {
			if err := hook(contextOrBackground(ctx), tx); err != nil {
				return err
			}
		}
		out, err = r.runByID(contextOrBackground(ctx), tx, runID)
		return err
	})
	return out, err
}

// CancelRunWithAudit performs the queued cancellation and an optional caller
// audit callback in one authority transaction. The callback does not commit or
// roll back the transaction.
func (r *Repository) CancelRunWithAudit(ctx context.Context, scope Scope, runID string, audit func(context.Context, Tx) error) (Run, error) {
	var out Run
	err := r.InTx(ctx, func(tx Tx) error {
		var err error
		out, err = r.CancelRunWithAuditTx(ctx, tx, scope, runID, audit)
		return err
	})
	return out, err
}

// CancelRunWithAuditTx performs the queued cancellation and optional caller
// side effects in a transaction owned by the caller. It is the composition
// seam used by native keyed cancellation so the shared operation authority,
// refresh run, canonical job, and audit rows all commit or roll back together.
func (r *Repository) CancelRunWithAuditTx(ctx context.Context, tx Tx, scope Scope, runID string, audit func(context.Context, Tx) error) (Run, error) {
	if tx == nil {
		return Run{}, ErrInvalid
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return Run{}, err
	}
	if err := validateGeneration(scope.GenerationID); err != nil || runID == "" || runID != strings.TrimSpace(runID) {
		return Run{}, ErrInvalid
	}
	tag, err := refreshdb.New(tx).CancelQueuedRun(contextOrBackground(ctx), refreshdb.CancelQueuedRunParams{RunID: runID, ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID})
	if err != nil {
		return Run{}, err
	}
	if tag != 1 {
		return Run{}, ErrStaleFence
	}
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "cancelled", json.RawMessage(`{"code":"REFRESH_CANCELLED"}`)); err != nil {
		return Run{}, err
	}
	if audit != nil {
		if err := audit(contextOrBackground(ctx), tx); err != nil {
			return Run{}, err
		}
	}
	return r.runByID(contextOrBackground(ctx), tx, runID)
}

// ClaimAttemptTx is the refresh-side evidence boundary. Worker claim
// admission itself belongs to jobs.Repository; this method links the exact
// jobs lease owner and generation to a run attempt in the same transaction.
func (r *Repository) ClaimAttemptTx(ctx context.Context, tx Tx, runID, owner string, fence int64, lease time.Duration) (Attempt, error) {
	ctx = contextOrBackground(ctx)
	if tx == nil {
		return Attempt{}, ErrInvalid
	}
	if err := canonicalID("run id", runID, 256); err != nil {
		return Attempt{}, err
	}
	if err := canonicalID("owner id", owner, 256); err != nil {
		return Attempt{}, err
	}
	if fence <= 0 || lease <= 0 || lease > MaxLease {
		return Attempt{}, ErrInvalid
	}
	var attempt Attempt
	var n int64
	// A prepared run retains a running attempt while publication is in flight.
	// If its lease expired, close that abandoned attempt before admitting the
	// higher-fence reclaim. This keeps exactly one live attempt per run while
	// allowing an interrupted prepared worker to be re-executed safely.
	if _, err := refreshdb.New(tx).ExpireAttempt(ctx, runID); err != nil {
		return Attempt{}, err
	}
	n, err := refreshdb.New(tx).ClaimRunAttempt(ctx, refreshdb.ClaimRunAttemptParams{RunID: runID, LeaseOwner: owner, FenceGeneration: fence, Lease: lease})
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, ErrBusy
	}
	if err != nil {
		return Attempt{}, err
	}
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, "running", nil); err != nil {
		return Attempt{}, err
	}
	_, err = refreshdb.New(tx).InsertAttempt(ctx, refreshdb.InsertAttemptParams{RunID: runID, AttemptNumber: n, FenceGeneration: fence, OwnerID: owner, Lease: lease})
	if err != nil {
		return Attempt{}, err
	}
	attempt = Attempt{RunID: runID, OwnerID: owner, AttemptNumber: n, FenceGeneration: fence, Status: "running"}
	return attempt, nil
}
func (r *Repository) ClaimAttempt(ctx context.Context, runID, owner string, fence int64, lease time.Duration) (Attempt, error) {
	var out Attempt
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = r.ClaimAttemptTx(ctx, tx, runID, owner, fence, lease)
		return e
	})
	return out, err
}

func (r *Repository) HeartbeatAttemptTx(ctx context.Context, tx Tx, runID, owner string, fence int64, lease time.Duration) error {
	if tx == nil {
		return ErrInvalid
	}
	if lease <= 0 || lease > MaxLease {
		return ErrInvalid
	}
	tag, err := refreshdb.New(tx).HeartbeatRunLease(contextOrBackground(ctx), refreshdb.HeartbeatRunLeaseParams{Lease: lease, RunID: runID, LeaseOwner: owner, FenceGeneration: fence})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	tag, err = refreshdb.New(tx).HeartbeatAttemptLease(contextOrBackground(ctx), refreshdb.HeartbeatAttemptLeaseParams{Lease: lease, RunID: runID, OwnerID: owner, FenceGeneration: fence})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	return nil
}
func (r *Repository) HeartbeatAttempt(ctx context.Context, runID, owner string, fence int64, lease time.Duration) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.HeartbeatAttemptTx(ctx, tx, runID, owner, fence, lease) })
}

func (r *Repository) finishAttemptTx(ctx context.Context, tx Tx, runID, owner string, fence int64, status string, evidence json.RawMessage, message string) error {
	if tx == nil {
		return ErrInvalid
	}
	if status != "succeeded" && status != "failed" && status != "cancelled" && status != "indeterminate" {
		return ErrInvalid
	}
	ev, err := boundedObject(evidence, MaxJSONBytes)
	if err != nil {
		return err
	}
	state := status
	runStatus := status
	if status == "indeterminate" {
		runStatus = "failed"
	}
	tag, err := refreshdb.New(tx).FinishAttempt(contextOrBackground(ctx), refreshdb.FinishAttemptParams{Status: state, Evidence: ev, Error: message, RunID: runID, OwnerID: owner, FenceGeneration: fence})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	tag, err = refreshdb.New(tx).FinishRun(contextOrBackground(ctx), refreshdb.FinishRunParams{Status: runStatus, Error: message, RunID: runID, LeaseOwner: owner, FenceGeneration: fence})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	if err := r.transitionRunOccurrenceTx(ctx, tx, runID, runStatus, ev); err != nil {
		return err
	}
	return nil
}
func (r *Repository) CompleteAttemptTx(ctx context.Context, tx Tx, runID, owner string, fence int64, evidence json.RawMessage) error {
	return r.finishAttemptTx(ctx, tx, runID, owner, fence, "succeeded", evidence, "")
}
func (r *Repository) FailAttemptTx(ctx context.Context, tx Tx, runID, owner string, fence int64, message string, evidence json.RawMessage) error {
	return r.finishAttemptTx(ctx, tx, runID, owner, fence, "failed", evidence, message)
}
func (r *Repository) CompleteAttempt(ctx context.Context, runID, owner string, fence int64, evidence json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.CompleteAttemptTx(ctx, tx, runID, owner, fence, evidence) })
}
func (r *Repository) FailAttempt(ctx context.Context, runID, owner string, fence int64, message string, evidence json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.FailAttemptTx(ctx, tx, runID, owner, fence, message, evidence) })
}

func (r *Repository) LinkPublicationTx(ctx context.Context, tx Tx, in PublicationInput) (Publication, error) {
	ctx = contextOrBackground(ctx)
	if tx == nil {
		return Publication{}, ErrInvalid
	}
	if in.PublicationID == "" {
		id, err := newID("")
		if err != nil {
			return Publication{}, err
		}
		in.PublicationID = id
	}
	if err := canonicalID("publication id", in.PublicationID, 256); err != nil {
		return Publication{}, err
	}
	if err := canonicalID("run id", in.RunID, 256); err != nil {
		return Publication{}, err
	}
	if err := canonicalID("base generation id", in.BaseGenerationID, 255); err != nil {
		return Publication{}, err
	}
	if err := canonicalID("result generation id", in.ResultGenerationID, 255); err != nil {
		return Publication{}, err
	}
	if err := digest("plan digest", in.PlanDigest); err != nil {
		return Publication{}, err
	}
	if err := digest("artifact digest", in.ArtifactDigest); err != nil {
		return Publication{}, err
	}
	if err := canonicalID("physical pool id", in.PhysicalPoolID, 255); err != nil {
		return Publication{}, err
	}
	if err := canonicalID("catalog id", in.CatalogID, 255); err != nil {
		return Publication{}, err
	}
	if in.FenceGeneration <= 0 {
		return Publication{}, ErrInvalid
	}
	if err := canonicalID("owner id", in.OwnerID, 256); err != nil {
		return Publication{}, err
	}
	ev, err := boundedObject(in.Evidence, MaxJSONBytes)
	if err != nil {
		return Publication{}, err
	}
	if bytes.Equal(ev, []byte(`{}`)) {
		return Publication{}, errors.New("publication link evidence is required")
	}
	// Lock and inspect the deterministic link before requiring a live run
	// fence. A committed link is durable completion evidence and must replay
	// exactly even after the worker lease or delivery pointer has advanced.
	if _, lockErr := refreshdb.New(tx).GetPublicationForUpdate(ctx, in.PublicationID); lockErr == nil {
		p, readErr := publicationByID(ctx, tx, in.PublicationID)
		if readErr != nil {
			return Publication{}, readErr
		}
		if !samePublicationLinkIdentity(p, in) || !jsonEqual(p.Evidence, ev) {
			return Publication{}, ErrConflict
		}
		if p.State == "committed" {
			if in.SnapshotID != 0 && p.SnapshotID != in.SnapshotID {
				return Publication{}, ErrConflict
			}
			return p, nil
		}
		if p.State != "pending" || p.SnapshotID != 0 || in.SnapshotID != 0 {
			return Publication{}, ErrConflict
		}
	} else if !errors.Is(lockErr, pgx.ErrNoRows) {
		return Publication{}, lockErr
	}
	runFenceRow, err := refreshdb.New(tx).GetRunPublicationFence(ctx, in.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, ErrNotFound
	} else if err != nil {
		return Publication{}, err
	} else if runFenceRow.GenerationID != in.BaseGenerationID || runFenceRow.PlanDigest != in.PlanDigest || runFenceRow.ArtifactDigest != in.ArtifactDigest || runFenceRow.LeaseOwner != in.OwnerID || runFenceRow.FenceGeneration != in.FenceGeneration || (runFenceRow.Status != "running" && runFenceRow.Status != "prepared") || !runFenceRow.Live {
		return Publication{}, ErrStaleFence
	}
	if existing, e := refreshdb.New(tx).GetRunPublicationLink(ctx, in.RunID); e == nil && existing != in.PublicationID {
		return Publication{}, ErrConflict
	} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Publication{}, e
	}
	// A fresh link is always pending and therefore cannot carry the physical
	// snapshot.  A non-zero snapshot is accepted only by the committed replay
	// branch above, where it is compared with the stored immutable value.
	if in.SnapshotID != 0 {
		return Publication{}, ErrInvalid
	}
	if in.ExpectedTargetRevision <= 0 || in.ResultTargetRevision <= in.ExpectedTargetRevision {
		return Publication{}, ErrInvalid
	}
	_, err = refreshdb.New(tx).InsertPublicationLink(ctx, refreshdb.InsertPublicationLinkParams{PublicationID: in.PublicationID, RunID: in.RunID, BaseGenerationID: in.BaseGenerationID, ResultGenerationID: in.ResultGenerationID, PlanDigest: in.PlanDigest, ArtifactDigest: in.ArtifactDigest, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, ExpectedTargetRevision: in.ExpectedTargetRevision, ResultTargetRevision: in.ResultTargetRevision, SnapshotID: in.SnapshotID, FenceGeneration: in.FenceGeneration, OwnerID: in.OwnerID, Evidence: ev})
	if err != nil {
		return Publication{}, err
	}
	p, err := publicationByID(ctx, tx, in.PublicationID)
	if err == nil {
		if !samePublicationLinkIdentity(p, in) || !jsonEqual(p.Evidence, ev) {
			return Publication{}, ErrConflict
		}
		if p.State == "committed" {
			if in.SnapshotID != 0 && p.SnapshotID != in.SnapshotID {
				return Publication{}, ErrConflict
			}
			return p, nil
		}
		if p.State != "pending" || p.SnapshotID != in.SnapshotID {
			return Publication{}, ErrConflict
		}
	}
	return p, err
}
func publicationByID(ctx context.Context, db DBTX, id string) (Publication, error) {
	r, err := refreshdb.New(db).GetPublication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, ErrNotFound
	}
	if err != nil {
		return Publication{}, err
	}
	return publicationFrom(r), nil
}
func (r *Repository) LinkPublication(ctx context.Context, in PublicationInput) (Publication, error) {
	var p Publication
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var e error
		p, e = r.LinkPublicationTx(ctx, tx, in)
		return e
	})
	return p, err
}
func samePublicationLinkIdentity(p Publication, in PublicationInput) bool {
	return p.PublicationID == in.PublicationID && p.RunID == in.RunID && p.BaseGenerationID == in.BaseGenerationID && p.ResultGenerationID == in.ResultGenerationID && p.PlanDigest == in.PlanDigest && p.ArtifactDigest == in.ArtifactDigest && p.PhysicalPoolID == in.PhysicalPoolID && p.CatalogID == in.CatalogID && p.ExpectedTargetRevision == in.ExpectedTargetRevision && p.ResultTargetRevision == in.ResultTargetRevision && p.FenceGeneration == in.FenceGeneration && p.OwnerID == in.OwnerID
}

func (r *Repository) CommitPublicationTx(ctx context.Context, tx Tx, publicationID, runID, owner string, fence int64, snapshotID int64, evidence json.RawMessage, physicalPoolID, catalogID string) error {
	if tx == nil {
		return ErrInvalid
	}
	for label, value := range map[string]string{"publication id": publicationID, "run id": runID, "owner id": owner} {
		if err := canonicalID(label, value, 256); err != nil {
			return err
		}
	}
	if fence <= 0 || snapshotID <= 0 {
		return ErrInvalid
	}
	if physicalPoolID == "" || catalogID == "" {
		return ErrInvalid
	}
	if err := canonicalID("physical pool id", physicalPoolID, 255); err != nil {
		return err
	}
	if err := canonicalID("catalog id", catalogID, 255); err != nil {
		return err
	}
	ev, err := boundedObject(evidence, MaxJSONBytes)
	if err != nil {
		return err
	}
	if bytes.Equal(ev, []byte(`{}`)) {
		return errors.New("publication commit evidence is required")
	}
	p, err := publicationByID(contextOrBackground(ctx), tx, publicationID)
	if err != nil {
		return err
	}
	if p.State == "committed" {
		if p.RunID == runID && p.OwnerID == owner && p.FenceGeneration == fence && p.PhysicalPoolID == physicalPoolID && p.CatalogID == catalogID && p.SnapshotID == snapshotID && jsonEqual(p.Evidence, ev) {
			return nil // exact publication replay
		}
		return ErrConflict
	}
	if p.State != "pending" {
		return ErrStaleFence
	}
	tag, err := refreshdb.New(tx).CommitPublication(contextOrBackground(ctx), refreshdb.CommitPublicationParams{SnapshotID: int64Ptr(snapshotID), Evidence: ev, PublicationID: publicationID, RunID: runID, OwnerID: owner, FenceGeneration: fence, PhysicalPoolID: physicalPoolID, CatalogID: catalogID})
	if err != nil {
		return err
	}
	if tag != 1 {
		return ErrStaleFence
	}
	return nil
}
func (r *Repository) CommitPublication(ctx context.Context, publicationID, runID, owner string, fence int64, snapshotID int64, evidence json.RawMessage, physicalPoolID, catalogID string) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		return r.CommitPublicationTx(ctx, tx, publicationID, runID, owner, fence, snapshotID, evidence, physicalPoolID, catalogID)
	})
}
func (r *Repository) Publication(ctx context.Context, id string) (Publication, error) {
	if err := r.requireDB(); err != nil {
		return Publication{}, err
	}
	return publicationByID(contextOrBackground(ctx), r.db, id)
}
func (r *Repository) GetPublication(ctx context.Context, id string) (Publication, error) {
	return r.Publication(ctx, id)
}

func (r *Repository) PublicationTx(ctx context.Context, tx Tx, id string) (Publication, error) {
	if tx == nil {
		return Publication{}, ErrInvalid
	}
	return publicationByID(contextOrBackground(ctx), tx, id)
}

// PublicationLinkMarkerTx reports whether any publication link already
// claims the run or result generation.  Canonical completion uses this before
// entering the first-time path so a missing deterministic link cannot mask a
// partial or conflicting publication under another identity.
func (r *Repository) PublicationLinkMarkerTx(ctx context.Context, tx Tx, runID, resultGenerationID string) (bool, error) {
	if tx == nil || runID == "" || resultGenerationID == "" {
		return false, ErrInvalid
	}
	count, err := refreshdb.New(tx).PublicationLinkMarker(contextOrBackground(ctx), refreshdb.PublicationLinkMarkerParams{RunID: runID, ResultGenerationID: resultGenerationID})
	return count > 0, err
}

// RunTreeSucceededTx proves that a refresh root and every admitted dependency
// provenance row are terminally succeeded in the same authority snapshot.
func (r *Repository) RunTreeSucceededTx(ctx context.Context, tx Tx, runID string) (bool, error) {
	if tx == nil || runID == "" {
		return false, ErrInvalid
	}
	ok, err := refreshdb.New(tx).RunTreeSucceeded(contextOrBackground(ctx), runID)
	if ok == nil {
		return false, err
	}
	return *ok, err
}

func (r *Repository) RecordRecoveryTx(ctx context.Context, tx Tx, in RecoveryInput) (RecoveryState, error) {
	ctx = contextOrBackground(ctx)
	if tx == nil {
		return RecoveryState{}, ErrInvalid
	}
	if err := canonicalID("run id", in.RunID, 256); err != nil {
		return RecoveryState{}, err
	}
	if in.State == "" {
		in.State = "reconciled"
	}
	if in.State != "pending" && in.State != "reconciled" && in.State != "indeterminate" && in.State != "quarantined" && in.State != "unreconciled" {
		return RecoveryState{}, ErrInvalid
	}
	if in.FenceGeneration > 0 {
		if err := canonicalID("recovery owner id", in.OwnerID, 256); err != nil {
			return RecoveryState{}, err
		}
	}
	if in.ExactExternalIdentity != "" {
		if err := canonicalID("external identity", in.ExactExternalIdentity, 256); err != nil {
			return RecoveryState{}, err
		}
	}
	if len(in.LastError) > 4096 {
		return RecoveryState{}, ErrInvalid
	}
	ev, err := boundedObject(in.Evidence, MaxJSONBytes)
	if err != nil {
		return RecoveryState{}, err
	}
	old, oldErr := recoveryByRun(ctx, tx, in.RunID)
	if oldErr == nil {
		if in.FenceGeneration < old.FenceGeneration {
			// A stale reconciler must not be able to observe the newer
			// authority row as if its write succeeded.  Surface the fence
			// failure just like stale run/attempt/publication mutations.
			return RecoveryState{}, ErrStaleFence
		}
		if in.FenceGeneration == old.FenceGeneration {
			if old.State == in.State && old.OwnerID == in.OwnerID && old.ExactExternalIdentity == in.ExactExternalIdentity && old.LastError == in.LastError && jsonEqual(old.Evidence, ev) {
				return old, nil
			}
			return RecoveryState{}, ErrConflict
		}
		if in.FenceGeneration != old.FenceGeneration+1 {
			return RecoveryState{}, ErrConflict
		}
		if in.Lease <= 0 || in.Lease > MaxLease {
			return RecoveryState{}, ErrInvalid
		}
	} else if !errors.Is(oldErr, ErrNotFound) {
		return RecoveryState{}, oldErr
	} else {
		if in.FenceGeneration != 1 || in.Lease <= 0 || in.Lease > MaxLease {
			return RecoveryState{}, ErrInvalid
		}
		if bytes.Equal(ev, []byte(`{}`)) {
			return RecoveryState{}, errors.New("recovery evidence is required for a fenced record")
		}
		runStatus, err := refreshdb.New(tx).GetRunStatus(ctx, in.RunID)
		if err != nil {
			return RecoveryState{}, err
		}
		if runStatus != "failed" && runStatus != "indeterminate" {
			return RecoveryState{}, ErrConflict
		}
	}
	_, err = refreshdb.New(tx).UpsertRecovery(ctx, refreshdb.UpsertRecoveryParams{RunID: in.RunID, State: in.State, ReconciliationFence: in.FenceGeneration, OwnerID: in.OwnerID, Lease: in.Lease, ExactExternalIdentity: in.ExactExternalIdentity, LastError: in.LastError, Evidence: ev, NextReconcileAt: nullableTime(in.NextReconcileAt)})
	if err != nil {
		return RecoveryState{}, err
	}
	return recoveryByRun(ctx, tx, in.RunID)
}
func recoveryByRun(ctx context.Context, db DBTX, id string) (RecoveryState, error) {
	r, err := refreshdb.New(db).GetRecovery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoveryState{}, ErrNotFound
	}
	if err != nil {
		return RecoveryState{}, err
	}
	return recoveryFrom(r), nil
}
func (r *Repository) RecordRecovery(ctx context.Context, in RecoveryInput) (RecoveryState, error) {
	var out RecoveryState
	err := r.withTx(ctx, func(tx pgx.Tx) error { var e error; out, e = r.RecordRecoveryTx(ctx, tx, in); return e })
	return out, err
}

// Recovery returns the latest durable reconciliation evidence for a run.
// Recovery rows are keyed by run identity and retain their highest fence.
func (r *Repository) Recovery(ctx context.Context, runID string) (RecoveryState, error) {
	if err := r.requireDB(); err != nil {
		return RecoveryState{}, err
	}
	if err := canonicalID("run id", runID, 256); err != nil {
		return RecoveryState{}, err
	}
	return recoveryByRun(contextOrBackground(ctx), r.db, runID)
}

func (r *Repository) GetRecovery(ctx context.Context, runID string) (RecoveryState, error) {
	return r.Recovery(ctx, runID)
}

func (r *Repository) SaveDataVersionTx(ctx context.Context, tx Tx, v DataVersion) error {
	if tx == nil || v.SnapshotID <= 0 || (v.Source != "publish" && v.Source != "refresh") || v.TargetRevision < 0 || v.LeaseRevision < 0 || (v.LeaseOwner == "") != (v.LeaseRevision == 0) {
		return ErrInvalid
	}
	// A published serving state must carry an exact refresh publication
	// identity. Snapshot-only writes (the legacy SQLite behavior) cannot prove
	// which deployment generation was committed and are rejected by the
	// PostgreSQL authority before touching the data-version row.
	if v.Source == "publish" && strings.TrimSpace(v.RunID) == "" {
		return fmt.Errorf("published data version requires committed publication provenance")
	}
	if err := validateScope(v.ProjectID, v.Environment); err != nil {
		return err
	}
	for label, val := range map[string]string{"semantic model id": v.SemanticModelID, "generation id": v.GenerationID, "physical pool id": v.PhysicalPoolID, "catalog id": v.CatalogID} {
		if err := canonicalID(label, val, 255); err != nil {
			return err
		}
	}
	publicationExists, err := refreshdb.New(tx).PublicationExistsForDataVersion(contextOrBackground(ctx), refreshdb.PublicationExistsForDataVersionParams{RunID: v.RunID, ResultGenerationID: v.GenerationID, PhysicalPoolID: v.PhysicalPoolID, CatalogID: v.CatalogID, SnapshotID: v.SnapshotID, Source: v.Source, FenceGeneration: v.LeaseRevision, OwnerID: v.LeaseOwner, ProjectID: v.ProjectID, Environment: v.Environment, SemanticModelID: v.SemanticModelID})
	if err != nil {
		return err
	}
	if !publicationExists {
		return fmt.Errorf("%w: no committed publication for run=%s result=%s pool=%s catalog=%s snapshot=%d fence=%d owner=%s", ErrConflict, v.RunID, v.GenerationID, v.PhysicalPoolID, v.CatalogID, v.SnapshotID, v.LeaseRevision, v.LeaseOwner)
	}
	if v.Source == "publish" && v.LeaseRevision != 0 {
		return ErrInvalid
	}
	if v.Source == "refresh" && v.LeaseRevision > 0 {
		runOK, err := refreshdb.New(tx).RefreshPublicationValid(contextOrBackground(ctx), refreshdb.RefreshPublicationValidParams{RunID: v.RunID, ProjectID: v.ProjectID, Environment: v.Environment, ResultGenerationID: v.GenerationID, FenceGeneration: v.LeaseRevision, OwnerID: v.LeaseOwner})
		if err != nil {
			return err
		}
		if !runOK {
			return ErrConflict
		}
	}
	oldRow, err := refreshdb.New(tx).GetDataVersionForUpdate(contextOrBackground(ctx), refreshdb.GetDataVersionForUpdateParams{ProjectID: v.ProjectID, Environment: v.Environment, SemanticModelID: v.SemanticModelID, GenerationID: v.GenerationID})
	old := dataVersionFromUpdate(oldRow)
	if err == nil {
		if v.LeaseRevision < old.LeaseRevision {
			return ErrStaleFence
		}
		if v.LeaseRevision == old.LeaseRevision {
			if v.SnapshotID == old.SnapshotID && v.Source == old.Source && v.PipelineID == old.PipelineID && v.RunID == old.RunID && v.TargetRevision == old.TargetRevision && v.LeaseOwner == old.LeaseOwner && v.PhysicalPoolID == old.PhysicalPoolID && v.CatalogID == old.CatalogID {
				return nil
			}
			return ErrConflict
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = refreshdb.New(tx).UpsertDataVersion(contextOrBackground(ctx), refreshdb.UpsertDataVersionParams{ProjectID: v.ProjectID, Environment: v.Environment, SemanticModelID: v.SemanticModelID, GenerationID: v.GenerationID, SnapshotID: v.SnapshotID, Source: v.Source, PhysicalPoolID: v.PhysicalPoolID, CatalogID: v.CatalogID, PipelineID: v.PipelineID, RunID: v.RunID, TargetRevision: v.TargetRevision, LeaseOwner: v.LeaseOwner, LeaseRevision: v.LeaseRevision})
	return err
}
func (r *Repository) SaveDataVersion(ctx context.Context, v DataVersion) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.SaveDataVersionTx(ctx, tx, v) })
}
func (r *Repository) DataVersion(ctx context.Context, projectID, environment, semanticModelID, generationID string) (DataVersion, bool, error) {
	if err := r.requireDB(); err != nil {
		return DataVersion{}, false, err
	}
	return r.dataVersion(ctx, r.db, projectID, environment, semanticModelID, generationID)
}

func (r *Repository) DataVersionTx(ctx context.Context, tx Tx, projectID, environment, semanticModelID, generationID string) (DataVersion, bool, error) {
	if tx == nil {
		return DataVersion{}, false, ErrInvalid
	}
	return r.dataVersion(ctx, tx, projectID, environment, semanticModelID, generationID)
}

func (r *Repository) dataVersion(ctx context.Context, tx DBTX, projectID, environment, semanticModelID, generationID string) (DataVersion, bool, error) {
	if tx == nil {
		return DataVersion{}, false, ErrInvalid
	}
	row, err := refreshdb.New(tx).GetDataVersion(contextOrBackground(ctx), refreshdb.GetDataVersionParams{ProjectID: projectID, Environment: environment, SemanticModelID: semanticModelID, GenerationID: generationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return DataVersion{}, false, nil
	}
	return dataVersionFrom(row), err == nil, err
}

func (r *Repository) GetDataVersion(ctx context.Context, projectID, environment, semanticModelID, generationID string) (DataVersion, bool, error) {
	return r.DataVersion(ctx, projectID, environment, semanticModelID, generationID)
}

func (r *Repository) Maintenance(ctx context.Context, limit int) (int64, error) {
	if err := r.requireDB(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	n, err := refreshdb.New(r.db).RunMaintenance(contextOrBackground(ctx), int32(limit))
	return n, err
}

// RecoveryRun is the narrow refresh-side projection used by the startup
// reconciler. It deliberately contains only durable run/lease identity; the
// canonical jobs authority is inspected through its own repository.
type RecoveryRun struct {
	RunID          string
	JobID          string
	Status         string
	Generation     int64
	LeaseOwner     string
	LeaseExpiresAt time.Time
	Environment    string
	CreatedAt      time.Time
}

func (r *Repository) RecoveryRunsTx(ctx context.Context, tx Tx, environment string, afterCreated time.Time, afterID string, limit int) ([]RecoveryRun, error) {
	if tx == nil || strings.TrimSpace(environment) == "" || limit < 1 || limit > MaxPageSize || (afterID != "" && afterCreated.IsZero()) {
		return nil, ErrInvalid
	}
	rows, err := refreshdb.New(tx).ListRecoveryRuns(contextOrBackground(ctx), refreshdb.ListRecoveryRunsParams{Environment: environment, AfterCreated: nullableTime(afterCreated), AfterID: afterID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]RecoveryRun, 0)
	for _, row := range rows {
		out = append(out, RecoveryRun{RunID: row.RunID, JobID: row.JobID, Status: row.Status, Generation: row.FenceGeneration, LeaseOwner: row.LeaseOwner, LeaseExpiresAt: ts(row.LeaseExpiresAt), Environment: row.Environment, CreatedAt: row.CreatedAt})
	}
	return out, nil
}

func (r *Repository) Attempts(ctx context.Context, runID string, limit int) ([]Attempt, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	rows, err := refreshdb.New(r.db).ListAttempts(contextOrBackground(ctx), refreshdb.ListAttemptsParams{RunID: runID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	var out []Attempt
	for _, row := range rows {
		a := Attempt{RunID: row.RunID, AttemptNumber: row.AttemptNumber, FenceGeneration: row.FenceGeneration, OwnerID: row.OwnerID, LeaseExpiresAt: row.LeaseExpiresAt, Status: row.Status, Evidence: append(json.RawMessage(nil), row.Evidence...), Error: row.Error, ClaimedAt: row.ClaimedAt, StartedAt: row.StartedAt, FinishedAt: ts(row.FinishedAt)}
		out = append(out, a)
	}
	return out, nil
}

func (r *Repository) ListOccurrences(ctx context.Context, scope Scope, limit int) ([]Occurrence, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if err := validateGeneration(scope.GenerationID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	rows, err := refreshdb.New(r.db).ListOccurrences(contextOrBackground(ctx), refreshdb.ListOccurrencesParams{ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: scope.GenerationID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	var out []Occurrence
	for _, id := range rows {
		o, e := occurrenceByID(contextOrBackground(ctx), r.db, id)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, nil
}
