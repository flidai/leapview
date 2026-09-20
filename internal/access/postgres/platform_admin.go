package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxPlatformAdminIdempotencyKeyBytes = 256

type principalLifecycleLock struct {
	Status     string
	DisabledAt pgtype.Timestamptz
	BlockedAt  pgtype.Timestamptz
}

// lockPlatformAdminAuthority serializes every principal lifecycle mutation
// that can change the set of usable platform administrators. The principal
// row is deliberately locked separately by each caller because service
// principals can use the same offboarding boundary without ever holding a
// platform role.
func (r *Repository) lockPlatformAdminAuthority(ctx context.Context, db DBTX, principalID string) (string, error) {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return "", err
	}
	if err := accessdb.New(db).LockPlatformRoleAuthority(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func lockPrincipalLifecycle(ctx context.Context, db DBTX, principalID string) (principalLifecycleLock, error) {
	parsed, err := pgUUID(principalID)
	if err != nil {
		return principalLifecycleLock{}, err
	}
	row, err := accessdb.New(db).LockPrincipalLifecycle(ctx, parsed)
	return principalLifecycleLock{Status: row.Status, DisabledAt: row.DisabledAt, BlockedAt: row.BlockedAt}, err
}

func (r *Repository) rejectLastUsablePlatformAdministrator(ctx context.Context, db DBTX, principalID string) error {
	state, err := r.listPlatformAdministrators(ctx, db)
	if err != nil {
		return err
	}
	if len(state.Administrators) == 1 && state.Administrators[0].Principal.ID == principalID {
		return access.ErrPlatformAdminLastAdmin
	}
	return nil
}

func platformAdminCommandDigest(action, principalID, expectedRevision string) string {
	payload, _ := json.Marshal(struct {
		Action           string `json:"action"`
		PrincipalID      string `json:"principalId"`
		ExpectedRevision string `json:"expectedRevision"`
	}{Action: action, PrincipalID: principalID, ExpectedRevision: expectedRevision})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalizePlatformAdminRevision(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	return value
}

func platformAdminPrincipalFromRow(row accessdb.ListPlatformAdministratorsRow) access.Principal {
	return principalFromGenerated(accessdb.GetPrincipalRow{
		ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status,
		Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt,
		BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	})
}

func platformAdministratorFromRow(row accessdb.ListPlatformAdministratorsRow) access.PlatformAdministrator {
	return access.PlatformAdministrator{
		BindingID: principalUUID(row.BindingID),
		Principal: platformAdminPrincipalFromRow(row),
		Role:      access.PlatformRole(row.Role),
		CreatedAt: principalTimestamp(row.RoleCreatedAt),
	}
}

func platformAdministratorFromAllRow(row accessdb.ListAllPlatformAdministratorsRow) access.PlatformAdministrator {
	return access.PlatformAdministrator{
		BindingID: principalUUID(row.BindingID),
		Principal: principalFromGenerated(accessdb.GetPrincipalRow{
			ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status,
			Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt,
			BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		}),
		Role: access.PlatformRole(row.Role), CreatedAt: principalTimestamp(row.RoleCreatedAt), RevokedAt: principalTimestamp(row.RevokedAt),
	}
}

func (r *Repository) listPlatformAdministrators(ctx context.Context, db DBTX) (access.PlatformAdministratorState, error) {
	rows, err := accessdb.New(db).ListPlatformAdministrators(ctx, maxPageSize)
	if err != nil {
		return access.PlatformAdministratorState{}, err
	}
	items := make([]access.PlatformAdministrator, 0, len(rows))
	for _, row := range rows {
		items = append(items, platformAdministratorFromRow(row))
	}
	revision, err := access.PlatformAdministratorRevision(items)
	if err != nil {
		return access.PlatformAdministratorState{}, err
	}
	return access.PlatformAdministratorState{Administrators: items, Revision: revision}, nil
}

func (r *Repository) listPlatformAdminAuthorities(ctx context.Context, db DBTX) (access.PlatformAdministratorState, error) {
	rows, err := accessdb.New(db).ListAllPlatformAdministrators(ctx)
	if err != nil {
		return access.PlatformAdministratorState{}, err
	}
	items := make([]access.PlatformAdministrator, 0, len(rows))
	active := make([]access.PlatformAdministrator, 0, len(rows))
	for _, row := range rows {
		item := platformAdministratorFromAllRow(row)
		items = append(items, item)
		if item.RevokedAt == "" && row.Status == "active" && item.Principal.AccessDisabled() == false {
			active = append(active, item)
		}
	}
	revision, err := access.PlatformAdministratorRevision(active)
	if err != nil {
		return access.PlatformAdministratorState{}, err
	}
	return access.PlatformAdministratorState{Administrators: items, Revision: revision}, nil
}

// ListPlatformAdministrators returns only usable administrators: the role
// binding and principal must both be active and the principal must not be
// disabled, blocked, or revoked.
func (r *Repository) ListPlatformAdministrators(ctx context.Context) (access.PlatformAdministratorState, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.PlatformAdministratorState{}, err
	}
	return r.listPlatformAdministrators(ctx, db)
}

// ListPlatformAdminAuthorities returns current and revoked durable role
// bindings. It is a redacted operator projection: no session, API token, or
// service credential data is included.
func (r *Repository) ListPlatformAdminAuthorities(ctx context.Context) (access.PlatformAdministratorState, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.PlatformAdministratorState{}, err
	}
	return r.listPlatformAdminAuthorities(ctx, db)
}

func (r *Repository) checkPlatformAdminOperation(ctx context.Context, db DBTX, key, digest, action string) (access.PlatformAdminGrantResult, access.PlatformAdministratorState, bool, error) {
	op, err := accessdb.New(db).GetPlatformRoleOperation(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, false, nil
	}
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, false, err
	}
	if op.RequestDigest != digest || op.Action != action {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, false, fmt.Errorf("%w: key %q", access.ErrPlatformAdminIdempotency, key)
	}
	state, err := r.listPlatformAdministrators(ctx, db)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, false, err
	}
	if action == "revoke" {
		return access.PlatformAdminGrantResult{}, state, true, nil
	}
	// A grant replay returns the original binding identity where its principal
	// still exists. The result state is always freshly read so ETag remains
	// useful to the caller after an unrelated delegation change.
	binding, err := accessdb.New(db).GetPlatformRoleBindingByID(ctx, op.BindingID)
	if err != nil {
		return access.PlatformAdminGrantResult{}, state, true, err
	}
	// Replays may run under a caller-owned audited transaction. Resolve the
	// principal through that transaction rather than the repository pool so a
	// replay observes the same snapshot and never mixes transaction locks with
	// an unrelated connection.
	principal, err := (&Repository{db: db, fingerprintKey: r.fingerprintKey}).PrincipalByID(ctx, principalUUID(op.PrincipalID))
	if err != nil {
		return access.PlatformAdminGrantResult{}, state, true, err
	}
	return access.PlatformAdminGrantResult{Administrator: access.PlatformAdministrator{
		BindingID: principalUUID(binding.BindingID), Principal: principal,
		Role: access.PlatformRole(binding.Role), CreatedAt: principalTimestamp(binding.RoleCreatedAt),
	}, State: state}, state, true, nil
}

func (r *Repository) platformAdminMutationTx(ctx context.Context, inputPrincipalID, expectedRevision, key, action string, mutate func(*accessdb.Queries, access.PlatformAdministratorState, access.Principal, accessdb.GetPlatformRoleBindingRow) (string, error)) (access.PlatformAdminGrantResult, access.PlatformAdministratorState, error) {
	if ctx == nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, errors.New("platform administrator context is nil")
	}
	principalID, err := uuidID("principal id", inputPrincipalID)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("%w: %v", access.ErrPlatformAdminInvalid, err)
	}
	key, err = bounded(strings.TrimSpace(key), "platform administrator idempotency key", maxPlatformAdminIdempotencyKeyBytes)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("%w: %v", access.ErrPlatformAdminIdempotency, err)
	}
	expectedRevision = normalizePlatformAdminRevision(expectedRevision)
	if expectedRevision == "" {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("%w: expected revision is required", access.ErrPlatformAdminStaleRevision)
	}
	digest := platformAdminCommandDigest(action, principalID, expectedRevision)
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	db := accessdb.New(tx)
	if err := db.LockPlatformRoleAuthority(ctx); err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	if replay, state, ok, replayErr := r.checkPlatformAdminOperation(ctx, tx, key, digest, action); ok || replayErr != nil {
		return replay, state, replayErr
	}
	state, err := r.listPlatformAdministrators(ctx, tx)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	if expectedRevision != state.Revision {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("%w: expected %q, current %q", access.ErrPlatformAdminStaleRevision, expectedRevision, state.Revision)
	}
	var principal access.Principal
	if action == "grant" {
		row, lockErr := db.LockEnabledPrincipalForPlatformRole(ctx, mustPGUUID(principalID))
		err = lockErr
		if err == nil {
			principal = principalFromGenerated(accessdb.GetPrincipalRow{ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status, Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt, BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
		}
	} else {
		row, lockErr := db.LockPrincipalForPlatformRole(ctx, mustPGUUID(principalID))
		err = lockErr
		if err == nil {
			principal = principalFromGenerated(accessdb.GetPrincipalRow{ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status, Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt, BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if action == "grant" {
			return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("%w: principal %q is not enabled", access.ErrPlatformAdminConflict, principalID)
		}
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("%w: principal %q", access.ErrPlatformAdminNotFound, principalID)
	}
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	var activeBinding accessdb.GetPlatformRoleBindingRow
	if binding, bindingErr := db.GetPlatformRoleBinding(ctx, mustPGUUID(principalID)); bindingErr == nil {
		activeBinding = binding
	} else if !errors.Is(bindingErr, pgx.ErrNoRows) {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, bindingErr
	}
	bindingID, err := mutate(db, state, principal, activeBinding)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	binding, err := db.GetPlatformRoleBindingByID(ctx, mustPGUUID(bindingID))
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	resultState, err := r.listPlatformAdministrators(ctx, tx)
	if err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
	}
	if err := db.InsertPlatformRoleOperation(ctx, accessdb.InsertPlatformRoleOperationParams{IdempotencyKey: key, RequestDigest: digest, Action: action, PrincipalID: mustPGUUID(principalID), BindingID: mustPGUUID(bindingID), ResultRevision: resultState.Revision}); err != nil {
		return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, fmt.Errorf("record platform administrator idempotency: %w", err)
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.PlatformAdminGrantResult{}, access.PlatformAdministratorState{}, err
		}
	}
	return access.PlatformAdminGrantResult{Administrator: access.PlatformAdministrator{BindingID: bindingID, Principal: principal, Role: access.PlatformRole(binding.Role), CreatedAt: principalTimestamp(binding.RoleCreatedAt)}, State: resultState}, resultState, nil
}

// GrantPlatformAdmin delegates the durable platform_admin role to one already
// existing enabled user principal.
func (r *Repository) GrantPlatformAdmin(ctx context.Context, input access.PlatformAdminGrantInput) (access.PlatformAdminGrantResult, error) {
	result, _, err := r.platformAdminMutationTx(ctx, input.PrincipalID, input.ExpectedRevision, input.IdempotencyKey, "grant", func(q *accessdb.Queries, _ access.PlatformAdministratorState, principal access.Principal, active accessdb.GetPlatformRoleBindingRow) (string, error) {
		if active.BindingID.Valid {
			return principalUUID(active.BindingID), nil
		}
		bindingID, err := newUUID()
		if err != nil {
			return "", err
		}
		if err := q.InsertPlatformRole(ctx, accessdb.InsertPlatformRoleParams{ID: mustPGUUID(bindingID), PrincipalID: mustPGUUID(principal.ID), Role: string(access.PlatformRoleAdmin)}); err != nil {
			return "", err
		}
		return bindingID, nil
	})
	return result, err
}

// RevokePlatformAdmin seals the active role binding. The authority lock and
// row locks make the last-usable-administrator check atomic with revocation.
func (r *Repository) RevokePlatformAdmin(ctx context.Context, input access.PlatformAdminRevokeInput) (access.PlatformAdministratorState, error) {
	_, state, err := r.platformAdminMutationTx(ctx, input.PrincipalID, input.ExpectedRevision, input.IdempotencyKey, "revoke", func(q *accessdb.Queries, current access.PlatformAdministratorState, principal access.Principal, active accessdb.GetPlatformRoleBindingRow) (string, error) {
		if !active.BindingID.Valid {
			return "", fmt.Errorf("%w: principal %q", access.ErrPlatformAdminNotFound, input.PrincipalID)
		}
		usableTarget := false
		for _, item := range current.Administrators {
			if item.Principal.ID == principal.ID {
				usableTarget = true
				break
			}
		}
		if usableTarget && len(current.Administrators) <= 1 {
			return "", access.ErrPlatformAdminLastAdmin
		}
		tag, err := q.RevokePlatformRole(ctx, mustPGUUID(input.PrincipalID))
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() != 1 {
			return "", fmt.Errorf("%w: principal %q", access.ErrPlatformAdminNotFound, input.PrincipalID)
		}
		return principalUUID(active.BindingID), nil
	})
	return state, err
}
