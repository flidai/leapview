package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
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
	canonical, err := validateCanonicalApprovalEvidenceTx(ctx, tx, approval)
	if err != nil {
		return deployment.Approval{}, err
	}
	if canonical && (approval.PlanDigest == "" || approval.EvidenceDigest == "") {
		return deployment.Approval{}, fmt.Errorf("%w: canonical publication approval evidence is incomplete", deployment.ErrApprovalScope)
	}
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
	approval, err := mapApproval(row)
	if err != nil {
		return deployment.Approval{}, err
	}
	approval, _, err = approvalEvidenceFromEventTx(ctx, r.db, approval, "approval_requested")
	return approval, err
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
	canonical, err := validateCanonicalApprovalEvidenceTx(ctx, tx, approval)
	if err != nil {
		return deployment.Approval{}, err
	}
	if canonical {
		requested, _, evidenceErr := approvalEvidenceFromEventTx(ctx, tx, approval, "approval_requested")
		if evidenceErr != nil {
			return deployment.Approval{}, evidenceErr
		}
		if requested.PlanDigest == "" || requested.EvidenceDigest == "" {
			// Historical canonical decisions did not carry an explicit evidence
			// reference. They remain readable and may move only to a terminal
			// non-activating state, but can never be approved or reused.
			if approval.Status != deployment.ApprovalDenied && approval.Status != deployment.ApprovalRevoked && approval.Status != deployment.ApprovalExpired {
				return deployment.Approval{}, fmt.Errorf("%w: canonical approval request has no evidence binding", deployment.ErrApprovalScope)
			}
		} else if approval.PlanDigest != requested.PlanDigest || approval.EvidenceDigest != requested.EvidenceDigest {
			return deployment.Approval{}, fmt.Errorf("%w: approval decision differs from requested evidence", deployment.ErrApprovalScope)
		}
	}
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
	if approval.ID != "" {
		// Approval revisions restart at one for each replacement approval. Keep
		// those independent revision streams in independent audit aggregates;
		// otherwise every replacement request collides with the first request's
		// (deployment:<id>:approval, 1) outbox row.
		intent.AggregateKey = approvalAuditAggregateKey(intent.AggregateKey, approval.ID)
	}
	intent.AggregateSequence = approval.Revision
	if approval.ID != "" {
		// Request-approval intents are built before the repository allocates its
		// random approval identity. Fill only that generated metadata field here;
		// all other metadata remains transport-owned and canonical.
		metadata, err := access.RewriteGeneratedAuditEnvelopePayload(intent.MetadataJSON, map[string]any{"approvalId": approval.ID})
		if err != nil {
			return err
		}
		intent.MetadataJSON = metadata
	}
	return r.hooks.Audit.RecordAuditIntent(ctx, tx, intent)
}

func approvalAuditAggregateKey(key, approvalID string) string {
	key = strings.TrimSpace(key)
	approvalID = strings.TrimSpace(approvalID)
	if approvalID == "" {
		return key
	}
	const marker = ":approval"
	if index := strings.LastIndex(key, marker); index >= 0 {
		return key[:index+len(marker)] + ":" + approvalID
	}
	return key + ":" + approvalID
}

// validateCanonicalApprovalEvidenceTx resolves the existing immutable
// publication -> plan -> candidate chain. The approval's publication scope is
// already stored in deployment_approvals; PlanDigest and EvidenceDigest are
// the explicit references retained in the append-only delivery event ledger.
// Legacy project-deployment approvals have no canonical publication and keep
// their existing request-digest binding.
func validateCanonicalApprovalEvidenceTx(ctx context.Context, q platformdb.DBTX, approval deployment.Approval) (bool, error) {
	publication, err := deliveryPublicationByIDTx(ctx, q, approval.DeploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if publication.ID != approval.DeploymentID ||
		publication.ProjectID.String() != approval.ProjectID ||
		publication.Environment != approval.Environment ||
		publication.RequestDigest != approval.RequestDigest {
		return true, deployment.ErrApprovalScope
	}
	plan, err := deliveryPlanByIDTx(ctx, q, publication.PlanID)
	if err != nil {
		return true, err
	}
	candidate, err := deliveryCandidateByIDTx(ctx, q, publication.CandidateID)
	if err != nil {
		return true, err
	}
	if plan.ID != publication.PlanID || plan.Digest != publication.PlanDigest ||
		plan.TargetID != publication.TargetID || plan.ProjectID != publication.ProjectID ||
		plan.Environment != publication.Environment ||
		candidate.ID != publication.CandidateID || candidate.PlanID != publication.PlanID ||
		candidate.PlanDigest != publication.PlanDigest || candidate.TargetID != publication.TargetID ||
		candidate.ProjectID != publication.ProjectID || candidate.Environment != publication.Environment ||
		candidate.ServingArtifactID != approval.ReleaseID {
		return true, deployment.ErrApprovalScope
	}
	if approval.PlanDigest == "" && approval.EvidenceDigest == "" {
		return true, nil
	}
	if approval.PlanDigest != plan.Digest || approval.EvidenceDigest != plan.EvidenceDigest {
		return true, deployment.ErrApprovalScope
	}
	return true, nil
}

// approvalEvidenceFromEventTx hydrates the explicit canonical evidence
// reference without adding approval columns. The boolean reports that a
// complete bound event was found (not merely that the parent is canonical).
// Existing delivery events are the immutable evidence authority and are
// written in the same transaction as the approval projection transition.
func approvalEvidenceFromEventTx(ctx context.Context, q platformdb.DBTX, approval deployment.Approval, kind string) (deployment.Approval, bool, error) {
	publication, err := deliveryPublicationByIDTx(ctx, q, approval.DeploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return approval, false, nil
	}
	if err != nil {
		return deployment.Approval{}, false, err
	}
	if publication.ProjectID.String() != approval.ProjectID ||
		publication.Environment != approval.Environment ||
		publication.RequestDigest != approval.RequestDigest {
		return deployment.Approval{}, false, deployment.ErrApprovalScope
	}
	row, err := platformdb.New(q).GetDeliveryEventByRequest(ctx, platformdb.GetDeliveryEventByRequestParams{
		TargetID: publication.TargetID, RequestDigest: approval.RequestDigest,
		EventKind: kind, ObjectKind: "approval", ObjectID: approval.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return approval, false, nil
	}
	if err != nil {
		return deployment.Approval{}, false, err
	}
	event, err := mapDeliveryEvent(row)
	if err != nil {
		return deployment.Approval{}, false, err
	}
	if event.TargetID != publication.TargetID || event.ProjectID != approval.ProjectID ||
		event.Environment != approval.Environment || event.RequestDigest != approval.RequestDigest ||
		event.EventKind != kind || event.ObjectKind != "approval" || event.ObjectID != approval.ID ||
		event.Outcome != "accepted" {
		return deployment.Approval{}, false, deployment.ErrApprovalScope
	}
	if event.PlanDigest == "" && event.ResultDigest == "" {
		return approval, false, nil
	}
	if event.PlanDigest == "" || event.ResultDigest == "" {
		return deployment.Approval{}, false, fmt.Errorf("%w: approval evidence event is incomplete", deployment.ErrApprovalInvalid)
	}
	if approval.PlanDigest != "" && (approval.PlanDigest != event.PlanDigest || approval.EvidenceDigest != event.ResultDigest) {
		return deployment.Approval{}, false, deployment.ErrApprovalScope
	}
	approval.PlanDigest = event.PlanDigest
	approval.EvidenceDigest = event.ResultDigest
	if err := approval.Validate(); err != nil {
		return deployment.Approval{}, false, err
	}
	return approval, true, nil
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
		PlanDigest: approval.PlanDigest, ResultDigest: approval.EvidenceDigest,
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
