package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	platformdb "github.com/flidai/leapview/internal/refresh/internal/db"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

type Repository struct {
	db *sql.DB
	q  *platformdb.Queries
}

const occurrenceClaimTimeout = 5 * time.Minute

// The scheduler polls once per minute by default. A zero starting deadline
// still admits the controller tick that observes the nominal time, but does not
// recover an occurrence after that polling window has elapsed.
const normalScheduleTickWindow = time.Minute

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db, q: platformdb.New(db)} }

type persistedSchedule struct {
	triggerID      string
	artifactDigest string
	cron           string
	timezone       string
	nextRunAt      time.Time
}

func (repository *Repository) Reconcile(ctx context.Context, input refreshschedule.ReconcileInput) error {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("refresh pipeline database is required")
	}
	if err := refreshschedule.ValidateScope(input.Identity); err != nil {
		return fmt.Errorf("refresh serving identity is required: %w", err)
	}
	if err := refreshschedule.ValidateArtifactDigest(input.ArtifactDigest); err != nil {
		return err
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	existing, err := loadPersistedSchedules(ctx, queries, input.Identity)
	if err != nil {
		return err
	}
	if err := queries.SupersedeStaleRefreshPipelineOccurrences(ctx, platformdb.SupersedeStaleRefreshPipelineOccurrencesParams{
		ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, GenerationID: input.Identity.GenerationID,
	}); err != nil {
		return err
	}
	if err := queries.DeleteRefreshPipelineSchedules(ctx, platformdb.DeleteRefreshPipelineSchedulesParams{ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, GenerationID: input.Identity.GenerationID}); err != nil {
		return err
	}
	pipelines := append([]refreshschedule.Definition(nil), input.Pipelines...)
	sort.SliceStable(pipelines, func(i, j int) bool { return pipelines[i].ID < pipelines[j].ID })
	for _, pipeline := range pipelines {
		if err := pipeline.Validate(); err != nil {
			return err
		}
		schedules := append([]refreshschedule.Schedule(nil), pipeline.Schedules...)
		sort.SliceStable(schedules, func(i, j int) bool {
			if schedules[i].Expression != schedules[j].Expression {
				return schedules[i].Expression < schedules[j].Expression
			}
			return schedules[i].ID < schedules[j].ID
		})
		seenTriggers := map[string]struct{}{}
		for _, schedule := range schedules {
			triggerID := schedule.ID
			if _, exists := seenTriggers[triggerID]; exists {
				return fmt.Errorf("refresh pipeline %q has duplicate trigger id %q", pipeline.ID, triggerID)
			}
			seenTriggers[triggerID] = struct{}{}
			key := scheduleKey(pipeline.ID.String(), triggerID)
			parsed, err := refreshschedule.ParseSchedule(schedule.Expression, pipeline.Timezone)
			if err != nil {
				return err
			}
			parsed.ID = schedule.ID
			next := parsed.Next(input.Now)
			if prior, ok := existing[key]; ok && prior.artifactDigest == input.ArtifactDigest &&
				prior.cron == schedule.Expression && prior.timezone == pipeline.Timezone {
				// Reconcile is the Argo controller's startup/recovery boundary.
				// Apply the deadline only to an unchanged persisted cursor; a new
				// or changed schedule always starts strictly after deployment time.
				next = prior.nextRunAt
				if next.Before(input.Now) {
					if pipeline.StartingDeadlineSeconds > 0 {
						latest := next
						candidate := parsed.Next(latest)
						for !candidate.IsZero() && !candidate.After(input.Now) {
							latest = candidate
							candidate = parsed.Next(candidate)
						}
						if input.Now.Sub(latest) <= deadlineDuration(pipeline.StartingDeadlineSeconds) {
							next = latest
						} else {
							next = parsed.Next(input.Now)
						}
					} else {
						next = parsed.Next(input.Now)
					}
				}
			}
			if next.IsZero() {
				return fmt.Errorf("refresh pipeline %q schedule %q has no next occurrence", pipeline.ID, schedule.Expression)
			}
			if err := queries.CreateRefreshPipelineSchedule(ctx, platformdb.CreateRefreshPipelineScheduleParams{
				ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, PipelineID: pipeline.ID.String(), TriggerID: triggerID,
				SemanticModelID: pipeline.SemanticModelID.String(), GenerationID: input.Identity.GenerationID, ArtifactDigest: input.ArtifactDigest,
				Cron: schedule.Expression, Timezone: pipeline.Timezone,
				StartingDeadlineSeconds: pipeline.StartingDeadlineSeconds, ConcurrencyPolicy: pipeline.ConcurrencyPolicy,
				NextRunAt: formatTime(next),
			}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func deadlineDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	const maxSeconds = int64(^uint64(0)>>1) / int64(time.Second)
	if seconds > maxSeconds {
		return time.Duration(^uint64(0) >> 1)
	}
	return time.Duration(seconds) * time.Second
}

func withinStartingDeadline(now, scheduledAt time.Time, seconds int64) bool {
	lateBy := now.Sub(scheduledAt)
	if lateBy <= 0 {
		return true
	}
	if seconds == 0 {
		return lateBy <= normalScheduleTickWindow
	}
	return lateBy <= deadlineDuration(seconds)
}

func loadPersistedSchedules(ctx context.Context, queries *platformdb.Queries, identity projectgraph.ServingIdentity) (map[string]persistedSchedule, error) {
	rows, err := queries.ListRefreshPipelineSchedules(ctx, platformdb.ListRefreshPipelineSchedulesParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID})
	if err != nil {
		return nil, err
	}
	out := map[string]persistedSchedule{}
	for _, row := range rows {
		next, err := parseTime(row.NextRunAt)
		if err != nil {
			return nil, err
		}
		out[scheduleKey(row.PipelineID, row.TriggerID)] = persistedSchedule{
			triggerID: row.TriggerID, artifactDigest: row.ArtifactDigest, cron: row.Cron,
			timezone: row.Timezone, nextRunAt: next,
		}
	}
	return out, nil
}

type dueSchedule struct {
	identity                projectgraph.ServingIdentity
	pipelineID              projectgraph.ResourceID
	triggerID               string
	semanticModelID         projectgraph.ResourceID
	expression              string
	timezone                string
	startingDeadlineSeconds int64
	concurrencyPolicy       string
	artifactDigest          string
	nextRunAt               time.Time
}

func (repository *Repository) ClaimDue(ctx context.Context, identity projectgraph.ServingIdentity, now time.Time) ([]refreshschedule.Occurrence, error) {
	if repository == nil || repository.db == nil {
		return nil, fmt.Errorf("refresh pipeline database is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := refreshschedule.ValidateScope(identity); err != nil {
		return nil, fmt.Errorf("refresh pipeline serving identity is required: %w", err)
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	claimedBefore := formatTime(now.Add(-occurrenceClaimTimeout))
	if err := queries.RequeueAbandonedRefreshPipelineSchedules(ctx, platformdb.RequeueAbandonedRefreshPipelineSchedulesParams{ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, ClaimedBefore: sql.NullString{String: claimedBefore, Valid: true}, Environment: identity.Environment}); err != nil {
		return nil, err
	}
	if err := queries.RequeueAbandonedRefreshPipelineOccurrences(ctx, platformdb.RequeueAbandonedRefreshPipelineOccurrencesParams{ProjectID: identity.ProjectID.String(), ClaimedBefore: sql.NullString{String: claimedBefore, Valid: true}, Environment: identity.Environment}); err != nil {
		return nil, err
	}
	if err := queries.SupersedeStaleRefreshPipelineOccurrences(ctx, platformdb.SupersedeStaleRefreshPipelineOccurrencesParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	}); err != nil {
		return nil, err
	}
	rows, err := queries.ListDueRefreshPipelineSchedules(ctx, platformdb.ListDueRefreshPipelineSchedulesParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, NextRunAt: formatTime(now)})
	if err != nil {
		return nil, err
	}
	due := make([]dueSchedule, 0, len(rows))
	for _, row := range rows {
		item := dueSchedule{
			identity: projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(row.ProjectID), Environment: row.Environment, GenerationID: row.GenerationID}, pipelineID: projectgraph.ResourceID(row.PipelineID),
			triggerID: row.TriggerID, semanticModelID: projectgraph.ResourceID(row.SemanticModelID), expression: row.Cron, timezone: row.Timezone,
			startingDeadlineSeconds: row.StartingDeadlineSeconds, concurrencyPolicy: row.ConcurrencyPolicy, artifactDigest: row.ArtifactDigest,
		}
		if item.identity != identity {
			return nil, fmt.Errorf("refresh schedule returned unexpected serving identity %v", item.identity)
		}
		item.nextRunAt, err = parseTime(row.NextRunAt)
		if err != nil {
			return nil, err
		}
		due = append(due, item)
	}
	type dueOccurrence struct {
		occurrence refreshschedule.Occurrence
		deadline   int64
		retry      bool
	}
	grouped := map[string]dueOccurrence{}
	for _, item := range due {
		schedule, err := refreshschedule.ParseSchedule(item.expression, item.timezone)
		if err != nil {
			return nil, err
		}
		scheduledAt := item.nextRunAt
		retry := false
		stored, lookupErr := queries.GetRefreshPipelineOccurrence(ctx, platformdb.GetRefreshPipelineOccurrenceParams{
			ProjectID: item.identity.ProjectID.String(), Environment: item.identity.Environment,
			PipelineID: item.pipelineID.String(), ScheduledAt: formatTime(scheduledAt),
		})
		if lookupErr == nil && stored.Status == "pending" {
			retry = true
		} else if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
		next := schedule.Next(scheduledAt)
		if retry {
			next = schedule.Next(now)
		} else {
			for !next.IsZero() && !next.After(now) {
				scheduledAt = next
				next = schedule.Next(next)
			}
		}
		if next.IsZero() {
			return nil, fmt.Errorf("refresh pipeline %q schedule %q has no next occurrence", item.pipelineID, item.expression)
		}
		if err := queries.AdvanceRefreshPipelineSchedule(ctx, platformdb.AdvanceRefreshPipelineScheduleParams{
			NextRunAt: formatTime(next), ProjectID: item.identity.ProjectID.String(), Environment: item.identity.Environment,
			GenerationID: item.identity.GenerationID, PipelineID: item.pipelineID.String(), TriggerID: item.triggerID,
		}); err != nil {
			return nil, err
		}
		key := item.identity.ProjectID.String() + "\x00" + item.identity.Environment + "\x00" + item.pipelineID.String()
		current := grouped[key]
		if current.occurrence.ScheduledAt.IsZero() || scheduledAt.After(current.occurrence.ScheduledAt) {
			current.occurrence = refreshschedule.Occurrence{
				Identity:   item.identity,
				PipelineID: item.pipelineID, MatchingScheduleIDs: []string{item.triggerID}, SemanticModelID: item.semanticModelID,
				ArtifactDigest: item.artifactDigest, Timezone: item.timezone, ScheduledAt: scheduledAt,
			}
			current.deadline = item.startingDeadlineSeconds
			current.retry = retry
			grouped[key] = current
		} else if scheduledAt.Equal(current.occurrence.ScheduledAt) {
			current.occurrence.MatchingScheduleIDs = append(current.occurrence.MatchingScheduleIDs, item.triggerID)
			if item.startingDeadlineSeconds < current.deadline {
				current.deadline = item.startingDeadlineSeconds
			}
			current.retry = current.retry || retry
			grouped[key] = current
		}
	}
	out := make([]refreshschedule.Occurrence, 0, len(grouped))
	for _, item := range grouped {
		if !item.retry && !withinStartingDeadline(now, item.occurrence.ScheduledAt, item.deadline) {
			continue
		}
		sort.Strings(item.occurrence.MatchingScheduleIDs)
		out = append(out, item.occurrence)
	}
	// Dispatch order is part of the contract: nominal UTC time first, then
	// stable trigger ID.  Pipeline and serving identity are deterministic tie
	// breakers for independent pipelines sharing a nominal instant and trigger.
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if !left.ScheduledAt.Equal(right.ScheduledAt) {
			return left.ScheduledAt.Before(right.ScheduledAt)
		}
		leftIDs, rightIDs := strings.Join(left.MatchingScheduleIDs, "\x00"), strings.Join(right.MatchingScheduleIDs, "\x00")
		if leftIDs != rightIDs {
			return leftIDs < rightIDs
		}
		if left.PipelineID != right.PipelineID {
			return left.PipelineID < right.PipelineID
		}
		if left.Identity.ProjectID != right.Identity.ProjectID {
			return left.Identity.ProjectID < right.Identity.ProjectID
		}
		if left.Identity.Environment != right.Identity.Environment {
			return left.Identity.Environment < right.Identity.Environment
		}
		return left.Identity.GenerationID < right.Identity.GenerationID
	})
	claimed := make([]refreshschedule.Occurrence, 0, len(out))
	for _, occurrence := range out {
		matchingJSON, err := encodeScheduleIDs(occurrence.MatchingScheduleIDs)
		if err != nil {
			return nil, err
		}
		result, err := queries.ClaimRefreshPipelineOccurrence(ctx, platformdb.ClaimRefreshPipelineOccurrenceParams{
			ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(),
			GenerationID: occurrence.Identity.GenerationID, ArtifactDigest: occurrence.ArtifactDigest, ScheduledAt: formatTime(occurrence.ScheduledAt), Timezone: occurrence.Timezone, MatchingScheduleIdsJson: matchingJSON, ClaimedAt: sql.NullString{String: formatTime(now), Valid: true},
		})
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			result, err = queries.ClaimPendingRefreshPipelineOccurrence(ctx, platformdb.ClaimPendingRefreshPipelineOccurrenceParams{
				ClaimedAt: sql.NullString{String: formatTime(now), Valid: true}, ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
				PipelineID: occurrence.PipelineID.String(), ScheduledAt: formatTime(occurrence.ScheduledAt),
			})
			if err != nil {
				return nil, err
			}
			affected, err = result.RowsAffected()
			if err != nil {
				return nil, err
			}
		}
		if affected != 1 {
			// A row claimed by another generation/dispatcher is intentionally
			// omitted; the logical occurrence key is generation-independent.
			continue
		}
		row, err := queries.GetRefreshPipelineOccurrence(ctx, platformdb.GetRefreshPipelineOccurrenceParams{ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(), ScheduledAt: formatTime(occurrence.ScheduledAt)})
		if err != nil {
			return nil, err
		}
		ids, err := decodeOccurrenceScheduleIDs(row.MatchingScheduleIdsJson)
		if err != nil {
			return nil, err
		}
		storedIdentity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
		if err != nil {
			return nil, err
		}
		occurrence.Identity, occurrence.ArtifactDigest, occurrence.Timezone, occurrence.MatchingScheduleIDs = storedIdentity, row.ArtifactDigest, row.Timezone, ids
		claimed = append(claimed, occurrence)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (repository *Repository) ReleaseOccurrence(ctx context.Context, occurrence refreshschedule.Occurrence) error {
	if err := validateOccurrence(occurrence); err != nil {
		return err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := repository.q.WithTx(tx)
	if err := verifyStoredOccurrenceTx(ctx, queries, occurrence); err != nil {
		return err
	}
	result, err := queries.ReleaseRefreshPipelineOccurrence(ctx, platformdb.ReleaseRefreshPipelineOccurrenceParams{
		TerminalReason: "dispatch failed", ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
		PipelineID: occurrence.PipelineID.String(), ScheduledAt: formatTime(occurrence.ScheduledAt),
	})
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("refresh pipeline occurrence no longer exists in serving generation")
	}
	if err := queries.RetryRefreshPipelineSchedules(ctx, platformdb.RetryRefreshPipelineSchedulesParams{
		RetryAt: formatTime(occurrence.ScheduledAt), ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
		PipelineID: occurrence.PipelineID.String(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func verifyStoredOccurrenceTx(ctx context.Context, queries *platformdb.Queries, occurrence refreshschedule.Occurrence) error {
	row, err := queries.GetRefreshPipelineOccurrence(ctx, platformdb.GetRefreshPipelineOccurrenceParams{ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(), ScheduledAt: formatTime(occurrence.ScheduledAt)})
	if err != nil {
		return fmt.Errorf("refresh pipeline occurrence lookup: %w", err)
	}
	return verifyStoredOccurrenceRow(row, occurrence)
}

func verifyStoredOccurrenceRow(row platformdb.GetRefreshPipelineOccurrenceRow, occurrence refreshschedule.Occurrence) error {
	if row.GenerationID != occurrence.Identity.GenerationID || row.ArtifactDigest != occurrence.ArtifactDigest {
		return fmt.Errorf("refresh pipeline occurrence serving identity does not match captured generation")
	}
	return nil
}

func (repository *Repository) NextRun(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID) (time.Time, bool, error) {
	if err := refreshschedule.ValidateScope(identity); err != nil {
		return time.Time{}, false, err
	}
	if err := pipelineID.Validate(); err != nil {
		return time.Time{}, false, err
	}
	value, err := repository.q.GetRefreshPipelineNextRun(ctx, platformdb.GetRefreshPipelineNextRunParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, PipelineID: pipelineID.String(),
	})
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	next, err := parseTime(value)
	if err != nil {
		return time.Time{}, false, err
	}
	return next, true, nil
}

func (repository *Repository) SaveDataVersion(ctx context.Context, version refreshschedule.DataVersion) error {
	if err := refreshschedule.ValidateScope(version.Identity); err != nil {
		return fmt.Errorf("complete semantic-model data version is required: %w", err)
	}
	if err := version.SemanticModelID.Validate(); err != nil || version.SnapshotID <= 0 || version.RefreshedAt.IsZero() {
		return fmt.Errorf("complete semantic-model data version is required")
	}
	if version.PipelineID != "" {
		if err := version.PipelineID.Validate(); err != nil {
			return fmt.Errorf("pipeline id is invalid: %w", err)
		}
	}
	if version.RunID != "" {
		if err := refreshschedule.ValidateOperationalID(version.RunID); err != nil {
			return err
		}
	}
	if version.Source != refreshschedule.DataVersionSourcePublish && version.Source != refreshschedule.DataVersionSourceRefresh {
		return fmt.Errorf("semantic-model data version source must be publish or refresh")
	}
	return repository.q.UpsertSemanticModelDataVersion(ctx, platformdb.UpsertSemanticModelDataVersionParams{
		ProjectID: version.Identity.ProjectID.String(), Environment: version.Identity.Environment, SemanticModelID: version.SemanticModelID.String(),
		SnapshotID: version.SnapshotID, GenerationID: version.Identity.GenerationID, RefreshedAt: formatTime(version.RefreshedAt),
		Source: version.Source, PipelineID: version.PipelineID.String(), RunID: version.RunID,
	})
}

func (repository *Repository) DataVersion(ctx context.Context, identity projectgraph.ServingIdentity, semanticModelID projectgraph.ResourceID) (refreshschedule.DataVersion, bool, error) {
	if err := refreshschedule.ValidateScope(identity); err != nil {
		return refreshschedule.DataVersion{}, false, err
	}
	if err := semanticModelID.Validate(); err != nil {
		return refreshschedule.DataVersion{}, false, err
	}
	row, err := repository.q.GetSemanticModelDataVersion(ctx, platformdb.GetSemanticModelDataVersionParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, SemanticModelID: semanticModelID.String(),
	})
	if err == sql.ErrNoRows {
		return refreshschedule.DataVersion{}, false, nil
	}
	if err != nil {
		return refreshschedule.DataVersion{}, false, err
	}
	refreshedAt, err := parseTime(row.RefreshedAt)
	if err != nil {
		return refreshschedule.DataVersion{}, false, err
	}
	storedIdentity, err := projectgraph.NewServingIdentity(identity.ProjectID, row.Environment, row.GenerationID)
	if err != nil || storedIdentity != identity {
		return refreshschedule.DataVersion{}, false, fmt.Errorf("stored semantic-model data version has stale serving identity")
	}
	version := refreshschedule.DataVersion{
		Identity: storedIdentity, SemanticModelID: projectgraph.ResourceID(row.SemanticModelID),
		SnapshotID: row.SnapshotID, RefreshedAt: refreshedAt, Source: row.Source, PipelineID: projectgraph.ResourceID(row.PipelineID), RunID: row.RunID,
	}
	return version, true, nil
}

func validateOccurrence(occurrence refreshschedule.Occurrence) error {
	if err := refreshschedule.ValidateScope(occurrence.Identity); err != nil {
		return err
	}
	if err := occurrence.PipelineID.Validate(); err != nil {
		return err
	}
	if err := occurrence.SemanticModelID.Validate(); err != nil {
		return err
	}
	if err := refreshschedule.ValidateArtifactDigest(occurrence.ArtifactDigest); err != nil {
		return err
	}
	if occurrence.ScheduledAt.IsZero() {
		return fmt.Errorf("scheduled occurrence time is required")
	}
	if occurrence.Timezone == "" {
		return fmt.Errorf("scheduled occurrence timezone is required")
	}
	if _, err := time.LoadLocation(occurrence.Timezone); err != nil {
		return fmt.Errorf("scheduled occurrence timezone is invalid: %w", err)
	}
	if len(occurrence.MatchingScheduleIDs) == 0 {
		return fmt.Errorf("scheduled occurrence matching schedule IDs are required")
	}
	for i, id := range occurrence.MatchingScheduleIDs {
		if err := refreshschedule.ValidateOperationalID(id); err != nil {
			return err
		}
		if i > 0 && occurrence.MatchingScheduleIDs[i-1] >= id {
			return fmt.Errorf("scheduled occurrence matching schedule IDs must be sorted and unique")
		}
	}
	return nil
}

func scheduleKey(pipelineID, triggerID string) string {
	return pipelineID + "\x00" + triggerID
}

func encodeScheduleIDs(ids []string) (string, error) {
	copyIDs := append([]string(nil), ids...)
	sort.Strings(copyIDs)
	if len(copyIDs) == 0 {
		return "[]", nil
	}
	for i := 1; i < len(copyIDs); i++ {
		if copyIDs[i] == copyIDs[i-1] {
			return "", fmt.Errorf("matching schedule IDs must be unique")
		}
	}
	value, err := json.Marshal(copyIDs)
	return string(value), err
}

func decodeOccurrenceScheduleIDs(value string) ([]string, error) {
	var ids []string
	if err := json.Unmarshal([]byte(value), &ids); err != nil {
		return nil, fmt.Errorf("decode matching schedule IDs: %w", err)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			return nil, fmt.Errorf("stored matching schedule IDs are not sorted and unique")
		}
	}
	return ids, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse refresh pipeline timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
