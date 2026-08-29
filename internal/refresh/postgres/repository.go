package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

type OperationInput struct {
	OperationID, ProjectID, Environment, IdempotencyKey string
	RequestDigest, OperationType, OwnerID               string
	Lease                                               time.Duration
}
type Operation struct {
	OperationInput
	State, RunID                                     string
	FenceGeneration                                  int64
	LeaseExpiresAt, CreatedAt, UpdatedAt, TerminalAt time.Time
	Outcome                                          json.RawMessage
}

type RunInput struct {
	RunID, OperationID, ProjectID, Environment, GenerationID string
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
	PublicationID, RunID, GenerationID, PlanDigest, ArtifactDigest, OwnerID string
	PhysicalPoolID, CatalogID                                               string
	FenceGeneration                                                         int64
	SnapshotID                                                              int64
	Evidence                                                                json.RawMessage
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

func New(db DBTX) *Repository { return &Repository{db: db} }

// NewRepository is an explicit constructor alias used by composition roots
// that name capability repositories uniformly.
func NewRepository(db DBTX) *Repository { return New(db) }

// WithTx returns a repository bound to a caller-owned transaction. Methods
// ending in Tx never commit or roll back that transaction.
func (r *Repository) WithTx(tx Tx) *Repository { return New(tx) }

func (r *Repository) requireDB() error {
	if r == nil || r.db == nil {
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
	row := tx.QueryRow(ctx, `
SELECT schedule_revision_id, project_id, environment, pipeline_id, schedule_id,
 semantic_model_id, generation_id, artifact_digest, cron, timezone,
 starting_deadline, concurrency_policy, schedule_digest, next_run_at,
 valid_from, COALESCE(closed_at,'epoch'::timestamptz), updated_at, enabled
FROM refresh.schedule_revision
WHERE project_id=$1 AND environment=$2 AND pipeline_id=$3 AND schedule_id=$4
  AND generation_id=$5 AND closed_at IS NULL AND enabled FOR UPDATE`, in.ProjectID, in.Environment, in.PipelineID, in.ScheduleID, in.GenerationID)
	var existing Schedule
	err := scanSchedule(row, &existing)
	if err == nil {
		if existing.ScheduleDigest == in.ScheduleDigest && existing.GenerationID == in.GenerationID && existing.ArtifactDigest == in.ArtifactDigest && existing.Cron == in.Cron && existing.Timezone == in.Timezone && existing.SemanticModelID == in.SemanticModelID && existing.ConcurrencyPolicy == in.ConcurrencyPolicy {
			return existing, nil
		}
		if _, err = tx.Exec(ctx, `UPDATE refresh.schedule_revision SET closed_at=clock_timestamp(), enabled=false, updated_at=clock_timestamp() WHERE schedule_revision_id=$1`, existing.ScheduleRevisionID); err != nil {
			return Schedule{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO refresh.schedule_revision
 (schedule_revision_id,project_id,environment,pipeline_id,schedule_id,semantic_model_id,generation_id,artifact_digest,cron,timezone,starting_deadline,concurrency_policy,schedule_digest,next_run_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT (schedule_revision_id) DO NOTHING`, in.ScheduleRevisionID, in.ProjectID, in.Environment, in.PipelineID, in.ScheduleID, in.SemanticModelID, in.GenerationID, in.ArtifactDigest, in.Cron, in.Timezone, in.StartingDeadline, in.ConcurrencyPolicy, in.ScheduleDigest, in.NextRunAt)
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
	var out Schedule
	err := scanSchedule(db.QueryRow(ctx, `SELECT schedule_revision_id, project_id, environment, pipeline_id, schedule_id, semantic_model_id, generation_id, artifact_digest, cron, timezone, starting_deadline, concurrency_policy, schedule_digest, next_run_at, valid_from, COALESCE(closed_at,'epoch'::timestamptz), updated_at, enabled FROM refresh.schedule_revision WHERE schedule_revision_id=$1`, id), &out)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	return out, err
}

func scanSchedule(row pgx.Row, out *Schedule) error {
	var closed time.Time
	err := row.Scan(&out.ScheduleRevisionID, &out.ProjectID, &out.Environment, &out.PipelineID, &out.ScheduleID, &out.SemanticModelID, &out.GenerationID, &out.ArtifactDigest, &out.Cron, &out.Timezone, &out.StartingDeadline, &out.ConcurrencyPolicy, &out.ScheduleDigest, &out.NextRunAt, &out.ValidFrom, &closed, &out.UpdatedAt, &out.Enabled)
	if err == nil && !closed.Equal(time.Unix(0, 0).UTC()) {
		out.ClosedAt = closed
	}
	return err
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
func (r *Repository) Reconcile(ctx context.Context, values []ScheduleInput) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.ReconcileSchedulesTx(ctx, tx, values) })
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
	_, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.schedule_revision s SET closed_at=clock_timestamp(),enabled=false,updated_at=clock_timestamp() WHERE s.project_id=$1 AND s.environment=$2 AND s.generation_id=$3 AND s.closed_at IS NULL AND s.enabled AND NOT EXISTS (SELECT 1 FROM unnest($4::text[],$5::text[]) AS desired(pipeline_id,schedule_id) WHERE desired.pipeline_id=s.pipeline_id AND desired.schedule_id=s.schedule_id)`, scope.ProjectID, scope.Environment, generation, pipelines, ids)
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
		if _, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.schedule_revision s SET closed_at=clock_timestamp(),enabled=false,updated_at=clock_timestamp() WHERE s.project_id=$1 AND s.environment=$2 AND s.generation_id=$3 AND s.closed_at IS NULL AND s.enabled AND NOT EXISTS (SELECT 1 FROM unnest($4::text[],$5::text[]) AS desired(pipeline_id,schedule_id) WHERE desired.pipeline_id=s.pipeline_id AND desired.schedule_id=s.schedule_id)`, project, environment, generation, pipelines, ids); err != nil {
			return err
		}
	}
	return nil
}

// ClaimDue claims due schedule occurrences atomically. Accepted optional
// arguments are (Scope, time.Time, owner string, lease time.Duration, limit
// int), (project, environment, time.Time, owner, lease, limit), or the compact
// (projectgraph.ServingIdentity, time.Time) form used by the scheduler.
func (r *Repository) ClaimDue(ctx context.Context, args ...any) ([]Occurrence, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	scope, now, owner, lease, limit, err := parseClaimArgs(args...)
	if err != nil {
		return nil, err
	}
	var out []Occurrence
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
	var dbNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return nil, err
	}
	if now.IsZero() || now.After(dbNow) {
		now = dbNow
	}
	// Recover only abandoned claims selected under row locks. This statement is
	// bounded and safe when multiple schedulers run concurrently.
	if _, err := tx.Exec(ctx, `WITH stale AS (SELECT occurrence_id FROM refresh.schedule_occurrence WHERE project_id=$1 AND environment=$2 AND ($3='' OR generation_id=$3) AND status='claimed' AND lease_expires_at <= clock_timestamp() ORDER BY lease_expires_at,occurrence_id LIMIT $4 FOR UPDATE SKIP LOCKED) UPDATE refresh.schedule_occurrence o SET status='pending',lease_owner='',lease_expires_at=NULL WHERE o.occurrence_id IN (SELECT occurrence_id FROM stale)`, scope.ProjectID, scope.Environment, scope.GenerationID, limit); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT schedule_revision_id,project_id,environment,pipeline_id,schedule_id,semantic_model_id,generation_id,artifact_digest,cron,timezone,starting_deadline,concurrency_policy,schedule_digest,next_run_at,valid_from,COALESCE(closed_at,'epoch'::timestamptz),updated_at,enabled FROM refresh.schedule_revision WHERE project_id=$1 AND environment=$2 AND ($3='' OR generation_id=$3) AND enabled AND closed_at IS NULL AND next_run_at <= $4 ORDER BY next_run_at,pipeline_id,schedule_id LIMIT $5 FOR UPDATE SKIP LOCKED`, scope.ProjectID, scope.Environment, scope.GenerationID, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	for rows.Next() {
		var s Schedule
		if err := scanSchedule(rows, &s); err != nil {
			return nil, err
		}
		dueSchedules = append(dueSchedules, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
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
		if _, err := tx.Exec(ctx, `UPDATE refresh.schedule_revision SET next_run_at=$2 WHERE schedule_revision_id=$1 AND generation_id=$3 AND closed_at IS NULL AND enabled`, s.ScheduleRevisionID, next, s.GenerationID); err != nil {
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
			if _, e := tx.Exec(ctx, `INSERT INTO refresh.schedule_occurrence(occurrence_id,project_id,environment,pipeline_id,nominal_time,schedule_revision_id,matching_schedule_ids,semantic_model_id,generation_id,artifact_digest) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10) ON CONFLICT DO NOTHING`, occID, scope.ProjectID, scope.Environment, s.PipelineID, g.scheduledAt, s.ScheduleRevisionID, ids, s.SemanticModelID, s.GenerationID, s.ArtifactDigest); e != nil {
				return nil, e
			}
			if _, e := tx.Exec(ctx, `UPDATE refresh.schedule_occurrence SET status='skipped',outcome='{"reason":"starting_deadline_exceeded"}'::jsonb,finished_at=clock_timestamp() WHERE occurrence_id=$1 AND status='pending'`, occID); e != nil {
				return nil, e
			}
			continue
		}
		if _, e := tx.Exec(ctx, `INSERT INTO refresh.schedule_occurrence(occurrence_id,project_id,environment,pipeline_id,nominal_time,schedule_revision_id,matching_schedule_ids,semantic_model_id,generation_id,artifact_digest) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10) ON CONFLICT(project_id,environment,pipeline_id,nominal_time) DO NOTHING`, occID, scope.ProjectID, scope.Environment, s.PipelineID, g.scheduledAt, s.ScheduleRevisionID, ids, s.SemanticModelID, s.GenerationID, s.ArtifactDigest); e != nil {
			return nil, e
		}
		var claimed bool
		e := tx.QueryRow(ctx, `UPDATE refresh.schedule_occurrence SET status='claimed',lease_owner=$2,lease_expires_at=clock_timestamp()+$3::interval,claimed_at=clock_timestamp(),fence_generation=fence_generation+1 WHERE occurrence_id=$1 AND generation_id=$4 AND status='pending' RETURNING true`, occID, owner, lease.String(), s.GenerationID).Scan(&claimed)
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
	id := ""
	err := db.QueryRow(ctx, `SELECT occurrence_id FROM refresh.schedule_occurrence WHERE project_id=$1 AND environment=$2 AND pipeline_id=$3 AND nominal_time=$4`, scope.ProjectID, scope.Environment, pipeline, nominal).Scan(&id)
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

func parseClaimArgs(args ...any) (Scope, time.Time, string, time.Duration, int, error) {
	if len(args) < 2 {
		return Scope{}, time.Time{}, "", 0, 0, errors.New("claim scope and time are required")
	}
	var scope Scope
	var now time.Time
	var owner string = "refresh-scheduler"
	var lease = 5 * time.Minute
	var limit = MaxPageSize
	i := 0
	switch v := args[0].(type) {
	case Scope:
		scope = v
		i = 1
	case projectgraph.ServingIdentity:
		scope = Scope{ProjectID: v.ProjectID.String(), Environment: v.Environment, GenerationID: v.GenerationID}
		i = 1
	case string:
		if len(args) < 3 {
			return Scope{}, time.Time{}, "", 0, 0, errors.New("environment is required")
		}
		env, ok := args[1].(string)
		if !ok {
			return Scope{}, time.Time{}, "", 0, 0, errors.New("environment is required")
		}
		scope = Scope{ProjectID: v, Environment: env}
		i = 2
	default:
		return Scope{}, time.Time{}, "", 0, 0, errors.New("unsupported claim scope")
	}
	if i >= len(args) {
		return Scope{}, time.Time{}, "", 0, 0, errors.New("claim time is required")
	}
	var ok bool
	now, ok = args[i].(time.Time)
	if !ok {
		return Scope{}, time.Time{}, "", 0, 0, errors.New("claim time is required")
	}
	i++
	if i < len(args) {
		if v, ok := args[i].(string); ok && v != "" {
			owner = v
			i++
		}
	}
	if i < len(args) {
		if v, ok := args[i].(time.Duration); ok {
			lease = v
			i++
		}
	}
	if i < len(args) {
		if v, ok := args[i].(int); ok {
			limit = v
			i++
		}
	}
	return scope, now, owner, lease, limit, nil
}

func occurrenceByID(ctx context.Context, db DBTX, id string) (Occurrence, error) {
	var o Occurrence
	var ids []byte
	var outcome []byte
	err := db.QueryRow(ctx, `SELECT occurrence_id,project_id,environment,pipeline_id,nominal_time,schedule_revision_id,matching_schedule_ids,semantic_model_id,generation_id,artifact_digest,status,COALESCE(run_id,''),fence_generation,lease_owner,COALESCE(lease_expires_at,'epoch'::timestamptz),COALESCE(claimed_at,'epoch'::timestamptz),COALESCE(finished_at,'epoch'::timestamptz),created_at,outcome FROM refresh.schedule_occurrence WHERE occurrence_id=$1`, id).Scan(&o.OccurrenceID, &o.ProjectID, &o.Environment, &o.PipelineID, &o.NominalTime, &o.ScheduleRevisionID, &ids, &o.SemanticModelID, &o.GenerationID, &o.ArtifactDigest, &o.Status, &o.RunID, &o.FenceGeneration, &o.LeaseOwner, &o.LeaseExpiresAt, &o.ClaimedAt, &o.FinishedAt, &o.CreatedAt, &outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return Occurrence{}, ErrNotFound
	}
	if err != nil {
		return Occurrence{}, err
	}
	_ = json.Unmarshal(ids, &o.MatchingScheduleIDs)
	o.Outcome = append(json.RawMessage(nil), outcome...)
	return o, nil
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
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.schedule_occurrence SET status='pending', lease_owner='', lease_expires_at=NULL WHERE occurrence_id=$1 AND status='claimed' AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at > clock_timestamp()`, o.OccurrenceID, o.LeaseOwner, o.FenceGeneration)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	// Requeue every schedule revision that contributed evidence so the next
	// dispatcher tick retries this nominal instant before later catch-up work.
	if _, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.schedule_revision SET next_run_at=LEAST(next_run_at,$2),updated_at=clock_timestamp() WHERE project_id=$1 AND environment=$3 AND pipeline_id=$4 AND schedule_revision_id = (SELECT schedule_revision_id FROM refresh.schedule_occurrence WHERE occurrence_id=$5) AND closed_at IS NULL AND enabled`, o.ProjectID, o.NominalTime, o.Environment, o.PipelineID, o.OccurrenceID); err != nil {
		return err
	}
	return nil
}

// ReserveOperationTx implements exact idempotency replay.  A duplicate key
// with a different request digest is a conflict; identical requests return
// the original durable identity and evidence.
func (r *Repository) ReserveOperationTx(ctx context.Context, tx Tx, in OperationInput) (Operation, bool, error) {
	ctx = contextOrBackground(ctx)
	if tx == nil {
		return Operation{}, false, ErrInvalid
	}
	if err := validateScope(in.ProjectID, in.Environment); err != nil {
		return Operation{}, false, err
	}
	for label, v := range map[string]string{"idempotency key": in.IdempotencyKey, "operation type": in.OperationType} {
		if err := canonicalID(label, v, 256); err != nil {
			return Operation{}, false, err
		}
	}
	if err := canonicalID("owner id", in.OwnerID, 256); err != nil {
		return Operation{}, false, err
	}
	if err := digest("request digest", in.RequestDigest); err != nil {
		return Operation{}, false, err
	}
	if in.Lease <= 0 || in.Lease > MaxLease {
		return Operation{}, false, errors.New("operation lease is outside bound")
	}
	id, err := newID(in.OperationID)
	if err != nil {
		return Operation{}, false, err
	}
	in.OperationID = id
	tag, err := tx.Exec(ctx, `INSERT INTO refresh.operation(operation_id,project_id,environment,idempotency_key,request_digest,operation_type,owner_id,lease_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()+$8::interval) ON CONFLICT(project_id,environment,idempotency_key) DO NOTHING`, id, in.ProjectID, in.Environment, in.IdempotencyKey, in.RequestDigest, in.OperationType, in.OwnerID, in.Lease.String())
	if err != nil {
		return Operation{}, false, err
	}
	var o Operation
	var outcome []byte
	err = tx.QueryRow(ctx, `SELECT operation_id,project_id,environment,idempotency_key,request_digest,operation_type,state,owner_id,fence_generation,COALESCE(lease_expires_at,'epoch'::timestamptz),COALESCE(run_id,''),outcome,created_at,updated_at,COALESCE(terminal_at,'epoch'::timestamptz) FROM refresh.operation WHERE project_id=$1 AND environment=$2 AND idempotency_key=$3 FOR UPDATE`, in.ProjectID, in.Environment, in.IdempotencyKey).Scan(&o.OperationID, &o.ProjectID, &o.Environment, &o.IdempotencyKey, &o.RequestDigest, &o.OperationType, &o.State, &o.OwnerID, &o.FenceGeneration, &o.LeaseExpiresAt, &o.RunID, &outcome, &o.CreatedAt, &o.UpdatedAt, &o.TerminalAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, false, ErrNotFound
	}
	if err != nil {
		return Operation{}, false, err
	}
	if o.RequestDigest != in.RequestDigest {
		return Operation{}, false, ErrConflict
	}
	o.Outcome = append(json.RawMessage(nil), outcome...)
	return o, tag.RowsAffected() == 0, nil
}

func (r *Repository) ReserveOperation(ctx context.Context, in OperationInput) (Operation, bool, error) {
	var out Operation
	var replay bool
	err := r.withTx(ctx, func(tx pgx.Tx) error { var e error; out, replay, e = r.ReserveOperationTx(ctx, tx, in); return e })
	return out, replay, err
}
func (r *Repository) CreateOperationTx(ctx context.Context, tx Tx, in OperationInput) (Operation, bool, error) {
	return r.ReserveOperationTx(ctx, tx, in)
}
func (r *Repository) CreateOperation(ctx context.Context, in OperationInput) (Operation, bool, error) {
	return r.ReserveOperation(ctx, in)
}

func (r *Repository) SetOperationRunTx(ctx context.Context, tx Tx, operationID, runID string) error {
	if tx == nil {
		return ErrInvalid
	}
	if err := canonicalID("operation id", operationID, 256); err != nil {
		return err
	}
	if err := canonicalID("run id", runID, 256); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(contextOrBackground(ctx), `SELECT EXISTS (SELECT 1 FROM refresh.run WHERE run_id=$1)`, runID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.operation SET run_id=$2 WHERE operation_id=$1 AND run_id IS NULL`, operationID, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) transitionOperationTx(ctx context.Context, tx Tx, operationID, owner string, fence int64, state string, outcome json.RawMessage) error {
	if tx == nil || fence <= 0 {
		return ErrInvalid
	}
	if state != "succeeded" && state != "failed" && state != "cancelled" && state != "indeterminate" {
		return ErrInvalid
	}
	ev, err := boundedObject(outcome, MaxJSONBytes)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.operation SET state=$4,outcome=$5::jsonb,terminal_at=CASE WHEN $4 IN ('succeeded','failed','cancelled') THEN clock_timestamp() ELSE NULL END,owner_id='',lease_expires_at=NULL WHERE operation_id=$1 AND owner_id=$2 AND fence_generation=$3 AND state IN ('pending','running','prepared') AND lease_expires_at > clock_timestamp()`, operationID, owner, fence, state, ev)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	return nil
}
func (r *Repository) CompleteOperationTx(ctx context.Context, tx Tx, operationID, owner string, fence int64, outcome json.RawMessage) error {
	return r.transitionOperationTx(ctx, tx, operationID, owner, fence, "succeeded", outcome)
}
func (r *Repository) FailOperationTx(ctx context.Context, tx Tx, operationID, owner string, fence int64, outcome json.RawMessage) error {
	return r.transitionOperationTx(ctx, tx, operationID, owner, fence, "failed", outcome)
}
func (r *Repository) IndeterminateOperationTx(ctx context.Context, tx Tx, operationID, owner string, fence int64, outcome json.RawMessage) error {
	return r.transitionOperationTx(ctx, tx, operationID, owner, fence, "indeterminate", outcome)
}
func (r *Repository) CompleteOperation(ctx context.Context, operationID, owner string, fence int64, outcome json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.CompleteOperationTx(ctx, tx, operationID, owner, fence, outcome) })
}
func (r *Repository) FailOperation(ctx context.Context, operationID, owner string, fence int64, outcome json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.FailOperationTx(ctx, tx, operationID, owner, fence, outcome) })
}
func (r *Repository) TakeoverOperationTx(ctx context.Context, tx Tx, operationID, owner string, lease time.Duration) (int64, error) {
	if tx == nil || lease <= 0 || lease > MaxLease {
		return 0, ErrInvalid
	}
	if err := canonicalID("owner id", owner, 256); err != nil {
		return 0, err
	}
	var fence int64
	err := tx.QueryRow(contextOrBackground(ctx), `UPDATE refresh.operation SET owner_id=$2,fence_generation=fence_generation+1,lease_expires_at=clock_timestamp()+$3::interval WHERE operation_id=$1 AND state IN ('pending','running','prepared') AND (lease_expires_at <= clock_timestamp() OR owner_id=$2) RETURNING fence_generation`, operationID, owner, lease.String()).Scan(&fence)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrBusy
	}
	return fence, err
}

func (r *Repository) Operation(ctx context.Context, projectID, environment, idempotencyKey string) (Operation, error) {
	if err := r.requireDB(); err != nil {
		return Operation{}, err
	}
	if err := validateScope(projectID, environment); err != nil {
		return Operation{}, err
	}
	var o Operation
	var ev []byte
	err := r.db.QueryRow(contextOrBackground(ctx), `SELECT operation_id,project_id,environment,idempotency_key,request_digest,operation_type,state,owner_id,fence_generation,COALESCE(lease_expires_at,'epoch'::timestamptz),COALESCE(run_id,''),outcome,created_at,updated_at,COALESCE(terminal_at,'epoch'::timestamptz) FROM refresh.operation WHERE project_id=$1 AND environment=$2 AND idempotency_key=$3`, projectID, environment, idempotencyKey).Scan(&o.OperationID, &o.ProjectID, &o.Environment, &o.IdempotencyKey, &o.RequestDigest, &o.OperationType, &o.State, &o.OwnerID, &o.FenceGeneration, &o.LeaseExpiresAt, &o.RunID, &ev, &o.CreatedAt, &o.UpdatedAt, &o.TerminalAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	o.Outcome = append(json.RawMessage(nil), ev...)
	return o, nil
}

func (r *Repository) GetOperation(ctx context.Context, projectID, environment, idempotencyKey string) (Operation, error) {
	return r.Operation(ctx, projectID, environment, idempotencyKey)
}

// CreateRunTx stores the immutable execution identity. A duplicate run ID is
// an exact replay only when all identity fields (including request-linked
// operation and plan/artifact digests) match.
func (r *Repository) CreateRunTx(ctx context.Context, tx Tx, in RunInput) (Run, error) {
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
	}
	// Scheduled overlap policy is scoped strictly to scheduled invocations.
	// Manual/backfill/dependency admission always conflicts with any active
	// invocation and must never be superseded by Replace.
	rows, err := tx.Query(ctx, `SELECT run_id,trigger_type FROM refresh.run WHERE project_id=$1 AND environment=$2 AND target_type=$3 AND target_id=$4 AND status IN ('queued','running','prepared') AND run_id<>$5 ORDER BY created_at,run_id FOR UPDATE`, in.ProjectID, in.Environment, in.TargetType, in.TargetID, in.RunID)
	if err != nil {
		return Run{}, err
	}
	var active, externalActive bool
	for rows.Next() {
		var activeID, activeTrigger string
		if err := rows.Scan(&activeID, &activeTrigger); err != nil {
			rows.Close()
			return Run{}, err
		}
		active = true
		if activeTrigger != "schedule" {
			externalActive = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Run{}, err
	}
	rows.Close()
	if active {
		if in.TriggerType != "schedule" || in.ConcurrencyPolicy == "Forbid" || externalActive {
			return Run{}, ErrConflict
		}
	}
	if in.TriggerType == "schedule" && in.ConcurrencyPolicy == "Replace" {
		if _, e := tx.Exec(ctx, `UPDATE refresh.run SET status='superseded',error='replaced by newer scheduled invocation',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE project_id=$1 AND environment=$2 AND target_type=$3 AND target_id=$4 AND trigger_type='schedule' AND status IN ('queued','running','prepared') AND run_id<>$5`, in.ProjectID, in.Environment, in.TargetType, in.TargetID, in.RunID); e != nil {
			return Run{}, e
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO refresh.run(run_id,operation_id,project_id,environment,generation_id,pipeline_id,semantic_model_id,target_type,target_id,target_revision,trigger_type,invocation_source,trigger_id,concurrency_policy,schedule_revision_id,occurrence_id,nominal_time,plan_digest,artifact_digest,matching_schedule_ids,materialization_scope,principal_id,job_id) VALUES($1,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NULLIF($17::timestamptz,'epoch'::timestamptz),$18,$19,$20::jsonb,$21::jsonb,$22,$23) ON CONFLICT(run_id) DO NOTHING`, in.RunID, in.OperationID, in.ProjectID, in.Environment, in.GenerationID, in.PipelineID, in.SemanticModelID, in.TargetType, in.TargetID, in.TargetRevision, in.TriggerType, in.InvocationSource, in.TriggerID, in.ConcurrencyPolicy, in.ScheduleRevisionID, in.OccurrenceID, nullableTime(in.NominalTime), in.PlanDigest, in.ArtifactDigest, matching, material, in.PrincipalID, in.JobID)
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
	if r.RunID != in.RunID || r.OperationID != in.OperationID || r.ProjectID != in.ProjectID || r.Environment != in.Environment || r.GenerationID != in.GenerationID || r.PipelineID != in.PipelineID || r.SemanticModelID != in.SemanticModelID || r.TargetType != in.TargetType || r.TargetID != in.TargetID || r.PlanDigest != in.PlanDigest || r.ArtifactDigest != in.ArtifactDigest || r.TriggerType != in.TriggerType || r.InvocationSource != in.InvocationSource || r.TriggerID != in.TriggerID || r.ConcurrencyPolicy != in.ConcurrencyPolicy || r.ScheduleRevisionID != in.ScheduleRevisionID || r.OccurrenceID != in.OccurrenceID || r.PrincipalID != in.PrincipalID || r.JobID != in.JobID || r.TargetRevision != in.TargetRevision || !r.NominalTime.Equal(in.NominalTime) {
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

// AttachJobTx links an already-enqueued platform job to its run. This method
// never inserts into or claims a queue; jobs.Repository.EnqueueTx is expected
// to run before it in the same caller-owned transaction.
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
	// The jobs schema is intentionally the sole queue authority. If it is not
	// installed, fail closed rather than silently creating a shadow queue.
	var exists bool
	if err := tx.QueryRow(contextOrBackground(ctx), `SELECT EXISTS (SELECT 1 FROM jobs.job WHERE id=$1)`, jobID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.run SET job_id=$2 WHERE run_id=$1 AND (job_id='' OR job_id=$2)`, runID, jobID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) runByID(ctx context.Context, db DBTX, id string) (Run, error) {
	var out Run
	var matching, material []byte
	var nominal time.Time
	err := db.QueryRow(ctx, `SELECT run_id,COALESCE(operation_id,''),project_id,environment,generation_id,pipeline_id,semantic_model_id,target_type,target_id,target_revision,trigger_type,invocation_source,trigger_id,concurrency_policy,schedule_revision_id,occurrence_id,COALESCE(nominal_time,'epoch'::timestamptz),plan_digest,artifact_digest,matching_schedule_ids,materialization_scope,principal_id,job_id,status,attempt_count,fence_generation,lease_owner,COALESCE(lease_expires_at,'epoch'::timestamptz),created_at,updated_at,COALESCE(started_at,'epoch'::timestamptz),COALESCE(finished_at,'epoch'::timestamptz),error FROM refresh.run WHERE run_id=$1`, id).Scan(&out.RunID, &out.OperationID, &out.ProjectID, &out.Environment, &out.GenerationID, &out.PipelineID, &out.SemanticModelID, &out.TargetType, &out.TargetID, &out.TargetRevision, &out.TriggerType, &out.InvocationSource, &out.TriggerID, &out.ConcurrencyPolicy, &out.ScheduleRevisionID, &out.OccurrenceID, &nominal, &out.PlanDigest, &out.ArtifactDigest, &matching, &material, &out.PrincipalID, &out.JobID, &out.Status, &out.AttemptCount, &out.FenceGeneration, &out.LeaseOwner, &out.LeaseExpiresAt, &out.CreatedAt, &out.UpdatedAt, &out.StartedAt, &out.FinishedAt, &out.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	if nominal.Equal(time.Unix(0, 0).UTC()) {
		out.NominalTime = time.Time{}
	} else {
		out.NominalTime = nominal
	}
	_ = json.Unmarshal(matching, &out.MatchingScheduleIDs)
	_ = json.Unmarshal(material, &out.MaterializationScope)
	return out, nil
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
	rows, err := r.db.Query(contextOrBackground(ctx), `SELECT run_id FROM refresh.run WHERE project_id=$1 AND environment=$2 AND ($3='' OR generation_id=$3) AND ($4='' OR run_id>$4) ORDER BY created_at DESC,run_id DESC LIMIT $5`, scope.ProjectID, scope.Environment, scope.GenerationID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		v, e := r.runByID(contextOrBackground(ctx), r.db, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
	err := tx.QueryRow(ctx, `UPDATE refresh.run SET status='running',attempt_count=attempt_count+1,fence_generation=$3,lease_owner=$2,lease_expires_at=clock_timestamp()+$4::interval,started_at=COALESCE(started_at,clock_timestamp()) WHERE run_id=$1 AND status IN ('queued','running') AND (status='queued' OR (lease_expires_at <= clock_timestamp() AND $3 > fence_generation)) RETURNING attempt_count`, runID, owner, fence, lease.String()).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, ErrBusy
	}
	if err != nil {
		return Attempt{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO refresh.attempt(run_id,attempt_number,fence_generation,owner_id,lease_expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+$5::interval)`, runID, n, fence, owner, lease.String())
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
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.run SET lease_expires_at=clock_timestamp()+$4::interval WHERE run_id=$1 AND status='running' AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at > clock_timestamp()`, runID, owner, fence, lease.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	tag, err = tx.Exec(contextOrBackground(ctx), `UPDATE refresh.attempt SET lease_expires_at=clock_timestamp()+$4::interval WHERE run_id=$1 AND status='running' AND owner_id=$2 AND fence_generation=$3`, runID, owner, fence, lease.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
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
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.attempt SET status=$4,evidence=$5::jsonb,error=$6,finished_at=clock_timestamp() WHERE run_id=$1 AND owner_id=$2 AND fence_generation=$3 AND status='running' AND lease_expires_at > clock_timestamp()`, runID, owner, fence, state, ev, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	tag, err = tx.Exec(contextOrBackground(ctx), `UPDATE refresh.run SET status=$4,error=$5,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE run_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND status='running'`, runID, owner, fence, runStatus, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
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
	if err := canonicalID("generation id", in.GenerationID, 255); err != nil {
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
	if in.SnapshotID != 0 {
		return Publication{}, ErrInvalid
	}
	if err := canonicalID("owner id", in.OwnerID, 256); err != nil {
		return Publication{}, err
	}
	var runGeneration, runPlan, runArtifact, runOwner, runStatus string
	var runFence int64
	var runLive bool
	if err := tx.QueryRow(ctx, `SELECT generation_id,plan_digest,artifact_digest,lease_owner,fence_generation,status,COALESCE(lease_expires_at,'epoch'::timestamptz) > clock_timestamp() FROM refresh.run WHERE run_id=$1`, in.RunID).Scan(&runGeneration, &runPlan, &runArtifact, &runOwner, &runFence, &runStatus, &runLive); errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, ErrNotFound
	} else if err != nil {
		return Publication{}, err
	} else if runGeneration != in.GenerationID || runPlan != in.PlanDigest || runArtifact != in.ArtifactDigest || runOwner != in.OwnerID || runFence != in.FenceGeneration || (runStatus != "running" && runStatus != "prepared") || !runLive {
		return Publication{}, ErrStaleFence
	}
	ev, err := boundedObject(in.Evidence, MaxJSONBytes)
	if err != nil {
		return Publication{}, err
	}
	if bytes.Equal(ev, []byte(`{}`)) {
		return Publication{}, errors.New("publication link evidence is required")
	}
	var existingID string
	if e := tx.QueryRow(ctx, `SELECT publication_id FROM refresh.publication_link WHERE run_id=$1 AND state IN ('pending','committed')`, in.RunID).Scan(&existingID); e == nil && existingID != in.PublicationID {
		return Publication{}, ErrConflict
	} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Publication{}, e
	}
	_, err = tx.Exec(ctx, `INSERT INTO refresh.publication_link(publication_id,run_id,generation_id,plan_digest,artifact_digest,physical_pool_id,catalog_id,snapshot_id,fence_generation,owner_id,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0),$9,$10,$11::jsonb) ON CONFLICT(publication_id) DO NOTHING`, in.PublicationID, in.RunID, in.GenerationID, in.PlanDigest, in.ArtifactDigest, in.PhysicalPoolID, in.CatalogID, in.SnapshotID, in.FenceGeneration, in.OwnerID, ev)
	if err != nil {
		return Publication{}, err
	}
	p, err := publicationByID(ctx, tx, in.PublicationID)
	if err == nil && (!samePublication(p, in) || !jsonEqual(p.Evidence, ev)) {
		return Publication{}, ErrConflict
	}
	return p, err
}
func publicationByID(ctx context.Context, db DBTX, id string) (Publication, error) {
	var p Publication
	var ev []byte
	err := db.QueryRow(ctx, `SELECT publication_id,run_id,generation_id,plan_digest,artifact_digest,physical_pool_id,catalog_id,COALESCE(snapshot_id,0),fence_generation,owner_id,state,evidence,created_at,COALESCE(committed_at,'epoch'::timestamptz) FROM refresh.publication_link WHERE publication_id=$1`, id).Scan(&p.PublicationID, &p.RunID, &p.GenerationID, &p.PlanDigest, &p.ArtifactDigest, &p.PhysicalPoolID, &p.CatalogID, &p.SnapshotID, &p.FenceGeneration, &p.OwnerID, &p.State, &ev, &p.CreatedAt, &p.CommittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, ErrNotFound
	}
	if err != nil {
		return Publication{}, err
	}
	p.Evidence = append(json.RawMessage(nil), ev...)
	if p.CommittedAt.Equal(time.Unix(0, 0).UTC()) {
		p.CommittedAt = time.Time{}
	}
	return p, nil
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
func samePublication(p Publication, in PublicationInput) bool {
	return p.PublicationID == in.PublicationID && p.RunID == in.RunID && p.GenerationID == in.GenerationID && p.PlanDigest == in.PlanDigest && p.ArtifactDigest == in.ArtifactDigest && p.PhysicalPoolID == in.PhysicalPoolID && p.CatalogID == in.CatalogID && p.FenceGeneration == in.FenceGeneration && p.OwnerID == in.OwnerID && p.SnapshotID == in.SnapshotID
}

func (r *Repository) CommitPublicationTx(ctx context.Context, tx Tx, publicationID, runID, owner string, fence int64, snapshotID int64, evidence json.RawMessage, physical ...string) error {
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
	if len(physical) != 2 || physical[0] == "" || physical[1] == "" {
		return ErrInvalid
	}
	if err := canonicalID("physical pool id", physical[0], 255); err != nil {
		return err
	}
	if err := canonicalID("catalog id", physical[1], 255); err != nil {
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
		if p.RunID == runID && p.OwnerID == owner && p.FenceGeneration == fence && p.PhysicalPoolID == physical[0] && p.CatalogID == physical[1] && p.SnapshotID == snapshotID && jsonEqual(p.Evidence, ev) {
			return nil // exact publication replay
		}
		return ErrConflict
	}
	if p.State != "pending" {
		return ErrStaleFence
	}
	tag, err := tx.Exec(contextOrBackground(ctx), `UPDATE refresh.publication_link SET state='committed',snapshot_id=$5,evidence=$6::jsonb,committed_at=clock_timestamp() WHERE publication_id=$1 AND run_id=$2 AND owner_id=$3 AND fence_generation=$4 AND physical_pool_id=$7 AND catalog_id=$8 AND state='pending' AND EXISTS (SELECT 1 FROM refresh.run WHERE run_id=$2 AND fence_generation=$4 AND lease_owner=$3 AND status IN ('running','prepared'))`, publicationID, runID, owner, fence, snapshotID, ev, physical[0], physical[1])
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	return nil
}
func (r *Repository) CommitPublication(ctx context.Context, publicationID, runID, owner string, fence int64, snapshotID int64, evidence json.RawMessage, physical ...string) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		return r.CommitPublicationTx(ctx, tx, publicationID, runID, owner, fence, snapshotID, evidence, physical...)
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
		var runStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM refresh.run WHERE run_id=$1`, in.RunID).Scan(&runStatus); err != nil {
			return RecoveryState{}, err
		}
		if runStatus != "failed" && runStatus != "indeterminate" {
			return RecoveryState{}, ErrConflict
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO refresh.recovery_state(run_id,state,reconciliation_fence,owner_id,lease_expires_at,exact_external_identity,last_error,evidence,next_reconcile_at) VALUES($1,$2,$3,$4,clock_timestamp()+$5::interval,$6,$7,$8::jsonb,NULLIF($9::timestamptz,'epoch'::timestamptz)) ON CONFLICT(run_id) DO UPDATE SET state=EXCLUDED.state,reconciliation_fence=EXCLUDED.reconciliation_fence,owner_id=EXCLUDED.owner_id,lease_expires_at=EXCLUDED.lease_expires_at,exact_external_identity=EXCLUDED.exact_external_identity,last_error=EXCLUDED.last_error,evidence=EXCLUDED.evidence,next_reconcile_at=EXCLUDED.next_reconcile_at WHERE refresh.recovery_state.reconciliation_fence < EXCLUDED.reconciliation_fence`, in.RunID, in.State, in.FenceGeneration, in.OwnerID, in.Lease.String(), in.ExactExternalIdentity, in.LastError, ev, nullableTime(in.NextReconcileAt))
	if err != nil {
		return RecoveryState{}, err
	}
	return recoveryByRun(ctx, tx, in.RunID)
}
func recoveryByRun(ctx context.Context, db DBTX, id string) (RecoveryState, error) {
	var out RecoveryState
	var next, leaseExpires time.Time
	var ev []byte
	err := db.QueryRow(ctx, `SELECT run_id,state,reconciliation_fence,owner_id,COALESCE(lease_expires_at,'epoch'::timestamptz),exact_external_identity,last_error,evidence,COALESCE(next_reconcile_at,'epoch'::timestamptz),updated_at FROM refresh.recovery_state WHERE run_id=$1`, id).Scan(&out.RunID, &out.State, &out.FenceGeneration, &out.OwnerID, &leaseExpires, &out.ExactExternalIdentity, &out.LastError, &ev, &next, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoveryState{}, ErrNotFound
	}
	if err != nil {
		return RecoveryState{}, err
	}
	out.Evidence = append(json.RawMessage(nil), ev...)
	if !leaseExpires.Equal(time.Unix(0, 0).UTC()) {
		out.LeaseExpiresAt = leaseExpires
	}
	if !next.Equal(time.Unix(0, 0).UTC()) {
		out.NextReconcileAt = next
	}
	return out, nil
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
	if err := validateScope(v.ProjectID, v.Environment); err != nil {
		return err
	}
	for label, val := range map[string]string{"semantic model id": v.SemanticModelID, "generation id": v.GenerationID, "physical pool id": v.PhysicalPoolID, "catalog id": v.CatalogID} {
		if err := canonicalID(label, val, 255); err != nil {
			return err
		}
	}
	var publicationExists bool
	if err := tx.QueryRow(contextOrBackground(ctx), `SELECT EXISTS (SELECT 1 FROM refresh.publication_link p JOIN refresh.run r ON r.run_id=p.run_id WHERE p.run_id=$1 AND p.state='committed' AND p.generation_id=$2 AND p.physical_pool_id=$3 AND p.catalog_id=$4 AND p.snapshot_id=$5 AND ( $9='publish' OR (p.fence_generation=$10 AND p.owner_id=$11) ) AND r.project_id=$6 AND r.environment=$7 AND r.semantic_model_id=$8)`, v.RunID, v.GenerationID, v.PhysicalPoolID, v.CatalogID, v.SnapshotID, v.ProjectID, v.Environment, v.SemanticModelID, v.Source, v.LeaseRevision, v.LeaseOwner).Scan(&publicationExists); err != nil {
		return err
	}
	if !publicationExists {
		return ErrConflict
	}
	if v.Source == "publish" && v.LeaseRevision != 0 {
		return ErrInvalid
	}
	if v.Source == "refresh" && v.LeaseRevision > 0 {
		var runOK bool
		if err := tx.QueryRow(contextOrBackground(ctx), `SELECT EXISTS (SELECT 1 FROM refresh.run r JOIN refresh.publication_link p ON p.run_id=r.run_id WHERE r.run_id=$1 AND r.project_id=$2 AND r.environment=$3 AND r.generation_id=$4 AND p.state='committed' AND p.fence_generation=$5 AND p.owner_id=$6 AND r.status IN ('running','prepared','succeeded'))`, v.RunID, v.ProjectID, v.Environment, v.GenerationID, v.LeaseRevision, v.LeaseOwner).Scan(&runOK); err != nil {
			return err
		}
		if !runOK {
			return ErrConflict
		}
	}
	var old DataVersion
	err := tx.QueryRow(contextOrBackground(ctx), `SELECT project_id,environment,semantic_model_id,generation_id,snapshot_id,refreshed_at,source,pipeline_id,run_id,target_revision,lease_owner,lease_revision,physical_pool_id,catalog_id FROM refresh.data_version WHERE project_id=$1 AND environment=$2 AND semantic_model_id=$3 AND generation_id=$4 FOR UPDATE`, v.ProjectID, v.Environment, v.SemanticModelID, v.GenerationID).Scan(&old.ProjectID, &old.Environment, &old.SemanticModelID, &old.GenerationID, &old.SnapshotID, &old.RefreshedAt, &old.Source, &old.PipelineID, &old.RunID, &old.TargetRevision, &old.LeaseOwner, &old.LeaseRevision, &old.PhysicalPoolID, &old.CatalogID)
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
	_, err = tx.Exec(contextOrBackground(ctx), `INSERT INTO refresh.data_version(project_id,environment,semantic_model_id,generation_id,snapshot_id,source,physical_pool_id,catalog_id,pipeline_id,run_id,target_revision,lease_owner,lease_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(project_id,environment,semantic_model_id,generation_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,refreshed_at=clock_timestamp(),source=EXCLUDED.source,physical_pool_id=EXCLUDED.physical_pool_id,catalog_id=EXCLUDED.catalog_id,pipeline_id=EXCLUDED.pipeline_id,run_id=EXCLUDED.run_id,target_revision=EXCLUDED.target_revision,lease_owner=EXCLUDED.lease_owner,lease_revision=EXCLUDED.lease_revision WHERE refresh.data_version.lease_revision < EXCLUDED.lease_revision`, v.ProjectID, v.Environment, v.SemanticModelID, v.GenerationID, v.SnapshotID, v.Source, v.PhysicalPoolID, v.CatalogID, v.PipelineID, v.RunID, v.TargetRevision, v.LeaseOwner, v.LeaseRevision)
	return err
}
func (r *Repository) SaveDataVersion(ctx context.Context, v DataVersion) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.SaveDataVersionTx(ctx, tx, v) })
}
func (r *Repository) DataVersion(ctx context.Context, projectID, environment, semanticModelID, generationID string) (DataVersion, bool, error) {
	if err := r.requireDB(); err != nil {
		return DataVersion{}, false, err
	}
	var v DataVersion
	err := r.db.QueryRow(contextOrBackground(ctx), `SELECT project_id,environment,semantic_model_id,generation_id,snapshot_id,refreshed_at,source,pipeline_id,run_id,target_revision,lease_owner,lease_revision,physical_pool_id,catalog_id FROM refresh.data_version WHERE project_id=$1 AND environment=$2 AND semantic_model_id=$3 AND generation_id=$4`, projectID, environment, semanticModelID, generationID).Scan(&v.ProjectID, &v.Environment, &v.SemanticModelID, &v.GenerationID, &v.SnapshotID, &v.RefreshedAt, &v.Source, &v.PipelineID, &v.RunID, &v.TargetRevision, &v.LeaseOwner, &v.LeaseRevision, &v.PhysicalPoolID, &v.CatalogID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DataVersion{}, false, nil
	}
	return v, err == nil, err
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
	var n int64
	err := r.db.QueryRow(contextOrBackground(ctx), `SELECT refresh.maintenance($1)`, limit).Scan(&n)
	return n, err
}

func (r *Repository) Attempts(ctx context.Context, runID string, limit int) ([]Attempt, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	rows, err := r.db.Query(contextOrBackground(ctx), `SELECT run_id,attempt_number,fence_generation,owner_id,lease_expires_at,status,evidence,error,claimed_at,started_at,COALESCE(finished_at,'epoch'::timestamptz) FROM refresh.attempt WHERE run_id=$1 ORDER BY attempt_number DESC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var ev []byte
		var fin time.Time
		if err := rows.Scan(&a.RunID, &a.AttemptNumber, &a.FenceGeneration, &a.OwnerID, &a.LeaseExpiresAt, &a.Status, &ev, &a.Error, &a.ClaimedAt, &a.StartedAt, &fin); err != nil {
			return nil, err
		}
		a.Evidence = append(json.RawMessage(nil), ev...)
		if !fin.Equal(time.Unix(0, 0).UTC()) {
			a.FinishedAt = fin
		}
		out = append(out, a)
	}
	return out, rows.Err()
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
	rows, err := r.db.Query(contextOrBackground(ctx), `SELECT occurrence_id FROM refresh.schedule_occurrence WHERE project_id=$1 AND environment=$2 AND ($3='' OR generation_id=$3) ORDER BY nominal_time DESC,occurrence_id DESC LIMIT $4`, scope.ProjectID, scope.Environment, scope.GenerationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Occurrence
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		o, e := occurrenceByID(contextOrBackground(ctx), r.db, id)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (r *Repository) ListOperations(ctx context.Context, scope Scope, limit int) ([]Operation, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	rows, err := r.db.Query(contextOrBackground(ctx), `SELECT idempotency_key FROM refresh.operation WHERE project_id=$1 AND environment=$2 ORDER BY created_at DESC,operation_id DESC LIMIT $3`, scope.ProjectID, scope.Environment, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		o, e := r.Operation(contextOrBackground(ctx), scope.ProjectID, scope.Environment, key)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
