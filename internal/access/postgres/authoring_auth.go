package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	if err = accessdb.New(tx).InsertDeviceAuthorization(ctx, accessdb.InsertDeviceAuthorizationParams{ID: record.ID, ClientID: record.ClientID,
		DeviceCodeHash: record.DeviceCodeHash, UserCodeHash: record.UserCodeHash, TargetID: record.Scope.TargetID,
		ProjectID: record.Scope.ProjectID.String(), Capabilities: caps, Status: string(record.Status), Ttl: pgInterval(ttl),
		PollIntervalSeconds: int32(record.PollInterval / time.Second)}); err != nil {
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
	row, err := accessdb.New(db).GetDeviceAuthorizationByUserCodeHash(ctx, strings.TrimSpace(hash))
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	return deviceAuthorizationFromGenerated(row)
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
	principalUUID, err := pgUUID(pid)
	if err != nil {
		return err
	}
	var tag pgconnCommandTag
	if approve {
		result, qerr := accessdb.New(tx).ApproveDeviceAuthorization(ctx, accessdb.ApproveDeviceAuthorizationParams{ID: id, PrincipalID: principalUUID})
		tag, err = result, qerr
	} else {
		result, qerr := accessdb.New(tx).DenyDeviceAuthorization(ctx, accessdb.DenyDeviceAuthorizationParams{ID: id, PrincipalID: principalUUID})
		tag, err = result, qerr
	}
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
	generatedRecord, qerr := accessdb.New(tx).LockDeviceAuthorizationByDeviceCodeHash(ctx, strings.TrimSpace(issue.DeviceCodeHash))
	if qerr == nil {
		record, qerr = deviceAuthorizationFromGenerated(generatedRecord)
	}
	err = qerr
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var dbNow time.Time
	nowEpoch, err := accessdb.New(tx).DatabaseNow(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	dbNow = dbEpochMicros(nowEpoch)
	if record.ClientID != issue.ClientID || !dbNow.Before(record.ExpiresAt) {
		return access.AuthoringCredential{}, access.ErrDeviceAuthorizationExpired
	}
	switch record.Status {
	case access.DeviceAuthorizationPending:
		if !record.LastPolledAt.IsZero() && dbNow.Sub(record.LastPolledAt) < record.PollInterval {
			return access.AuthoringCredential{}, access.ErrDeviceAuthorizationSlowDown
		}
		if err = accessdb.New(tx).TouchDeviceAuthorizationPoll(ctx, record.ID); err != nil {
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
	principalID, err := pgUUID(record.PrincipalID)
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	principalRow, err := accessdb.New(tx).GetActiveAuthoringPrincipal(ctx, principalID)
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	principal := principalFromGenerated(accessdb.GetPrincipalRow{ID: principalRow.ID, PrincipalType: principalRow.PrincipalType, Status: principalRow.Status,
		Email: principalRow.Email, DisplayName: principalRow.DisplayName, DisabledAt: principalRow.DisabledAt, BlockedAt: principalRow.BlockedAt,
		LastSeenAt: principalRow.LastSeenAt, CreatedAt: principalRow.CreatedAt, UpdatedAt: principalRow.UpdatedAt})
	consumeTag, err := accessdb.New(tx).ConsumeDeviceAuthorization(ctx, record.ID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if consumeTag.RowsAffected() != 1 {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if !dbNow.Before(issue.AccessExpiresAt) || !dbNow.Before(issue.RefreshExpiresAt) || !issue.AccessExpiresAt.Before(issue.RefreshExpiresAt) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err = insertAuthoringSessionAndCredential(ctx, tx, issue.SessionID, access.AuthoringSessionHumanCLI, record.ClientID, principal.ID, record.Scope, issue.Now, issue.RefreshExpiresAt, issue.CredentialID, issue.AccessTokenHash, issue.RefreshTokenHash, issue.AccessExpiresAt, issue.RefreshExpiresAt); err != nil {
		return access.AuthoringCredential{}, err
	}
	var sessionCreated time.Time
	createdAt, err := accessdb.New(tx).GetAuthoringSessionCreatedAt(ctx, issue.SessionID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	sessionCreated, err = pgRequiredTime("authoring session created_at", createdAt)
	if err != nil {
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
	principalID, err := pgUUID(issue.Session.PrincipalID)
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	principalRow, err := accessdb.New(tx).GetActiveServiceAuthoringPrincipal(ctx, principalID)
	if err != nil {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	principal := principalFromGenerated(accessdb.GetPrincipalRow{ID: principalRow.ID, PrincipalType: principalRow.PrincipalType, Status: principalRow.Status,
		Email: principalRow.Email, DisplayName: principalRow.DisplayName, DisabledAt: principalRow.DisabledAt, BlockedAt: principalRow.BlockedAt,
		LastSeenAt: principalRow.LastSeenAt, CreatedAt: principalRow.CreatedAt, UpdatedAt: principalRow.UpdatedAt})
	var dbNow time.Time
	nowEpoch, err := accessdb.New(tx).DatabaseNow(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	dbNow = dbEpochMicros(nowEpoch)
	if !dbNow.Before(issue.Session.ExpiresAt) || !dbNow.Before(issue.AccessExpiresAt) || issue.Session.ExpiresAt.Before(issue.AccessExpiresAt) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err = insertAuthoringSessionAndCredential(ctx, tx, issue.Session.ID, issue.Session.Kind, issue.Session.ClientID, principal.ID, issue.Session.Scope, issue.Session.CreatedAt, issue.Session.ExpiresAt, issue.CredentialID, issue.AccessTokenHash, "", issue.AccessExpiresAt, time.Time{}); err != nil {
		return access.AuthoringCredential{}, err
	}
	createdAt, err := accessdb.New(tx).GetAuthoringSessionCreatedAt(ctx, issue.Session.ID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	sessionCreated, err := pgRequiredTime("authoring session created_at", createdAt)
	if err != nil {
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
	parsedPrincipalID, err := pgUUID(principalID)
	if err != nil {
		return err
	}
	if err = accessdb.New(tx).InsertAuthoringSession(ctx, accessdb.InsertAuthoringSessionParams{ID: sessionID, Kind: string(kind), ClientID: clientID,
		PrincipalID: parsedPrincipalID, TargetID: scope.TargetID, ProjectID: scope.ProjectID.String(), Capabilities: caps, ExpiresAt: pgTimestamp(expiresAt)}); err != nil {
		return err
	}
	var refresh *string
	if refreshHash != "" {
		refresh = &refreshHash
	}
	var refreshExp pgtype.Timestamptz
	if !refreshExpiresAt.IsZero() {
		refreshExp = pgTimestamp(refreshExpiresAt)
	}
	return accessdb.New(tx).InsertAuthoringCredential(ctx, accessdb.InsertAuthoringCredentialParams{ID: credentialID, SessionID: sessionID,
		AccessTokenHash: accessHash, RefreshTokenHash: refresh, AccessExpiresAt: pgTimestamp(accessExpiresAt), RefreshExpiresAt: refreshExp})
}

func (r *Repository) RotateAuthoringCredential(ctx context.Context, rotation access.AuthoringCredentialRotation) (access.AuthoringCredential, error) {
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var refreshTokenHash = rotation.RefreshTokenHash
	principalPGID, err := accessdb.New(tx).LookupAuthoringPrincipalIDForRotation(ctx, &refreshTokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	principalRow, err := accessdb.New(tx).GetActiveAuthoringPrincipal(ctx, principalPGID)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringPrincipal
	}
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	principal := principalFromGenerated(accessdb.GetPrincipalRow{ID: principalRow.ID, PrincipalType: principalRow.PrincipalType, Status: principalRow.Status,
		Email: principalRow.Email, DisplayName: principalRow.DisplayName, DisabledAt: principalRow.DisabledAt, BlockedAt: principalRow.BlockedAt,
		LastSeenAt: principalRow.LastSeenAt, CreatedAt: principalRow.CreatedAt, UpdatedAt: principalRow.UpdatedAt})
	row, err := accessdb.New(tx).LockAuthoringCredentialForRotation(ctx, &refreshTokenHash)
	var oldID, sessionID, principalID, kind, clientID, targetID, projectID, capsJSON string
	var refreshExp, sessionExpires, sessionCreated time.Time
	var sessionRevoked *time.Time
	var active bool
	if err == nil {
		refreshExp, err = pgRequiredTime("authoring refresh_expires_at", row.RefreshExpiresAt)
		if err != nil {
			return access.AuthoringCredential{}, err
		}
		oldID, sessionID, active = row.ID, row.SessionID, row.Active
		principalID, kind, clientID, targetID, projectID = principalUUID(row.PrincipalID), row.Kind, row.ClientID, row.TargetID, row.ProjectID
		if row.PrincipalID != principalPGID {
			return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
		}
		sessionExpires, err = pgRequiredTime("authoring session expires_at", row.ExpiresAt)
		if err != nil {
			return access.AuthoringCredential{}, err
		}
		sessionCreated, err = pgRequiredTime("authoring session created_at", row.CreatedAt)
		if err != nil {
			return access.AuthoringCredential{}, err
		}
		capsJSON = string(row.Capabilities)
		if row.RevokedAt.Valid {
			t := row.RevokedAt.Time
			sessionRevoked = &t
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	nowEpoch, err := accessdb.New(tx).DatabaseNow(ctx)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	dbNow := dbEpochMicros(nowEpoch)
	if !active {
		if parsedReplayPrincipal, parseErr := pgUUID(principalID); parseErr == nil {
			_, _ = accessdb.New(tx).RevokeAuthoringSession(ctx, accessdb.RevokeAuthoringSessionParams{ID: sessionID, PrincipalID: parsedReplayPrincipal})
		}
		_ = tx.Commit(ctx)
		return access.AuthoringCredential{}, access.ErrAuthoringRefreshReplay
	}
	if sessionRevoked != nil || !dbNow.Before(refreshExp) || !dbNow.Before(sessionExpires) || !dbNow.Before(rotation.AccessExpiresAt) || !dbNow.Before(rotation.RefreshExpiresAt) || !rotation.AccessExpiresAt.Before(rotation.RefreshExpiresAt) {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	if err = accessdb.New(tx).MarkAuthoringCredentialInactive(ctx, oldID); err != nil {
		return access.AuthoringCredential{}, err
	}
	tag, err := accessdb.New(tx).ExtendAuthoringSession(ctx, accessdb.ExtendAuthoringSessionParams{ID: sessionID, ExpiresAt: pgTimestamp(rotation.RefreshExpiresAt)})
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	if tag.RowsAffected() != 1 {
		return access.AuthoringCredential{}, access.ErrInvalidAuthoringCredential
	}
	refreshTokenHashNew := rotation.RefreshTokenHashNew
	if err = accessdb.New(tx).InsertRotatedAuthoringCredential(ctx, accessdb.InsertRotatedAuthoringCredentialParams{ID: rotation.CredentialID, SessionID: sessionID,
		AccessTokenHash: rotation.AccessTokenHash, RefreshTokenHash: &refreshTokenHashNew, AccessExpiresAt: pgTimestamp(rotation.AccessExpiresAt), RefreshExpiresAt: pgTimestamp(rotation.RefreshExpiresAt)}); err != nil {
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
	row, err := accessdb.New(db).GetAuthoringCredentialByAccessTokenHash(ctx, hash)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	_ = accessdb.New(db).TouchAuthoringSession(ctx, row.SessionID)
	project, err := graph.NewResourceID(row.ProjectID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	var capabilities []access.Capability
	if err = json.Unmarshal(row.Capabilities, &capabilities); err != nil {
		return access.AuthoringCredential{}, err
	}
	principalID := principalUUID(row.PrincipalID)
	principal, err := r.PrincipalByID(ctx, principalID)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	sessionExpires, err := pgRequiredTime("authoring session expires_at", row.ExpiresAt)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	accessExpires, err := pgRequiredTime("authoring access_expires_at", row.AccessExpiresAt)
	if err != nil {
		return access.AuthoringCredential{}, err
	}
	return access.AuthoringCredential{ID: row.ID, Principal: principal, Session: access.AuthoringSession{ID: row.SessionID, Kind: access.AuthoringSessionKind(row.Kind), ClientID: row.ClientID, PrincipalID: principalID, Scope: access.AuthoringScope{TargetID: row.TargetID, ProjectID: project, Capabilities: capabilities}, ExpiresAt: sessionExpires}, AccessExpiresAt: accessExpires}, nil
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
	parsedPrincipalID, err := pgUUID(id)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListAuthoringSessions(ctx, accessdb.ListAuthoringSessionsParams{PrincipalID: parsedPrincipalID, PageSize: maxPageSize})
	if err != nil {
		return nil, err
	}
	out := make([]access.AuthoringSession, 0, len(rows))
	for _, row := range rows {
		var value access.AuthoringSession
		value.ID, value.Kind, value.ClientID = row.ID, access.AuthoringSessionKind(row.Kind), row.ClientID
		value.Scope.TargetID = row.TargetID
		value.PrincipalID = id
		value.Scope.ProjectID, err = graph.NewResourceID(row.ProjectID)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(row.Capabilities, &value.Scope.Capabilities); err != nil {
			return nil, err
		}
		value.CreatedAt, err = pgRequiredTime("authoring session created_at", row.CreatedAt)
		if err != nil {
			return nil, err
		}
		if row.LastUsedAt.Valid {
			value.LastUsedAt = row.LastUsedAt.Time.UTC()
		}
		value.ExpiresAt, err = pgRequiredTime("authoring session expires_at", row.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if row.RevokedAt.Valid {
			value.RevokedAt = row.RevokedAt.Time.UTC()
		}
		out = append(out, value)
	}
	return out, nil
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
	parsedPrincipalID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).RevokeAuthoringSession(ctx, accessdb.RevokeAuthoringSessionParams{ID: sessionID, PrincipalID: parsedPrincipalID})
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
	tag, err := accessdb.New(db).RevokeAuthoringSessionByAccessTokenHash(ctx, hash)
	if err == nil && tag.RowsAffected() == 0 {
		return access.ErrInvalidAuthoringCredential
	}
	return err
}

func dbEpochMicros(value int64) time.Time {
	return time.UnixMicro(value).UTC()
}

func pgRequiredTime(label string, value pgtype.Timestamptz) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, fmt.Errorf("%s is unexpectedly null", label)
	}
	return value.Time.UTC(), nil
}

func deviceAuthorizationFromGenerated(row accessdb.AccessDeviceAuthorization) (access.DeviceAuthorization, error) {
	projectID, err := graph.NewResourceID(row.ProjectID)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	value := access.DeviceAuthorization{ID: row.ID, ClientID: row.ClientID, DeviceCodeHash: row.DeviceCodeHash,
		UserCodeHash: row.UserCodeHash, Scope: access.AuthoringScope{TargetID: row.TargetID, ProjectID: projectID},
		Status: access.DeviceAuthorizationStatus(row.Status), PollInterval: time.Duration(row.PollIntervalSeconds) * time.Second}
	if row.Capabilities != nil {
		if err := json.Unmarshal(row.Capabilities, &value.Scope.Capabilities); err != nil {
			return access.DeviceAuthorization{}, err
		}
	}
	value.PrincipalID = principalUUID(row.PrincipalID)
	value.ExpiresAt, err = pgRequiredTime("device authorization expires_at", row.ExpiresAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	if row.LastPolledAt.Valid {
		value.LastPolledAt = row.LastPolledAt.Time.UTC()
	}
	value.CreatedAt, err = pgRequiredTime("device authorization created_at", row.CreatedAt)
	if err != nil {
		return access.DeviceAuthorization{}, err
	}
	if row.ApprovedAt.Valid {
		value.ApprovedAt = row.ApprovedAt.Time.UTC()
	}
	if row.DeniedAt.Valid {
		value.DeniedAt = row.DeniedAt.Time.UTC()
	}
	if row.ConsumedAt.Valid {
		value.ConsumedAt = row.ConsumedAt.Time.UTC()
	}
	return value, nil
}
