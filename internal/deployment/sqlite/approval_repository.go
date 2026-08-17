package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	platformdb "github.com/flidai/leapview/internal/deployment/internal/db"
)

func (r *Repository) CreateApproval(
	ctx context.Context,
	approval deployment.Approval,
) (deployment.Approval, error) {
	if r == nil || r.queries == nil {
		return deployment.Approval{}, fmt.Errorf("deployment approval repository is unavailable")
	}
	if err := approval.Validate(); err != nil {
		return deployment.Approval{}, err
	}
	parent, err := r.DeploymentByID(ctx, approval.DeploymentID)
	if err != nil {
		return deployment.Approval{}, err
	}
	if parent.ServingIdentity.ProjectID.String() != approval.ProjectID ||
		parent.ServingIdentity.Environment != approval.Environment ||
		parent.RequestDigest != approval.RequestDigest {
		return deployment.Approval{}, deployment.ErrApprovalScope
	}
	err = r.queries.CreateDeploymentApproval(
		ctx,
		platformdb.CreateDeploymentApprovalParams{
			ID: approval.ID, ProjectID: approval.ProjectID,
			DeploymentID:           approval.DeploymentID,
			Environment:            approval.Environment,
			RequestDigest:          approval.RequestDigest,
			ReleaseID:              approval.ReleaseID,
			Status:                 string(approval.Status),
			RequestedBy:            approval.RequestedBy,
			RequestCredentialClass: string(approval.RequestCredentialClass),
			RequestCredentialID:    approval.RequestCredentialID,
			RequestedAt:            formatApprovalTime(approval.RequestedAt),
			ApprovedBy:             nullableSQLString(approval.ApprovedBy),
			ApprovalCredentialClass: nullableSQLString(
				string(approval.ApprovalCredentialClass),
			),
			ApprovalCredentialID: nullableSQLString(approval.ApprovalCredentialID),
			ApprovalCredentialExpiresAt: nullableApprovalTime(
				approval.ApprovalCredentialExpiresAt,
			),
			ApprovedAt: nullableApprovalTime(approval.ApprovedAt),
			RevokedBy:  nullableSQLString(approval.RevokedBy),
			RevokedAt:  nullableApprovalTime(approval.RevokedAt),
			ExpiresAt:  formatApprovalTime(approval.ExpiresAt),
			Revision:   approval.Revision,
		},
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return deployment.Approval{}, deployment.ErrApprovalConflict
		}
		return deployment.Approval{}, err
	}
	return approval, nil
}

func (r *Repository) ApprovalByDeployment(
	ctx context.Context,
	deploymentID string,
) (deployment.Approval, error) {
	if r == nil || r.queries == nil || deploymentID == "" || deploymentID != strings.TrimSpace(deploymentID) {
		return deployment.Approval{}, deployment.ErrApprovalNotFound
	}
	row, err := r.queries.GetCurrentDeploymentApproval(
		ctx,
		deploymentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Approval{}, deployment.ErrApprovalNotFound
	}
	if err != nil {
		return deployment.Approval{}, err
	}
	return mapApproval(row)
}

func (r *Repository) SaveApproval(
	ctx context.Context,
	approval deployment.Approval,
	expectedRevision int64,
) (deployment.Approval, error) {
	if r == nil || r.queries == nil || expectedRevision <= 0 ||
		approval.Revision != expectedRevision+1 {
		return deployment.Approval{}, deployment.ErrApprovalConflict
	}
	if err := approval.Validate(); err != nil {
		return deployment.Approval{}, err
	}
	count, err := r.queries.UpdateDeploymentApproval(
		ctx,
		platformdb.UpdateDeploymentApprovalParams{
			Status:     string(approval.Status),
			ApprovedBy: nullableSQLString(approval.ApprovedBy),
			ApprovalCredentialClass: nullableSQLString(
				string(approval.ApprovalCredentialClass),
			),
			ApprovalCredentialID: nullableSQLString(approval.ApprovalCredentialID),
			ApprovalCredentialExpiresAt: nullableApprovalTime(
				approval.ApprovalCredentialExpiresAt,
			),
			ApprovedAt: nullableApprovalTime(approval.ApprovedAt),
			RevokedBy:  nullableSQLString(approval.RevokedBy),
			RevokedAt:  nullableApprovalTime(approval.RevokedAt),
			ExpiresAt:  formatApprovalTime(approval.ExpiresAt),
			Revision:   approval.Revision,
			ID:         approval.ID, DeploymentID: approval.DeploymentID,
			Revision_2: expectedRevision,
		},
	)
	if err != nil {
		return deployment.Approval{}, err
	}
	if count != 1 {
		return deployment.Approval{}, deployment.ErrApprovalConflict
	}
	return approval, nil
}

func mapApproval(row platformdb.DeploymentApproval) (deployment.Approval, error) {
	requestedAt, err := parseApprovalTime(row.RequestedAt)
	if err != nil {
		return deployment.Approval{}, err
	}
	approvedAt, err := parseNullableApprovalTime(row.ApprovedAt)
	if err != nil {
		return deployment.Approval{}, err
	}
	approvalCredentialExpiresAt, err := parseNullableApprovalTime(
		row.ApprovalCredentialExpiresAt,
	)
	if err != nil {
		return deployment.Approval{}, err
	}
	revokedAt, err := parseNullableApprovalTime(row.RevokedAt)
	if err != nil {
		return deployment.Approval{}, err
	}
	expiresAt, err := parseApprovalTime(row.ExpiresAt)
	if err != nil {
		return deployment.Approval{}, err
	}
	approval := deployment.Approval{
		ID: row.ID, ProjectID: row.ProjectID,
		DeploymentID:  row.DeploymentID,
		Environment:   row.Environment,
		RequestDigest: row.RequestDigest,
		ReleaseID:     row.ReleaseID,
		Status:        deployment.ApprovalStatus(row.Status),
		RequestedBy:   row.RequestedBy,
		RequestCredentialClass: deployment.CredentialClass(
			row.RequestCredentialClass,
		),
		RequestCredentialID: row.RequestCredentialID,
		RequestedAt:         requestedAt,
		ApprovedBy:          row.ApprovedBy.String,
		ApprovalCredentialClass: deployment.CredentialClass(
			row.ApprovalCredentialClass.String,
		),
		ApprovalCredentialID:        row.ApprovalCredentialID.String,
		ApprovalCredentialExpiresAt: approvalCredentialExpiresAt,
		ApprovedAt:                  approvedAt,
		RevokedBy:                   row.RevokedBy.String,
		RevokedAt:                   revokedAt,
		ExpiresAt:                   expiresAt,
		Revision:                    row.Revision,
	}
	if err := approval.Validate(); err != nil {
		return deployment.Approval{}, err
	}
	return approval, nil
}

func formatApprovalTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableApprovalTime(value time.Time) sql.NullString {
	if value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{
		String: formatApprovalTime(value),
		Valid:  true,
	}
}

func parseApprovalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"%w: invalid approval timestamp",
			deployment.ErrApprovalInvalid,
		)
	}
	return parsed.UTC(), nil
}

func parseNullableApprovalTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return parseApprovalTime(value.String)
}
