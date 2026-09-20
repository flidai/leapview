package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

const maxPlatformRoleApprovalPageSize = 1000

func platformRoleApprovalDigest(action, principalID, requesterID, expectedRevision string) string {
	payload, _ := json.Marshal(struct {
		Action, PrincipalID, RequesterID, ExpectedRevision string
	}{action, principalID, requesterID, expectedRevision})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func platformRoleApprovalOperationDigest(action, approvalID, actorID string, revision int64) string {
	payload, _ := json.Marshal(struct {
		Action, ApprovalID, ActorID string
		Revision                    int64
	}{action, approvalID, actorID, revision})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func approvalString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case *string:
		if typed != nil {
			return *typed
		}
	}
	return fmt.Sprint(value)
}

func platformRoleApprovalFromRow(row accessdb.GetPlatformRoleApprovalByIDRow) access.PlatformRoleApproval {
	return access.PlatformRoleApproval{
		ID: row.ID, Action: row.Action, PrincipalID: row.PrincipalID, RequesterID: row.RequesterID,
		ApproverID: approvalString(row.ApproverID), CanceledBy: approvalString(row.CanceledBy), ExpiredBy: approvalString(row.ExpiredBy),
		Status: access.PlatformRoleApprovalStatus(row.Status), ExpectedRevision: row.ExpectedRevision, Revision: row.Revision,
		IdempotencyKey: row.IdempotencyKey, ExpiresAt: principalTimestamp(row.ExpiresAt), CreatedAt: principalTimestamp(row.CreatedAt),
		ApprovedAt: principalTimestamp(row.ApprovedAt), CanceledAt: principalTimestamp(row.CanceledAt), ExpiredAt: principalTimestamp(row.ExpiredAt),
		ExecutedAt: principalTimestamp(row.ExecutedAt), BindingID: approvalString(row.BindingID), ResultRevision: row.ResultRevision,
	}
}

func platformRoleApprovalFromListRow(row accessdb.ListPlatformRoleApprovalsRow) access.PlatformRoleApproval {
	return platformRoleApprovalFromRow(accessdb.GetPlatformRoleApprovalByIDRow{
		ID: row.ID, Action: row.Action, PrincipalID: row.PrincipalID, RequesterID: row.RequesterID,
		ApproverID: row.ApproverID, CanceledBy: row.CanceledBy, ExpiredBy: row.ExpiredBy, Status: row.Status,
		ExpectedRevision: row.ExpectedRevision, RequestDigest: row.RequestDigest, IdempotencyKey: row.IdempotencyKey,
		Revision: row.Revision, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, ApprovedAt: row.ApprovedAt,
		CanceledAt: row.CanceledAt, ExpiredAt: row.ExpiredAt, ExecutedAt: row.ExecutedAt, BindingID: row.BindingID,
		ResultRevision: row.ResultRevision,
	})
}

func (r *Repository) GetPlatformRoleApproval(ctx context.Context, approvalID string) (access.PlatformRoleApproval, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	id, err := uuidID("platform role approval id", approvalID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	row, err := accessdb.New(db).GetPlatformRoleApprovalByID(ctx, mustPGUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalNotFound
	}
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	return platformRoleApprovalFromRow(row), nil
}

func (r *Repository) ListPlatformRoleApprovals(ctx context.Context, requesterID string) ([]access.PlatformRoleApproval, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	requesterID = strings.TrimSpace(requesterID)
	if requesterID != "" {
		requesterID, err = uuidID("requester id", requesterID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
		}
	}
	rows, err := accessdb.New(db).ListPlatformRoleApprovals(ctx, accessdb.ListPlatformRoleApprovalsParams{RequesterID: requesterID, PageSize: maxPlatformRoleApprovalPageSize})
	if err != nil {
		return nil, err
	}
	items := make([]access.PlatformRoleApproval, 0, len(rows))
	for _, row := range rows {
		items = append(items, platformRoleApprovalFromListRow(row))
	}
	return items, nil
}

func (r *Repository) approvalOperation(ctx context.Context, db DBTX, key, digest, action, approvalID string) (access.PlatformRoleApproval, bool, error) {
	row, err := accessdb.New(db).GetPlatformRoleApprovalOperation(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.PlatformRoleApproval{}, false, nil
	}
	if err != nil {
		return access.PlatformRoleApproval{}, false, err
	}
	if row.ApprovalID != approvalID || row.RequestDigest != digest || row.Action != action {
		return access.PlatformRoleApproval{}, false, fmt.Errorf("%w: idempotency key conflicts with current request", access.ErrPlatformRoleApprovalConflict)
	}
	// Read through the supplied transaction. Reading through r would use the
	// pool again and could miss the state that this transaction just wrote (or
	// observe a different concurrent lifecycle transition).
	id, err := uuidID("platform role approval id", approvalID)
	if err != nil {
		return access.PlatformRoleApproval{}, false, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	requestRow, err := accessdb.New(db).GetPlatformRoleApprovalByID(ctx, mustPGUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.PlatformRoleApproval{}, false, access.ErrPlatformRoleApprovalNotFound
	}
	if err != nil {
		return access.PlatformRoleApproval{}, false, err
	}
	return platformRoleApprovalFromRow(requestRow), true, nil
}

func normalizePlatformRoleApprovalAction(action string) (string, error) {
	action = strings.TrimSpace(action)
	if action != "grant" && action != "revoke" {
		return "", fmt.Errorf("%w: action must be grant or revoke", access.ErrPlatformRoleApprovalInvalid)
	}
	return action, nil
}

func (r *Repository) RequestPlatformRoleApproval(ctx context.Context, input access.PlatformRoleApprovalRequestInput) (access.PlatformRoleApproval, error) {
	if ctx == nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: context is nil", access.ErrPlatformRoleApprovalInvalid)
	}
	action, err := normalizePlatformRoleApprovalAction(input.Action)
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	principalID, err := uuidID("principal id", input.PrincipalID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	requesterID, err := uuidID("requester id", input.RequesterID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	expectedRevision := normalizePlatformAdminRevision(input.ExpectedRevision)
	if expectedRevision == "" {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: expected revision is required", access.ErrPlatformRoleApprovalInvalid)
	}
	key, err := bounded(strings.TrimSpace(input.IdempotencyKey), "platform role approval idempotency key", maxPlatformAdminIdempotencyKeyBytes)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	digest := platformRoleApprovalDigest(action, principalID, requesterID, expectedRevision)
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	q := accessdb.New(tx)
	if err := q.LockPlatformRoleAuthority(ctx); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if existing, getErr := q.GetPlatformRoleApprovalByIdempotencyKey(ctx, key); getErr == nil {
		if existing.RequestDigest != digest || existing.Action != action || existing.PrincipalID != principalID || existing.RequesterID != requesterID || existing.ExpectedRevision != expectedRevision {
			return access.PlatformRoleApproval{}, fmt.Errorf("%w: idempotency key conflicts with current request", access.ErrPlatformRoleApprovalConflict)
		}
		request := platformRoleApprovalFromRow(accessdb.GetPlatformRoleApprovalByIDRow{ID: existing.ID, Action: existing.Action, PrincipalID: existing.PrincipalID, RequesterID: existing.RequesterID, ApproverID: existing.ApproverID, CanceledBy: existing.CanceledBy, ExpiredBy: existing.ExpiredBy, Status: existing.Status, ExpectedRevision: existing.ExpectedRevision, RequestDigest: existing.RequestDigest, IdempotencyKey: existing.IdempotencyKey, Revision: existing.Revision, ExpiresAt: existing.ExpiresAt, CreatedAt: existing.CreatedAt, ApprovedAt: existing.ApprovedAt, CanceledAt: existing.CanceledAt, ExpiredAt: existing.ExpiredAt, ExecutedAt: existing.ExecutedAt, BindingID: existing.BindingID, ResultRevision: existing.ResultRevision})
		if owned {
			if err := tx.Commit(ctx); err != nil {
				return access.PlatformRoleApproval{}, err
			}
		}
		return request, nil
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return access.PlatformRoleApproval{}, getErr
	}
	// Keep the request authority-bound even when a caller bypasses the HTTP
	// authorization layer. Only a currently usable platform administrator may
	// create a request; execution still rechecks the captured CAS revision.
	state, stateErr := r.listPlatformAdministrators(ctx, tx)
	if stateErr != nil {
		return access.PlatformRoleApproval{}, stateErr
	}
	requesterIsAdmin := false
	for _, administrator := range state.Administrators {
		if administrator.Principal.ID == requesterID {
			requesterIsAdmin = true
			break
		}
	}
	if !requesterIsAdmin {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: requester is not a usable platform administrator", access.ErrPlatformRoleApprovalInvalid)
	}
	id, err := newUUID()
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if err := q.InsertPlatformRoleApproval(ctx, accessdb.InsertPlatformRoleApprovalParams{ID: mustPGUUID(id), Action: action, PrincipalID: mustPGUUID(principalID), RequesterID: mustPGUUID(requesterID), ExpectedRevision: expectedRevision, RequestDigest: digest, IdempotencyKey: key, ExpiresAt: pgTimestamp(time.Now().UTC().Add(access.PlatformRoleApprovalLifetime))}); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if err := q.InsertPlatformRoleApprovalOperation(ctx, accessdb.InsertPlatformRoleApprovalOperationParams{IdempotencyKey: key, ApprovalID: mustPGUUID(id), RequestDigest: digest, Action: "request", ResultRevision: 1}); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	row, err := q.GetPlatformRoleApprovalByID(ctx, mustPGUUID(id))
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.PlatformRoleApproval{}, err
		}
	}
	return platformRoleApprovalFromRow(row), nil
}

func (r *Repository) transitionPlatformRoleApproval(ctx context.Context, input access.PlatformRoleApprovalDecisionInput, action string) (access.PlatformRoleApproval, error) {
	approvalID, err := uuidID("platform role approval id", input.ApprovalID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	actorID, err := uuidID("approval actor id", input.ActorID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	key, err := bounded(strings.TrimSpace(input.IdempotencyKey), "platform role approval idempotency key", maxPlatformAdminIdempotencyKeyBytes)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	if input.ExpectedRevision <= 0 {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: expected approval revision is required", access.ErrPlatformRoleApprovalInvalid)
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	q := accessdb.New(tx)
	if err := q.LockPlatformRoleAuthority(ctx); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if _, err := q.LockPlatformRoleApproval(ctx, mustPGUUID(approvalID)); errors.Is(err, pgx.ErrNoRows) {
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalNotFound
	} else if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	row, err := q.GetPlatformRoleApprovalByID(ctx, mustPGUUID(approvalID))
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	request := platformRoleApprovalFromRow(row)
	digest := platformRoleApprovalOperationDigest(action, approvalID, actorID, input.ExpectedRevision)
	if replay, ok, replayErr := r.approvalOperation(ctx, tx, key, digest, action, approvalID); ok || replayErr != nil {
		if replayErr != nil {
			return access.PlatformRoleApproval{}, replayErr
		}
		if owned {
			if err := tx.Commit(ctx); err != nil {
				return access.PlatformRoleApproval{}, err
			}
		}
		return replay, nil
	}
	currentState, stateErr := r.listPlatformAdministrators(ctx, tx)
	if stateErr != nil {
		return access.PlatformRoleApproval{}, stateErr
	}
	if !platformRoleApprovalActorIsAdmin(currentState, actorID) {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: lifecycle actor is not a usable platform administrator", access.ErrPlatformRoleApprovalInvalid)
	}
	if action == "approve" && actorID == request.RequesterID {
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalSeparationOfDuty
	}
	if request.Status != access.PlatformRoleApprovalPending {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: request is %s", access.ErrPlatformRoleApprovalConflict, request.Status)
	}
	var tag interface{ RowsAffected() int64 }
	switch action {
	case "approve":
		value, transitionErr := q.ApprovePlatformRoleApproval(ctx, accessdb.ApprovePlatformRoleApprovalParams{ActorID: mustPGUUID(actorID), ID: mustPGUUID(approvalID), ExpectedRevision: input.ExpectedRevision})
		if transitionErr != nil {
			return access.PlatformRoleApproval{}, transitionErr
		}
		tag = value
	case "cancel":
		value, transitionErr := q.CancelPlatformRoleApproval(ctx, accessdb.CancelPlatformRoleApprovalParams{ActorID: mustPGUUID(actorID), ID: mustPGUUID(approvalID), ExpectedRevision: input.ExpectedRevision})
		if transitionErr != nil {
			return access.PlatformRoleApproval{}, transitionErr
		}
		tag = value
	case "expire":
		value, transitionErr := q.ExpirePlatformRoleApproval(ctx, accessdb.ExpirePlatformRoleApprovalParams{ActorID: actorID, ID: mustPGUUID(approvalID), ExpectedRevision: input.ExpectedRevision})
		if transitionErr != nil {
			return access.PlatformRoleApproval{}, transitionErr
		}
		tag = value
	default:
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalInvalid
	}
	if tag.RowsAffected() != 1 {
		if action == "expire" && time.Now().UTC().Before(parseApprovalTime(request.ExpiresAt)) {
			return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalNotDue
		}
		if action == "approve" && !time.Now().UTC().Before(parseApprovalTime(request.ExpiresAt)) {
			return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalExpired
		}
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalConflict
	}
	updated, err := q.GetPlatformRoleApprovalByID(ctx, mustPGUUID(approvalID))
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	updatedRequest := platformRoleApprovalFromRow(updated)
	if err := q.InsertPlatformRoleApprovalOperation(ctx, accessdb.InsertPlatformRoleApprovalOperationParams{IdempotencyKey: key, ApprovalID: mustPGUUID(approvalID), RequestDigest: digest, Action: action, ResultRevision: updatedRequest.Revision}); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.PlatformRoleApproval{}, err
		}
	}
	return updatedRequest, nil
}

func parseApprovalTime(raw string) time.Time {
	value, _ := time.Parse(time.RFC3339Nano, raw)
	return value
}

func platformRoleApprovalActorIsAdmin(state access.PlatformAdministratorState, actorID string) bool {
	for _, administrator := range state.Administrators {
		if administrator.Principal.ID == actorID {
			return true
		}
	}
	return false
}

func (r *Repository) ApprovePlatformRoleApproval(ctx context.Context, input access.PlatformRoleApprovalDecisionInput) (access.PlatformRoleApproval, error) {
	return r.transitionPlatformRoleApproval(ctx, input, "approve")
}
func (r *Repository) CancelPlatformRoleApproval(ctx context.Context, input access.PlatformRoleApprovalDecisionInput) (access.PlatformRoleApproval, error) {
	return r.transitionPlatformRoleApproval(ctx, input, "cancel")
}
func (r *Repository) ExpirePlatformRoleApproval(ctx context.Context, input access.PlatformRoleApprovalDecisionInput) (access.PlatformRoleApproval, error) {
	return r.transitionPlatformRoleApproval(ctx, input, "expire")
}

func (r *Repository) ExecutePlatformRoleApproval(ctx context.Context, input access.PlatformRoleApprovalExecuteInput) (access.PlatformRoleApproval, error) {
	approvalID, err := uuidID("platform role approval id", input.ApprovalID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	actorID, err := uuidID("approval executor id", input.ActorID)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	key, err := bounded(strings.TrimSpace(input.IdempotencyKey), "platform role approval idempotency key", maxPlatformAdminIdempotencyKeyBytes)
	if err != nil {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: %v", access.ErrPlatformRoleApprovalInvalid, err)
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	q := accessdb.New(tx)
	if err := q.LockPlatformRoleAuthority(ctx); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if _, err := q.LockPlatformRoleApproval(ctx, mustPGUUID(approvalID)); errors.Is(err, pgx.ErrNoRows) {
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalNotFound
	} else if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	row, err := q.GetPlatformRoleApprovalByID(ctx, mustPGUUID(approvalID))
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	request := platformRoleApprovalFromRow(row)
	// The approval revision changes from approved to executed. Keep the
	// operation digest tied to the immutable command identity so a retry after
	// commit replays successfully instead of conflicting with the new revision.
	digest := platformRoleApprovalOperationDigest("execute", approvalID, actorID, 0)
	if replay, ok, replayErr := r.approvalOperation(ctx, tx, key, digest, "execute", approvalID); ok || replayErr != nil {
		if replayErr != nil {
			return access.PlatformRoleApproval{}, replayErr
		}
		if owned {
			if err := tx.Commit(ctx); err != nil {
				return access.PlatformRoleApproval{}, err
			}
		}
		return replay, nil
	}
	currentState, stateErr := r.listPlatformAdministrators(ctx, tx)
	if stateErr != nil {
		return access.PlatformRoleApproval{}, stateErr
	}
	if !platformRoleApprovalActorIsAdmin(currentState, actorID) {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: execution actor is not a usable platform administrator", access.ErrPlatformRoleApprovalInvalid)
	}
	if request.Status != access.PlatformRoleApprovalApproved {
		return access.PlatformRoleApproval{}, fmt.Errorf("%w: request is %s", access.ErrPlatformRoleApprovalConflict, request.Status)
	}
	if !time.Now().UTC().Before(parseApprovalTime(request.ExpiresAt)) {
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalExpired
	}
	transactional := &Repository{db: tx, fingerprintKey: r.fingerprintKey, ownership: r.ownership}
	executionKey := "platform-role-approval-execute-" + approvalID
	var bindingID, resultRevision string
	if request.Action == "grant" {
		result, mutationErr := transactional.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: request.PrincipalID, ExpectedRevision: request.ExpectedRevision, IdempotencyKey: executionKey})
		if mutationErr != nil {
			return access.PlatformRoleApproval{}, mutationErr
		}
		bindingID, resultRevision = result.Administrator.BindingID, result.State.Revision
	} else {
		before, readErr := transactional.ListPlatformAdministrators(ctx)
		if readErr != nil {
			return access.PlatformRoleApproval{}, readErr
		}
		for _, item := range before.Administrators {
			if item.Principal.ID == request.PrincipalID {
				bindingID = item.BindingID
				break
			}
		}
		result, mutationErr := transactional.RevokePlatformAdmin(ctx, access.PlatformAdminRevokeInput{PrincipalID: request.PrincipalID, ExpectedRevision: request.ExpectedRevision, IdempotencyKey: executionKey})
		if mutationErr != nil {
			return access.PlatformRoleApproval{}, mutationErr
		}
		resultRevision = result.Revision
	}
	if bindingID == "" {
		return access.PlatformRoleApproval{}, access.ErrPlatformAdminNotFound
	}
	tag, err := q.ExecutePlatformRoleApproval(ctx, accessdb.ExecutePlatformRoleApprovalParams{BindingID: mustPGUUID(bindingID), ResultRevision: &resultRevision, ID: mustPGUUID(approvalID)})
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if tag.RowsAffected() != 1 {
		return access.PlatformRoleApproval{}, access.ErrPlatformRoleApprovalConflict
	}
	updated, err := q.GetPlatformRoleApprovalByID(ctx, mustPGUUID(approvalID))
	if err != nil {
		return access.PlatformRoleApproval{}, err
	}
	updatedRequest := platformRoleApprovalFromRow(updated)
	if err := q.InsertPlatformRoleApprovalOperation(ctx, accessdb.InsertPlatformRoleApprovalOperationParams{IdempotencyKey: key, ApprovalID: mustPGUUID(approvalID), RequestDigest: digest, Action: "execute", ResultRevision: updatedRequest.Revision}); err != nil {
		return access.PlatformRoleApproval{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.PlatformRoleApproval{}, err
		}
	}
	return updatedRequest, nil
}

var _ access.PlatformRoleApprovalReader = (*Repository)(nil)
var _ access.PlatformRoleApprovalWriter = (*Repository)(nil)
