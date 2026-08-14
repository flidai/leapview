package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

func (r *Repository) CreateDeviceAuthorization(ctx context.Context, record access.DeviceAuthorization) error {
	privileges, err := json.Marshal(record.Scope.Privileges)
	if err != nil {
		return err
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	if err := txRepo.q.CreateDeviceAuthorization(ctx, platformdb.CreateDeviceAuthorizationParams{
		ID: record.ID, ClientID: record.ClientID,
		DeviceCodeHash: record.DeviceCodeHash, UserCodeHash: record.UserCodeHash,
		TargetID: record.Scope.TargetID, ProjectID: record.Scope.ProjectID,
		PrivilegesJson: string(privileges), Status: string(record.Status),
		ExpiresAt:           formatAuthoringTime(record.ExpiresAt),
		PollIntervalSeconds: int64(record.PollInterval / time.Second),
		CreatedAt:           formatAuthoringTime(record.CreatedAt),
	}); err != nil {
		return err
	}
	if err := txRepo.RecordAuditEvent(ctx, authoringDeviceAudit("authoring.device.started", record, "", "success")); err != nil {
		return fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	return tx.Commit()
}

func (r *Repository) DeviceAuthorizationByUserCodeHash(ctx context.Context, hash string) (access.DeviceAuthorization, error) {
	row, err := r.q.GetDeviceAuthorizationByUserCodeHash(ctx, hash)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	return mapDeviceAuthorization(row)
}

func (r *Repository) ApproveDeviceAuthorization(ctx context.Context, id, principalID string, now time.Time) error {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	affected, err := txRepo.q.ApproveDeviceAuthorization(ctx, platformdb.ApproveDeviceAuthorizationParams{
		ID: id, PrincipalID: nullableString(principalID), ApprovedAt: nullableTime(now),
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return access.ErrDeviceAuthorizationExpired
	}
	row, err := txRepo.q.GetDeviceAuthorizationByID(ctx, id)
	if err != nil {
		return err
	}
	record, err := mapDeviceAuthorization(row)
	if err != nil {
		return err
	}
	if err := txRepo.RecordAuditEvent(ctx, authoringDeviceAudit("authoring.device.decided", record, principalID, "success")); err != nil {
		return fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	return tx.Commit()
}

func (r *Repository) DenyDeviceAuthorization(ctx context.Context, id, principalID string, now time.Time) error {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	affected, err := txRepo.q.DenyDeviceAuthorization(ctx, platformdb.DenyDeviceAuthorizationParams{
		ID: id, PrincipalID: nullableString(principalID), DeniedAt: nullableTime(now),
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return access.ErrDeviceAuthorizationExpired
	}
	row, err := txRepo.q.GetDeviceAuthorizationByID(ctx, id)
	if err != nil {
		return err
	}
	record, err := mapDeviceAuthorization(row)
	if err != nil {
		return err
	}
	if err := txRepo.RecordAuditEvent(ctx, authoringDeviceAudit("authoring.device.decided", record, principalID, "success")); err != nil {
		return fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	return tx.Commit()
}

func (r *Repository) IssueDeviceCredential(ctx context.Context, issue access.DeviceCredentialIssue) (access.AuthoringCredential, error) {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := r.q.WithTx(tx)
	row, err := queries.GetDeviceAuthorizationByDeviceCodeHash(ctx, issue.DeviceCodeHash)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	record, err := mapDeviceAuthorization(row)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if record.ClientID != issue.ClientID {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if !issue.Now.Before(record.ExpiresAt) {
		return access.AuthoringCredential{}, access.ErrDeviceAuthorizationExpired
	}
	switch record.Status {
	case access.DeviceAuthorizationPending:
		if !record.LastPolledAt.IsZero() && issue.Now.Sub(record.LastPolledAt) < record.PollInterval {
			return access.AuthoringCredential{}, access.ErrDeviceAuthorizationSlowDown
		}
		if err := queries.RecordDeviceAuthorizationPoll(ctx, platformdb.RecordDeviceAuthorizationPollParams{
			ID: record.ID, LastPolledAt: nullableTime(issue.Now),
		}); err != nil {
			return access.AuthoringCredential{}, err
		}
		if err := tx.Commit(); err != nil {
			return access.AuthoringCredential{}, err
		}
		return access.AuthoringCredential{}, access.ErrDeviceAuthorizationPending
	case access.DeviceAuthorizationDenied:
		return access.AuthoringCredential{}, access.ErrDeviceAuthorizationDenied
	case access.DeviceAuthorizationConsumed:
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	case access.DeviceAuthorizationApproved:
	default:
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if record.PrincipalID == "" {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	principalRow, err := queries.GetPrincipal(ctx, record.PrincipalID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	principal := mapPrincipal(principalRow)
	if principal.AccessDisabled() {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	affected, err := queries.ConsumeDeviceAuthorization(ctx, platformdb.ConsumeDeviceAuthorizationParams{
		ID: record.ID, ConsumedAt: nullableTime(issue.Now),
	})
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if affected != 1 {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	session := access.AuthoringSession{
		ID: issue.SessionID, Kind: access.AuthoringSessionHumanCLI, ClientID: record.ClientID,
		PrincipalID: principal.ID, Scope: record.Scope, CreatedAt: issue.Now, ExpiresAt: issue.RefreshExpiresAt,
	}
	if err := createAuthoringSession(ctx, queries, session); err != nil {
		return access.AuthoringCredential{}, err
	}
	if err := createAuthoringCredential(ctx, queries, issue.CredentialID, session.ID, issue.AccessTokenHash, issue.RefreshTokenHash, issue.AccessExpiresAt, issue.RefreshExpiresAt, issue.Now); err != nil {
		return access.AuthoringCredential{}, err
	}
	txRepo := &Repository{root: r.root, db: tx, q: queries}
	if err := txRepo.RecordAuditEvent(ctx, authoringSessionAudit("authoring.session.created", session, "success")); err != nil {
		return access.AuthoringCredential{}, fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	if err := tx.Commit(); err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{
		ID: issue.CredentialID, Principal: principal, Session: session,
		AccessExpiresAt: issue.AccessExpiresAt, RefreshExpiresAt: issue.RefreshExpiresAt,
	}, nil
}

func (r *Repository) CreateWorkloadCredential(ctx context.Context, issue access.WorkloadCredentialIssue) (access.AuthoringCredential, error) {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := r.q.WithTx(tx)
	principalRow, err := queries.GetPrincipal(ctx, issue.Session.PrincipalID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	principal := mapPrincipal(principalRow)
	if principal.Kind != access.PrincipalKindServicePrincipal || principal.AccessDisabled() {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	if err := createAuthoringSession(ctx, queries, issue.Session); err != nil {
		return access.AuthoringCredential{}, err
	}
	if err := createAuthoringCredential(ctx, queries, issue.CredentialID, issue.Session.ID, issue.AccessTokenHash, "", issue.AccessExpiresAt, time.Time{}, issue.Session.CreatedAt); err != nil {
		return access.AuthoringCredential{}, err
	}
	txRepo := &Repository{root: r.root, db: tx, q: queries}
	if err := txRepo.RecordAuditEvent(ctx, authoringSessionAudit("authoring.workload.created", issue.Session, "success")); err != nil {
		return access.AuthoringCredential{}, fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	if err := tx.Commit(); err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{
		ID: issue.CredentialID, Principal: principal, Session: issue.Session, AccessExpiresAt: issue.AccessExpiresAt,
	}, nil
}

func (r *Repository) RotateAuthoringCredential(ctx context.Context, rotation access.AuthoringCredentialRotation) (access.AuthoringCredential, error) {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := r.q.WithTx(tx)
	row, err := queries.GetAuthoringCredentialByRefreshHash(ctx, nullableString(rotation.RefreshTokenHash))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
		}
		return access.AuthoringCredential{}, err
	}
	if row.Active == 0 {
		if err := queries.RevokeAuthoringSessionByID(ctx, platformdb.RevokeAuthoringSessionByIDParams{
			ID: row.SessionID, RevokedAt: nullableTime(rotation.Now),
		}); err != nil {
			return access.AuthoringCredential{}, err
		}
		session, mapErr := mapRefreshCredential(row)
		if mapErr != nil {
			return access.AuthoringCredential{}, mapErr
		}
		txRepo := &Repository{root: r.root, db: tx, q: queries}
		if err := txRepo.RecordAuditEvent(ctx, authoringSessionAudit("authoring.refresh.replay", session.Session, "security_event")); err != nil {
			return access.AuthoringCredential{}, fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
		}
		if err := tx.Commit(); err != nil {
			return access.AuthoringCredential{}, err
		}
		return access.AuthoringCredential{}, access.ErrAuthoringRefreshReplay
	}
	refreshExpiresAt, err := parseNullableAuthoringTime(row.RefreshExpiresAt)
	if err != nil || refreshExpiresAt.IsZero() || !rotation.Now.Before(refreshExpiresAt) || row.RevokedAt.Valid {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	affected, err := queries.ReplaceAuthoringCredential(ctx, platformdb.ReplaceAuthoringCredentialParams{
		ID: row.CredentialID, ReplacedAt: nullableTime(rotation.Now),
	})
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if affected != 1 {
		return access.AuthoringCredential{}, access.ErrAuthoringRefreshReplay
	}
	if err := queries.UpdateAuthoringSessionExpiry(ctx, platformdb.UpdateAuthoringSessionExpiryParams{
		ID: row.SessionID, ExpiresAt: formatAuthoringTime(rotation.RefreshExpiresAt),
	}); err != nil {
		return access.AuthoringCredential{}, err
	}
	if err := createAuthoringCredential(ctx, queries, rotation.CredentialID, row.SessionID, rotation.AccessTokenHash, rotation.RefreshTokenHashNew, rotation.AccessExpiresAt, rotation.RefreshExpiresAt, rotation.Now); err != nil {
		return access.AuthoringCredential{}, err
	}
	credential, err := mapRefreshCredential(row)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	credential.ID = rotation.CredentialID
	credential.AccessExpiresAt = rotation.AccessExpiresAt
	credential.RefreshExpiresAt = rotation.RefreshExpiresAt
	credential.Session.ExpiresAt = rotation.RefreshExpiresAt
	txRepo := &Repository{root: r.root, db: tx, q: queries}
	if err := txRepo.RecordAuditEvent(ctx, authoringSessionAudit("authoring.token.refreshed", credential.Session, "success")); err != nil {
		return access.AuthoringCredential{}, fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	if err := tx.Commit(); err != nil {
		return access.AuthoringCredential{}, err
	}
	return credential, nil
}

func (r *Repository) AuthoringCredentialByAccessTokenHash(ctx context.Context, hash string, now time.Time) (access.AuthoringCredential, error) {
	row, err := r.q.GetAuthoringCredentialByAccessHash(ctx, hash)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if row.Active == 0 || row.RevokedAt.Valid {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	credential, err := mapAccessCredential(row)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if err := r.q.TouchAuthoringSession(ctx, platformdb.TouchAuthoringSessionParams{
		ID: credential.Session.ID, LastUsedAt: nullableTime(now),
	}); err != nil {
		return access.AuthoringCredential{}, err
	}
	credential.Session.LastUsedAt = now
	return credential, nil
}

func (r *Repository) ListAuthoringSessions(ctx context.Context, principalID string) ([]access.AuthoringSession, error) {
	rows, err := r.q.ListAuthoringSessions(ctx, principalID)
	if err != nil {
		return nil, err
	}
	sessions := make([]access.AuthoringSession, 0, len(rows))
	for _, row := range rows {
		session, err := mapAuthoringSession(row)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (r *Repository) RevokeAuthoringSession(ctx context.Context, principalID, sessionID string, now time.Time) error {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	affected, err := txRepo.q.RevokeAuthoringSession(ctx, platformdb.RevokeAuthoringSessionParams{
		ID: sessionID, PrincipalID: principalID, RevokedAt: nullableTime(now),
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return access.ErrInvalidAuthoringCredential
	}
	if err := txRepo.RecordAuditEvent(ctx, authoringSessionAudit("authoring.session.revoked", access.AuthoringSession{ID: sessionID, PrincipalID: principalID}, "success")); err != nil {
		return fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	return tx.Commit()
}

func (r *Repository) RevokeAuthoringSessionByAccessTokenHash(ctx context.Context, hash string, now time.Time) error {
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := r.q.WithTx(tx)
	row, err := queries.GetAuthoringCredentialByAccessHash(ctx, hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.ErrInvalidAuthoringCredential
		}
		return err
	}
	if err := queries.RevokeAuthoringSessionByID(ctx, platformdb.RevokeAuthoringSessionByIDParams{
		ID: row.SessionID, RevokedAt: nullableTime(now),
	}); err != nil {
		return err
	}
	session, err := mapAccessCredential(row)
	if err != nil {
		return err
	}
	txRepo := &Repository{root: r.root, db: tx, q: queries}
	if err := txRepo.RecordAuditEvent(ctx, authoringSessionAudit("authoring.session.revoked", session.Session, "success")); err != nil {
		return fmt.Errorf("%w: %v", access.ErrAuditTransaction, err)
	}
	return tx.Commit()
}

func authoringDeviceAudit(action string, record access.DeviceAuthorization, principalID, status string) access.AuditEventInput {
	metadataValues := map[string]any{
		"clientId": record.ClientID, "targetId": record.Scope.TargetID,
		"projectId": record.Scope.ProjectID, "privileges": record.Scope.Privileges,
		"decision": string(record.Status),
	}
	metadata, _ := json.Marshal(metadataValues)
	if action == "authoring.device.decided" {
		privileges := make([]string, len(record.Scope.Privileges))
		for index, privilege := range record.Scope.Privileges {
			privileges[index] = string(privilege)
		}
		if encoded, encodeErr := accessgen.EncodeGenDecideDeviceAuthorizationAuditPayload(accessgen.GenSchemaDeviceAuthorizationDecidedAuditPayload{
			ClientId: record.ClientID, TargetId: record.Scope.TargetID, ProjectId: record.Scope.ProjectID,
			Privileges: privileges, Decision: string(record.Status),
		}); encodeErr == nil {
			metadata = []byte(encoded)
		}
	}
	return access.AuditEventInput{
		PrincipalID: principalID, Action: action, TargetType: "device_authorization",
		TargetID: record.ID, Status: status, MetadataJSON: string(metadata),
	}
}

func authoringSessionAudit(action string, session access.AuthoringSession, status string) access.AuditEventInput {
	metadataValues := map[string]any{
		"kind": session.Kind, "clientId": session.ClientID, "targetId": session.Scope.TargetID,
		"projectId": session.Scope.ProjectID, "privileges": session.Scope.Privileges,
	}
	metadata, _ := json.Marshal(metadataValues)
	if action == "authoring.session.revoked" {
		privileges := make([]string, len(session.Scope.Privileges))
		for index, privilege := range session.Scope.Privileges {
			privileges[index] = string(privilege)
		}
		if encoded, err := accessgen.EncodeGenRevokeCurrentAuthoringSessionAuditPayload(accessgen.GenSchemaAuthoringSessionRevokedAuditPayload{
			Kind: string(session.Kind), ClientId: session.ClientID, TargetId: session.Scope.TargetID,
			ProjectId: session.Scope.ProjectID, Privileges: privileges,
		}); err == nil {
			metadata = []byte(encoded)
		}
	}
	return access.AuditEventInput{
		PrincipalID: session.PrincipalID, Action: action, TargetType: "authoring_session",
		TargetID: session.ID, Status: status, MetadataJSON: string(metadata),
	}
}

func createAuthoringSession(ctx context.Context, queries *platformdb.Queries, session access.AuthoringSession) error {
	privileges, err := json.Marshal(session.Scope.Privileges)
	if err != nil {
		return err
	}
	return queries.CreateAuthoringSession(ctx, platformdb.CreateAuthoringSessionParams{
		ID: session.ID, Kind: string(session.Kind), ClientID: session.ClientID, PrincipalID: session.PrincipalID,
		TargetID: session.Scope.TargetID, ProjectID: session.Scope.ProjectID, PrivilegesJson: string(privileges),
		CreatedAt: formatAuthoringTime(session.CreatedAt), ExpiresAt: formatAuthoringTime(session.ExpiresAt),
	})
}

func createAuthoringCredential(
	ctx context.Context,
	queries *platformdb.Queries,
	id, sessionID, accessHash, refreshHash string,
	accessExpiresAt, refreshExpiresAt, createdAt time.Time,
) error {
	return queries.CreateAuthoringCredential(ctx, platformdb.CreateAuthoringCredentialParams{
		ID: id, SessionID: sessionID, AccessTokenHash: accessHash,
		RefreshTokenHash: nullableString(refreshHash), AccessExpiresAt: formatAuthoringTime(accessExpiresAt),
		RefreshExpiresAt: nullableTime(refreshExpiresAt), CreatedAt: formatAuthoringTime(createdAt),
	})
}

func mapDeviceAuthorization(row platformdb.OauthDeviceAuthorization) (access.DeviceAuthorization, error) {
	scope, err := mapAuthoringScope(row.TargetID, row.ProjectID, row.PrivilegesJson)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	expiresAt, err := parseAuthoringTime(row.ExpiresAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	createdAt, err := parseAuthoringTime(row.CreatedAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	lastPolledAt, err := parseNullableAuthoringTime(row.LastPolledAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	approvedAt, err := parseNullableAuthoringTime(row.ApprovedAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	deniedAt, err := parseNullableAuthoringTime(row.DeniedAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	consumedAt, err := parseNullableAuthoringTime(row.ConsumedAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	return access.DeviceAuthorization{
		ID: row.ID, ClientID: row.ClientID, DeviceCodeHash: row.DeviceCodeHash, UserCodeHash: row.UserCodeHash,
		Scope: scope, Status: access.DeviceAuthorizationStatus(row.Status), PrincipalID: row.PrincipalID.String,
		ExpiresAt: expiresAt, PollInterval: time.Duration(row.PollIntervalSeconds) * time.Second,
		LastPolledAt: lastPolledAt, CreatedAt: createdAt, ApprovedAt: approvedAt, DeniedAt: deniedAt, ConsumedAt: consumedAt,
	}, nil
}

func mapAuthoringSession(row platformdb.OauthAuthoringSession) (access.AuthoringSession, error) {
	scope, err := mapAuthoringScope(row.TargetID, row.ProjectID, row.PrivilegesJson)
	if err != nil {
		return access.AuthoringSession{}, err
	}
	createdAt, err := parseAuthoringTime(row.CreatedAt)
	if err != nil {
		return access.AuthoringSession{}, err
	}
	lastUsedAt, err := parseNullableAuthoringTime(row.LastUsedAt)
	if err != nil {
		return access.AuthoringSession{}, err
	}
	expiresAt, err := parseAuthoringTime(row.ExpiresAt)
	if err != nil {
		return access.AuthoringSession{}, err
	}
	revokedAt, err := parseNullableAuthoringTime(row.RevokedAt)
	if err != nil {
		return access.AuthoringSession{}, err
	}
	return access.AuthoringSession{
		ID: row.ID, Kind: access.AuthoringSessionKind(row.Kind), ClientID: row.ClientID,
		PrincipalID: row.PrincipalID, Scope: scope, CreatedAt: createdAt, LastUsedAt: lastUsedAt,
		ExpiresAt: expiresAt, RevokedAt: revokedAt,
	}, nil
}

func mapAccessCredential(row platformdb.GetAuthoringCredentialByAccessHashRow) (access.AuthoringCredential, error) {
	return mapCredential(
		row.CredentialID, row.SessionID, row.AccessExpiresAt, row.RefreshExpiresAt,
		row.Kind, row.ClientID, row.PrincipalID, row.TargetID, row.ProjectID, row.PrivilegesJson,
		row.SessionCreatedAt, row.LastUsedAt, row.SessionExpiresAt, row.RevokedAt,
		row.ID, row.PrincipalKind, row.Email, row.DisplayName, row.DisabledAt, row.BlockedAt,
		row.PrincipalCreatedAt, row.PrincipalUpdatedAt,
	)
}

func mapRefreshCredential(row platformdb.GetAuthoringCredentialByRefreshHashRow) (access.AuthoringCredential, error) {
	return mapCredential(
		row.CredentialID, row.SessionID, row.AccessExpiresAt, row.RefreshExpiresAt,
		row.Kind, row.ClientID, row.PrincipalID, row.TargetID, row.ProjectID, row.PrivilegesJson,
		row.SessionCreatedAt, row.LastUsedAt, row.SessionExpiresAt, row.RevokedAt,
		row.ID, row.PrincipalKind, row.Email, row.DisplayName, row.DisabledAt, row.BlockedAt,
		row.PrincipalCreatedAt, row.PrincipalUpdatedAt,
	)
}

func mapCredential(
	credentialID, sessionID, accessExpiresValue string, refreshExpiresValue sql.NullString,
	kind, clientID, principalID, targetID, projectID, privilegesJSON, sessionCreatedValue string,
	lastUsedValue sql.NullString, sessionExpiresValue string, revokedValue sql.NullString,
	id, principalKind, email, displayName string, disabledValue, blockedValue sql.NullString,
	principalCreatedAt, principalUpdatedAt string,
) (access.AuthoringCredential, error) {
	scope, err := mapAuthoringScope(targetID, projectID, privilegesJSON)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	accessExpiresAt, err := parseAuthoringTime(accessExpiresValue)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	refreshExpiresAt, err := parseNullableAuthoringTime(refreshExpiresValue)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	sessionCreatedAt, err := parseAuthoringTime(sessionCreatedValue)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	lastUsedAt, err := parseNullableAuthoringTime(lastUsedValue)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	sessionExpiresAt, err := parseAuthoringTime(sessionExpiresValue)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	revokedAt, err := parseNullableAuthoringTime(revokedValue)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{
		ID: credentialID,
		Principal: access.Principal{
			ID: id, Kind: access.PrincipalKind(principalKind), Email: email, DisplayName: displayName,
			DisabledAt: disabledValue.String, BlockedAt: blockedValue.String, CreatedAt: principalCreatedAt, UpdatedAt: principalUpdatedAt,
		},
		Session: access.AuthoringSession{
			ID: sessionID, Kind: access.AuthoringSessionKind(kind), ClientID: clientID,
			PrincipalID: principalID, Scope: scope, CreatedAt: sessionCreatedAt,
			LastUsedAt: lastUsedAt, ExpiresAt: sessionExpiresAt, RevokedAt: revokedAt,
		},
		AccessExpiresAt: accessExpiresAt, RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

func mapAuthoringScope(targetID, projectID, privilegesJSON string) (access.AuthoringScope, error) {
	var privileges []access.Privilege
	if err := json.Unmarshal([]byte(privilegesJSON), &privileges); err != nil {
		return access.AuthoringScope{}, fmt.Errorf("decode authoring actions: %w", err)
	}
	return access.NewAuthoringScope(targetID, projectID, privileges)
}

func formatAuthoringTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseAuthoringTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse authoring timestamp: %w", err)
	}
	return parsed, nil
}

func parseNullableAuthoringTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return parseAuthoringTime(value.String)
}

func nullableTime(value time.Time) sql.NullString {
	if value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatAuthoringTime(value), Valid: true}
}

func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
