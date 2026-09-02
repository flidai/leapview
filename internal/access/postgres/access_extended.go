package postgres

// This file contains the access slices that are deliberately kept separate
// from the identity/credential core: lifecycle tombstones, preferences,
// avatars, immutable authorization snapshots, and first-party desktop OAuth.

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
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/access/desktopauth"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrAuthorizationSnapshotIdentityConflict = errors.New("authorization snapshot identity already installed with a different digest")

func (r *Repository) InsertPlatformSettingIfMissing(ctx context.Context, key, value string) (bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return false, err
	}
	tag, err := db.Exec(ctx, `INSERT INTO access.platform_setting(key,value) VALUES($1,$2) ON CONFLICT(key) DO NOTHING`, strings.TrimSpace(key), value)
	return tag.RowsAffected() == 1, err
}

func (r *Repository) DeletePrincipal(ctx context.Context, id string) error {
	id, err := uuidID("principal id", id)
	if err != nil {
		return err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	tag, err := tx.Exec(ctx, `UPDATE access.principal SET status='disabled', disabled_at=COALESCE(disabled_at,clock_timestamp()), revoked_at=COALESCE(revoked_at,clock_timestamp()), updated_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.service_principal_secret SET revoked_at=clock_timestamp() WHERE service_principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id)
	if ownTx {
		return tx.Commit(ctx)
	}
	return nil
}

func (r *Repository) setPrincipalDisabled(ctx context.Context, id string, provisioned bool, disabled bool) (access.Principal, error) {
	id, err := uuidID("principal id", id)
	if err != nil {
		return access.Principal{}, err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.Principal{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	var tag pgconnCommandTag
	if disabled {
		// Provisioned and administrator disables have separate call sites but
		// share the same durable state transition. A block is represented by
		// blocked_at; an external lifecycle disable by disabled_at.
		if provisioned {
			tag, err = tx.Exec(ctx, `UPDATE access.principal SET status='disabled', disabled_at=COALESCE(disabled_at,clock_timestamp()), updated_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE access.principal SET status='disabled', blocked_at=COALESCE(blocked_at,clock_timestamp()), updated_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
		}
	} else {
		tag, err = tx.Exec(ctx, `UPDATE access.principal SET status='active', disabled_at=NULL, blocked_at=NULL, updated_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
	}
	if err != nil {
		return access.Principal{}, err
	}
	if tag.RowsAffected() == 0 {
		return access.Principal{}, pgx.ErrNoRows
	}
	if disabled {
		if _, err = tx.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
			return access.Principal{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
			return access.Principal{}, err
		}
	}
	if ownTx {
		if err = tx.Commit(ctx); err != nil {
			return access.Principal{}, err
		}
	}
	if !ownTx {
		return (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).PrincipalByID(ctx, id)
	}
	return r.PrincipalByID(ctx, id)
}

// pgconnCommandTag keeps this file independent of pgconn's concrete type in
// the branch where a narrow DBTX is used; RowsAffected is the only operation
// needed here.
type pgconnCommandTag interface{ RowsAffected() int64 }

func (r *Repository) DisablePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setPrincipalDisabled(ctx, id, false, true)
}

func (r *Repository) DisableProvisionedPrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setPrincipalDisabled(ctx, id, true, true)
}

func (r *Repository) EnablePrincipal(ctx context.Context, id string) (access.Principal, error) {
	return r.setPrincipalDisabled(ctx, id, false, false)
}

func (r *Repository) ListServicePrincipalSecrets(ctx context.Context, principalID string) ([]access.ServicePrincipalSecret, error) {
	principalID, err := uuidID("service principal id", principalID)
	if err != nil {
		return nil, err
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT id::text,service_principal_id::text,name,expires_at,created_at,revoked_at FROM access.service_principal_secret WHERE service_principal_id=$1::uuid ORDER BY created_at DESC LIMIT $2`, principalID, maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.ServicePrincipalSecret, 0)
	for rows.Next() {
		var value access.ServicePrincipalSecret
		var expires, created, revoked *time.Time
		if err := rows.Scan(&value.ID, &value.ServicePrincipalID, &value.Name, &expires, &created, &revoked); err != nil {
			return nil, err
		}
		value.ExpiresAt, value.CreatedAt, value.RevokedAt = formatTimePtr(expires), formatTimePtr(created), formatTimePtr(revoked)
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *Repository) GetServicePrincipalSecret(ctx context.Context, principalID, secretID string) (access.ServicePrincipalSecret, error) {
	principalID, err := uuidID("service principal id", principalID)
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	secretID, err = uuidID("secret id", secretID)
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	var value access.ServicePrincipalSecret
	var expires, created, revoked *time.Time
	err = db.QueryRow(ctx, `SELECT id::text,service_principal_id::text,name,expires_at,created_at,revoked_at FROM access.service_principal_secret WHERE id=$1::uuid AND service_principal_id=$2::uuid`, secretID, principalID).Scan(&value.ID, &value.ServicePrincipalID, &value.Name, &expires, &created, &revoked)
	value.ExpiresAt, value.CreatedAt, value.RevokedAt = formatTimePtr(expires), formatTimePtr(created), formatTimePtr(revoked)
	return value, err
}

func (r *Repository) PrincipalPreferences(ctx context.Context, principalID string) (access.PrincipalPreferences, error) {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	var theme string
	var updated time.Time
	err = db.QueryRow(ctx, `SELECT theme,updated_at FROM access.principal_preferences WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id).Scan(&theme, &updated)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	parsed, ok := access.ParseThemeMode(theme)
	if !ok {
		return access.PrincipalPreferences{}, fmt.Errorf("unsupported stored theme %q", theme)
	}
	return access.PrincipalPreferences{PrincipalID: id, Theme: parsed, UpdatedAt: formatTime(updated)}, nil
}

func (r *Repository) SetPrincipalTheme(ctx context.Context, principalID string, theme access.ThemeMode) (access.PrincipalPreferences, error) {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	parsed, ok := access.ParseThemeMode(string(theme))
	if !ok || parsed != theme {
		return access.PrincipalPreferences{}, fmt.Errorf("unsupported theme %q", theme)
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Preferences are versioned rows: a revoked row is never reactivated.
	if _, err = tx.Exec(ctx, `UPDATE access.principal_preferences SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return access.PrincipalPreferences{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO access.principal_preferences(principal_id,theme) SELECT $1::uuid,$2 WHERE EXISTS(SELECT 1 FROM access.principal WHERE id=$1::uuid AND revoked_at IS NULL)`, id, string(theme)); err != nil {
		return access.PrincipalPreferences{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return access.PrincipalPreferences{}, err
	}
	return r.PrincipalPreferences(ctx, id)
}

func (r *Repository) SetPrincipalThemeAudited(ctx context.Context, principalID string, theme access.ThemeMode) error {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return err
	}
	parsed, ok := access.ParseThemeMode(string(theme))
	if !ok || parsed != theme {
		return fmt.Errorf("unsupported theme %q", theme)
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE access.principal_preferences SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO access.principal_preferences(principal_id,theme) SELECT $1::uuid,$2 WHERE EXISTS(SELECT 1 FROM access.principal WHERE id=$1::uuid AND revoked_at IS NULL)`, id, string(theme))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	metadata, _ := json.Marshal(map[string]string{"theme": string(theme)})
	auditRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err = auditRepo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: id, Action: "principal.theme.updated", ResourceKind: "principal", ResourceID: id, Status: "success", MetadataJSON: string(metadata)}); err != nil {
		return fmt.Errorf("%w: record preference audit: %v", access.ErrAuditTransaction, err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) Avatar(ctx context.Context, principalID string) (avatar.Metadata, error) {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return avatar.Metadata{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return avatar.Metadata{}, err
	}
	var value avatar.Metadata
	var updated time.Time
	err = db.QueryRow(ctx, `SELECT principal_id::text,sha256,media_type,size_bytes,width,height,updated_at FROM access.principal_avatar WHERE principal_id=$1::uuid AND revoked_at IS NULL ORDER BY updated_at DESC LIMIT 1`, id).Scan(&value.PrincipalID, &value.SHA256, &value.MediaType, &value.SizeBytes, &value.Width, &value.Height, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return avatar.Metadata{}, avatar.ErrNotFound
	}
	value.UpdatedAt = formatTime(updated)
	return value, err
}

func (r *Repository) UpsertAvatar(ctx context.Context, value avatar.Metadata) (avatar.Metadata, error) {
	id, err := uuidID("principal id", value.PrincipalID)
	if err != nil {
		return avatar.Metadata{}, err
	}
	sha := strings.ToLower(strings.TrimSpace(value.SHA256))
	if len(sha) != 64 {
		return avatar.Metadata{}, fmt.Errorf("avatar digest is invalid")
	}
	if _, err = hex.DecodeString(sha); err != nil || value.MediaType != "image/png" || value.SizeBytes <= 0 || value.Width != 256 || value.Height != 256 {
		return avatar.Metadata{}, fmt.Errorf("avatar metadata is invalid")
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return avatar.Metadata{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var principalExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.principal WHERE id=$1::uuid AND revoked_at IS NULL)`, id).Scan(&principalExists); err != nil {
		return avatar.Metadata{}, err
	}
	if !principalExists {
		return avatar.Metadata{}, pgx.ErrNoRows
	}
	objectTag, err := tx.Exec(ctx, `INSERT INTO access.avatar_object(sha256,object_key,media_type,size_bytes) VALUES($1,'avatars/'||$1,'image/png',$2) ON CONFLICT(sha256) DO NOTHING`, sha, value.SizeBytes)
	if err != nil {
		return avatar.Metadata{}, err
	}
	if objectTag.RowsAffected() == 0 {
		var objectKey, mediaType string
		var objectSize int64
		if err = tx.QueryRow(ctx, `SELECT object_key,media_type,size_bytes FROM access.avatar_object WHERE sha256=$1`, sha).Scan(&objectKey, &mediaType, &objectSize); err != nil {
			return avatar.Metadata{}, err
		}
		if objectKey != "avatars/"+sha || mediaType != "image/png" || objectSize != value.SizeBytes {
			return avatar.Metadata{}, fmt.Errorf("avatar object metadata conflicts with digest %s", sha)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE access.principal_avatar SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return avatar.Metadata{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO access.principal_avatar(principal_id,sha256,media_type,size_bytes,width,height) SELECT $1::uuid,$2,'image/png',$3,256,256 WHERE EXISTS(SELECT 1 FROM access.principal WHERE id=$1::uuid AND revoked_at IS NULL)`, id, sha, value.SizeBytes); err != nil {
		return avatar.Metadata{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return avatar.Metadata{}, err
	}
	return r.Avatar(ctx, id)
}

func (r *Repository) DeleteAvatar(ctx context.Context, principalID string) error {
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return err
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE access.principal_avatar SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return avatar.ErrNotFound
	}
	return err
}

func (r *Repository) InstallAuthorizationSnapshot(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot) error {
	if err := snapshot.ValidateBound(); err != nil {
		return fmt.Errorf("validate authorization snapshot: %w", err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return err
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	identity := snapshot.Identity()
	projectID, environment, generation := identity.ProjectID.String(), identity.Environment, identity.GenerationID
	tag, err := tx.Exec(ctx, `INSERT INTO access.authorization_snapshot(project_id,environment,generation_id,digest) VALUES($1,$2,$3,$4) ON CONFLICT(project_id,environment,generation_id) DO NOTHING`, projectID, environment, generation, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var installed string
		if err = tx.QueryRow(ctx, `SELECT digest FROM access.authorization_snapshot WHERE project_id=$1 AND environment=$2 AND generation_id=$3`, projectID, environment, generation).Scan(&installed); err != nil {
			return err
		}
		if installed != digest {
			return fmt.Errorf("%w: project=%s environment=%s generation=%s", ErrAuthorizationSnapshotIdentityConflict, projectID, environment, generation)
		}
		return tx.Commit(ctx)
	}
	for _, binding := range snapshot.RoleBindings() {
		caps, marshalErr := json.Marshal(binding.Capabilities)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO access.authorization_role_binding(id,project_id,environment,generation_id,subject_kind,subject_id,role,capabilities,name) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)`, binding.ID, projectID, environment, generation, binding.Subject.Kind, binding.Subject.ID, binding.Role, caps, binding.Name); err != nil {
			return err
		}
	}
	for _, grant := range snapshot.Grants() {
		canonical := grant.Canonical
		if _, err = tx.Exec(ctx, `INSERT INTO access.authorization_grant(id,project_id,environment,generation_id,subject_kind,subject_id,resource_id,resource_kind,capability,name) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, grant.ID, projectID, environment, generation, canonical.Subject().Kind, canonical.Subject().ID, canonical.Resource().ID().String(), canonical.Resource().Kind(), canonical.Capability(), grant.Name); err != nil {
			return err
		}
	}
	for _, policy := range snapshot.DataPolicies() {
		var subjectKind, subjectID any
		if policy.Subject != nil {
			subjectKind, subjectID = policy.Subject.Kind, policy.Subject.ID
		}
		if !json.Valid([]byte(policy.ExpressionJSON)) {
			return fmt.Errorf("data policy %q expression is invalid JSON", policy.ID)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO access.authorization_data_policy(id,project_id,environment,generation_id,resource_id,resource_kind,subject_kind,subject_id,policy_type,expression) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`, policy.ID, projectID, environment, generation, policy.Resource.ID().String(), policy.Resource.Kind(), subjectKind, subjectID, policy.PolicyType, policy.ExpressionJSON); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) RecordCanonicalAuditEvent(ctx context.Context, event access.CanonicalAuditEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	metadata, err := event.CanonicalMetadataJSON()
	if err != nil {
		return err
	}
	var metadataObject map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &metadataObject); err != nil || metadataObject == nil {
		return errors.New("canonical audit metadata must be a JSON object")
	}
	principalID, err := uuidID("audit principal id", event.PrincipalID)
	if err != nil {
		return err
	}
	requestID, err := optionalUUID("audit request id", event.RequestID)
	if err != nil {
		return err
	}
	correlationID, err := optionalUUID("audit correlation id", event.CorrelationID)
	if err != nil {
		return err
	}
	outcome := strings.TrimSpace(event.Status)
	if outcome == "" {
		outcome = "success"
	}
	if outcome != "success" && outcome != "failure" && outcome != "denied" {
		return fmt.Errorf("audit outcome %q is invalid", outcome)
	}
	intentDigest := canonicalAuditDigest(event, metadata)
	id := uuid.New()
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO audit.audit_event(audit_id,principal_id,source,operation,action,resource_kind,resource_id,project_id,environment,generation_id,capability,outcome,request_id,correlation_id,aggregate_key,aggregate_sequence,intent_digest,metadata) VALUES($1::uuid,$2::uuid,'access','authorization',$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,NULLIF($12,'')::uuid,$13,0,$14,$15::jsonb)`, id, principalID, event.Action, event.Resource.Kind(), event.Resource.ID().String(), event.Identity.ProjectID.String(), event.Identity.Environment, event.Identity.GenerationID, event.Capability.String(), outcome, requestID, correlationID, event.Resource.ID().String(), intentDigest, metadata)
	return err
}

func optionalUUID(label, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return uuidID(label, value)
}

func canonicalAuditDigest(event access.CanonicalAuditEvent, metadata string) string {
	payload, _ := json.Marshal(struct {
		ProjectID, Environment, GenerationID                              string
		PrincipalID, Action, ResourceID, ResourceKind, Capability, Status string
		RequestID, CorrelationID, MetadataJSON                            string
	}{
		ProjectID: event.Identity.ProjectID.String(), Environment: event.Identity.Environment, GenerationID: event.Identity.GenerationID,
		PrincipalID: event.PrincipalID, Action: event.Action, ResourceID: event.Resource.ID().String(), ResourceKind: string(event.Resource.Kind()),
		Capability: event.Capability.String(), Status: event.Status, RequestID: event.RequestID, CorrelationID: event.CorrelationID,
		MetadataJSON: metadata,
	})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (r *Repository) StoreAuthorizationCode(ctx context.Context, code desktopauth.AuthorizationCode) error {
	id, err := uuidID("principal id", code.PrincipalID)
	if err != nil {
		return err
	}
	ttl := code.ExpiresAt.Sub(code.CreatedAt)
	if ttl <= 0 || ttl > 10*time.Minute {
		return fmt.Errorf("desktop authorization expiry is invalid")
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `WITH db_now AS (SELECT clock_timestamp() AS ts) INSERT INTO access.desktop_authorization_code(code_hash,principal_id,client_id,instance_id,profile_id,redirect_uri,code_challenge,return_path,expires_at,created_at) SELECT $1,$2::uuid,$3,$4,$5,$6,$7,$8,db_now.ts+$9::interval,db_now.ts FROM db_now WHERE EXISTS(SELECT 1 FROM access.principal WHERE id=$2::uuid AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL)`, code.CodeHash[:], id, code.ClientID, code.InstanceID, code.ProfileID, code.RedirectURI, code.CodeChallenge, code.ReturnPath, ttl.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) ConsumeAuthorizationCode(ctx context.Context, hash [32]byte, now time.Time, validate func(desktopauth.AuthorizationCode) bool) (string, error) {
	if validate == nil {
		return "", errors.New("desktop authorization validator is required")
	}
	if tx, ok := r.db.(pgx.Tx); ok {
		return consumeDesktopCode(ctx, tx, hash, now, validate)
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	principal, err := consumeDesktopCode(ctx, tx, hash, now, validate)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return principal, nil
}

func consumeDesktopCode(ctx context.Context, tx pgx.Tx, hash [32]byte, now time.Time, validate func(desktopauth.AuthorizationCode) bool) (string, error) {
	var grant desktopauth.AuthorizationCode
	var expires, created, consumed *time.Time
	var principalID, clientID, instanceID, profileID, redirectURI, challenge, returnPath string
	err := tx.QueryRow(ctx, `SELECT principal_id::text,client_id,instance_id,profile_id,redirect_uri,code_challenge,return_path,expires_at,created_at,consumed_at FROM access.desktop_authorization_code WHERE code_hash=$1 AND expires_at>clock_timestamp() FOR UPDATE`, hash[:]).Scan(&principalID, &clientID, &instanceID, &profileID, &redirectURI, &challenge, &returnPath, &expires, &created, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", desktopauth.ErrInvalidGrant
	}
	if err != nil {
		return "", err
	}
	grant.CodeHash = hash
	grant.PrincipalID, grant.ClientID, grant.InstanceID, grant.ProfileID = principalID, clientID, instanceID, profileID
	grant.RedirectURI, grant.CodeChallenge, grant.ReturnPath = redirectURI, challenge, returnPath
	grant.ExpiresAt, grant.CreatedAt = expires.UTC(), created.UTC()
	grant.Consumed = consumed != nil
	if !validate(grant) || grant.Consumed {
		return "", desktopauth.ErrInvalidGrant
	}
	tag, err := tx.Exec(ctx, `UPDATE access.desktop_authorization_code SET consumed_at=clock_timestamp() WHERE code_hash=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp()`, hash[:])
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return "", err
		}
		return "", desktopauth.ErrInvalidGrant
	}
	return principalID, nil
}

var _ access.Repository = (*Repository)(nil)
var _ access.PrincipalPreferencesReader = (*Repository)(nil)
var _ access.PrincipalPreferencesWriter = (*Repository)(nil)
var _ access.AuditedPrincipalPreferences = (*Repository)(nil)
var _ access.PrincipalIdentityManagementRepository = (*Repository)(nil)
var _ access.AuthoringAuthRepository = (*Repository)(nil)
var _ avatar.Repository = (*Repository)(nil)
var _ desktopauth.Store = (*Repository)(nil)
