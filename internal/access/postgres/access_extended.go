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
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/jackc/pgx/v5"
)

var ErrAuthorizationSnapshotIdentityConflict = errors.New("authorization snapshot identity already installed with a different digest")

func (r *Repository) InsertPlatformSettingIfMissing(ctx context.Context, key, value string) (bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return false, err
	}
	tag, err := accessdb.New(db).InsertPlatformSetting(ctx, accessdb.InsertPlatformSettingParams{Key: strings.TrimSpace(key), Value: value})
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
	principalID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(tx).RevokePrincipal(ctx, principalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if err = accessdb.New(tx).RevokePrincipalSessions(ctx, principalID); err != nil {
		return err
	}
	if err = accessdb.New(tx).RevokePrincipalTokens(ctx, principalID); err != nil {
		return err
	}
	if err = accessdb.New(tx).RevokePrincipalSecrets(ctx, principalID); err != nil {
		return err
	}
	if err = accessdb.New(tx).RevokePrincipalAuthoringSessions(ctx, principalID); err != nil {
		return err
	}
	_ = accessdb.New(tx).RevokePrincipalGroups(ctx, principalID)
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
	principalID, err := pgUUID(id)
	if err != nil {
		return access.Principal{}, err
	}
	if disabled {
		// Provisioned and administrator disables have separate call sites but
		// share the same durable state transition. A block is represented by
		// blocked_at; an external lifecycle disable by disabled_at.
		if provisioned {
			tag, err = accessdb.New(tx).DisablePrincipal(ctx, principalID)
		} else {
			tag, err = accessdb.New(tx).BlockPrincipal(ctx, principalID)
		}
	} else {
		tag, err = accessdb.New(tx).EnablePrincipal(ctx, principalID)
	}
	if err != nil {
		return access.Principal{}, err
	}
	if tag.RowsAffected() == 0 {
		return access.Principal{}, pgx.ErrNoRows
	}
	if disabled {
		if err = accessdb.New(tx).RevokePrincipalSessions(ctx, principalID); err != nil {
			return access.Principal{}, err
		}
		if err = accessdb.New(tx).RevokePrincipalTokens(ctx, principalID); err != nil {
			return access.Principal{}, err
		}
		if err = accessdb.New(tx).RevokePrincipalSecrets(ctx, principalID); err != nil {
			return access.Principal{}, err
		}
		if err = accessdb.New(tx).RevokePrincipalAuthoringSessions(ctx, principalID); err != nil {
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
	parsedID, err := pgUUID(principalID)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListServiceSecrets(ctx, accessdb.ListServiceSecretsParams{PrincipalID: parsedID, PageSize: maxPageSize})
	if err != nil {
		return nil, err
	}
	out := make([]access.ServicePrincipalSecret, 0, len(rows))
	for _, row := range rows {
		value := access.ServicePrincipalSecret{ID: principalUUID(row.ID), ServicePrincipalID: principalUUID(row.ServicePrincipalID), Name: row.Name,
			ExpiresAt: principalTimestamp(row.ExpiresAt), CreatedAt: principalTimestamp(row.CreatedAt), RevokedAt: principalTimestamp(row.RevokedAt)}
		out = append(out, value)
	}
	return out, nil
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
	parsedSecretID, err := pgUUID(secretID)
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	parsedPrincipalID, err := pgUUID(principalID)
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	row, err := accessdb.New(db).GetServiceSecretForPrincipal(ctx, accessdb.GetServiceSecretForPrincipalParams{ID: parsedSecretID, PrincipalID: parsedPrincipalID})
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	return access.ServicePrincipalSecret{ID: principalUUID(row.ID), ServicePrincipalID: principalUUID(row.ServicePrincipalID), Name: row.Name,
		ExpiresAt: principalTimestamp(row.ExpiresAt), CreatedAt: principalTimestamp(row.CreatedAt), RevokedAt: principalTimestamp(row.RevokedAt)}, nil
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	row, err := accessdb.New(db).GetPrincipalPreferences(ctx, parsedID)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	parsed, ok := access.ParseThemeMode(row.Theme)
	if !ok {
		return access.PrincipalPreferences{}, fmt.Errorf("unsupported stored theme %q", row.Theme)
	}
	return access.PrincipalPreferences{PrincipalID: id, Theme: parsed, UpdatedAt: principalTimestamp(row.UpdatedAt)}, nil
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	if err = accessdb.New(tx).RevokePrincipalPreferences(ctx, parsedID); err != nil {
		return access.PrincipalPreferences{}, err
	}
	if _, err = accessdb.New(tx).InsertPrincipalPreferences(ctx, accessdb.InsertPrincipalPreferencesParams{PrincipalID: parsedID, Theme: string(theme)}); err != nil {
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return err
	}
	if err = accessdb.New(tx).RevokePrincipalPreferences(ctx, parsedID); err != nil {
		return err
	}
	tag, err := accessdb.New(tx).InsertPrincipalPreferences(ctx, accessdb.InsertPrincipalPreferencesParams{PrincipalID: parsedID, Theme: string(theme)})
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return avatar.Metadata{}, err
	}
	row, err := accessdb.New(db).GetAvatar(ctx, parsedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return avatar.Metadata{}, avatar.ErrNotFound
	}
	value := avatar.Metadata{PrincipalID: principalUUID(row.PrincipalID), SHA256: row.Sha256, MediaType: row.MediaType,
		SizeBytes: row.SizeBytes, Width: int(row.Width), Height: int(row.Height), UpdatedAt: principalTimestamp(row.UpdatedAt)}
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return avatar.Metadata{}, err
	}
	principalExists, err := accessdb.New(tx).AvatarPrincipalExists(ctx, parsedID)
	if err != nil {
		return avatar.Metadata{}, err
	}
	if !principalExists {
		return avatar.Metadata{}, pgx.ErrNoRows
	}
	objectTag, err := accessdb.New(tx).InsertAvatarObject(ctx, accessdb.InsertAvatarObjectParams{Sha256: sha, SizeBytes: value.SizeBytes})
	if err != nil {
		return avatar.Metadata{}, err
	}
	if objectTag.RowsAffected() == 0 {
		object, getErr := accessdb.New(tx).GetAvatarObject(ctx, sha)
		if getErr != nil {
			return avatar.Metadata{}, getErr
		}
		if object.ObjectKey != "avatars/"+sha || object.MediaType != "image/png" || object.SizeBytes != value.SizeBytes {
			return avatar.Metadata{}, fmt.Errorf("avatar object metadata conflicts with digest %s", sha)
		}
	}
	if _, err = accessdb.New(tx).RevokePrincipalAvatar(ctx, parsedID); err != nil {
		return avatar.Metadata{}, err
	}
	if err = accessdb.New(tx).InsertPrincipalAvatar(ctx, accessdb.InsertPrincipalAvatarParams{PrincipalID: parsedID, Sha256: sha, SizeBytes: value.SizeBytes}); err != nil {
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).RevokePrincipalAvatar(ctx, parsedID)
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
	tag, err := accessdb.New(tx).InsertAuthorizationSnapshot(ctx, accessdb.InsertAuthorizationSnapshotParams{ProjectID: projectID, Environment: environment, GenerationID: generation, Digest: digest})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		installed, getErr := accessdb.New(tx).GetAuthorizationSnapshotDigest(ctx, accessdb.GetAuthorizationSnapshotDigestParams{ProjectID: projectID, Environment: environment, GenerationID: generation})
		if getErr != nil {
			return getErr
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
		if err = accessdb.New(tx).InsertAuthorizationRoleBinding(ctx, accessdb.InsertAuthorizationRoleBindingParams{ID: binding.ID, ProjectID: projectID,
			Environment: environment, GenerationID: generation, SubjectKind: string(binding.Subject.Kind), SubjectID: binding.Subject.ID,
			Role: string(binding.Role), Capabilities: caps, Name: binding.Name}); err != nil {
			return err
		}
	}
	for _, grant := range snapshot.Grants() {
		canonical := grant.Canonical
		if err = accessdb.New(tx).InsertAuthorizationGrant(ctx, accessdb.InsertAuthorizationGrantParams{ID: grant.ID, ProjectID: projectID,
			Environment: environment, GenerationID: generation, SubjectKind: string(canonical.Subject().Kind), SubjectID: canonical.Subject().ID,
			ResourceID: canonical.Resource().ID().String(), ResourceKind: string(canonical.Resource().Kind()), Capability: canonical.Capability().String(), Name: grant.Name}); err != nil {
			return err
		}
	}
	for _, policy := range snapshot.DataPolicies() {
		var subjectKind, subjectID *string
		if policy.Subject != nil {
			kind, value := string(policy.Subject.Kind), policy.Subject.ID
			subjectKind, subjectID = &kind, &value
		}
		if !json.Valid([]byte(policy.ExpressionJSON)) {
			return fmt.Errorf("data policy %q expression is invalid JSON", policy.ID)
		}
		if err = accessdb.New(tx).InsertAuthorizationDataPolicy(ctx, accessdb.InsertAuthorizationDataPolicyParams{ID: policy.ID, ProjectID: projectID,
			Environment: environment, GenerationID: generation, ResourceID: policy.Resource.ID().String(), ResourceKind: string(policy.Resource.Kind()),
			SubjectKind: subjectKind, SubjectID: subjectID, PolicyType: policy.PolicyType, Expression: []byte(policy.ExpressionJSON)}); err != nil {
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
	outcome := strings.TrimSpace(event.Status)
	if outcome == "" {
		outcome = "success"
	}
	if outcome != "success" && outcome != "failure" && outcome != "denied" {
		return fmt.Errorf("audit outcome %q is invalid", outcome)
	}
	intentDigest := canonicalAuditDigest(event, metadata)
	idString, err := newUUID()
	if err != nil {
		return err
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	parsedPrincipalID, err := pgUUID(principalID)
	if err != nil {
		return err
	}
	id, err := pgUUID(idString)
	if err != nil {
		return err
	}
	resourceKind := string(event.Resource.Kind())
	resourceID := event.Resource.ID().String()
	projectID := event.Identity.ProjectID.String()
	environment := event.Identity.Environment
	generationID := event.Identity.GenerationID
	err = accessdb.New(db).RecordCanonicalAudit(ctx, accessdb.RecordCanonicalAuditParams{AuditID: id, PrincipalID: parsedPrincipalID,
		Action: event.Action, ResourceKind: &resourceKind, ResourceID: &resourceID, ProjectID: &projectID, Environment: &environment,
		GenerationID: &generationID, Capability: event.Capability.String(), Outcome: outcome, RequestID: auditNullableTextPointer(event.RequestID),
		CorrelationID: auditNullableTextPointer(event.CorrelationID), AggregateKey: resourceID, IntentDigest: intentDigest, Metadata: []byte(metadata)})
	return err
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
	principalID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).CreateDesktopAuthorizationCode(ctx, accessdb.CreateDesktopAuthorizationCodeParams{CodeHash: code.CodeHash[:], PrincipalID: principalID,
		ClientID: code.ClientID, InstanceID: code.InstanceID, ProfileID: code.ProfileID, RedirectUri: code.RedirectURI,
		CodeChallenge: code.CodeChallenge, ReturnPath: code.ReturnPath, Ttl: pgInterval(ttl)})
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
	row, err := accessdb.New(tx).FindAuthorizationCode(ctx, hash[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return "", desktopauth.ErrInvalidGrant
	}
	if err != nil {
		return "", err
	}
	grant.CodeHash = hash
	grant.PrincipalID, grant.ClientID, grant.InstanceID, grant.ProfileID = principalUUID(row.PrincipalID), row.ClientID, row.InstanceID, row.ProfileID
	grant.RedirectURI, grant.CodeChallenge, grant.ReturnPath = row.RedirectUri, row.CodeChallenge, row.ReturnPath
	grant.ExpiresAt, grant.CreatedAt = row.ExpiresAt.Time.UTC(), row.CreatedAt.Time.UTC()
	grant.Consumed = row.ConsumedAt.Valid
	if !validate(grant) || grant.Consumed {
		return "", desktopauth.ErrInvalidGrant
	}
	tag, err := accessdb.New(tx).ConsumeAuthorizationCode(ctx, hash[:])
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return "", err
		}
		return "", desktopauth.ErrInvalidGrant
	}
	return grant.PrincipalID, nil
}

var _ access.Repository = (*Repository)(nil)
var _ access.PrincipalPreferencesReader = (*Repository)(nil)
var _ access.PrincipalPreferencesWriter = (*Repository)(nil)
var _ access.AuditedPrincipalPreferences = (*Repository)(nil)
var _ access.PrincipalIdentityManagementRepository = (*Repository)(nil)
var _ access.AuthoringAuthRepository = (*Repository)(nil)
var _ avatar.Repository = (*Repository)(nil)
var _ desktopauth.Store = (*Repository)(nil)
