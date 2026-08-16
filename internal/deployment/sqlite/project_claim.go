package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func (r *Repository) ClaimProject(ctx context.Context, input deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	if err := input.Validate(); err != nil {
		return deployment.ProjectClaim{}, err
	}
	if r == nil || r.db == nil {
		return deployment.ProjectClaim{}, fmt.Errorf("project claim database is unavailable")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.ProjectClaim{}, err
	}
	defer tx.Rollback()
	claim, err := claimProjectTx(ctx, deploydb.New(tx), input)
	if err != nil {
		return deployment.ProjectClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.ProjectClaim{}, err
	}
	return claim, nil
}

func (r *Repository) ProjectClaim(ctx context.Context) (deployment.ProjectClaim, error) {
	if r == nil || r.db == nil {
		return deployment.ProjectClaim{}, fmt.Errorf("project claim database is unavailable")
	}
	row, err := r.queries.GetInstanceProjectClaim(ctx)
	return mapProjectClaim(row, err)
}

func (r *Repository) GetProjectClaim(ctx context.Context) (deployment.ProjectClaim, error) {
	return r.ProjectClaim(ctx)
}

type claimQuerier interface {
	InsertInstanceProjectClaim(context.Context, deploydb.InsertInstanceProjectClaimParams) (sql.Result, error)
	GetInstanceProjectClaim(context.Context) (deploydb.GetInstanceProjectClaimRow, error)
}

func claimProjectTx(ctx context.Context, db claimQuerier, input deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	if err := input.Validate(); err != nil {
		return deployment.ProjectClaim{}, err
	}
	result, err := db.InsertInstanceProjectClaim(ctx, deploydb.InsertInstanceProjectClaimParams{ProjectID: input.ProjectID.String(), Environment: string(input.Environment), ClaimedBy: input.ClaimedBy, ClaimedAt: input.ClaimedAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return deployment.ProjectClaim{}, err
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		return deployment.ProjectClaim{ProjectID: input.ProjectID, Environment: input.Environment, ClaimedBy: input.ClaimedBy, ClaimedAt: input.ClaimedAt.UTC()}, nil
	}
	row, readErr := db.GetInstanceProjectClaim(ctx)
	existing, err := mapProjectClaim(row, readErr)
	if err != nil {
		return deployment.ProjectClaim{}, err
	}
	if existing.ProjectID != input.ProjectID || existing.Environment != input.Environment {
		return deployment.ProjectClaim{}, fmt.Errorf("%w: existing claim is %s/%s, requested %s/%s", deployment.ErrProjectClaimConflict, existing.ProjectID, existing.Environment, input.ProjectID, input.Environment)
	}
	return existing, nil
}

func mapProjectClaim(row deploydb.GetInstanceProjectClaimRow, err error) (deployment.ProjectClaim, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimNotFound
	}
	if err != nil {
		return deployment.ProjectClaim{}, err
	}
	id, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.ProjectClaim{}, fmt.Errorf("stored project claim project: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, row.ClaimedAt)
	if err != nil {
		return deployment.ProjectClaim{}, fmt.Errorf("stored project claim time: %w", err)
	}
	claim := deployment.ProjectClaim{ProjectID: id, Environment: servingstate.Environment(row.Environment), ClaimedBy: row.ClaimedBy, ClaimedAt: parsedAt.UTC()}
	if err := claim.Validate(); err != nil {
		return deployment.ProjectClaim{}, fmt.Errorf("stored project claim: %w", err)
	}
	return claim, nil
}

// claimProjectTx is also used by candidate creation, keeping the singleton
// claim and candidate row in one SQLite transaction.
func (r *Repository) claimProjectTx(ctx context.Context, tx *sql.Tx, input deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	return claimProjectTx(ctx, deploydb.New(tx), input)
}

var _ deployment.ProjectClaimRepository = (*Repository)(nil)
