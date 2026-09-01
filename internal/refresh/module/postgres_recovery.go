package module

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/pkg/jobs"
)

// PostgresTerminalRecovery is the explicit startup authority for refresh runs
// and their canonical jobs left live across process interruption.
type PostgresTerminalRecovery struct {
	Refresh *refreshpostgres.Repository
	Jobs    PostgresQueueRecovery
}

var _ TerminalRunRecovery = (*PostgresTerminalRecovery)(nil)

func NewPostgresTerminalRecovery(refresh *refreshpostgres.Repository, jobs PostgresQueueRecovery) (*PostgresTerminalRecovery, error) {
	if err := validatePostgresQueueAuthority(refresh, jobs); err != nil {
		return nil, err
	}
	return &PostgresTerminalRecovery{Refresh: refresh, Jobs: jobs}, nil
}

func validatePostgresQueueAuthority(refresh *refreshpostgres.Repository, jobs PostgresQueueRecovery) error {
	if refresh == nil || !refresh.Configured() {
		return errors.New("configured refresh PostgreSQL repository is required")
	}
	if isNilPostgresCapability(jobs) {
		return errors.New("canonical PostgreSQL jobs recovery authority is required")
	}
	authority, ok := jobs.(postgresQueueAuthority)
	if !ok {
		return errors.New("canonical PostgreSQL jobs recovery authority provenance is required")
	}
	if !authority.Configured() {
		return errors.New("configured canonical PostgreSQL jobs recovery authority is required")
	}
	if !authority.MatchesRefreshRepository(refresh) {
		return errors.New("canonical PostgreSQL jobs recovery authority does not match refresh repository")
	}
	return nil
}

func isNilPostgresCapability(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (r *PostgresTerminalRecovery) FailRunsForTerminalServingStates(ctx context.Context, environment, message string) error {
	if r == nil {
		return errors.New("PostgreSQL terminal recovery is unavailable")
	}
	if err := validatePostgresQueueAuthority(r.Refresh, r.Jobs); err != nil {
		return fmt.Errorf("PostgreSQL terminal recovery is unavailable: %w", err)
	}
	if environment == "" {
		return errors.New("PostgreSQL terminal recovery environment is required")
	}
	// The caller's message is intentionally ignored: startup recovery persists
	// only this bounded classifier, never arbitrary process/payload text.
	message = "refresh startup reconciliation"
	return r.Refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
		var runAfterCreated time.Time
		var runAfterID string
		for {
			runs, err := r.Refresh.RecoveryRunsTx(ctx, tx, environment, runAfterCreated, runAfterID, refreshpostgres.MaxPageSize)
			if err != nil {
				return err
			}
			for _, run := range runs {
				job, jobErr := r.Jobs.GetJobTx(ctx, tx, run.JobID)
				if errors.Is(jobErr, jobs.ErrNotFound) {
					if err := r.failRunForTerminalEvidence(ctx, tx, run, message); err != nil {
						return err
					}
					continue
				}
				if jobErr != nil {
					return jobErr
				}
				if err := r.reconcileActivePair(ctx, tx, run, job, message); err != nil {
					return err
				}
			}
			if len(runs) < refreshpostgres.MaxPageSize {
				break
			}
			last := runs[len(runs)-1]
			runAfterCreated, runAfterID = last.CreatedAt, last.RunID
			if runAfterCreated.IsZero() || runAfterID == "" {
				return errors.New("active refresh run projection returned invalid cursor")
			}
		}
		// Active jobs may point at terminal refresh rows, which are excluded from
		// RecoveryRunsTx. Walk only the indexed live-job projection and reconcile
		// those reverse links; terminal history is never scanned.
		var afterCreated time.Time
		var afterID string
		for {
			activeJobs, listErr := r.Jobs.ActiveRefreshJobsTx(ctx, tx, afterCreated, afterID, refreshpostgres.MaxPageSize)
			if listErr != nil {
				return listErr
			}
			if len(activeJobs) == 0 {
				break
			}
			for _, job := range activeJobs {
				run, lookupErr := r.Refresh.LookupRunTx(ctx, tx, job.ResourceID)
				if errors.Is(lookupErr, refreshpostgres.ErrNotFound) {
					if err := r.Jobs.ReconcileTerminalTx(ctx, tx, job.ID, jobs.StatusCancelled); err != nil {
						return err
					}
					continue
				}
				if lookupErr != nil {
					return lookupErr
				}
				if run.Environment != environment {
					// The jobs projection spans the database; this startup pass is
					// explicitly scoped to one configured environment.
					continue
				}
				if isTerminalRun(run.Status) {
					if run.Status != "skipped" {
						if err := r.Refresh.ReconcileOccurrenceTerminalTx(ctx, tx, run.RunID, run.Status, []byte(`{"code":"REFRESH_STARTUP_RECONCILIATION"}`)); err != nil {
							return err
						}
					}
					desired, mapErr := desiredJobStatus(run.Status)
					if mapErr != nil {
						return mapErr
					}
					if err := r.Jobs.ReconcileTerminalTx(ctx, tx, job.ID, desired); err != nil {
						return err
					}
					continue
				}
				if run.JobID != job.ID {
					return fmt.Errorf("ambiguous refresh/job recovery pair %q/%q", run.RunID, job.ID)
				}
				if err := r.reconcileActivePair(ctx, tx, refreshpostgres.RecoveryRun{RunID: run.RunID, JobID: run.JobID, Status: run.Status, Generation: run.FenceGeneration, LeaseOwner: run.LeaseOwner, LeaseExpiresAt: run.LeaseExpiresAt, Environment: run.Environment}, job, message); err != nil {
					return err
				}
			}
			if len(activeJobs) < refreshpostgres.MaxPageSize {
				break
			}
			last := activeJobs[len(activeJobs)-1]
			afterCreated, _ = time.Parse(time.RFC3339Nano, last.CreatedAt)
			afterID = last.ID
			if afterCreated.IsZero() {
				return errors.New("active refresh job projection returned invalid created timestamp")
			}
		}
		return nil
	})
}

func desiredJobStatus(runStatus string) (jobs.Status, error) {
	switch runStatus {
	case "succeeded":
		return jobs.StatusSucceeded, nil
	case "failed":
		return jobs.StatusFailed, nil
	case "cancelled", "superseded", "skipped":
		return jobs.StatusCancelled, nil
	default:
		return "", fmt.Errorf("unsupported terminal refresh status %q", runStatus)
	}
}

func (r *PostgresTerminalRecovery) reconcileActivePair(ctx context.Context, tx refreshpostgres.Tx, run refreshpostgres.RecoveryRun, job jobs.Job, message string) error {
	if run.Status == "queued" && job.Status == jobs.StatusQueued {
		return nil
	}
	if (run.Status == "running" || run.Status == "prepared") && job.Status == jobs.StatusRunning && run.LeaseOwner == job.LeaseOwner && run.Generation == job.LeaseGeneration {
		return nil
	}
	if job.Status == jobs.StatusFailed || job.Status == jobs.StatusCancelled || job.Status == jobs.StatusSucceeded {
		return r.failRunForTerminalEvidence(ctx, tx, run, message)
	}
	return fmt.Errorf("ambiguous refresh/job recovery pair %q/%q", run.RunID, run.JobID)
}

func isTerminalRun(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "superseded", "skipped":
		return true
	default:
		return false
	}
}

func (r *PostgresTerminalRecovery) failRunForTerminalEvidence(ctx context.Context, tx refreshpostgres.Tx, run refreshpostgres.RecoveryRun, message string) error {
	_ = message
	message = "refresh startup reconciliation"
	evidence := []byte(`{"code":"REFRESH_STARTUP_RECONCILIATION"}`)
	if run.Status == "queued" {
		return r.Refresh.FailQueuedRunTreeTx(ctx, tx, run.RunID, message)
	}
	return r.Refresh.FailRunTerminalEvidenceTx(ctx, tx, run.RunID, message, evidence)
}

// Compile-time assertion keeps the concrete jobs repository aligned with the
// recovery surface without importing it into refresh persistence contracts.
var _ PostgresQueueRecovery = (*PostgresJobsAdapter)(nil)
