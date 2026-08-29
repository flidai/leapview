package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateDeviceAuthorization(ctx context.Context, record access.DeviceAuthorization) error {
	if record.ID == "" || record.ClientID != access.AuthoringCLIClientID || record.DeviceCodeHash == "" || record.UserCodeHash == "" {
		return errors.New("device authorization identity is invalid")
	}
	ttl := record.ExpiresAt.Sub(record.CreatedAt)
	if ttl <= 0 || ttl > 24*time.Hour {
		return errors.New("device authorization expiry is invalid")
	}
	caps, err := json.Marshal(record.Scope.Capabilities)
	if err != nil {
		return err
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `WITH db_now AS (SELECT clock_timestamp() AS ts) INSERT INTO access.device_authorization(id,client_id,device_code_hash,user_code_hash,target_id,project_id,capabilities,status,expires_at,created_at,poll_interval_seconds) SELECT $1,$2,$3,$4,$5,$6,$7::jsonb,$8,db_now.ts+$9::interval,db_now.ts,$10 FROM db_now`, record.ID, record.ClientID, record.DeviceCodeHash, record.UserCodeHash, record.Scope.TargetID, record.Scope.ProjectID.String(), caps, record.Status, ttl.String(), int(record.PollInterval/time.Second)); err != nil {
		return err
	}
	auditRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err = auditRepo.RecordAuditEvent(ctx, access.AuditEventInput{Action: "authoring.device.started", ResourceKind: "device_authorization", ResourceID: record.ID, Status: "success"}); err != nil {
		return fmt.Errorf("%w: record device authorization audit: %v", access.ErrAuditTransaction, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (r *Repository) DeviceAuthorizationByUserCodeHash(ctx context.Context, hash string) (access.DeviceAuthorization, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	return scanDeviceAuthorization(db.QueryRow(ctx, `SELECT id,client_id,device_code_hash,user_code_hash,target_id,project_id,capabilities::text,status,principal_id::text,expires_at,poll_interval_seconds,last_polled_at,created_at,approved_at,denied_at,consumed_at FROM access.device_authorization WHERE user_code_hash=$1`, strings.TrimSpace(hash)))
}

func (r *Repository) ApproveDeviceAuthorization(ctx context.Context, id, principalID string, now time.Time) error {
	return r.decideDeviceAuthorization(ctx, id, principalID, true)
}

func (r *Repository) DenyDeviceAuthorization(ctx context.Context, id, principalID string, now time.Time) error {
	return r.decideDeviceAuthorization(ctx, id, principalID, false)
}

func (r *Repository) decideDeviceAuthorization(ctx context.Context, id, principalID string, approve bool) error {
	pid, err := uuidID("principal id", principalID)
	if err != nil {
		return err
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status := "denied"
	column := "denied_at"
	if approve {
		status, column = "approved", "approved_at"
	}
	q := fmt.Sprintf(`UPDATE access.device_authorization SET status='%s',principal_id=$2::uuid,%s=clock_timestamp() WHERE id=$1 AND status='pending' AND expires_at>clock_timestamp()`, status, column)
	tag, err := tx.Exec(ctx, q, id, pid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return access.ErrDeviceAuthorizationExpired
	}
	auditRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err = auditRepo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: principalID, Action: "authoring.device.decided", ResourceKind: "device_authorization", ResourceID: id, Status: "success"}); err != nil {
		return fmt.Errorf("%w: record device decision audit: %v", access.ErrAuditTransaction, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (r *Repository) IssueDeviceCredential(ctx context.Context, issue access.DeviceCredentialIssue) (access.AuthoringCredential, error) {
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var record access.DeviceAuthorization
	record, err = scanDeviceAuthorization(tx.QueryRow(ctx, `SELECT id,client_id,device_code_hash,user_code_hash,target_id,project_id,capabilities::text,status,principal_id::text,expires_at,poll_interval_seconds,last_polled_at,created_at,approved_at,denied_at,consumed_at FROM access.device_authorization WHERE device_code_hash=$1 FOR UPDATE`, strings.TrimSpace(issue.DeviceCodeHash)))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var dbNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return access.AuthoringCredential{}, err
	}
	if record.ClientID != issue.ClientID || !dbNow.Before(record.ExpiresAt) {
		return access.AuthoringCredential{}, access.ErrDeviceAuthorizationExpired
	}
	switch record.Status {
	case access.DeviceAuthorizationPending:
		if !record.LastPolledAt.IsZero() && dbNow.Sub(record.LastPolledAt) < record.PollInterval {
			return access.AuthoringCredential{}, access.ErrDeviceAuthorizationSlowDown
		}
		if _, err = tx.Exec(ctx, `UPDATE access.device_authorization SET last_polled_at=clock_timestamp() WHERE id=$1`, record.ID); err != nil {
			return access.AuthoringCredential{}, err
		}
		if err = tx.Commit(ctx); err != nil {
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
	principal, err := scanPrincipal(tx.QueryRow(ctx, principalSelect()+` WHERE id=$1::uuid AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL`, record.PrincipalID))
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	tag, err := tx.Exec(ctx, `UPDATE access.device_authorization SET status='consumed',consumed_at=clock_timestamp() WHERE id=$1 AND status='approved' AND expires_at>clock_timestamp()`, record.ID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if tag.RowsAffected() != 1 {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if !dbNow.Before(issue.AccessExpiresAt) || !dbNow.Before(issue.RefreshExpiresAt) || !issue.AccessExpiresAt.Before(issue.RefreshExpiresAt) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err = insertAuthoringSessionAndCredential(ctx, tx, issue.SessionID, access.AuthoringSessionHumanCLI, record.ClientID, principal.ID, record.Scope, issue.Now, issue.RefreshExpiresAt, issue.CredentialID, issue.AccessTokenHash, issue.RefreshTokenHash, issue.AccessExpiresAt, issue.RefreshExpiresAt); err != nil {
		return access.AuthoringCredential{}, err
	}
	var sessionCreated time.Time
	if err = tx.QueryRow(ctx, `SELECT created_at FROM access.authoring_session WHERE id=$1`, issue.SessionID).Scan(&sessionCreated); err != nil {
		return access.AuthoringCredential{}, err
	}
	auditRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err = auditRepo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: principal.ID, Action: "authoring.session.created", ResourceKind: "authoring_session", ResourceID: issue.SessionID, Status: "success"}); err != nil {
		return access.AuthoringCredential{}, fmt.Errorf("%w: record authoring session audit: %v", access.ErrAuditTransaction, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{ID: issue.CredentialID, Principal: principal, Session: access.AuthoringSession{ID: issue.SessionID, Kind: access.AuthoringSessionHumanCLI, ClientID: record.ClientID, PrincipalID: principal.ID, Scope: record.Scope, CreatedAt: sessionCreated.UTC(), ExpiresAt: issue.RefreshExpiresAt}, AccessExpiresAt: issue.AccessExpiresAt, RefreshExpiresAt: issue.RefreshExpiresAt}, nil
}

func (r *Repository) CreateWorkloadCredential(ctx context.Context, issue access.WorkloadCredentialIssue) (access.AuthoringCredential, error) {
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	principal, err := scanPrincipal(tx.QueryRow(ctx, principalSelect()+` WHERE id=$1::uuid AND principal_type='service' AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL`, issue.Session.PrincipalID))
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	var dbNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return access.AuthoringCredential{}, err
	}
	if !dbNow.Before(issue.Session.ExpiresAt) || !dbNow.Before(issue.AccessExpiresAt) || issue.Session.ExpiresAt.Before(issue.AccessExpiresAt) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err = insertAuthoringSessionAndCredential(ctx, tx, issue.Session.ID, issue.Session.Kind, issue.Session.ClientID, principal.ID, issue.Session.Scope, issue.Session.CreatedAt, issue.Session.ExpiresAt, issue.CredentialID, issue.AccessTokenHash, "", issue.AccessExpiresAt, time.Time{}); err != nil {
		return access.AuthoringCredential{}, err
	}
	var sessionCreated time.Time
	if err = tx.QueryRow(ctx, `SELECT created_at FROM access.authoring_session WHERE id=$1`, issue.Session.ID).Scan(&sessionCreated); err != nil {
		return access.AuthoringCredential{}, err
	}
	auditRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err = auditRepo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: principal.ID, Action: "authoring.workload.created", ResourceKind: "authoring_session", ResourceID: issue.Session.ID, Status: "success"}); err != nil {
		return access.AuthoringCredential{}, fmt.Errorf("%w: record workload audit: %v", access.ErrAuditTransaction, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return access.AuthoringCredential{}, err
	}
	issue.Session.CreatedAt = sessionCreated.UTC()
	return access.AuthoringCredential{ID: issue.CredentialID, Principal: principal, Session: issue.Session, AccessExpiresAt: issue.AccessExpiresAt}, nil
}

func insertAuthoringSessionAndCredential(ctx context.Context, tx pgx.Tx, sessionID string, kind access.AuthoringSessionKind, clientID, principalID string, scope access.AuthoringScope, createdAt, expiresAt time.Time, credentialID, accessHash, refreshHash string, accessExpiresAt, refreshExpiresAt time.Time) error {
	caps, err := json.Marshal(scope.Capabilities)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO access.authoring_session(id,kind,client_id,principal_id,target_id,project_id,capabilities,created_at,expires_at) VALUES($1,$2,$3,$4::uuid,$5,$6,$7::jsonb,clock_timestamp(),$8)`, sessionID, kind, clientID, principalID, scope.TargetID, scope.ProjectID.String(), caps, expiresAt.UTC()); err != nil {
		return err
	}
	var refresh any
	if refreshHash != "" {
		refresh = refreshHash
	}
	var refreshExp any
	if !refreshExpiresAt.IsZero() {
		refreshExp = refreshExpiresAt.UTC()
	}
	_, err = tx.Exec(ctx, `INSERT INTO access.authoring_credential(id,session_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1,$2,$3,$4,$5,$6)`, credentialID, sessionID, accessHash, refresh, accessExpiresAt.UTC(), refreshExp)
	return err
}

func (r *Repository) RotateAuthoringCredential(ctx context.Context, rotation access.AuthoringCredentialRotation) (access.AuthoringCredential, error) {
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldID, sessionID, principalID, kind, clientID, targetID, projectID, capsJSON string
	var accessExp, refreshExp, sessionExpires, sessionCreated time.Time
	var sessionRevoked *time.Time
	var active bool
	err = tx.QueryRow(ctx, `SELECT c.id,c.session_id,c.access_expires_at,COALESCE(c.refresh_expires_at,'epoch'::timestamptz),c.active,s.principal_id::text,s.kind,s.client_id,s.target_id,s.project_id,s.capabilities::text,s.expires_at,s.created_at,s.revoked_at FROM access.authoring_credential c JOIN access.authoring_session s ON s.id=c.session_id WHERE c.refresh_token_hash=$1 FOR UPDATE`, rotation.RefreshTokenHash).Scan(&oldID, &sessionID, &accessExp, &refreshExp, &active, &principalID, &kind, &clientID, &targetID, &projectID, &capsJSON, &sessionExpires, &sessionCreated, &sessionRevoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var dbNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return access.AuthoringCredential{}, err
	}
	if !active {
		_, _ = tx.Exec(ctx, `UPDATE access.authoring_session SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL`, sessionID)
		_ = tx.Commit(ctx)
		return access.AuthoringCredential{}, access.ErrAuthoringRefreshReplay
	}
	if sessionRevoked != nil || !dbNow.Before(refreshExp) || !dbNow.Before(sessionExpires) || !dbNow.Before(rotation.AccessExpiresAt) || !dbNow.Before(rotation.RefreshExpiresAt) || !rotation.AccessExpiresAt.Before(rotation.RefreshExpiresAt) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if _, err = tx.Exec(ctx, `UPDATE access.authoring_credential SET active=false,replaced_at=clock_timestamp() WHERE id=$1 AND active`, oldID); err != nil {
		return access.AuthoringCredential{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE access.authoring_session SET expires_at=$2 WHERE id=$1 AND revoked_at IS NULL AND $2>clock_timestamp()`, sessionID, rotation.RefreshExpiresAt.UTC())
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if tag.RowsAffected() != 1 {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if _, err = tx.Exec(ctx, `INSERT INTO access.authoring_credential(id,session_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1,$2,$3,$4,$5,$6)`, rotation.CredentialID, sessionID, rotation.AccessTokenHash, rotation.RefreshTokenHashNew, rotation.AccessExpiresAt.UTC(), rotation.RefreshExpiresAt.UTC()); err != nil {
		return access.AuthoringCredential{}, err
	}
	project, err := graph.NewResourceID(projectID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var capabilities []access.Capability
	if err = json.Unmarshal([]byte(capsJSON), &capabilities); err != nil {
		return access.AuthoringCredential{}, err
	}
	principal, err := scanPrincipal(tx.QueryRow(ctx, principalSelect()+` WHERE id=$1::uuid AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL FOR UPDATE`, principalID))
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	if err = tx.Commit(ctx); err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{ID: rotation.CredentialID, Principal: principal, Session: access.AuthoringSession{ID: sessionID, Kind: access.AuthoringSessionKind(kind), ClientID: clientID, PrincipalID: principalID, Scope: access.AuthoringScope{TargetID: targetID, ProjectID: project, Capabilities: capabilities}, CreatedAt: sessionCreated.UTC(), ExpiresAt: rotation.RefreshExpiresAt}, AccessExpiresAt: rotation.AccessExpiresAt, RefreshExpiresAt: rotation.RefreshExpiresAt}, nil
}

func (r *Repository) AuthoringCredentialByAccessTokenHash(ctx context.Context, hash string, now time.Time) (access.AuthoringCredential, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var credentialID, sessionID, principalID, kind, clientID, targetID, projectID, capsJSON string
	var accessExpires, sessionExpires time.Time
	err = db.QueryRow(ctx, `SELECT c.id,c.session_id,s.principal_id::text,s.kind,s.client_id,s.target_id,s.project_id,s.capabilities::text,c.access_expires_at,s.expires_at FROM access.authoring_credential c JOIN access.authoring_session s ON s.id=c.session_id JOIN access.principal p ON p.id=s.principal_id WHERE c.access_token_hash=$1 AND c.active AND c.access_expires_at>clock_timestamp() AND s.expires_at>clock_timestamp() AND s.revoked_at IS NULL AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL`, hash).Scan(&credentialID, &sessionID, &principalID, &kind, &clientID, &targetID, &projectID, &capsJSON, &accessExpires, &sessionExpires)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	_, _ = db.Exec(ctx, `UPDATE access.authoring_session SET last_used_at=clock_timestamp() WHERE id=$1`, sessionID)
	project, err := graph.NewResourceID(projectID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var capabilities []access.Capability
	if err = json.Unmarshal([]byte(capsJSON), &capabilities); err != nil {
		return access.AuthoringCredential{}, err
	}
	principal, err := r.PrincipalByID(ctx, principalID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{ID: credentialID, Principal: principal, Session: access.AuthoringSession{ID: sessionID, Kind: access.AuthoringSessionKind(kind), ClientID: clientID, PrincipalID: principalID, Scope: access.AuthoringScope{TargetID: targetID, ProjectID: project, Capabilities: capabilities}, ExpiresAt: sessionExpires}, AccessExpiresAt: accessExpires}, nil
}

func (r *Repository) ListAuthoringSessions(ctx context.Context, principalID string) ([]access.AuthoringSession, error) {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return nil, err
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT id,kind,client_id,target_id,project_id,capabilities::text,created_at,last_used_at,expires_at,revoked_at FROM access.authoring_session WHERE principal_id=$1::uuid ORDER BY created_at DESC LIMIT $2`, id, maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.AuthoringSession, 0)
	for rows.Next() {
		var value access.AuthoringSession
		var kind, projectID, caps string
		var created, last, expires, revoked *time.Time
		if err := rows.Scan(&value.ID, &kind, &value.ClientID, &value.Scope.TargetID, &projectID, &caps, &created, &last, &expires, &revoked); err != nil {
			return nil, err
		}
		value.Kind = access.AuthoringSessionKind(kind)
		value.PrincipalID = id
		value.Scope.ProjectID, err = graph.NewResourceID(projectID)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(caps), &value.Scope.Capabilities); err != nil {
			return nil, err
		}
		if created != nil {
			value.CreatedAt = created.UTC()
		}
		if last != nil {
			value.LastUsedAt = last.UTC()
		}
		if expires != nil {
			value.ExpiresAt = expires.UTC()
		}
		if revoked != nil {
			value.RevokedAt = revoked.UTC()
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *Repository) RevokeAuthoringSession(ctx context.Context, principalID, sessionID string, now time.Time) error {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return err
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE access.authoring_session SET revoked_at=clock_timestamp() WHERE id=$1 AND principal_id=$2::uuid AND revoked_at IS NULL`, sessionID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return access.ErrInvalidAuthoringCredential
	}
	return err
}

func (r *Repository) RevokeAuthoringSessionByAccessTokenHash(ctx context.Context, hash string, now time.Time) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE access.authoring_session SET revoked_at=clock_timestamp() WHERE id=(SELECT session_id FROM access.authoring_credential WHERE access_token_hash=$1) AND revoked_at IS NULL`, hash)
	if err == nil && tag.RowsAffected() == 0 {
		return access.ErrInvalidAuthoringCredential
	}
	return err
}

func scanDeviceAuthorization(row interface{ Scan(...any) error }) (access.DeviceAuthorization, error) {
	var value access.DeviceAuthorization
	var caps string
	var status string
	var principal sql.NullString
	var expires, last, created, approved, denied, consumed *time.Time
	var poll int
	err := row.Scan(&value.ID, &value.ClientID, &value.DeviceCodeHash, &value.UserCodeHash, &value.Scope.TargetID, &value.Scope.ProjectID, &caps, &status, &principal, &expires, &poll, &last, &created, &approved, &denied, &consumed)
	if err != nil {
		return value, err
	}
	value.Status = access.DeviceAuthorizationStatus(status)
	if principal.Valid {
		value.PrincipalID = principal.String
	}
	var parseErr error
	value.Scope.ProjectID, parseErr = graph.NewResourceID(value.Scope.ProjectID.String())
	if parseErr != nil {
		return value, parseErr
	}
	if err = json.Unmarshal([]byte(caps), &value.Scope.Capabilities); err != nil {
		return value, err
	}
	value.PollInterval = time.Duration(poll) * time.Second
	if expires != nil {
		value.ExpiresAt = expires.UTC()
	}
	if last != nil {
		value.LastPolledAt = last.UTC()
	}
	if created != nil {
		value.CreatedAt = created.UTC()
	}
	if approved != nil {
		value.ApprovedAt = approved.UTC()
	}
	if denied != nil {
		value.DeniedAt = denied.UTC()
	}
	if consumed != nil {
		value.ConsumedAt = consumed.UTC()
	}
	return value, nil
}
