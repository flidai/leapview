package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
	parentProject, parentEnvironment, parentRequestDigest, err := r.approvalParentScope(ctx, approval.DeploymentID)
	if err != nil {
		return deployment.Approval{}, err
	}
	if parentProject != approval.ProjectID ||
		parentEnvironment != approval.Environment ||
		parentRequestDigest != approval.RequestDigest {
		return deployment.Approval{}, deployment.ErrApprovalScope
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Approval{}, err
	}
	defer tx.Rollback()
	err = platformdb.New(tx).CreateDeploymentApproval(
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
	if err := appendApprovalEventTx(ctx, tx, approval, "approval_requested", approval.RequestedBy, approval.RequestedAt); err != nil {
		return deployment.Approval{}, err
	}
	if err := r.recordApprovalAuditIntent(ctx, tx, approval); err != nil {
		return deployment.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Approval{}, err
	}
	return approval, nil
}

func (r *Repository) approvalParentScope(ctx context.Context, id string) (string, string, string, error) {
	parent, err := r.DeploymentByID(ctx, id)
	if err == nil {
		return parent.ServingIdentity.ProjectID.String(), parent.ServingIdentity.Environment, parent.RequestDigest, nil
	}
	if !errors.Is(err, deployment.ErrNotFound) {
		return "", "", "", err
	}
	publication, err := r.DeliveryPublicationByID(ctx, id)
	if err != nil {
		return "", "", "", err
	}
	return publication.ProjectID.String(), publication.Environment, publication.RequestDigest, nil
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Approval{}, err
	}
	defer tx.Rollback()
	count, err := platformdb.New(tx).UpdateDeploymentApproval(
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
	switch approval.Status {
	case deployment.ApprovalApproved:
		if err := appendApprovalEventTx(ctx, tx, approval, "approval_granted", approval.ApprovedBy, approval.ApprovedAt); err != nil {
			return deployment.Approval{}, err
		}
	case deployment.ApprovalDenied:
		if err := appendApprovalEventTx(ctx, tx, approval, "approval_rejected", approval.ApprovedBy, approval.ApprovedAt); err != nil {
			return deployment.Approval{}, err
		}
	case deployment.ApprovalRevoked:
		if err := appendApprovalEventTx(ctx, tx, approval, "approval_revoked", approval.RevokedBy, approval.RevokedAt); err != nil {
			return deployment.Approval{}, err
		}
	}
	if err := r.recordApprovalAuditIntent(ctx, tx, approval); err != nil {
		return deployment.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Approval{}, err
	}
	return approval, nil
}

func (r *Repository) recordApprovalAuditIntent(ctx context.Context, tx *sql.Tx, approval deployment.Approval) error {
	intent, ok := deployment.AuditIntentFromContext(ctx)
	if !ok {
		return nil
	}
	if r.hooks.Audit == nil {
		return fmt.Errorf("deployment audit intent recorder is required")
	}
	intent.AggregateSequence = approval.Revision
	if approval.ID != "" {
		// Request-approval intents are built before the repository allocates its
		// random approval identity. Fill only that generated metadata field here;
		// all other metadata remains transport-owned and canonical.
		intent.MetadataJSON = setAuditMetadataString(intent.MetadataJSON, "approvalId", approval.ID)
	}
	return r.hooks.Audit.RecordAuditIntent(ctx, tx, intent)
}

func setAuditMetadataString(raw, key, value string) string {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return raw
	}
	metadata[key] = value
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// appendApprovalEventTx bridges the deployment approval projection to the
// delivery ledger when the deployment scope has a plan-delivery target. Older
// legacy-only deployments have no target revision and therefore cannot satisfy
// the ledger's target foreign key; those rows remain governed by their own
// immutable deployment approval history.
func appendApprovalEventTx(ctx context.Context, q platformdb.DBTX, approval deployment.Approval, kind, actor string, at time.Time) error {
	scope, err := platformdb.New(q).GetDeliveryTargetScope(ctx, platformdb.GetDeliveryTargetScopeParams{ProjectID: approval.ProjectID, Environment: approval.Environment})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	targetID := scope.TargetID
	if strings.TrimSpace(actor) == "" {
		actor = approval.RequestedBy
	}
	if at.IsZero() {
		at = approval.RequestedAt
	}
	_, err = appendDeliveryEventTx(ctx, q, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(targetID, approval.RequestDigest, kind, "approval", approval.ID), TargetID: targetID,
		ProjectID: approval.ProjectID, Environment: approval.Environment, ActorID: actor, EventKind: kind,
		ObjectKind: "approval", ObjectID: approval.ID, RequestDigest: approval.RequestDigest,
		Outcome: "accepted", Details: map[string]any{"status": string(approval.Status)}, CreatedAt: at,
	})
	return err
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
