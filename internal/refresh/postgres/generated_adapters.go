package postgres

// Typed adapters around sqlc's generated rows. They keep the repository's
// domain models independent from pgtype null wrappers while retaining sqlc as
// the sole SQL execution boundary.

import (
	"encoding/json"
	"time"

	db "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func ts(v pgtype.Timestamptz) time.Time {
	if !v.Valid || v.Time.Equal(time.Unix(0, 0).UTC()) {
		return time.Time{}
	}
	return v.Time
}

func scheduleFrom(r db.GetScheduleRevisionRow) Schedule {
	return Schedule{ScheduleInput: ScheduleInput{ScheduleRevisionID: r.ScheduleRevisionID, ProjectID: r.ProjectID, Environment: r.Environment, PipelineID: r.PipelineID, ScheduleID: r.ScheduleID, SemanticModelID: r.SemanticModelID, GenerationID: r.GenerationID, ArtifactDigest: r.ArtifactDigest, Cron: r.Cron, Timezone: r.Timezone, StartingDeadline: r.StartingDeadline, ConcurrencyPolicy: r.ConcurrencyPolicy, ScheduleDigest: r.ScheduleDigest, NextRunAt: r.NextRunAt, Enabled: r.Enabled}, ValidFrom: r.ValidFrom, ClosedAt: ts(r.ClosedAt), UpdatedAt: r.UpdatedAt}
}
func scheduleForUpdate(r db.GetScheduleRevisionForUpdateRow) Schedule {
	return Schedule{ScheduleInput: ScheduleInput{ScheduleRevisionID: r.ScheduleRevisionID, ProjectID: r.ProjectID, Environment: r.Environment, PipelineID: r.PipelineID, ScheduleID: r.ScheduleID, SemanticModelID: r.SemanticModelID, GenerationID: r.GenerationID, ArtifactDigest: r.ArtifactDigest, Cron: r.Cron, Timezone: r.Timezone, StartingDeadline: r.StartingDeadline, ConcurrencyPolicy: r.ConcurrencyPolicy, ScheduleDigest: r.ScheduleDigest, NextRunAt: r.NextRunAt, Enabled: r.Enabled}, ValidFrom: r.ValidFrom, ClosedAt: ts(r.ClosedAt), UpdatedAt: r.UpdatedAt}
}
func scheduleFromDue(r db.ListDueSchedulesRow) Schedule {
	return Schedule{ScheduleInput: ScheduleInput{ScheduleRevisionID: r.ScheduleRevisionID, ProjectID: r.ProjectID, Environment: r.Environment, PipelineID: r.PipelineID, ScheduleID: r.ScheduleID, SemanticModelID: r.SemanticModelID, GenerationID: r.GenerationID, ArtifactDigest: r.ArtifactDigest, Cron: r.Cron, Timezone: r.Timezone, StartingDeadline: r.StartingDeadline, ConcurrencyPolicy: r.ConcurrencyPolicy, ScheduleDigest: r.ScheduleDigest, NextRunAt: r.NextRunAt, Enabled: r.Enabled}, ValidFrom: r.ValidFrom, ClosedAt: ts(r.ClosedAt), UpdatedAt: r.UpdatedAt}
}

func occurrenceFrom(r db.GetOccurrenceRow) Occurrence {
	var ids []string
	_ = json.Unmarshal(r.MatchingScheduleIds, &ids)
	return Occurrence{OccurrenceID: r.OccurrenceID, ProjectID: r.ProjectID, Environment: r.Environment, PipelineID: r.PipelineID, NominalTime: r.NominalTime, ScheduleRevisionID: r.ScheduleRevisionID, MatchingScheduleIDs: ids, SemanticModelID: r.SemanticModelID, GenerationID: r.GenerationID, ArtifactDigest: r.ArtifactDigest, Status: r.Status, RunID: r.RunID, FenceGeneration: r.FenceGeneration, LeaseOwner: r.LeaseOwner, LeaseExpiresAt: ts(r.LeaseExpiresAt), ClaimedAt: ts(r.ClaimedAt), FinishedAt: ts(r.FinishedAt), CreatedAt: r.CreatedAt, Outcome: r.Outcome}
}

func runFrom(r db.GetRunRow) Run {
	var m, s []string
	_ = json.Unmarshal(r.MatchingScheduleIds, &m)
	_ = json.Unmarshal(r.MaterializationScope, &s)
	return Run{RunInput: RunInput{RunID: r.RunID, OperationID: r.OperationID, ProjectID: r.ProjectID, Environment: r.Environment, GenerationID: r.GenerationID, ParentRunID: r.ParentRunID, PipelineID: r.PipelineID, SemanticModelID: r.SemanticModelID, TargetType: r.TargetType, TargetID: r.TargetID, TargetRevision: r.TargetRevision, TriggerType: r.TriggerType, InvocationSource: r.InvocationSource, TriggerID: r.TriggerID, ConcurrencyPolicy: r.ConcurrencyPolicy, ScheduleRevisionID: r.ScheduleRevisionID, OccurrenceID: r.OccurrenceID, NominalTime: ts(r.NominalTime), PlanDigest: r.PlanDigest, ArtifactDigest: r.ArtifactDigest, MatchingScheduleIDs: m, MaterializationScope: s, PrincipalID: r.PrincipalID, JobID: r.JobID}, Status: r.Status, AttemptCount: r.AttemptCount, FenceGeneration: r.FenceGeneration, LeaseOwner: r.LeaseOwner, LeaseExpiresAt: ts(r.LeaseExpiresAt), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, StartedAt: ts(r.StartedAt), FinishedAt: ts(r.FinishedAt), Error: r.Error}
}

func operationFrom(r db.GetOperationRow) Operation {
	return Operation{OperationInput: OperationInput{OperationID: r.OperationID, ProjectID: r.ProjectID, Environment: r.Environment, IdempotencyKey: r.IdempotencyKey, RequestDigest: r.RequestDigest, OperationType: r.OperationType, OwnerID: r.OwnerID}, State: r.State, RunID: r.RunID, FenceGeneration: r.FenceGeneration, LeaseExpiresAt: ts(r.LeaseExpiresAt), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, TerminalAt: ts(r.TerminalAt), Outcome: r.Outcome}
}
func operationForUpdateFrom(r db.GetOperationForUpdateRow) Operation {
	return Operation{OperationInput: OperationInput{OperationID: r.OperationID, ProjectID: r.ProjectID, Environment: r.Environment, IdempotencyKey: r.IdempotencyKey, RequestDigest: r.RequestDigest, OperationType: r.OperationType, OwnerID: r.OwnerID}, State: r.State, RunID: r.RunID, FenceGeneration: r.FenceGeneration, LeaseExpiresAt: ts(r.LeaseExpiresAt), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, TerminalAt: ts(r.TerminalAt), Outcome: r.Outcome}
}
func publicationFrom(r db.GetPublicationRow) Publication {
	return Publication{PublicationInput: PublicationInput{PublicationID: r.PublicationID, RunID: r.RunID, BaseGenerationID: r.BaseGenerationID, ResultGenerationID: r.ResultGenerationID, PlanDigest: r.PlanDigest, ArtifactDigest: r.ArtifactDigest, PhysicalPoolID: r.PhysicalPoolID, CatalogID: r.CatalogID, FenceGeneration: r.FenceGeneration, ExpectedTargetRevision: r.ExpectedTargetRevision, ResultTargetRevision: r.ResultTargetRevision, SnapshotID: r.SnapshotID, OwnerID: r.OwnerID, Evidence: r.Evidence}, State: r.State, CreatedAt: r.CreatedAt, CommittedAt: ts(r.CommittedAt)}
}
func recoveryFrom(r db.GetRecoveryRow) RecoveryState {
	return RecoveryState{RecoveryInput: RecoveryInput{RunID: r.RunID, State: r.State, FenceGeneration: r.ReconciliationFence, OwnerID: r.OwnerID, ExactExternalIdentity: r.ExactExternalIdentity, LastError: r.LastError, Evidence: r.Evidence, NextReconcileAt: ts(r.NextReconcileAt)}, LeaseExpiresAt: ts(r.LeaseExpiresAt), UpdatedAt: r.UpdatedAt}
}
func dataVersionFrom(r db.GetDataVersionRow) DataVersion {
	return DataVersion{ProjectID: r.ProjectID, Environment: r.Environment, SemanticModelID: r.SemanticModelID, GenerationID: r.GenerationID, SnapshotID: r.SnapshotID, RefreshedAt: r.RefreshedAt, Source: r.Source, PipelineID: r.PipelineID, RunID: r.RunID, TargetRevision: r.TargetRevision, LeaseOwner: r.LeaseOwner, LeaseRevision: r.LeaseRevision, PhysicalPoolID: r.PhysicalPoolID, CatalogID: r.CatalogID}
}
func dataVersionFromUpdate(r db.GetDataVersionForUpdateRow) DataVersion {
	return DataVersion{ProjectID: r.ProjectID, Environment: r.Environment, SemanticModelID: r.SemanticModelID, GenerationID: r.GenerationID, SnapshotID: r.SnapshotID, RefreshedAt: r.RefreshedAt, Source: r.Source, PipelineID: r.PipelineID, RunID: r.RunID, TargetRevision: r.TargetRevision, LeaseOwner: r.LeaseOwner, LeaseRevision: r.LeaseRevision, PhysicalPoolID: r.PhysicalPoolID, CatalogID: r.CatalogID}
}
