package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	materializedb "github.com/flidai/leapview/internal/refresh/internal/db"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// PublicationUnitOfWork owns the fenced cross-table transaction that makes a
// prepared refresh candidate visible and completes its durable work.
type PublicationUnitOfWork struct {
	db                  *sql.DB
	applyAccessSnapshot func(context.Context, transaction.Transaction, string) error
}

func NewPublicationUnitOfWork(database *sql.DB, applyAccessSnapshot func(context.Context, transaction.Transaction, string) error) *PublicationUnitOfWork {
	return &PublicationUnitOfWork{db: database, applyAccessSnapshot: applyAccessSnapshot}
}

func (u *PublicationUnitOfWork) Publish(ctx context.Context, identity projectgraph.ServingIdentity, servingStateID servingstate.ID, version refreshschedule.DataVersion) error {
	if u == nil || u.db == nil {
		return fmt.Errorf("refresh publication database is required")
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if version.Identity != identity {
		return fmt.Errorf("refresh publication identity does not match data version")
	}
	if servingStateID != "" && string(servingStateID) != identity.GenerationID {
		return fmt.Errorf("refresh publication serving state does not match identity")
	}
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := materializedb.New(tx)
	active, err := q.RefreshPublicationFenceActive(ctx, materializedb.RefreshPublicationFenceActiveParams{
		RunID: version.RunID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
		TargetRevision: version.TargetRevision, LeaseOwner: version.LeaseOwner, LeaseRevision: version.LeaseRevision,
	})
	if err != nil {
		return err
	}
	if active != 1 {
		return refreshrun.ErrLeaseLost
	}
	expectedRuns, err := q.CountRefreshPublicationTreeRuns(ctx, materializedb.CountRefreshPublicationTreeRunsParams{
		RunID: version.RunID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
	})
	if err != nil {
		return err
	}
	expectedJobs, err := q.CountRefreshPublicationTreeJobs(ctx, materializedb.CountRefreshPublicationTreeJobsParams{
		RunID: version.RunID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
	})
	if err != nil {
		return err
	}
	if expectedRuns < 1 || expectedJobs < 1 {
		return refreshrun.ErrLeaseLost
	}
	candidate, err := q.RefreshPublicationCandidate(ctx, materializedb.RefreshPublicationCandidateParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	})
	if err != nil {
		return err
	}
	if candidate.ProjectID != identity.ProjectID.String() {
		return fmt.Errorf("serving generation %s is not in project %s", identity.GenerationID, identity.ProjectID)
	}
	if candidate.Environment != identity.Environment {
		return fmt.Errorf("serving generation %s environment = %q, want %q", identity.GenerationID, candidate.Environment, identity.Environment)
	}
	status := servingstate.Status(candidate.Status)
	if status != servingstate.StatusValidated && status != servingstate.StatusInactive && status != servingstate.StatusActive {
		return fmt.Errorf("serving generation %s has status %q, want validated", identity.GenerationID, status)
	}
	if err := validatePublicationVersion(candidate, identity, version); err != nil {
		return err
	}
	if u.applyAccessSnapshot != nil {
		if err := u.applyAccessSnapshot(ctx, tx, identity.GenerationID); err != nil {
			return err
		}
	}
	if err := q.DrainOtherRefreshServingStates(ctx, materializedb.DrainOtherRefreshServingStatesParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	}); err != nil {
		return err
	}
	if err := q.ActivateRefreshServingState(ctx, materializedb.ActivateRefreshServingStateParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	}); err != nil {
		return err
	}
	if err := q.SetRefreshActiveServingState(ctx, materializedb.SetRefreshActiveServingStateParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	}); err != nil {
		return err
	}
	if err := q.AdvanceRefreshSemanticModelDataVersions(ctx, materializedb.AdvanceRefreshSemanticModelDataVersionsParams{
		SnapshotID: version.SnapshotID, GenerationID: identity.GenerationID, ProjectID: identity.ProjectID.String(),
		Environment: identity.Environment, SemanticModelID: version.SemanticModelID.String(),
	}); err != nil {
		return err
	}
	if err := q.UpsertRefreshPublicationDataVersion(ctx, materializedb.UpsertRefreshPublicationDataVersionParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, SemanticModelID: version.SemanticModelID.String(),
		SnapshotID: version.SnapshotID, GenerationID: identity.GenerationID,
		RefreshedAt: version.RefreshedAt.UTC().Format(time.RFC3339Nano), PipelineID: version.PipelineID.String(), RunID: version.RunID,
	}); err != nil {
		return err
	}
	completed, err := q.CompleteRefreshPublicationRun(ctx, materializedb.CompleteRefreshPublicationRunParams{
		RunID: version.RunID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
		TargetRevision: version.TargetRevision, LeaseOwner: version.LeaseOwner, LeaseRevision: version.LeaseRevision,
	})
	if err != nil {
		return err
	}
	if completed != expectedRuns {
		return refreshrun.ErrLeaseLost
	}
	completed, err = q.CompleteRefreshPublicationJob(ctx, materializedb.CompleteRefreshPublicationJobParams{
		RunID: version.RunID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
		LeaseOwner: version.LeaseOwner, LeaseRevision: version.LeaseRevision,
	})
	if err != nil {
		return err
	}
	if completed != expectedJobs {
		return refreshrun.ErrLeaseLost
	}
	return tx.Commit()
}

func validatePublicationVersion(candidate materializedb.RefreshPublicationCandidateRow, identity projectgraph.ServingIdentity, version refreshschedule.DataVersion) error {
	if candidate.DucklakeSnapshotID <= 0 || version.SemanticModelID == "" || version.RefreshedAt.IsZero() ||
		candidate.ProjectID != identity.ProjectID.String() || version.Identity != identity ||
		version.SnapshotID != candidate.DucklakeSnapshotID || version.Source != refreshschedule.DataVersionSourceRefresh ||
		version.TargetRevision <= 0 || strings.TrimSpace(version.LeaseOwner) == "" || version.LeaseRevision <= 0 {
		return fmt.Errorf("refresh publication requires a matching semantic-model data version")
	}
	return nil
}
