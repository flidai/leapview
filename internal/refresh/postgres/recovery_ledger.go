package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	refreshdb "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
	"github.com/flidai/leapview/internal/refresh/recovery"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RecoveryLedger is the PostgreSQL authority for scheduled UBDR qualification
// state. It deliberately contains no scenario execution or provider restore
// behavior.
type RecoveryLedger struct{ db DBTX }

func NewRecoveryLedger(db DBTX) *RecoveryLedger { return &RecoveryLedger{db: db} }

// DB returns the native handle solely so production composition can prove
// that every persistence surface belongs to the same PostgreSQL authority.
func (r *RecoveryLedger) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

func (r *RecoveryLedger) configured() bool { return r != nil && nativeDBConfigured(r.db) }

func (r *RecoveryLedger) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if !r.configured() {
		return fmt.Errorf("recovery qualification database is required")
	}
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("recovery qualification database must support transactions")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *RecoveryLedger) ReconcileSchedule(ctx context.Context, d recovery.Definition, now time.Time) error {
	if !r.configured() {
		return fmt.Errorf("recovery qualification database is required")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("recovery qualification reconciliation time is required")
	}
	schedule, err := refreshschedule.ParseSchedule(d.Cron, d.Timezone)
	if err != nil {
		return err
	}
	next := schedule.Next(now)
	if next.IsZero() {
		return fmt.Errorf("recovery qualification schedule has no next occurrence")
	}
	revision, err := recovery.ScheduleRevisionID(d)
	if err != nil {
		return err
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		active, err := queries.GetActiveRecoveryQualificationSchedule(ctx, d.ScheduleID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && active.ScheduleRevisionID == revision {
			return queries.SetRecoveryQualificationScheduleEnabled(ctx, refreshdb.SetRecoveryQualificationScheduleEnabledParams{Enabled: d.Enabled, UpdatedAt: now.UTC(), ScheduleRevisionID: revision})
		}
		if err == nil {
			if active.Enabled {
				complete, err := r.materializeSchedule(ctx, tx, active.ScheduleRevisionID, now, 1000)
				if err != nil {
					return err
				}
				if !complete {
					return nil
				}
			}
			if err := queries.CloseRecoveryQualificationSchedule(ctx, refreshdb.CloseRecoveryQualificationScheduleParams{ClosedAt: timestamp(now), ScheduleRevisionID: active.ScheduleRevisionID}); err != nil {
				return err
			}
		}
		return queries.InsertRecoveryQualificationSchedule(ctx, refreshdb.InsertRecoveryQualificationScheduleParams{ScheduleRevisionID: revision, ScheduleID: d.ScheduleID, Scenario: d.Scenario, Operation: d.Operation, PolicyVersion: d.PolicyVersion, PolicySha256: d.PolicySHA256, TargetScope: d.TargetScope, ArtifactIdentity: d.ArtifactIdentity, Cron: d.Cron, Timezone: d.Timezone, StaleAfter: d.StaleAfter, NextRunAt: next, Enabled: d.Enabled, ValidFrom: now.UTC()})
	})
}

func (r *RecoveryLedger) ReconcileSchedules(ctx context.Context, definitions []recovery.Definition, now time.Time) error {
	seen := make(map[string]struct{}, len(definitions))
	for _, d := range definitions {
		if err := d.Validate(); err != nil {
			return err
		}
		if _, ok := seen[d.ScheduleID]; ok {
			return fmt.Errorf("recovery qualification schedule %q is duplicated", d.ScheduleID)
		}
		seen[d.ScheduleID] = struct{}{}
	}
	for _, d := range definitions {
		if err := r.ReconcileSchedule(ctx, d, now); err != nil {
			return err
		}
	}
	rows, err := refreshdb.New(r.db).ListActiveRecoveryQualificationSchedules(ctx)
	if err != nil {
		return err
	}
	var missing []recovery.Definition
	for _, row := range rows {
		d := recovery.Definition{ScheduleID: row.ScheduleID, Scenario: row.Scenario, Operation: row.Operation, PolicyVersion: row.PolicyVersion, PolicySHA256: row.PolicySha256, TargetScope: row.TargetScope, ArtifactIdentity: row.ArtifactIdentity, Cron: row.Cron, Timezone: row.Timezone, StaleAfter: row.StaleAfter}
		if _, ok := seen[d.ScheduleID]; !ok {
			missing = append(missing, d)
		}
	}
	for _, d := range missing {
		d.Enabled = false
		if err := r.ReconcileSchedule(ctx, d, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *RecoveryLedger) Enqueue(ctx context.Context, input recovery.EnqueueInput, now time.Time) (recovery.Occurrence, bool, error) {
	if !r.configured() {
		return recovery.Occurrence{}, false, fmt.Errorf("recovery qualification database is required")
	}
	if now.IsZero() {
		return recovery.Occurrence{}, false, fmt.Errorf("recovery qualification enqueue time is required")
	}
	var out recovery.Occurrence
	var created bool
	err := r.withTx(ctx, func(tx pgx.Tx) error { var err error; out, created, err = r.enqueue(ctx, tx, input, now); return err })
	return out, created, err
}

func (r *RecoveryLedger) enqueue(ctx context.Context, tx pgx.Tx, in recovery.EnqueueInput, now time.Time) (recovery.Occurrence, bool, error) {
	rev, err := recovery.ScheduleRevisionForInput(in)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	in.ScheduleRevision = rev
	id, err := recovery.OccurrenceID(in)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	digest, err := recovery.RequestDigest(in)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	created, err := refreshdb.New(tx).InsertRecoveryQualificationOccurrence(ctx, refreshdb.InsertRecoveryQualificationOccurrenceParams{OccurrenceID: id, RequestDigest: digest, ScheduleID: in.ScheduleID, ScheduleRevisionID: rev, Scenario: in.Scenario, Operation: in.Operation, PolicyVersion: in.PolicyVersion, PolicySha256: in.PolicySHA256, TargetScope: in.TargetScope, ArtifactIdentity: in.ArtifactIdentity, PlannedAt: in.PlannedAt.UTC(), ExpiresAt: in.PlannedAt.Add(in.StaleAfter).UTC(), CreatedAt: now.UTC()})
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	out, storedDigest, err := readOccurrence(ctx, tx, id)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	if storedDigest != digest {
		return recovery.Occurrence{}, false, recovery.ErrConflict
	}
	return out, created == 1, nil
}

func (r *RecoveryLedger) materializeSchedule(ctx context.Context, tx pgx.Tx, revision string, now time.Time, limit int) (bool, error) {
	queries := refreshdb.New(tx)
	row, err := queries.GetRecoveryQualificationScheduleForMaterialize(ctx, revision)
	if err != nil {
		return false, err
	}
	if !row.Enabled {
		return true, nil
	}
	d := recovery.Definition{ScheduleID: row.ScheduleID, Scenario: row.Scenario, Operation: row.Operation, PolicyVersion: row.PolicyVersion, PolicySHA256: row.PolicySha256, TargetScope: row.TargetScope, ArtifactIdentity: row.ArtifactIdentity, Cron: row.Cron, Timezone: row.Timezone, StaleAfter: row.StaleAfter, Enabled: row.Enabled}
	next := row.NextRunAt
	parsed, err := refreshschedule.ParseSchedule(d.Cron, d.Timezone)
	if err != nil {
		return false, err
	}
	for count := 0; !next.After(now) && count < limit; count++ {
		_, _, err = r.enqueue(ctx, tx, recovery.EnqueueInput{ScheduleID: d.ScheduleID, ScheduleRevision: revision, Scenario: d.Scenario, Operation: d.Operation, PolicyVersion: d.PolicyVersion, PolicySHA256: d.PolicySHA256, TargetScope: d.TargetScope, ArtifactIdentity: d.ArtifactIdentity, PlannedAt: next, StaleAfter: d.StaleAfter}, now)
		if err != nil {
			return false, err
		}
		next = parsed.Next(next)
		if next.IsZero() {
			return false, fmt.Errorf("recovery qualification schedule %q has no next occurrence", d.ScheduleID)
		}
	}
	changed, err := queries.AdvanceRecoveryQualificationSchedule(ctx, refreshdb.AdvanceRecoveryQualificationScheduleParams{NextRunAt: next, UpdatedAt: now.UTC(), ScheduleRevisionID: revision, NextRunAt_2: row.NextRunAt})
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, fmt.Errorf("recovery qualification schedule %q changed while materializing", d.ScheduleID)
	}
	return next.After(now), nil
}

func (r *RecoveryLedger) EnqueueDue(ctx context.Context, now time.Time, limit int) ([]recovery.Occurrence, error) {
	if now.IsZero() || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("recovery qualification enqueue time and limit between 1 and 1000 are required")
	}
	var result []recovery.Occurrence
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		rows, err := queries.ListDueRecoveryQualificationSchedules(ctx, now.UTC())
		if err != nil {
			return err
		}
		cursor, err := queries.GetRecoveryQualificationEnqueueCursor(ctx)
		if err != nil {
			return err
		}
		type dueCursor struct {
			row      refreshdb.ListDueRecoveryQualificationSchedulesRow
			schedule refreshschedule.Schedule
			planned  time.Time
		}
		cursors := make([]dueCursor, 0, len(rows))
		for _, row := range rows {
			parsed, err := refreshschedule.ParseSchedule(row.Cron, row.Timezone)
			if err != nil {
				return err
			}
			cursors = append(cursors, dueCursor{row: row, schedule: parsed, planned: row.NextRunAt})
		}
		start := sort.Search(len(cursors), func(i int) bool {
			if cursors[i].row.ScheduleID != cursor.LastScheduleID {
				return cursors[i].row.ScheduleID > cursor.LastScheduleID
			}
			return cursors[i].row.ScheduleRevisionID > cursor.LastScheduleRevisionID
		})
		if start == len(cursors) {
			start = 0
		}
		if start > 0 {
			cursors = append(append(make([]dueCursor, 0, len(cursors)), cursors[start:]...), cursors[:start]...)
		}
		processed := 0
		lastID, lastRevision := "", ""
		for processed < limit {
			advanced := false
			for i := range cursors {
				if processed >= limit {
					break
				}
				c := &cursors[i]
				if c.planned.After(now) {
					continue
				}
				o, created, err := r.enqueue(ctx, tx, recovery.EnqueueInput{ScheduleID: c.row.ScheduleID, ScheduleRevision: c.row.ScheduleRevisionID, Scenario: c.row.Scenario, Operation: c.row.Operation, PolicyVersion: c.row.PolicyVersion, PolicySHA256: c.row.PolicySha256, TargetScope: c.row.TargetScope, ArtifactIdentity: c.row.ArtifactIdentity, PlannedAt: c.planned, StaleAfter: c.row.StaleAfter}, now)
				if err != nil {
					return err
				}
				next := c.schedule.Next(c.planned)
				if next.IsZero() {
					return fmt.Errorf("recovery qualification schedule %q has no next occurrence", c.row.ScheduleID)
				}
				changed, err := queries.AdvanceRecoveryQualificationSchedule(ctx, refreshdb.AdvanceRecoveryQualificationScheduleParams{NextRunAt: next, UpdatedAt: now.UTC(), ScheduleRevisionID: c.row.ScheduleRevisionID, NextRunAt_2: c.planned})
				if err != nil {
					return err
				}
				if changed != 1 {
					return fmt.Errorf("recovery qualification schedule %q changed while enqueueing", c.row.ScheduleID)
				}
				if created {
					result = append(result, o)
				}
				processed++
				advanced = true
				lastID, lastRevision = c.row.ScheduleID, c.row.ScheduleRevisionID
				c.planned = next
			}
			if !advanced {
				break
			}
		}
		if processed > 0 {
			changed, err := queries.UpdateRecoveryQualificationEnqueueCursor(ctx, refreshdb.UpdateRecoveryQualificationEnqueueCursorParams{LastScheduleID: lastID, LastScheduleRevisionID: lastRevision, UpdatedAt: now.UTC()})
			if err != nil {
				return err
			}
			if changed != 1 {
				return fmt.Errorf("recovery qualification enqueue fairness cursor is unavailable")
			}
		}
		return nil
	})
	return result, err
}

func (r *RecoveryLedger) ClaimNext(ctx context.Context, in recovery.ClaimInput) (recovery.Occurrence, bool, error) {
	if err := in.Validate(); err != nil {
		return recovery.Occurrence{}, false, err
	}
	var out recovery.Occurrence
	var ok bool
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		if err := reclaimExecution(ctx, tx, in.Now); err != nil {
			return err
		}
		if err := queries.ExpirePendingRecoveryQualificationOccurrences(ctx, timestamp(in.Now)); err != nil {
			return err
		}
		id, err := queries.NextPendingRecoveryQualificationOccurrence(ctx, in.Now.UTC())
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		expires := in.Now.Add(in.Lease).UTC()
		claim, err := queries.ClaimRecoveryQualificationOccurrence(ctx, refreshdb.ClaimRecoveryQualificationOccurrenceParams{ClaimedAt: timestamp(in.Now), LeaseOwner: in.WorkerID, LeaseExpiresAt: timestamp(expires), Actor: in.Actor, OccurrenceID: id})
		if err != nil {
			return err
		}
		if err := queries.InsertRecoveryQualificationAttempt(ctx, refreshdb.InsertRecoveryQualificationAttemptParams{OccurrenceID: id, AttemptNumber: claim.AttemptCount, FenceGeneration: claim.FenceGeneration, WorkerID: in.WorkerID, Actor: in.Actor, ClaimedAt: in.Now.UTC(), LeaseExpiresAt: expires}); err != nil {
			return err
		}
		out, _, err = readOccurrence(ctx, tx, id)
		ok = err == nil
		return err
	})
	return out, ok, err
}

func reclaimExecution(ctx context.Context, tx pgx.Tx, now time.Time) error {
	queries := refreshdb.New(tx)
	rows, err := queries.ListExpiredRecoveryQualificationLeases(ctx, timestamp(now))
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := queries.AbandonRecoveryQualificationAttempt(ctx, refreshdb.AbandonRecoveryQualificationAttemptParams{FinishedAt: timestamp(now), OccurrenceID: row.OccurrenceID, FenceGeneration: row.FenceGeneration}); err != nil {
			return err
		}
		if err := queries.RequeueRecoveryQualificationOccurrence(ctx, refreshdb.RequeueRecoveryQualificationOccurrenceParams{OccurrenceID: row.OccurrenceID, FenceGeneration: row.FenceGeneration}); err != nil {
			return err
		}
	}
	return nil
}

func validateMutation(id string, f recovery.Fence, now time.Time) error {
	if id == "" || now.IsZero() {
		return fmt.Errorf("recovery qualification occurrence id and mutation time are required")
	}
	return f.Validate()
}
func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
func optionalTimestamp(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timestamp(value)
}
func optionalAge(value, now time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	age := max(0, int64(now.Sub(value)/time.Second))
	return &age
}
func changed(count int64, err error) error {
	if err != nil {
		return err
	}
	if count != 1 {
		return recovery.ErrFenced
	}
	return nil
}

func (r *RecoveryLedger) Start(ctx context.Context, id string, f recovery.Fence, now time.Time) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		count, err := queries.StartRecoveryQualificationOccurrence(ctx, refreshdb.StartRecoveryQualificationOccurrenceParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, StartedAt: timestamp(now)})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.StartRecoveryQualificationAttempt(ctx, refreshdb.StartRecoveryQualificationAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, StartedAt: timestamp(now)})
		return changed(count, err)
	})
}

func (r *RecoveryLedger) Heartbeat(ctx context.Context, id string, f recovery.Fence, now time.Time, lease time.Duration) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	if lease <= 0 {
		return fmt.Errorf("recovery qualification heartbeat lease must be positive")
	}
	expires := now.Add(lease).UTC()
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		count, err := queries.HeartbeatRecoveryQualificationOccurrence(ctx, refreshdb.HeartbeatRecoveryQualificationOccurrenceParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, LeaseExpiresAt: timestamp(now), LeaseExpiresAt_2: timestamp(expires)})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.HeartbeatRecoveryQualificationAttempt(ctx, refreshdb.HeartbeatRecoveryQualificationAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, LeaseExpiresAt: expires})
		return changed(count, err)
	})
}

func (r *RecoveryLedger) RecordPhase(ctx context.Context, id string, f recovery.Fence, phase, event string, now time.Time) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	queries := refreshdb.New(r.db)
	var count int64
	var err error
	switch phase + "/" + event {
	case "restore/started":
		count, err = queries.StartRecoveryRestorePhase(ctx, refreshdb.StartRecoveryRestorePhaseParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, RestoreStartedAt: timestamp(now)})
	case "restore/completed":
		count, err = queries.CompleteRecoveryRestorePhase(ctx, refreshdb.CompleteRecoveryRestorePhaseParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, RestoreCompletedAt: timestamp(now)})
	case "readiness/started":
		count, err = queries.StartRecoveryReadinessPhase(ctx, refreshdb.StartRecoveryReadinessPhaseParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, ReadinessStartedAt: timestamp(now)})
	case "readiness/completed":
		count, err = queries.CompleteRecoveryReadinessPhase(ctx, refreshdb.CompleteRecoveryReadinessPhaseParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, ReadinessCompletedAt: timestamp(now)})
	default:
		return fmt.Errorf("unsupported recovery qualification phase transition %s/%s", phase, event)
	}
	return changed(count, err)
}

func phaseDurations(o recovery.Occurrence, now time.Time, required bool) (time.Duration, time.Duration, time.Duration, error) {
	if o.StartedAt.IsZero() || now.Before(o.StartedAt) {
		if required {
			return 0, 0, 0, fmt.Errorf("recovery qualification persisted execution phase is incomplete")
		}
		return 0, 0, 0, nil
	}
	total := now.Sub(o.StartedAt)
	phase := func(start, end time.Time, label string) (time.Duration, error) {
		if start.IsZero() || end.IsZero() {
			if required {
				return 0, fmt.Errorf("recovery qualification %s phase is incomplete", label)
			}
			return 0, nil
		}
		if start.Before(o.StartedAt) || end.Before(start) || end.After(now) {
			return 0, fmt.Errorf("recovery qualification %s phase chronology is invalid", label)
		}
		return end.Sub(start), nil
	}
	switch o.Operation {
	case recovery.OperationRestore:
		d, e := phase(o.RestoreStartedAt, o.RestoreCompletedAt, "restore")
		return d, 0, total, e
	case recovery.OperationUpgrade, recovery.OperationRollback:
		d, e := phase(o.ReadinessStartedAt, o.ReadinessCompletedAt, "readiness")
		return 0, d, total, e
	default:
		return 0, 0, total, nil
	}
}

func (r *RecoveryLedger) Complete(ctx context.Context, id string, f recovery.Fence, now time.Time, result recovery.Result) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	if err := result.Validate(now); err != nil {
		return err
	}
	encoded, err := recovery.EncodeEvidenceReferences(result.Evidence)
	if err != nil {
		return err
	}
	var refs any
	if err = json.Unmarshal([]byte(encoded), &refs); err != nil {
		return err
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		o, _, err := readOccurrence(ctx, tx, id)
		if err != nil {
			return err
		}
		rd, qd, total, err := phaseDurations(o, now, true)
		if err != nil {
			return err
		}
		pointAge, rdms, qdms, totalms := max(0, int64(now.Sub(result.RecoveryPointAt)/time.Second)), rd.Milliseconds(), qd.Milliseconds(), total.Milliseconds()
		count, err := queries.CompleteRecoveryQualificationOccurrence(ctx, refreshdb.CompleteRecoveryQualificationOccurrenceParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, FinishedAt: timestamp(now), RecoveryPointAt: timestamp(result.RecoveryPointAt), RecoveryPointAgeSeconds: &pointAge, RestoreDurationMillis: &rdms, ReadinessDurationMillis: &qdms, QualificationDurationMillis: &totalms, EvidenceRefs: []byte(encoded)})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.CompleteRecoveryQualificationAttempt(ctx, refreshdb.CompleteRecoveryQualificationAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, FinishedAt: timestamp(now)})
		return changed(count, err)
	})
}

func validateFailureResult(result recovery.Result, now time.Time) error {
	if !result.RecoveryPointAt.IsZero() && result.RecoveryPointAt.After(now) {
		return fmt.Errorf("recovery point must not be after completion")
	}
	if result.RestoreDuration != 0 || result.ReadinessDuration != 0 {
		return fmt.Errorf("recovery qualification durations are owned by persisted ledger phases")
	}
	_, err := recovery.CanonicalEvidenceReferences(result.Evidence)
	return err
}
func (r *RecoveryLedger) Fail(ctx context.Context, id string, f recovery.Fence, now time.Time, result recovery.Result, cause error) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("recovery qualification failure reason is required")
	}
	if err := validateFailureResult(result, now); err != nil {
		return err
	}
	encoded, err := recovery.EncodeEvidenceReferences(result.Evidence)
	if err != nil {
		return err
	}
	code, reason := recovery.FailureDetails(cause, "qualification_failed")
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		o, _, err := readOccurrence(ctx, tx, id)
		if err != nil {
			return err
		}
		rd, qd, total, err := phaseDurations(o, now, false)
		if err != nil {
			return err
		}
		eStatus := "none"
		var retry time.Time
		if len(result.Evidence) > 0 {
			eStatus = "pending"
			retry = now.UTC()
		}
		rdms, qdms, totalms := rd.Milliseconds(), qd.Milliseconds(), total.Milliseconds()
		count, err := queries.FailRecoveryQualificationOccurrence(ctx, refreshdb.FailRecoveryQualificationOccurrenceParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, FinishedAt: timestamp(now), RecoveryPointAt: optionalTimestamp(result.RecoveryPointAt), RecoveryPointAgeSeconds: optionalAge(result.RecoveryPointAt, now), RestoreDurationMillis: &rdms, ReadinessDurationMillis: &qdms, QualificationDurationMillis: &totalms, EvidenceRefs: []byte(encoded), EvidenceStatus: eStatus, EvidenceNextAttemptAt: optionalTimestamp(retry), FailureReasonRedacted: reason, FailureCode: code})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.FailRecoveryQualificationAttempt(ctx, refreshdb.FailRecoveryQualificationAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, FinishedAt: timestamp(now), FailureReasonRedacted: reason, FailureCode: code})
		return changed(count, err)
	})
}

func (r *RecoveryLedger) Cancel(ctx context.Context, id string, f recovery.Fence, now time.Time, cause error) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("recovery qualification cancellation reason is required")
	}
	code, reason := recovery.FailureDetails(cause, "qualification_canceled")
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		o, _, err := readOccurrence(ctx, tx, id)
		if err != nil {
			return err
		}
		_, _, total, err := phaseDurations(o, now, false)
		if err != nil {
			return err
		}
		totalms := total.Milliseconds()
		count, err := queries.CancelRecoveryQualificationOccurrence(ctx, refreshdb.CancelRecoveryQualificationOccurrenceParams{OccurrenceID: id, LeaseOwner: f.Owner, FenceGeneration: f.Generation, FinishedAt: timestamp(now), QualificationDurationMillis: &totalms, FailureReasonRedacted: reason, FailureCode: code})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.CancelRecoveryQualificationAttempt(ctx, refreshdb.CancelRecoveryQualificationAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, FinishedAt: timestamp(now), FailureReasonRedacted: reason, FailureCode: code})
		return changed(count, err)
	})
}

func retryDelay(attempt int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Minute
	for i := int64(1); i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	if d > time.Hour {
		return time.Hour
	}
	return d
}
func (r *RecoveryLedger) ClaimEvidence(ctx context.Context, publisher string, now time.Time, lease time.Duration) (recovery.Occurrence, bool, error) {
	if err := (recovery.ClaimInput{WorkerID: publisher, Actor: publisher, Now: now, Lease: lease}).Validate(); err != nil {
		return recovery.Occurrence{}, false, err
	}
	var out recovery.Occurrence
	var ok bool
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		if err := reclaimEvidence(ctx, tx, now); err != nil {
			return err
		}
		id, err := queries.NextPendingRecoveryEvidence(ctx, timestamp(now))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		expires := now.Add(lease).UTC()
		claim, err := queries.ClaimRecoveryEvidence(ctx, refreshdb.ClaimRecoveryEvidenceParams{OccurrenceID: id, EvidenceLeaseOwner: publisher, EvidenceLeaseExpiresAt: timestamp(expires)})
		if err != nil {
			return err
		}
		if err := queries.InsertRecoveryEvidenceAttempt(ctx, refreshdb.InsertRecoveryEvidenceAttemptParams{OccurrenceID: id, AttemptNumber: claim.EvidenceAttemptCount, FenceGeneration: claim.EvidenceFenceGeneration, PublisherID: publisher, ClaimedAt: now.UTC(), LeaseExpiresAt: expires}); err != nil {
			return err
		}
		out, _, err = readOccurrence(ctx, tx, id)
		ok = err == nil
		return err
	})
	return out, ok, err
}
func reclaimEvidence(ctx context.Context, tx pgx.Tx, now time.Time) error {
	queries := refreshdb.New(tx)
	rows, err := queries.ListExpiredRecoveryEvidenceLeases(ctx, timestamp(now))
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := queries.AbandonRecoveryEvidenceAttempt(ctx, refreshdb.AbandonRecoveryEvidenceAttemptParams{OccurrenceID: row.OccurrenceID, FenceGeneration: row.EvidenceFenceGeneration, FinishedAt: timestamp(now)}); err != nil {
			return err
		}
		if err := queries.RequeueRecoveryEvidence(ctx, refreshdb.RequeueRecoveryEvidenceParams{OccurrenceID: row.OccurrenceID, EvidenceFenceGeneration: row.EvidenceFenceGeneration, EvidenceNextAttemptAt: timestamp(now)}); err != nil {
			return err
		}
	}
	return nil
}

func (r *RecoveryLedger) PublishEvidence(ctx context.Context, id string, f recovery.Fence, now time.Time) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		count, err := queries.PublishRecoveryEvidence(ctx, refreshdb.PublishRecoveryEvidenceParams{OccurrenceID: id, EvidenceLeaseOwner: f.Owner, EvidenceFenceGeneration: f.Generation, EvidencePublishedAt: timestamp(now)})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.PublishRecoveryEvidenceAttempt(ctx, refreshdb.PublishRecoveryEvidenceAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, FinishedAt: timestamp(now)})
		return changed(count, err)
	})
}
func (r *RecoveryLedger) FailEvidence(ctx context.Context, id string, f recovery.Fence, now time.Time, cause error) error {
	if err := validateMutation(id, f, now); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("recovery evidence publication failure reason is required")
	}
	code, reason := recovery.FailureDetails(cause, "evidence_publication_failed")
	return r.withTx(ctx, func(tx pgx.Tx) error {
		queries := refreshdb.New(tx)
		n, err := queries.GetRecoveryEvidenceAttemptCount(ctx, id)
		if err != nil {
			return err
		}
		count, err := queries.FailRecoveryEvidence(ctx, refreshdb.FailRecoveryEvidenceParams{OccurrenceID: id, EvidenceLeaseOwner: f.Owner, EvidenceFenceGeneration: f.Generation, EvidenceLeaseExpiresAt: timestamp(now), EvidenceNextAttemptAt: timestamp(now.Add(retryDelay(n))), EvidenceFailureReasonRedacted: reason, EvidenceFailureCode: code})
		if err := changed(count, err); err != nil {
			return err
		}
		count, err = queries.FailRecoveryEvidenceAttempt(ctx, refreshdb.FailRecoveryEvidenceAttemptParams{OccurrenceID: id, FenceGeneration: f.Generation, FinishedAt: timestamp(now), FailureReasonRedacted: reason, FailureCode: code})
		return changed(count, err)
	})
}

func (r *RecoveryLedger) Occurrence(ctx context.Context, id string) (recovery.Occurrence, error) {
	if id == "" {
		return recovery.Occurrence{}, fmt.Errorf("recovery qualification occurrence id is required")
	}
	o, _, err := readOccurrence(ctx, r.db, id)
	return o, err
}
func (r *RecoveryLedger) Occurrences(ctx context.Context) ([]recovery.Occurrence, error) {
	ids, err := refreshdb.New(r.db).ListRecoveryQualificationOccurrenceIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []recovery.Occurrence
	for _, id := range ids {
		o, _, err := readOccurrence(ctx, r.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func readOccurrence(ctx context.Context, db DBTX, id string) (recovery.Occurrence, string, error) {
	row, err := refreshdb.New(db).GetRecoveryQualificationOccurrence(ctx, id)
	if err != nil {
		return recovery.Occurrence{}, "", err
	}
	o := recovery.Occurrence{ID: row.OccurrenceID, ScheduleID: row.ScheduleID, ScheduleRevision: row.ScheduleRevisionID, Scenario: row.Scenario, Operation: row.Operation, PolicyVersion: row.PolicyVersion, PolicySHA256: row.PolicySha256, TargetScope: row.TargetScope, ArtifactIdentity: row.ArtifactIdentity, PlannedAt: row.PlannedAt, ExpiresAt: row.ExpiresAt, Status: row.Status, Result: row.Result, AttemptCount: row.AttemptCount, Fence: recovery.Fence{Owner: row.LeaseOwner, Generation: row.FenceGeneration}, LeaseExpiresAt: pgTime(row.LeaseExpiresAt), Actor: row.Actor, CreatedAt: row.CreatedAt, ClaimedAt: pgTime(row.ClaimedAt), StartedAt: pgTime(row.StartedAt), RestoreStartedAt: pgTime(row.RestoreStartedAt), RestoreCompletedAt: pgTime(row.RestoreCompletedAt), ReadinessStartedAt: pgTime(row.ReadinessStartedAt), ReadinessCompletedAt: pgTime(row.ReadinessCompletedAt), FinishedAt: pgTime(row.FinishedAt), RecoveryPointAt: pgTime(row.RecoveryPointAt), RecoveryPointAgeSeconds: derefInt(row.RecoveryPointAgeSeconds), RestoreDurationMillis: derefInt(row.RestoreDurationMillis), ReadinessDurationMillis: derefInt(row.ReadinessDurationMillis), QualificationDurationMillis: derefInt(row.QualificationDurationMillis), FailureReasonRedacted: row.FailureReasonRedacted, FailureCode: row.FailureCode, EvidenceStatus: row.EvidenceStatus, EvidenceAttemptCount: row.EvidenceAttemptCount, EvidenceFence: recovery.Fence{Owner: row.EvidenceLeaseOwner, Generation: row.EvidenceFenceGeneration}, EvidenceLeaseExpiresAt: pgTime(row.EvidenceLeaseExpiresAt), EvidenceNextAttemptAt: pgTime(row.EvidenceNextAttemptAt), EvidencePublishedAt: pgTime(row.EvidencePublishedAt), EvidenceFailureRedacted: row.EvidenceFailureReasonRedacted, EvidenceFailureCode: row.EvidenceFailureCode}
	if err := json.Unmarshal(row.EvidenceRefs, &o.Evidence); err != nil {
		return recovery.Occurrence{}, "", err
	}
	if o.Evidence == nil {
		o.Evidence = []recovery.EvidenceReference{}
	}
	return o, row.RequestDigest, nil
}

func pgTime(v pgtype.Timestamptz) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return v.Time.UTC()
}
func derefInt(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func (r *RecoveryLedger) Attempts(ctx context.Context, id string) ([]recovery.Attempt, error) {
	rows, err := refreshdb.New(r.db).ListRecoveryQualificationAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]recovery.Attempt, 0, len(rows))
	for _, row := range rows {
		out = append(out, recovery.Attempt{OccurrenceID: row.OccurrenceID, AttemptNumber: row.AttemptNumber, FenceGeneration: row.FenceGeneration, WorkerID: row.WorkerID, Actor: row.Actor, Status: row.Status, ClaimedAt: row.ClaimedAt, StartedAt: pgTime(row.StartedAt), LeaseExpiresAt: row.LeaseExpiresAt, FinishedAt: pgTime(row.FinishedAt), FailureReasonRedacted: row.FailureReasonRedacted, FailureCode: row.FailureCode})
	}
	return out, nil
}

func (r *RecoveryLedger) EvidenceAttempts(ctx context.Context, id string) ([]recovery.EvidenceAttempt, error) {
	rows, err := refreshdb.New(r.db).ListRecoveryEvidenceAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]recovery.EvidenceAttempt, 0, len(rows))
	for _, row := range rows {
		out = append(out, recovery.EvidenceAttempt{OccurrenceID: row.OccurrenceID, AttemptNumber: row.AttemptNumber, FenceGeneration: row.FenceGeneration, PublisherID: row.PublisherID, Status: row.Status, ClaimedAt: row.ClaimedAt, LeaseExpiresAt: row.LeaseExpiresAt, FinishedAt: pgTime(row.FinishedAt), FailureReasonRedacted: row.FailureReasonRedacted, FailureCode: row.FailureCode})
	}
	return out, nil
}

func terminal(s string) bool {
	return s == recovery.StatusSucceeded || s == recovery.StatusFailed || s == recovery.StatusCanceled || s == recovery.StatusExpired
}
func newer(a, b recovery.Occurrence) bool {
	return b.ID == "" || a.FinishedAt.After(b.FinishedAt) || (a.FinishedAt.Equal(b.FinishedAt) && a.ID > b.ID)
}
func (r *RecoveryLedger) Status(ctx context.Context, now time.Time) (recovery.StatusSnapshot, error) {
	if now.IsZero() {
		return recovery.StatusSnapshot{}, fmt.Errorf("recovery qualification status time is required")
	}
	occ, err := r.Occurrences(ctx)
	if err != nil {
		return recovery.StatusSnapshot{}, err
	}
	s := recovery.StatusSnapshot{GeneratedAt: now}
	rows, err := refreshdb.New(r.db).ListRecoveryQualificationScheduleStatus(ctx)
	if err != nil {
		return s, err
	}
	for _, row := range rows {
		s.ConfiguredSchedules++
		parsed, err := refreshschedule.ParseSchedule(row.Cron, row.Timezone)
		if err != nil {
			return s, err
		}
		next := row.NextRunAt
		for i := 0; !next.After(now); i++ {
			if i >= 10000 {
				return s, fmt.Errorf("recovery qualification schedule has more than 10000 missing runs")
			}
			s.MissingRuns++
			s.Due++
			if !next.Add(row.StaleAfter).After(now) {
				s.Overdue++
			}
			next = parsed.Next(next)
		}
	}
	s.Unconfigured = s.ConfiguredSchedules == 0
	by := map[string]*recovery.OperationStatus{}
	last := map[string]recovery.Occurrence{}
	for _, op := range []string{recovery.OperationBackup, recovery.OperationRestore, recovery.OperationUpgrade, recovery.OperationRollback} {
		by[op] = &recovery.OperationStatus{Operation: op}
	}
	for _, o := range occ {
		v := by[o.Operation]
		switch o.Status {
		case recovery.StatusPending:
			v.Pending++
			if !o.PlannedAt.After(now) {
				s.Due++
			}
			if !o.ExpiresAt.After(now) {
				s.Overdue++
			}
		case recovery.StatusClaimed, recovery.StatusRunning:
			if !o.LeaseExpiresAt.After(now) {
				s.StaleExecutionLeases++
				s.Overdue++
				s.Failed++
				v.Failed++
			} else {
				s.Running++
				v.Running++
			}
		case recovery.StatusFailed:
			v.Failed++
			s.Failed++
		case recovery.StatusExpired:
			v.Expired++
			s.Failed++
		case recovery.StatusSucceeded:
			if newer(o, last[o.Operation]) {
				last[o.Operation] = o
			}
		}
		if terminal(o.Status) {
			switch o.EvidenceStatus {
			case "claimed":
				if !o.EvidenceLeaseExpiresAt.After(now) {
					s.StaleEvidenceLeases++
					s.EvidenceFailed++
				} else {
					s.EvidencePending++
				}
			case "pending":
				s.EvidencePending++
			case "failed":
				s.EvidenceFailed++
			}
		}
	}
	s.RecoveredExpiredLeases, err = refreshdb.New(r.db).CountAbandonedRecoveryQualificationAttempts(ctx)
	if err != nil {
		return s, err
	}
	for _, op := range []string{recovery.OperationBackup, recovery.OperationRestore, recovery.OperationUpgrade, recovery.OperationRollback} {
		v := by[op]
		if o := last[op]; !o.FinishedAt.IsZero() {
			age := max(0, int64(now.Sub(o.FinishedAt)/time.Second))
			rd, qd, total, point := o.RestoreDurationMillis, o.ReadinessDurationMillis, o.QualificationDurationMillis, o.RecoveryPointAgeSeconds
			v.LastSuccessAgeSeconds = &age
			v.LastRestoreDurationMillis = &rd
			v.LastReadinessDurationMillis = &qd
			v.LastQualificationDurationMillis = &total
			v.LastRecoveryPointAgeSeconds = &point
		}
		s.Operations = append(s.Operations, *v)
	}
	return s, nil
}

func (r *RecoveryLedger) Retain(ctx context.Context, p recovery.RetentionPolicy) (recovery.RetentionResult, error) {
	if p.Now.IsZero() || p.ComplianceWindow <= 0 {
		return recovery.RetentionResult{}, fmt.Errorf("recovery qualification retention time and positive compliance window are required")
	}
	cutoff := p.Now.Add(-p.ComplianceWindow)
	queries := refreshdb.New(r.db)
	deleted, err := queries.RetainRecoveryQualificationOccurrences(ctx, refreshdb.RetainRecoveryQualificationOccurrencesParams{PActiveAt: p.Now.UTC(), PFinishedBefore: cutoff.UTC(), PLimit: 1000})
	if err != nil {
		return recovery.RetentionResult{}, err
	}
	preserved, err := queries.ListRecoveryQualificationRetentionProtectedIDs(ctx, 1000)
	if err != nil {
		return recovery.RetentionResult{}, err
	}
	out := recovery.RetentionResult{DeletedIDs: deleted, PreservedIDs: preserved}
	sort.Strings(out.DeletedIDs)
	sort.Strings(out.PreservedIDs)
	return out, nil
}

var _ recovery.Repository = (*RecoveryLedger)(nil)
