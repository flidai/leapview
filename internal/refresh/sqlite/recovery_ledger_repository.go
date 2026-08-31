package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	platformdb "github.com/flidai/leapview/internal/refresh/internal/db"
	"github.com/flidai/leapview/internal/refresh/recovery"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const recoveryTimeLayout = "2006-01-02T15:04:05.000000000Z"

func (repository *Repository) ReconcileSchedule(ctx context.Context, definition recovery.Definition, now time.Time) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("recovery qualification database is required")
	}
	if err := definition.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("recovery qualification reconciliation time is required")
	}
	parsed, err := refreshschedule.ParseSchedule(definition.Cron, definition.Timezone)
	if err != nil {
		return err
	}
	next := parsed.Next(now)
	if next.IsZero() {
		return fmt.Errorf("recovery qualification schedule has no next occurrence")
	}
	revisionID, err := recovery.ScheduleRevisionID(definition)
	if err != nil {
		return err
	}
	enabled := int64(0)
	if definition.Enabled {
		enabled = 1
	}
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		active, activeErr := queries.GetActiveRecoveryQualificationSchedule(ctx, definition.ScheduleID)
		if activeErr == nil && active.ScheduleRevisionID == revisionID {
			if active.Enabled == 1 && enabled == 0 {
				if err := materializeClosedSchedule(ctx, queries, active, now); err != nil {
					return err
				}
			}
			changed, updateErr := queries.UpdateRecoveryQualificationScheduleEnabled(ctx, platformdb.UpdateRecoveryQualificationScheduleEnabledParams{
				Enabled: enabled, UpdatedAt: recoveryFormatTime(now), ScheduleRevisionID: revisionID,
			})
			return recoveryChanged(changed, updateErr)
		}
		if activeErr != nil && !errors.Is(activeErr, sql.ErrNoRows) {
			return activeErr
		}
		if activeErr == nil {
			if active.Enabled == 1 {
				if err := materializeClosedSchedule(ctx, queries, active, now); err != nil {
					return err
				}
			}
			changed, closeErr := queries.CloseRecoveryQualificationSchedule(ctx, platformdb.CloseRecoveryQualificationScheduleParams{
				ClosedAt: recoveryNullTime(now), UpdatedAt: recoveryFormatTime(now), ScheduleRevisionID: active.ScheduleRevisionID,
			})
			if closeErr != nil || changed != 1 {
				return recoveryChanged(changed, closeErr)
			}
		}
		return queries.InsertRecoveryQualificationSchedule(ctx, platformdb.InsertRecoveryQualificationScheduleParams{
			ScheduleRevisionID: revisionID, ScheduleID: definition.ScheduleID,
			Scenario: definition.Scenario, Operation: definition.Operation,
			PolicyVersion: definition.PolicyVersion, PolicySha256: definition.PolicySHA256, TargetScope: definition.TargetScope,
			ArtifactIdentity: definition.ArtifactIdentity, Cron: definition.Cron, Timezone: definition.Timezone,
			StaleAfterSeconds: int64(definition.StaleAfter / time.Second), NextRunAt: recoveryFormatTime(next),
			Enabled: enabled, ValidFrom: recoveryFormatTime(now), UpdatedAt: recoveryFormatTime(now),
		})
	})
}

func (repository *Repository) ReconcileSchedules(ctx context.Context, definitions []recovery.Definition, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("recovery qualification reconciliation time is required")
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[definition.ScheduleID]; duplicate {
			return fmt.Errorf("recovery qualification schedule %q is duplicated", definition.ScheduleID)
		}
		seen[definition.ScheduleID] = struct{}{}
	}
	for _, definition := range definitions {
		if err := repository.ReconcileSchedule(ctx, definition, now); err != nil {
			return err
		}
	}
	schedules, err := repository.q.ListRecoveryQualificationSchedules(ctx)
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		if schedule.ClosedAt.Valid || schedule.Enabled != 1 {
			continue
		}
		if _, exists := seen[schedule.ScheduleID]; exists {
			continue
		}
		definition := recovery.Definition{
			ScheduleID: schedule.ScheduleID, Scenario: schedule.Scenario, Operation: schedule.Operation,
			PolicyVersion: schedule.PolicyVersion, PolicySHA256: schedule.PolicySha256, TargetScope: schedule.TargetScope,
			ArtifactIdentity: schedule.ArtifactIdentity, Cron: schedule.Cron, Timezone: schedule.Timezone,
			StaleAfter: time.Duration(schedule.StaleAfterSeconds) * time.Second, Enabled: false,
		}
		if err := repository.ReconcileSchedule(ctx, definition, now); err != nil {
			return err
		}
	}
	return nil
}

func materializeClosedSchedule(ctx context.Context, queries *platformdb.Queries, row platformdb.RecoveryQualificationSchedule, now time.Time) error {
	plannedAt, err := recoveryParseTime(row.NextRunAt)
	if err != nil {
		return err
	}
	parsed, err := refreshschedule.ParseSchedule(row.Cron, row.Timezone)
	if err != nil {
		return err
	}
	for count := 0; !plannedAt.After(now); count++ {
		if count >= 1000 {
			return fmt.Errorf("recovery qualification schedule %q has more than 1000 missed runs; reconcile them before changing immutable intent", row.ScheduleID)
		}
		_, _, err := enqueueRecoveryOccurrence(ctx, queries, recovery.EnqueueInput{
			ScheduleID: row.ScheduleID, ScheduleRevision: row.ScheduleRevisionID,
			Scenario: row.Scenario, Operation: row.Operation, PolicyVersion: row.PolicyVersion, PolicySHA256: row.PolicySha256,
			TargetScope: row.TargetScope, ArtifactIdentity: row.ArtifactIdentity,
			PlannedAt: plannedAt, StaleAfter: time.Duration(row.StaleAfterSeconds) * time.Second,
		}, now)
		if err != nil {
			return err
		}
		next := parsed.Next(plannedAt)
		if next.IsZero() {
			return fmt.Errorf("recovery qualification schedule %q has no next occurrence", row.ScheduleID)
		}
		plannedAt = next
	}
	return nil
}

func (repository *Repository) Enqueue(ctx context.Context, input recovery.EnqueueInput, now time.Time) (recovery.Occurrence, bool, error) {
	if repository == nil || repository.db == nil {
		return recovery.Occurrence{}, false, fmt.Errorf("recovery qualification database is required")
	}
	if now.IsZero() {
		return recovery.Occurrence{}, false, fmt.Errorf("recovery qualification enqueue time is required")
	}
	return enqueueRecoveryOccurrence(ctx, repository.q, input, now)
}

func enqueueRecoveryOccurrence(ctx context.Context, queries *platformdb.Queries, input recovery.EnqueueInput, now time.Time) (recovery.Occurrence, bool, error) {
	revision, err := recovery.ScheduleRevisionForInput(input)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	input.ScheduleRevision = revision
	id, err := recovery.OccurrenceID(input)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	digest, err := recovery.RequestDigest(input)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	created, err := queries.InsertRecoveryQualificationOccurrence(ctx, platformdb.InsertRecoveryQualificationOccurrenceParams{
		OccurrenceID: id, RequestDigest: digest, ScheduleID: input.ScheduleID, ScheduleRevisionID: input.ScheduleRevision,
		Scenario: input.Scenario, Operation: input.Operation, PolicyVersion: input.PolicyVersion, PolicySha256: input.PolicySHA256,
		TargetScope: input.TargetScope, ArtifactIdentity: input.ArtifactIdentity,
		PlannedAt: recoveryFormatTime(input.PlannedAt), ExpiresAt: recoveryFormatTime(input.PlannedAt.Add(input.StaleAfter)),
		CreatedAt: recoveryFormatTime(now),
	})
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	if row.RequestDigest != digest {
		return recovery.Occurrence{}, false, recovery.ErrConflict
	}
	occurrence, err := recoveryOccurrenceFromRow(row)
	return occurrence, created == 1, err
}

func (repository *Repository) EnqueueDue(ctx context.Context, now time.Time, limit int) ([]recovery.Occurrence, error) {
	if repository == nil || repository.db == nil {
		return nil, fmt.Errorf("recovery qualification database is required")
	}
	if now.IsZero() || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("recovery qualification enqueue time and limit between 1 and 1000 are required")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	rows, err := queries.ListDueRecoveryQualificationSchedules(ctx, recoveryFormatTime(now))
	if err != nil {
		return nil, err
	}
	type dueScheduleCursor struct {
		row           platformdb.ListDueRecoveryQualificationSchedulesRow
		schedule      refreshschedule.Schedule
		plannedAt     time.Time
		previousRunAt string
	}
	cursors := make([]dueScheduleCursor, 0, len(rows))
	for _, row := range rows {
		plannedAt, err := recoveryParseTime(row.NextRunAt)
		if err != nil {
			return nil, err
		}
		parsed, err := refreshschedule.ParseSchedule(row.Cron, row.Timezone)
		if err != nil {
			return nil, err
		}
		cursors = append(cursors, dueScheduleCursor{
			row: row, schedule: parsed, plannedAt: plannedAt, previousRunAt: row.NextRunAt,
		})
	}
	sort.Slice(cursors, func(left, right int) bool {
		if cursors[left].row.ScheduleID != cursors[right].row.ScheduleID {
			return cursors[left].row.ScheduleID < cursors[right].row.ScheduleID
		}
		return cursors[left].row.ScheduleRevisionID < cursors[right].row.ScheduleRevisionID
	})
	fairnessCursor, err := queries.GetRecoveryQualificationEnqueueCursor(ctx)
	if err != nil {
		return nil, err
	}
	start := sort.Search(len(cursors), func(index int) bool {
		if cursors[index].row.ScheduleID != fairnessCursor.LastScheduleID {
			return cursors[index].row.ScheduleID > fairnessCursor.LastScheduleID
		}
		return cursors[index].row.ScheduleRevisionID > fairnessCursor.LastScheduleRevisionID
	})
	if start == len(cursors) {
		start = 0
	}
	if start > 0 {
		rotated := make([]dueScheduleCursor, 0, len(cursors))
		rotated = append(rotated, cursors[start:]...)
		rotated = append(rotated, cursors[:start]...)
		cursors = rotated
	}
	result := make([]recovery.Occurrence, 0, min(limit, len(rows)))
	lastScheduleID := ""
	lastScheduleRevisionID := ""
	for len(result) < limit {
		enqueued := false
		for index := range cursors {
			if len(result) >= limit {
				break
			}
			cursor := &cursors[index]
			if cursor.plannedAt.After(now) {
				continue
			}
			input := recovery.EnqueueInput{
				ScheduleID: cursor.row.ScheduleID, ScheduleRevision: cursor.row.ScheduleRevisionID,
				Scenario: cursor.row.Scenario, Operation: cursor.row.Operation,
				PolicyVersion: cursor.row.PolicyVersion, PolicySHA256: cursor.row.PolicySha256, TargetScope: cursor.row.TargetScope,
				ArtifactIdentity: cursor.row.ArtifactIdentity, PlannedAt: cursor.plannedAt,
				StaleAfter: time.Duration(cursor.row.StaleAfterSeconds) * time.Second,
			}
			occurrence, _, err := enqueueRecoveryOccurrence(ctx, queries, input, now)
			if err != nil {
				return nil, err
			}
			next := cursor.schedule.Next(cursor.plannedAt)
			if next.IsZero() {
				return nil, fmt.Errorf("recovery qualification schedule %q has no next occurrence", cursor.row.ScheduleID)
			}
			nextRunAt := recoveryFormatTime(next)
			advanced, err := queries.AdvanceRecoveryQualificationSchedule(ctx, platformdb.AdvanceRecoveryQualificationScheduleParams{
				NextRunAt: nextRunAt, UpdatedAt: recoveryFormatTime(now),
				ScheduleRevisionID: cursor.row.ScheduleRevisionID, PreviousRunAt: cursor.previousRunAt,
			})
			if err != nil {
				return nil, err
			}
			if advanced != 1 {
				return nil, fmt.Errorf("recovery qualification schedule %q changed while enqueueing", cursor.row.ScheduleID)
			}
			result = append(result, occurrence)
			lastScheduleID = cursor.row.ScheduleID
			lastScheduleRevisionID = cursor.row.ScheduleRevisionID
			cursor.plannedAt = next
			cursor.previousRunAt = nextRunAt
			enqueued = true
		}
		if !enqueued {
			break
		}
	}
	if len(result) > 0 {
		updated, err := queries.UpdateRecoveryQualificationEnqueueCursor(ctx, platformdb.UpdateRecoveryQualificationEnqueueCursorParams{
			LastScheduleID: lastScheduleID, LastScheduleRevisionID: lastScheduleRevisionID, UpdatedAt: recoveryFormatTime(now),
		})
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, fmt.Errorf("recovery qualification enqueue fairness cursor is unavailable")
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (repository *Repository) ClaimNext(ctx context.Context, input recovery.ClaimInput) (recovery.Occurrence, bool, error) {
	if repository == nil || repository.db == nil {
		return recovery.Occurrence{}, false, fmt.Errorf("recovery qualification database is required")
	}
	if err := input.Validate(); err != nil {
		return recovery.Occurrence{}, false, err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	if err := reclaimRecoveryLeases(ctx, queries, input.Now); err != nil {
		return recovery.Occurrence{}, false, err
	}
	if _, err := queries.ExpirePendingRecoveryQualificationOccurrences(ctx, platformdb.ExpirePendingRecoveryQualificationOccurrencesParams{
		FinishedAt: recoveryNullTime(input.Now), ExpiresAt: recoveryFormatTime(input.Now),
	}); err != nil {
		return recovery.Occurrence{}, false, err
	}
	id, err := queries.NextPendingRecoveryQualificationOccurrence(ctx, platformdb.NextPendingRecoveryQualificationOccurrenceParams{
		PlannedAt: recoveryFormatTime(input.Now), ExpiresAt: recoveryFormatTime(input.Now),
	})
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return recovery.Occurrence{}, false, err
		}
		return recovery.Occurrence{}, false, nil
	}
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	leaseExpiresAt := input.Now.Add(input.Lease)
	changed, err := queries.ClaimRecoveryQualificationOccurrence(ctx, platformdb.ClaimRecoveryQualificationOccurrenceParams{
		LeaseOwner: input.WorkerID, LeaseExpiresAt: recoveryNullTime(leaseExpiresAt), Actor: input.Actor,
		ClaimedAt: recoveryNullTime(input.Now), OccurrenceID: id,
		PlannedAt: recoveryFormatTime(input.Now), ExpiresAt: recoveryFormatTime(input.Now),
	})
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	if changed != 1 {
		if err := tx.Commit(); err != nil {
			return recovery.Occurrence{}, false, err
		}
		return recovery.Occurrence{}, false, nil
	}
	row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	if err := queries.InsertRecoveryQualificationAttempt(ctx, platformdb.InsertRecoveryQualificationAttemptParams{
		OccurrenceID: id, AttemptNumber: row.AttemptCount, FenceGeneration: row.FenceGeneration,
		WorkerID: input.WorkerID, Actor: input.Actor, ClaimedAt: recoveryFormatTime(input.Now),
		LeaseExpiresAt: recoveryFormatTime(leaseExpiresAt),
	}); err != nil {
		return recovery.Occurrence{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return recovery.Occurrence{}, false, err
	}
	occurrence, err := recoveryOccurrenceFromRow(row)
	return occurrence, err == nil, err
}

func reclaimRecoveryLeases(ctx context.Context, queries *platformdb.Queries, now time.Time) error {
	rows, err := queries.ListExpiredRecoveryQualificationLeases(ctx, recoveryNullTime(now))
	if err != nil {
		return err
	}
	for _, row := range rows {
		changed, err := queries.AbandonRecoveryQualificationAttempt(ctx, platformdb.AbandonRecoveryQualificationAttemptParams{
			FinishedAt: recoveryNullTime(now), OccurrenceID: row.OccurrenceID, FenceGeneration: row.FenceGeneration,
		})
		if err != nil {
			return err
		}
		if changed != 1 {
			return fmt.Errorf("recovery qualification attempt history is inconsistent for %s", row.OccurrenceID)
		}
		changed, err = queries.RequeueRecoveryQualificationOccurrence(ctx, platformdb.RequeueRecoveryQualificationOccurrenceParams{
			OccurrenceID: row.OccurrenceID, FenceGeneration: row.FenceGeneration,
		})
		if err != nil {
			return err
		}
		if changed != 1 {
			return fmt.Errorf("recovery qualification occurrence changed while reclaiming %s", row.OccurrenceID)
		}
	}
	return nil
}

func (repository *Repository) Start(ctx context.Context, id string, fence recovery.Fence, now time.Time) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if err := validateRecoveryFence(row, fence, now); err != nil {
			return err
		}
		if err := validateRecoveryStartChronology(row, now); err != nil {
			return err
		}
		changed, err := queries.StartRecoveryQualificationOccurrence(ctx, platformdb.StartRecoveryQualificationOccurrenceParams{
			StartedAt: recoveryNullTime(now), OccurrenceID: id, LeaseOwner: fence.Owner,
			FenceGeneration: fence.Generation, LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.StartRecoveryQualificationAttempt(ctx, platformdb.StartRecoveryQualificationAttemptParams{
			StartedAt: recoveryNullTime(now), OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) Heartbeat(ctx context.Context, id string, fence recovery.Fence, now time.Time, lease time.Duration) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	if lease <= 0 {
		return fmt.Errorf("recovery qualification heartbeat lease must be positive")
	}
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if err := validateRecoveryFence(row, fence, now); err != nil {
			return err
		}
		if err := validateRecoveryMutationChronology(row, now, nil, false); err != nil {
			return err
		}
		expires := now.Add(lease)
		changed, err := queries.HeartbeatRecoveryQualificationOccurrence(ctx, platformdb.HeartbeatRecoveryQualificationOccurrenceParams{
			LeaseExpiresAt: recoveryNullTime(expires), OccurrenceID: id, LeaseOwner: fence.Owner,
			FenceGeneration: fence.Generation, LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.HeartbeatRecoveryQualificationAttempt(ctx, platformdb.HeartbeatRecoveryQualificationAttemptParams{
			LeaseExpiresAt: recoveryFormatTime(expires), OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) RecordPhase(ctx context.Context, id string, fence recovery.Fence, phase, event string, now time.Time) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if err := validateRecoveryFence(row, fence, now); err != nil {
			return err
		}
		if err := validateRecoveryMutationChronology(row, now, nil, true); err != nil {
			return err
		}
		common := func() (sql.NullString, string, string, int64, sql.NullString) {
			return recoveryNullTime(now), id, fence.Owner, fence.Generation, recoveryNullTime(now)
		}
		var changed int64
		switch {
		case phase == recovery.PhaseRestore && event == recovery.PhaseStarted:
			phaseAt, occurrenceID, owner, generation, validAt := common()
			changed, err = queries.StartRecoveryRestorePhase(ctx, platformdb.StartRecoveryRestorePhaseParams{
				PhaseAt: phaseAt, OccurrenceID: occurrenceID, LeaseOwner: owner, FenceGeneration: generation, LeaseValidAt: validAt,
			})
		case phase == recovery.PhaseRestore && event == recovery.PhaseCompleted:
			phaseAt, occurrenceID, owner, generation, validAt := common()
			changed, err = queries.CompleteRecoveryRestorePhase(ctx, platformdb.CompleteRecoveryRestorePhaseParams{
				PhaseAt: phaseAt, OccurrenceID: occurrenceID, LeaseOwner: owner, FenceGeneration: generation, LeaseValidAt: validAt,
			})
		case phase == recovery.PhaseReadiness && event == recovery.PhaseStarted:
			phaseAt, occurrenceID, owner, generation, validAt := common()
			changed, err = queries.StartRecoveryReadinessPhase(ctx, platformdb.StartRecoveryReadinessPhaseParams{
				PhaseAt: phaseAt, OccurrenceID: occurrenceID, LeaseOwner: owner, FenceGeneration: generation, LeaseValidAt: validAt,
			})
		case phase == recovery.PhaseReadiness && event == recovery.PhaseCompleted:
			phaseAt, occurrenceID, owner, generation, validAt := common()
			changed, err = queries.CompleteRecoveryReadinessPhase(ctx, platformdb.CompleteRecoveryReadinessPhaseParams{
				PhaseAt: phaseAt, OccurrenceID: occurrenceID, LeaseOwner: owner, FenceGeneration: generation, LeaseValidAt: validAt,
			})
		default:
			return fmt.Errorf("unsupported recovery qualification phase transition %s/%s", phase, event)
		}
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) Complete(ctx context.Context, id string, fence recovery.Fence, now time.Time, result recovery.Result) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	if err := result.Validate(now); err != nil {
		return err
	}
	encoded, err := recovery.EncodeEvidenceReferences(result.Evidence)
	if err != nil {
		return err
	}
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if err := validateRecoveryFence(row, fence, now); err != nil {
			return err
		}
		if err := validateRecoveryMutationChronology(row, now, &result, true); err != nil {
			return err
		}
		restoreDuration, readinessDuration, qualificationDuration, err := recoveryPhaseDurations(row, now, true)
		if err != nil {
			return err
		}
		changed, err := queries.CompleteRecoveryQualificationOccurrence(ctx, platformdb.CompleteRecoveryQualificationOccurrenceParams{
			FinishedAt: recoveryNullTime(now), RecoveryPointAt: recoveryNullTime(result.RecoveryPointAt),
			RecoveryPointAgeSeconds:     sql.NullInt64{Int64: max(0, int64(now.Sub(result.RecoveryPointAt)/time.Second)), Valid: true},
			RestoreDurationMillis:       sql.NullInt64{Int64: restoreDuration.Milliseconds(), Valid: true},
			ReadinessDurationMillis:     sql.NullInt64{Int64: readinessDuration.Milliseconds(), Valid: true},
			QualificationDurationMillis: sql.NullInt64{Int64: qualificationDuration.Milliseconds(), Valid: true},
			EvidenceRefsJson:            encoded, OccurrenceID: id, LeaseOwner: fence.Owner,
			EvidenceNextAttemptAt: recoveryNullTime(now),
			FenceGeneration:       fence.Generation, LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.CompleteRecoveryQualificationAttempt(ctx, platformdb.CompleteRecoveryQualificationAttemptParams{
			FinishedAt: recoveryNullTime(now), OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) Fail(ctx context.Context, id string, fence recovery.Fence, now time.Time, result recovery.Result, cause error) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("recovery qualification failure reason is required")
	}
	if err := result.ValidateFailure(now); err != nil {
		return err
	}
	encoded, err := recovery.EncodeEvidenceReferences(result.Evidence)
	if err != nil {
		return err
	}
	code, reason := recovery.FailureDetails(cause, "qualification_failed")
	pointAt, pointAge := recoveryResultPoint(result.RecoveryPointAt, now)
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if err := validateRecoveryFence(row, fence, now); err != nil {
			return err
		}
		if err := validateRecoveryMutationChronology(row, now, &result, false); err != nil {
			return err
		}
		restoreDuration, readinessDuration, qualificationDuration, err := recoveryPhaseDurations(row, now, false)
		if err != nil {
			return err
		}
		evidenceStatus := recovery.EvidenceNone
		nextAttemptAt := sql.NullString{}
		if len(result.Evidence) > 0 {
			evidenceStatus = recovery.EvidencePending
			nextAttemptAt = recoveryNullTime(now)
		}
		changed, err := queries.FailRecoveryQualificationOccurrence(ctx, platformdb.FailRecoveryQualificationOccurrenceParams{
			FinishedAt: recoveryNullTime(now), RecoveryPointAt: pointAt, RecoveryPointAgeSeconds: pointAge,
			RestoreDurationMillis:       sql.NullInt64{Int64: restoreDuration.Milliseconds(), Valid: true},
			ReadinessDurationMillis:     sql.NullInt64{Int64: readinessDuration.Milliseconds(), Valid: true},
			QualificationDurationMillis: sql.NullInt64{Int64: qualificationDuration.Milliseconds(), Valid: true},
			EvidenceRefsJson:            encoded, FailureReasonRedacted: reason, FailureCode: code, OccurrenceID: id,
			EvidenceStatus: evidenceStatus, EvidenceNextAttemptAt: nextAttemptAt,
			LeaseOwner: fence.Owner, FenceGeneration: fence.Generation, LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.FailRecoveryQualificationAttempt(ctx, platformdb.FailRecoveryQualificationAttemptParams{
			FinishedAt: recoveryNullTime(now), FailureReasonRedacted: reason, FailureCode: code,
			OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) Cancel(ctx context.Context, id string, fence recovery.Fence, now time.Time, cause error) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("recovery qualification cancellation reason is required")
	}
	code, reason := recovery.FailureDetails(cause, "qualification_canceled")
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if err := validateRecoveryFence(row, fence, now); err != nil {
			return err
		}
		if err := validateRecoveryMutationChronology(row, now, nil, false); err != nil {
			return err
		}
		_, _, qualificationDuration, err := recoveryPhaseDurations(row, now, false)
		if err != nil {
			return err
		}
		changed, err := queries.CancelRecoveryQualificationOccurrence(ctx, platformdb.CancelRecoveryQualificationOccurrenceParams{
			FinishedAt: recoveryNullTime(now), FailureReasonRedacted: reason,
			FailureCode: code, QualificationDurationMillis: sql.NullInt64{Int64: qualificationDuration.Milliseconds(), Valid: true},
			OccurrenceID: id, LeaseOwner: fence.Owner, FenceGeneration: fence.Generation,
			LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.CancelRecoveryQualificationAttempt(ctx, platformdb.CancelRecoveryQualificationAttemptParams{
			FinishedAt: recoveryNullTime(now), FailureReasonRedacted: reason, FailureCode: code,
			OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) ClaimEvidence(ctx context.Context, publisherID string, now time.Time, lease time.Duration) (recovery.Occurrence, bool, error) {
	if repository == nil || repository.db == nil {
		return recovery.Occurrence{}, false, fmt.Errorf("recovery qualification database is required")
	}
	if err := recoveryClaimIdentity(publisherID, now, lease); err != nil {
		return recovery.Occurrence{}, false, err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	if err := reclaimRecoveryEvidence(ctx, queries, now); err != nil {
		return recovery.Occurrence{}, false, err
	}
	id, err := queries.NextPendingRecoveryEvidence(ctx, recoveryNullTime(now))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return recovery.Occurrence{}, false, err
		}
		return recovery.Occurrence{}, false, nil
	}
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	expires := now.Add(lease)
	changed, err := queries.ClaimRecoveryEvidence(ctx, platformdb.ClaimRecoveryEvidenceParams{
		EvidenceLeaseOwner: publisherID, EvidenceLeaseExpiresAt: recoveryNullTime(expires), OccurrenceID: id,
		EligibleAt: recoveryNullTime(now),
	})
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	if changed != 1 {
		return recovery.Occurrence{}, false, recovery.ErrFenced
	}
	row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
	if err != nil {
		return recovery.Occurrence{}, false, err
	}
	if err := queries.InsertRecoveryEvidenceAttempt(ctx, platformdb.InsertRecoveryEvidenceAttemptParams{
		OccurrenceID: id, AttemptNumber: row.EvidenceAttemptCount,
		FenceGeneration: row.EvidenceFenceGeneration, PublisherID: publisherID,
		ClaimedAt: recoveryFormatTime(now), LeaseExpiresAt: recoveryFormatTime(expires),
	}); err != nil {
		return recovery.Occurrence{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return recovery.Occurrence{}, false, err
	}
	occurrence, err := recoveryOccurrenceFromRow(row)
	return occurrence, err == nil, err
}

func reclaimRecoveryEvidence(ctx context.Context, queries *platformdb.Queries, now time.Time) error {
	rows, err := queries.ListExpiredRecoveryEvidenceLeases(ctx, recoveryNullTime(now))
	if err != nil {
		return err
	}
	for _, row := range rows {
		changed, err := queries.AbandonRecoveryEvidenceAttempt(ctx, platformdb.AbandonRecoveryEvidenceAttemptParams{
			FinishedAt: recoveryNullTime(now), OccurrenceID: row.OccurrenceID,
			FenceGeneration: row.EvidenceFenceGeneration,
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.RequeueRecoveryEvidence(ctx, platformdb.RequeueRecoveryEvidenceParams{
			EvidenceNextAttemptAt: recoveryNullTime(now), OccurrenceID: row.OccurrenceID, FenceGeneration: row.EvidenceFenceGeneration,
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
	}
	return nil
}

func (repository *Repository) PublishEvidence(ctx context.Context, id string, fence recovery.Fence, now time.Time) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		changed, err := queries.PublishRecoveryEvidence(ctx, platformdb.PublishRecoveryEvidenceParams{
			EvidencePublishedAt: recoveryNullTime(now), OccurrenceID: id,
			EvidenceLeaseOwner: fence.Owner, EvidenceFenceGeneration: fence.Generation,
			LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.PublishRecoveryEvidenceAttempt(ctx, platformdb.PublishRecoveryEvidenceAttemptParams{
			FinishedAt: recoveryNullTime(now), OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) FailEvidence(ctx context.Context, id string, fence recovery.Fence, now time.Time, cause error) error {
	if err := validateRecoveryMutation(id, fence, now); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("recovery evidence publication failure reason is required")
	}
	code, reason := recovery.FailureDetails(cause, "evidence_publication_failed")
	return repository.withRecoveryTx(ctx, func(queries *platformdb.Queries) error {
		row, err := queries.GetRecoveryQualificationOccurrence(ctx, id)
		if err != nil {
			return err
		}
		retryAt := now.Add(recoveryEvidenceRetryDelay(row.EvidenceAttemptCount))
		changed, err := queries.FailRecoveryEvidence(ctx, platformdb.FailRecoveryEvidenceParams{
			EvidenceNextAttemptAt: recoveryNullTime(retryAt), EvidenceFailureReasonRedacted: reason, OccurrenceID: id,
			EvidenceFailureCode: code,
			EvidenceLeaseOwner:  fence.Owner, EvidenceFenceGeneration: fence.Generation,
			LeaseValidAt: recoveryNullTime(now),
		})
		if err != nil || changed != 1 {
			return recoveryChanged(changed, err)
		}
		changed, err = queries.FailRecoveryEvidenceAttempt(ctx, platformdb.FailRecoveryEvidenceAttemptParams{
			FinishedAt: recoveryNullTime(now), FailureReasonRedacted: reason, FailureCode: code,
			OccurrenceID: id, FenceGeneration: fence.Generation,
		})
		return recoveryChanged(changed, err)
	})
}

func (repository *Repository) Occurrence(ctx context.Context, id string) (recovery.Occurrence, error) {
	if id == "" {
		return recovery.Occurrence{}, fmt.Errorf("recovery qualification occurrence id is required")
	}
	row, err := repository.q.GetRecoveryQualificationOccurrence(ctx, id)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	return recoveryOccurrenceFromRow(row)
}

func (repository *Repository) Occurrences(ctx context.Context) ([]recovery.Occurrence, error) {
	return recoveryOccurrences(ctx, repository.q)
}

func recoveryOccurrences(ctx context.Context, queries *platformdb.Queries) ([]recovery.Occurrence, error) {
	rows, err := queries.ListRecoveryQualificationOccurrences(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]recovery.Occurrence, 0, len(rows))
	for _, row := range rows {
		occurrence, err := recoveryOccurrenceFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, occurrence)
	}
	return result, nil
}

func (repository *Repository) Attempts(ctx context.Context, id string) ([]recovery.Attempt, error) {
	rows, err := repository.q.ListRecoveryQualificationAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	result := make([]recovery.Attempt, 0, len(rows))
	for _, row := range rows {
		claimed, err := recoveryParseTime(row.ClaimedAt)
		if err != nil {
			return nil, err
		}
		lease, err := recoveryParseTime(row.LeaseExpiresAt)
		if err != nil {
			return nil, err
		}
		started, err := recoveryParseNullTime(row.StartedAt)
		if err != nil {
			return nil, err
		}
		finished, err := recoveryParseNullTime(row.FinishedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, recovery.Attempt{
			OccurrenceID: row.OccurrenceID, AttemptNumber: row.AttemptNumber,
			FenceGeneration: row.FenceGeneration, WorkerID: row.WorkerID, Actor: row.Actor,
			Status: row.Status, ClaimedAt: claimed, StartedAt: started,
			LeaseExpiresAt: lease, FinishedAt: finished, FailureReasonRedacted: row.FailureReasonRedacted,
			FailureCode: row.FailureCode,
		})
	}
	return result, nil
}

func (repository *Repository) EvidenceAttempts(ctx context.Context, id string) ([]recovery.EvidenceAttempt, error) {
	rows, err := repository.q.ListRecoveryEvidenceAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	result := make([]recovery.EvidenceAttempt, 0, len(rows))
	for _, row := range rows {
		claimed, err := recoveryParseTime(row.ClaimedAt)
		if err != nil {
			return nil, err
		}
		lease, err := recoveryParseTime(row.LeaseExpiresAt)
		if err != nil {
			return nil, err
		}
		finished, err := recoveryParseNullTime(row.FinishedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, recovery.EvidenceAttempt{
			OccurrenceID: row.OccurrenceID, AttemptNumber: row.AttemptNumber,
			FenceGeneration: row.FenceGeneration, PublisherID: row.PublisherID,
			Status: row.Status, ClaimedAt: claimed, LeaseExpiresAt: lease,
			FinishedAt: finished, FailureReasonRedacted: row.FailureReasonRedacted,
			FailureCode: row.FailureCode,
		})
	}
	return result, nil
}

func (repository *Repository) Status(ctx context.Context, now time.Time) (recovery.StatusSnapshot, error) {
	if now.IsZero() {
		return recovery.StatusSnapshot{}, fmt.Errorf("recovery qualification status time is required")
	}
	if repository == nil || repository.db == nil {
		return recovery.StatusSnapshot{}, fmt.Errorf("recovery qualification database is required")
	}
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return recovery.StatusSnapshot{}, err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	occurrences, err := recoveryOccurrences(ctx, queries)
	if err != nil {
		return recovery.StatusSnapshot{}, err
	}
	snapshot := recovery.StatusSnapshot{GeneratedAt: now}
	schedules, err := queries.ListRecoveryQualificationSchedules(ctx)
	if err != nil {
		return recovery.StatusSnapshot{}, err
	}
	for _, schedule := range schedules {
		if schedule.Enabled != 1 || schedule.ClosedAt.Valid {
			continue
		}
		snapshot.ConfiguredSchedules++
		next, parseErr := recoveryParseTime(schedule.NextRunAt)
		if parseErr != nil {
			return recovery.StatusSnapshot{}, parseErr
		}
		parsed, parseErr := refreshschedule.ParseSchedule(schedule.Cron, schedule.Timezone)
		if parseErr != nil {
			return recovery.StatusSnapshot{}, parseErr
		}
		for count := 0; !next.After(now); count++ {
			if count >= 10000 {
				return recovery.StatusSnapshot{}, fmt.Errorf("recovery qualification schedule %q has more than 10000 missing runs", schedule.ScheduleID)
			}
			snapshot.MissingRuns++
			snapshot.Due++
			if !next.Add(time.Duration(schedule.StaleAfterSeconds) * time.Second).After(now) {
				snapshot.Overdue++
			}
			next = parsed.Next(next)
			if next.IsZero() {
				return recovery.StatusSnapshot{}, fmt.Errorf("recovery qualification schedule %q has no next occurrence", schedule.ScheduleID)
			}
		}
	}
	snapshot.Unconfigured = snapshot.ConfiguredSchedules == 0
	byOperation := map[string]*recovery.OperationStatus{}
	lastSuccess := map[string]recovery.Occurrence{}
	for _, operation := range []string{recovery.OperationBackup, recovery.OperationRestore, recovery.OperationUpgrade, recovery.OperationRollback} {
		byOperation[operation] = &recovery.OperationStatus{Operation: operation}
	}
	for _, occurrence := range occurrences {
		operation := byOperation[occurrence.Operation]
		switch occurrence.Status {
		case recovery.StatusPending:
			operation.Pending++
			if !occurrence.PlannedAt.After(now) {
				snapshot.Due++
			}
			if !occurrence.ExpiresAt.After(now) {
				snapshot.Overdue++
			}
		case recovery.StatusClaimed, recovery.StatusRunning:
			if !occurrence.LeaseExpiresAt.IsZero() && !occurrence.LeaseExpiresAt.After(now) {
				snapshot.StaleExecutionLeases++
				snapshot.Overdue++
				snapshot.Failed++
				operation.Failed++
			} else {
				operation.Running++
				snapshot.Running++
			}
		case recovery.StatusFailed:
			operation.Failed++
			snapshot.Failed++
		case recovery.StatusExpired:
			operation.Expired++
			snapshot.Failed++
		case recovery.StatusSucceeded:
			prior := lastSuccess[occurrence.Operation]
			if prior.FinishedAt.IsZero() || occurrence.FinishedAt.After(prior.FinishedAt) {
				lastSuccess[occurrence.Operation] = occurrence
			}
		}
		if recoveryTerminal(occurrence.Status) {
			switch occurrence.EvidenceStatus {
			case recovery.EvidenceClaimed:
				if !occurrence.EvidenceLeaseExpiresAt.IsZero() && !occurrence.EvidenceLeaseExpiresAt.After(now) {
					snapshot.StaleEvidenceLeases++
					snapshot.EvidenceFailed++
				} else {
					snapshot.EvidencePending++
				}
			case recovery.EvidencePending:
				snapshot.EvidencePending++
			case recovery.EvidenceFailed:
				snapshot.EvidenceFailed++
			}
		}
	}
	snapshot.RecoveredExpiredLeases, err = queries.CountAbandonedRecoveryQualificationAttempts(ctx)
	if err != nil {
		return recovery.StatusSnapshot{}, err
	}
	for _, operationName := range []string{recovery.OperationBackup, recovery.OperationRestore, recovery.OperationUpgrade, recovery.OperationRollback} {
		operation := byOperation[operationName]
		if last := lastSuccess[operationName]; !last.FinishedAt.IsZero() {
			age := max(0, int64(now.Sub(last.FinishedAt)/time.Second))
			restoreDuration := last.RestoreDurationMillis
			readinessDuration := last.ReadinessDurationMillis
			qualificationDuration := last.QualificationDurationMillis
			pointAge := last.RecoveryPointAgeSeconds
			operation.LastSuccessAgeSeconds = &age
			operation.LastRestoreDurationMillis = &restoreDuration
			operation.LastReadinessDurationMillis = &readinessDuration
			operation.LastQualificationDurationMillis = &qualificationDuration
			operation.LastRecoveryPointAgeSeconds = &pointAge
		}
		snapshot.Operations = append(snapshot.Operations, *operation)
	}
	return snapshot, nil
}

func (repository *Repository) Retain(ctx context.Context, policy recovery.RetentionPolicy) (recovery.RetentionResult, error) {
	if policy.Now.IsZero() || policy.ComplianceWindow <= 0 {
		return recovery.RetentionResult{}, fmt.Errorf("recovery qualification retention time and positive compliance window are required")
	}
	occurrences, err := repository.Occurrences(ctx)
	if err != nil {
		return recovery.RetentionResult{}, err
	}
	cutoff := policy.Now.Add(-policy.ComplianceWindow)
	preserve := map[string]bool{}
	latestSuccess := map[string]recovery.Occurrence{}
	latestFailure := map[string]recovery.Occurrence{}
	for _, occurrence := range occurrences {
		terminal := recoveryTerminal(occurrence.Status)
		if !terminal || !occurrence.FinishedAt.Before(cutoff) ||
			(occurrence.EvidenceStatus == recovery.EvidenceClaimed && occurrence.EvidenceLeaseExpiresAt.After(policy.Now)) {
			preserve[occurrence.ID] = true
		}
		key := occurrence.Scenario + "\x00" + occurrence.Operation
		if occurrence.Status == recovery.StatusSucceeded {
			if newerRecoveryOccurrence(occurrence, latestSuccess[key]) {
				latestSuccess[key] = occurrence
			}
		} else if occurrence.Status == recovery.StatusFailed || occurrence.Status == recovery.StatusExpired {
			if newerRecoveryOccurrence(occurrence, latestFailure[key]) {
				latestFailure[key] = occurrence
			}
		}
	}
	for _, occurrence := range latestSuccess {
		preserve[occurrence.ID] = true
	}
	for _, occurrence := range latestFailure {
		preserve[occurrence.ID] = true
	}
	result := recovery.RetentionResult{}
	for _, occurrence := range occurrences {
		if preserve[occurrence.ID] {
			result.PreservedIDs = append(result.PreservedIDs, occurrence.ID)
			continue
		}
		changed, err := repository.q.DeleteRecoveryQualificationOccurrence(ctx, platformdb.DeleteRecoveryQualificationOccurrenceParams{
			OccurrenceID: occurrence.ID, ActiveAt: recoveryNullTime(policy.Now), FinishedBefore: recoveryNullTime(cutoff),
		})
		if err != nil {
			return recovery.RetentionResult{}, err
		}
		if changed == 1 {
			result.DeletedIDs = append(result.DeletedIDs, occurrence.ID)
		} else {
			result.PreservedIDs = append(result.PreservedIDs, occurrence.ID)
		}
	}
	sort.Strings(result.DeletedIDs)
	sort.Strings(result.PreservedIDs)
	return result, nil
}

func (repository *Repository) withRecoveryTx(ctx context.Context, mutation func(*platformdb.Queries) error) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("recovery qualification database is required")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := mutation(repository.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func recoveryOccurrenceFromRow(row platformdb.RecoveryQualificationOccurrence) (recovery.Occurrence, error) {
	planned, err := recoveryParseTime(row.PlannedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	expires, err := recoveryParseTime(row.ExpiresAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	created, err := recoveryParseTime(row.CreatedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	claimed, err := recoveryParseNullTime(row.ClaimedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	started, err := recoveryParseNullTime(row.StartedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	finished, err := recoveryParseNullTime(row.FinishedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	point, err := recoveryParseNullTime(row.RecoveryPointAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	published, err := recoveryParseNullTime(row.EvidencePublishedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	var evidence []recovery.EvidenceReference
	if err := json.Unmarshal([]byte(row.EvidenceRefsJson), &evidence); err != nil {
		return recovery.Occurrence{}, fmt.Errorf("decode recovery qualification evidence: %w", err)
	}
	if evidence == nil {
		evidence = []recovery.EvidenceReference{}
	}
	leaseExpires, err := recoveryParseNullTime(row.LeaseExpiresAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	evidenceLeaseExpires, err := recoveryParseNullTime(row.EvidenceLeaseExpiresAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	restoreStarted, err := recoveryParseNullTime(row.RestoreStartedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	restoreCompleted, err := recoveryParseNullTime(row.RestoreCompletedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	readinessStarted, err := recoveryParseNullTime(row.ReadinessStartedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	readinessCompleted, err := recoveryParseNullTime(row.ReadinessCompletedAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	evidenceNextAttempt, err := recoveryParseNullTime(row.EvidenceNextAttemptAt)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	return recovery.Occurrence{
		ID: row.OccurrenceID, ScheduleID: row.ScheduleID, ScheduleRevision: row.ScheduleRevisionID, Scenario: row.Scenario,
		Operation: row.Operation, PolicyVersion: row.PolicyVersion, PolicySHA256: row.PolicySha256, TargetScope: row.TargetScope,
		ArtifactIdentity: row.ArtifactIdentity, PlannedAt: planned, ExpiresAt: expires,
		Status: row.Status, Result: row.Result, AttemptCount: row.AttemptCount,
		Fence:          recovery.Fence{Owner: row.LeaseOwner, Generation: row.FenceGeneration},
		LeaseExpiresAt: leaseExpires, Actor: row.Actor,
		CreatedAt: created, ClaimedAt: claimed, StartedAt: started,
		RestoreStartedAt: restoreStarted, RestoreCompletedAt: restoreCompleted,
		ReadinessStartedAt: readinessStarted, ReadinessCompletedAt: readinessCompleted, FinishedAt: finished,
		RecoveryPointAt: point, RecoveryPointAgeSeconds: row.RecoveryPointAgeSeconds.Int64,
		RestoreDurationMillis:       row.RestoreDurationMillis.Int64,
		ReadinessDurationMillis:     row.ReadinessDurationMillis.Int64,
		QualificationDurationMillis: row.QualificationDurationMillis.Int64,
		FailureReasonRedacted:       row.FailureReasonRedacted, FailureCode: row.FailureCode, Evidence: evidence,
		EvidenceStatus: row.EvidenceStatus, EvidenceAttemptCount: row.EvidenceAttemptCount,
		EvidenceFence:          recovery.Fence{Owner: row.EvidenceLeaseOwner, Generation: row.EvidenceFenceGeneration},
		EvidenceLeaseExpiresAt: evidenceLeaseExpires,
		EvidenceNextAttemptAt:  evidenceNextAttempt,
		EvidencePublishedAt:    published, EvidenceFailureRedacted: row.EvidenceFailureReasonRedacted,
		EvidenceFailureCode: row.EvidenceFailureCode,
	}, nil
}

func recoveryResultPoint(value, now time.Time) (sql.NullString, sql.NullInt64) {
	if value.IsZero() {
		return sql.NullString{}, sql.NullInt64{}
	}
	return recoveryNullTime(value), sql.NullInt64{Int64: max(0, int64(now.Sub(value)/time.Second)), Valid: true}
}

func validateRecoveryStartChronology(row platformdb.RecoveryQualificationOccurrence, now time.Time) error {
	planned, err := recoveryParseTime(row.PlannedAt)
	if err != nil {
		return err
	}
	claimed, err := recoveryParseNullTime(row.ClaimedAt)
	if err != nil {
		return err
	}
	if claimed.IsZero() || claimed.Before(planned) || now.Before(claimed) {
		return fmt.Errorf("recovery qualification chronology requires scheduled <= claimed <= started")
	}
	return nil
}

func validateRecoveryFence(row platformdb.RecoveryQualificationOccurrence, fence recovery.Fence, now time.Time) error {
	lease, err := recoveryParseNullTime(row.LeaseExpiresAt)
	if err != nil {
		return err
	}
	if row.LeaseOwner != fence.Owner || row.FenceGeneration != fence.Generation || lease.IsZero() || !lease.After(now) {
		return recovery.ErrFenced
	}
	return nil
}

func validateRecoveryMutationChronology(row platformdb.RecoveryQualificationOccurrence, now time.Time, result *recovery.Result, requireStarted bool) error {
	claimed, err := recoveryParseNullTime(row.ClaimedAt)
	if err != nil {
		return err
	}
	started, err := recoveryParseNullTime(row.StartedAt)
	if err != nil {
		return err
	}
	if claimed.IsZero() || now.Before(claimed) {
		return fmt.Errorf("recovery qualification chronology requires completion after claim")
	}
	if requireStarted && started.IsZero() {
		return fmt.Errorf("recovery qualification must start before successful completion")
	}
	if !started.IsZero() && (started.Before(claimed) || now.Before(started)) {
		return fmt.Errorf("recovery qualification chronology requires claimed <= started <= completed")
	}
	if result == nil {
		return nil
	}
	if started.IsZero() {
		if result.RestoreDuration != 0 || result.ReadinessDuration != 0 {
			return fmt.Errorf("recovery qualification cannot record durations before execution starts")
		}
		return nil
	}
	elapsed := now.Sub(started)
	if result.RestoreDuration > elapsed || result.ReadinessDuration > elapsed || result.RestoreDuration+result.ReadinessDuration > elapsed {
		return fmt.Errorf("recovery qualification durations exceed persisted execution interval")
	}
	return nil
}

func recoveryPhaseDurations(row platformdb.RecoveryQualificationOccurrence, completed time.Time, requireComplete bool) (time.Duration, time.Duration, time.Duration, error) {
	started, err := recoveryParseNullTime(row.StartedAt)
	if err != nil {
		return 0, 0, 0, err
	}
	if started.IsZero() || completed.Before(started) {
		if requireComplete {
			return 0, 0, 0, fmt.Errorf("recovery qualification persisted execution phase is incomplete")
		}
		return 0, 0, 0, nil
	}
	total := completed.Sub(started)
	phaseDuration := func(startValue, completedValue sql.NullString, label string) (time.Duration, error) {
		phaseStart, parseErr := recoveryParseNullTime(startValue)
		if parseErr != nil {
			return 0, parseErr
		}
		phaseCompleted, parseErr := recoveryParseNullTime(completedValue)
		if parseErr != nil {
			return 0, parseErr
		}
		if phaseStart.IsZero() || phaseCompleted.IsZero() {
			if requireComplete {
				return 0, fmt.Errorf("recovery qualification %s phase is incomplete", label)
			}
			return 0, nil
		}
		if phaseStart.Before(started) || phaseCompleted.Before(phaseStart) || phaseCompleted.After(completed) {
			return 0, fmt.Errorf("recovery qualification %s phase chronology is invalid", label)
		}
		return phaseCompleted.Sub(phaseStart), nil
	}
	switch row.Operation {
	case recovery.OperationRestore:
		restore, err := phaseDuration(row.RestoreStartedAt, row.RestoreCompletedAt, recovery.PhaseRestore)
		return restore, 0, total, err
	case recovery.OperationUpgrade, recovery.OperationRollback:
		readiness, err := phaseDuration(row.ReadinessStartedAt, row.ReadinessCompletedAt, recovery.PhaseReadiness)
		return 0, readiness, total, err
	default:
		return 0, 0, total, nil
	}
}

func recoveryEvidenceRetryDelay(attempt int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute
	for count := int64(1); count < attempt && delay < time.Hour; count++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func validateRecoveryMutation(id string, fence recovery.Fence, now time.Time) error {
	if id == "" || now.IsZero() {
		return fmt.Errorf("recovery qualification occurrence id and mutation time are required")
	}
	return fence.Validate()
}

func recoveryClaimIdentity(value string, now time.Time, lease time.Duration) error {
	return (recovery.ClaimInput{WorkerID: value, Actor: value, Now: now, Lease: lease}).Validate()
}

func recoveryChanged(changed int64, err error) error {
	if err != nil {
		return err
	}
	if changed != 1 {
		return recovery.ErrFenced
	}
	return nil
}

func recoveryTerminal(status string) bool {
	switch status {
	case recovery.StatusSucceeded, recovery.StatusFailed, recovery.StatusCanceled, recovery.StatusExpired:
		return true
	default:
		return false
	}
}

func newerRecoveryOccurrence(candidate, current recovery.Occurrence) bool {
	return current.ID == "" || candidate.FinishedAt.After(current.FinishedAt) ||
		(candidate.FinishedAt.Equal(current.FinishedAt) && candidate.ID > current.ID)
}

func recoveryFormatTime(value time.Time) string { return value.UTC().Format(recoveryTimeLayout) }

func recoveryParseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(recoveryTimeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse recovery qualification time %q: %w", value, err)
	}
	return parsed, nil
}

func recoveryNullTime(value time.Time) sql.NullString {
	if value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: recoveryFormatTime(value), Valid: true}
}

func recoveryParseNullTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return recoveryParseTime(value.String)
}
