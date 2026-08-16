package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db, q: platformdb.New(db)} }

type persistedSchedule struct {
	artifactDigest string
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
	if err := queries.DeleteRefreshPipelineSchedules(ctx, platformdb.DeleteRefreshPipelineSchedulesParams{ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, GenerationID: input.Identity.GenerationID}); err != nil {
		return err
	}
	for _, pipeline := range input.Pipelines {
		if err := pipeline.Validate(); err != nil {
			return err
		}
		for _, schedule := range pipeline.Schedules {
			key := scheduleKey(pipeline.ID, schedule.Expression, schedule.Timezone)
			next := schedule.Next(input.Now)
			if prior, ok := existing[key]; ok && prior.artifactDigest == input.ArtifactDigest {
				next = prior.nextRunAt
			}
			if next.IsZero() {
				return fmt.Errorf("refresh pipeline %q schedule %q has no next occurrence", pipeline.ID, schedule.Expression)
			}
			if err := queries.CreateRefreshPipelineSchedule(ctx, platformdb.CreateRefreshPipelineScheduleParams{
				ProjectID: input.Identity.ProjectID.String(), Environment: input.Identity.Environment, PipelineID: pipeline.ID.String(),
				SemanticModelID: pipeline.SemanticModelID.String(), GenerationID: input.Identity.GenerationID, ArtifactDigest: input.ArtifactDigest,
				Cron: schedule.Expression, Timezone: schedule.Timezone, NextRunAt: formatTime(next),
			}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
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
		out[scheduleKey(row.PipelineID, row.Cron, row.Timezone)] = persistedSchedule{artifactDigest: row.ArtifactDigest, nextRunAt: next}
	}
	return out, nil
}

type dueSchedule struct {
	identity        projectgraph.ServingIdentity
	pipelineID      projectgraph.ResourceID
	semanticModelID projectgraph.ResourceID
	expression      string
	timezone        string
	artifactDigest  string
	nextRunAt       time.Time
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
	if err := queries.RequeueAbandonedRefreshPipelineSchedules(ctx, platformdb.RequeueAbandonedRefreshPipelineSchedulesParams{ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, ClaimedBefore: claimedBefore, Environment: identity.Environment}); err != nil {
		return nil, err
	}
	if err := queries.DeleteAbandonedRefreshPipelineOccurrences(ctx, platformdb.DeleteAbandonedRefreshPipelineOccurrencesParams{ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, ClaimedBefore: claimedBefore, Environment: identity.Environment}); err != nil {
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
			semanticModelID: projectgraph.ResourceID(row.SemanticModelID), expression: row.Cron, timezone: row.Timezone, artifactDigest: row.ArtifactDigest,
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
	type pipelineDue struct {
		occurrence refreshschedule.Occurrence
	}
	grouped := map[string]pipelineDue{}
	for _, item := range due {
		schedule, err := refreshschedule.ParseSchedule(item.expression, item.timezone)
		if err != nil {
			return nil, err
		}
		scheduledAt := item.nextRunAt
		next := schedule.Next(scheduledAt)
		for !next.IsZero() && !next.After(now) {
			scheduledAt = next
			next = schedule.Next(next)
		}
		if next.IsZero() {
			return nil, fmt.Errorf("refresh pipeline %q schedule %q has no next occurrence", item.pipelineID, item.expression)
		}
		if err := queries.AdvanceRefreshPipelineSchedule(ctx, platformdb.AdvanceRefreshPipelineScheduleParams{
			NextRunAt: formatTime(next), ProjectID: item.identity.ProjectID.String(), Environment: item.identity.Environment,
			GenerationID: item.identity.GenerationID, PipelineID: item.pipelineID.String(), Cron: item.expression, Timezone: item.timezone,
		}); err != nil {
			return nil, err
		}
		key := item.identity.ProjectID.String() + "\x00" + item.identity.Environment + "\x00" + item.identity.GenerationID + "\x00" + item.pipelineID.String()
		current := grouped[key]
		if current.occurrence.ScheduledAt.IsZero() || scheduledAt.After(current.occurrence.ScheduledAt) {
			current.occurrence = refreshschedule.Occurrence{
				Identity:   item.identity,
				PipelineID: item.pipelineID, SemanticModelID: item.semanticModelID, ArtifactDigest: item.artifactDigest, ScheduledAt: scheduledAt,
			}
			grouped[key] = current
		}
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]refreshschedule.Occurrence, 0, len(keys))
	for _, key := range keys {
		occurrence := grouped[key].occurrence
		result, err := queries.ClaimRefreshPipelineOccurrence(ctx, platformdb.ClaimRefreshPipelineOccurrenceParams{
			ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(),
			GenerationID: occurrence.Identity.GenerationID, ArtifactDigest: occurrence.ArtifactDigest, ScheduledAt: formatTime(occurrence.ScheduledAt), ClaimedAt: formatTime(now),
		})
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			out = append(out, occurrence)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (repository *Repository) AttachRun(ctx context.Context, occurrence refreshschedule.Occurrence, runID string) error {
	if err := validateOccurrence(occurrence); err != nil {
		return err
	}
	if err := refreshschedule.ValidateOperationalID(runID); err != nil {
		return err
	}
	result, err := repository.q.AttachRefreshPipelineRun(ctx, platformdb.AttachRefreshPipelineRunParams{
		RunID: sql.NullString{String: runID, Valid: true}, ProjectID: occurrence.Identity.ProjectID.String(),
		Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(), GenerationID: occurrence.Identity.GenerationID, ScheduledAt: formatTime(occurrence.ScheduledAt),
	})
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("refresh pipeline occurrence no longer exists")
	}
	return nil
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
	result, err := queries.DeleteUnattachedRefreshPipelineOccurrence(ctx, platformdb.DeleteUnattachedRefreshPipelineOccurrenceParams{
		ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
		PipelineID: occurrence.PipelineID.String(), GenerationID: occurrence.Identity.GenerationID, ScheduledAt: formatTime(occurrence.ScheduledAt),
	})
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		if err := queries.RetryRefreshPipelineSchedules(ctx, platformdb.RetryRefreshPipelineSchedulesParams{
			RetryAt: formatTime(occurrence.ScheduledAt), ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
			PipelineID: occurrence.PipelineID.String(), GenerationID: occurrence.Identity.GenerationID, ArtifactDigest: occurrence.ArtifactDigest,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	return nil
}

func scheduleKey(pipelineID, expression, timezone string) string {
	return pipelineID + "\x00" + expression + "\x00" + timezone
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse refresh pipeline timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
