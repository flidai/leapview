package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
	"strings"
)

func (r *Repository) PrincipalByID(ctx context.Context, id string) (access.Principal, error) {
	row, err := r.q.GetPrincipal(ctx, id)
	if err != nil {
		return access.Principal{}, err
	}
	return mapPrincipal(row), nil
}

func (r *Repository) ListPrincipals(ctx context.Context, filter access.PrincipalFilter) ([]access.Principal, error) {
	if r == nil || r.db == nil {
		return []access.Principal{}, nil
	}
	email := strings.TrimSpace(filter.Email)
	query := strings.TrimSpace(filter.Query)
	rows, err := r.q.ListPrincipals(ctx, platformdb.ListPrincipalsParams{Email: email, Search: query})
	if err != nil {
		return nil, err
	}
	out := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapPrincipal(row))
	}
	return out, nil
}

// ListPrincipalsWithActivity is the administration read model for people
// directory activity. Generic principal reads avoid paying for this aggregate.
func (r *Repository) ListPrincipalsWithActivity(ctx context.Context, filter access.PrincipalFilter) ([]access.Principal, error) {
	if r == nil || r.db == nil {
		return []access.Principal{}, nil
	}
	rows, err := r.q.ListPrincipalsWithActivity(ctx, platformdb.ListPrincipalsWithActivityParams{
		Email: strings.TrimSpace(filter.Email), Search: strings.TrimSpace(filter.Query),
	})
	if err != nil {
		return nil, err
	}
	out := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		out = append(out, access.Principal{
			ID:          row.ID,
			Kind:        access.PrincipalKind(row.Kind),
			Email:       row.Email,
			DisplayName: row.DisplayName,
			DisabledAt:  nullString(row.DisabledAt),
			BlockedAt:   nullString(row.BlockedAt),
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
			LastSeenAt:  row.LastSeenAt,
		})
	}
	return out, nil
}

func (r *Repository) SearchPrincipals(ctx context.Context, query string, limit int) ([]access.Principal, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []access.Principal{}, nil
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := r.q.SearchPrincipals(ctx, platformdb.SearchPrincipalsParams{Search: query, ResultLimit: int64(limit)})
	if err != nil {
		return nil, err
	}
	principals := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		principals = append(principals, mapPrincipal(row))
	}
	return principals, nil
}

func (r *Repository) principalDisabled(ctx context.Context, principalID string) (bool, error) {
	row, err := r.q.GetPrincipal(ctx, principalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return (row.DisabledAt.Valid && row.DisabledAt.String != "") || (row.BlockedAt.Valid && row.BlockedAt.String != ""), nil
}

func (r *Repository) UpsertPrincipal(ctx context.Context, input access.PrincipalInput) (access.Principal, error) {
	if strings.TrimSpace(input.ID) == "" {
		id, err := newID("principal")
		if err != nil {
			return access.Principal{}, err
		}
		input.ID = id
	}
	if input.Kind == "" {
		input.Kind = access.PrincipalKindUser
	}
	if err := r.q.UpsertPrincipal(ctx, platformdb.UpsertPrincipalParams{
		ID:          input.ID,
		Kind:        string(input.Kind),
		Email:       input.Email,
		DisplayName: input.DisplayName,
	}); err != nil {
		return access.Principal{}, err
	}
	row, err := r.q.GetPrincipal(ctx, input.ID)
	if err != nil {
		return access.Principal{}, err
	}
	return mapPrincipal(row), nil
}

func (r *Repository) CreateLocalUser(ctx context.Context, input access.LocalUserInput) (access.LocalPasswordReset, error) {
	if _, inTransaction := r.db.(*sql.Tx); inTransaction {
		return r.createLocalUser(ctx, input)
	}
	if r == nil || r.root == nil {
		return access.LocalPasswordReset{}, fmt.Errorf("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	created, err := txRepo.createLocalUser(ctx, input)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	if err := tx.Commit(); err != nil {
		return access.LocalPasswordReset{}, err
	}
	return created, nil
}

func (r *Repository) createLocalUser(ctx context.Context, input access.LocalUserInput) (access.LocalPasswordReset, error) {
	email := access.NormalizeEmail(input.Email)
	if email == "" {
		return access.LocalPasswordReset{}, fmt.Errorf("email is required")
	}
	if _, err := r.q.GetPrincipalByEmail(ctx, email); err == nil {
		return access.LocalPasswordReset{}, access.ErrPrincipalAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return access.LocalPasswordReset{}, err
	}
	password := strings.TrimSpace(input.Password)
	if password == "" {
		generated, err := newTemporaryPassword()
		if err != nil {
			return access.LocalPasswordReset{}, err
		}
		password = generated
	}
	verifier, err := newSecretVerifier(password)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	principalID := access.PrincipalIDForEmail(email)
	result, err := r.q.InsertPrincipalCreateOnly(ctx, platformdb.InsertPrincipalCreateOnlyParams{
		ID:          principalID,
		Kind:        string(access.PrincipalKindUser),
		Email:       email,
		DisplayName: firstNonEmpty(strings.TrimSpace(input.DisplayName), email),
	})
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	if created != 1 {
		return access.LocalPasswordReset{}, access.ErrPrincipalAlreadyExists
	}
	row, err := r.q.GetPrincipal(ctx, principalID)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	principal := mapPrincipal(row)
	if err := r.upsertLocalCredential(ctx, principal.ID, verifier, input.MustChange); err != nil {
		return access.LocalPasswordReset{}, err
	}
	return access.LocalPasswordReset{Principal: principal, Password: password}, nil
}

func (r *Repository) VerifyLocalPassword(ctx context.Context, email, password string) (access.Principal, access.LocalCredential, error) {
	email = access.NormalizeEmail(email)
	if email == "" || strings.TrimSpace(password) == "" {
		return access.Principal{}, access.LocalCredential{}, sql.ErrNoRows
	}
	principal, credential, verifier, err := r.localCredentialByEmail(ctx, email)
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, err
	}
	if principal.AccessDisabled() || !verifySecret(password, verifier) {
		return access.Principal{}, access.LocalCredential{}, sql.ErrNoRows
	}
	return principal, credential, nil
}

func (r *Repository) ResetLocalPassword(ctx context.Context, principalID string) (access.LocalPasswordReset, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return access.LocalPasswordReset{}, fmt.Errorf("principal id is required")
	}
	principal, err := r.PrincipalByID(ctx, principalID)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	if principal.Kind != access.PrincipalKindUser {
		return access.LocalPasswordReset{}, fmt.Errorf("local passwords are only supported for user principals")
	}
	password, err := newTemporaryPassword()
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	verifier, err := newSecretVerifier(password)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	if err := r.upsertLocalCredential(ctx, principal.ID, verifier, true); err != nil {
		return access.LocalPasswordReset{}, err
	}
	return access.LocalPasswordReset{Principal: principal, Password: password}, nil
}

func (r *Repository) ChangeLocalPassword(ctx context.Context, principalID, currentPassword, newPassword string) (access.LocalCredential, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return access.LocalCredential{}, fmt.Errorf("principal id is required")
	}
	if strings.TrimSpace(newPassword) == "" {
		return access.LocalCredential{}, fmt.Errorf("new password is required")
	}
	principal, credential, verifier, err := r.localCredentialByPrincipalID(ctx, principalID)
	if err != nil {
		return access.LocalCredential{}, err
	}
	if principal.AccessDisabled() || !verifySecret(currentPassword, verifier) {
		return access.LocalCredential{}, sql.ErrNoRows
	}
	newVerifier, err := newSecretVerifier(newPassword)
	if err != nil {
		return access.LocalCredential{}, err
	}
	if err := r.q.ChangeLocalCredentialPassword(ctx, platformdb.ChangeLocalCredentialPasswordParams{
		PasswordVerifier: newVerifier, PrincipalID: principalID,
	}); err != nil {
		return access.LocalCredential{}, err
	}
	credential, err = r.LocalCredential(ctx, principalID)
	if err != nil {
		return access.LocalCredential{}, err
	}
	return credential, nil
}

func (r *Repository) LocalCredential(ctx context.Context, principalID string) (access.LocalCredential, error) {
	_, credential, _, err := r.localCredentialByPrincipalID(ctx, principalID)
	return credential, err
}

func (r *Repository) upsertLocalCredential(ctx context.Context, principalID, verifier string, mustChange bool) error {
	mustChangeValue := 0
	if mustChange {
		mustChangeValue = 1
	}
	return r.q.UpsertLocalCredential(ctx, platformdb.UpsertLocalCredentialParams{
		PrincipalID: principalID, PasswordVerifier: verifier, MustChangePassword: int64(mustChangeValue),
	})
}

func (r *Repository) localCredentialByEmail(ctx context.Context, email string) (access.Principal, access.LocalCredential, string, error) {
	row, err := r.q.GetLocalCredentialByEmail(ctx, email)
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, "", err
	}
	return localCredentialValues(row.ID, row.Kind, row.Email, row.DisplayName, row.DisabledAt, row.BlockedAt, row.CreatedAt, row.UpdatedAt,
		row.PasswordVerifier, row.MustChangePassword, row.CredentialCreatedAt, row.CredentialUpdatedAt, row.PasswordChangedAt)
}

func (r *Repository) localCredentialByPrincipalID(ctx context.Context, principalID string) (access.Principal, access.LocalCredential, string, error) {
	row, err := r.q.GetLocalCredentialByPrincipalID(ctx, strings.TrimSpace(principalID))
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, "", err
	}
	return localCredentialValues(row.ID, row.Kind, row.Email, row.DisplayName, row.DisabledAt, row.BlockedAt, row.CreatedAt, row.UpdatedAt,
		row.PasswordVerifier, row.MustChangePassword, row.CredentialCreatedAt, row.CredentialUpdatedAt, row.PasswordChangedAt)
}

func localCredentialValues(id, kind, email, displayName string, disabledAt, blockedAt sql.NullString, createdAt, updatedAt, verifier string,
	mustChange int64, credentialCreatedAt, credentialUpdatedAt string, passwordChangedAt sql.NullString,
) (access.Principal, access.LocalCredential, string, error) {
	principal := access.Principal{ID: id, Kind: access.PrincipalKind(kind), Email: email, DisplayName: displayName, CreatedAt: createdAt, UpdatedAt: updatedAt}
	principal.DisabledAt = nullString(disabledAt)
	principal.BlockedAt = nullString(blockedAt)
	credential := access.LocalCredential{
		PrincipalID: principal.ID, MustChangePassword: mustChange != 0,
		CreatedAt: credentialCreatedAt, UpdatedAt: credentialUpdatedAt, PasswordChangedAt: nullString(passwordChangedAt),
	}
	return principal, credential, verifier, nil
}

func (r *Repository) SetPlatformRole(ctx context.Context, input access.PlatformRoleInput) (access.Principal, error) {
	principalID := strings.TrimSpace(input.PrincipalID)
	email := access.NormalizeEmail(input.Email)
	if principalID == "" && email == "" {
		return access.Principal{}, fmt.Errorf("principal id or email is required")
	}
	roleName, err := access.ParsePlatformRole(string(input.Role))
	if err != nil {
		return access.Principal{}, err
	}
	if principalID == "" {
		principalID = access.PrincipalIDForEmail(email)
	}
	principal, err := r.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          principalID,
		Email:       email,
		DisplayName: firstNonEmpty(strings.TrimSpace(input.DisplayName), email, principalID),
	})
	if err != nil {
		return access.Principal{}, err
	}
	bindingID, err := newID("platformrolebinding")
	if err != nil {
		return access.Principal{}, err
	}
	if err := r.q.InsertPlatformRoleBinding(ctx, platformdb.InsertPlatformRoleBindingParams{
		ID:          bindingID,
		Role:        string(roleName),
		PrincipalID: principal.ID,
	}); err != nil {
		return access.Principal{}, err
	}
	return principal, nil
}

// IsPlatformAdmin reports the immutable instance-wide administrator role. The
// check is intentionally backed directly by platform_role_bindings so HTTP
// administration cannot inherit project authorization state.
func (r *Repository) IsPlatformAdmin(ctx context.Context, principalID string) (bool, error) {
	if principalID == "" || principalID != strings.TrimSpace(principalID) {
		return false, fmt.Errorf("canonical principal id is required")
	}
	row, err := r.q.IsPlatformAdmin(ctx, principalID)
	if err != nil {
		return false, err
	}
	return row != 0, nil
}
