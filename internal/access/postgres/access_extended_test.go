package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/access/desktopauth"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestAccessExtendedPostgreSQL18AuthorityBoundaries(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}

	// A missing principal must not leave an orphaned content-addressed object.
	digest := strings.Repeat("a", 64)
	if _, err := repo.UpsertAvatar(t.Context(), avatarMetadata("00000000-0000-7000-8000-000000000001", digest, 10)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing-principal avatar error = %v", err)
	}
	var objects int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.avatar_object WHERE sha256=$1`, digest).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if objects != 0 {
		t.Fatalf("orphan avatar objects = %d", objects)
	}

	user, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "extended@example.com", Password: "extended avatar password"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.avatar_object(sha256,object_key,media_type,size_bytes) VALUES($1,'avatars/'||$1,'image/png',11)`, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertAvatar(t.Context(), avatarMetadata(user.Principal.ID, digest, 10)); err == nil || !strings.Contains(err.Error(), "conflicts with digest") {
		t.Fatalf("avatar object conflict error = %v", err)
	}
	stored, err := repo.UpsertAvatar(t.Context(), avatarMetadata(user.Principal.ID, strings.Repeat("b", 64), 12))
	if err != nil || stored.SHA256 != strings.Repeat("b", 64) {
		t.Fatalf("avatar upsert = %#v, %v", stored, err)
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal_avatar SET size_bytes=13 WHERE principal_id=$1::uuid AND revoked_at IS NULL`, user.Principal.ID); err == nil {
		t.Fatal("avatar identity rewrite accepted")
	}
	if _, err := repo.SetPrincipalTheme(t.Context(), user.Principal.ID, access.ThemeDark); err != nil {
		t.Fatalf("first theme update: %v", err)
	}
	if _, err := repo.SetPrincipalTheme(t.Context(), user.Principal.ID, access.ThemeLight); err != nil {
		t.Fatalf("second theme update: %v", err)
	}
	var preferenceRows, activePreferences int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE revoked_at IS NULL) FROM access.principal_preferences WHERE principal_id=$1::uuid`, user.Principal.ID).Scan(&preferenceRows, &activePreferences); err != nil {
		t.Fatal(err)
	}
	if preferenceRows != 2 || activePreferences != 1 {
		t.Fatalf("preference history = %d total, %d active", preferenceRows, activePreferences)
	}

	if err := repo.RecordAuditEvent(t.Context(), access.AuditEventInput{Action: "extended.invalid", MetadataJSON: `[]`}); err == nil {
		t.Fatal("array audit metadata accepted")
	}
	if err := repo.RecordAuditEvent(t.Context(), access.AuditEventInput{Action: "extended.valid", MetadataJSON: `{}`}); err != nil {
		t.Fatalf("object audit metadata rejected: %v", err)
	}
	projectRef, err := access.NewResourceRef("project_extended", graph.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	canonical := access.CanonicalAuditEvent{
		Identity:    graph.ServingIdentity{ProjectID: projectIDForTest("project_extended"), Environment: "production", GenerationID: "generation_extended"},
		PrincipalID: user.Principal.ID, Action: "project.read", Resource: projectRef,
		Capability: access.CapabilityProjectAdmin, Status: "denied",
		RequestID: "00000000-0000-7000-8000-000000000010", CorrelationID: "00000000-0000-7000-8000-000000000011", MetadataJSON: `{"z":2,"a":1}`,
	}
	if err := repo.RecordCanonicalAuditEvent(t.Context(), canonical); err != nil {
		t.Fatalf("canonical audit: %v", err)
	}
	var projectID, environment, generationID, principalID, action, capability, outcome, requestID, correlationID, metadata string
	if err := db.admin.QueryRow(t.Context(), `SELECT project_id,environment,generation_id,principal_id::text,action,capability,outcome,request_id::text,correlation_id::text,metadata::text FROM audit.audit_event WHERE action='project.read'`).Scan(&projectID, &environment, &generationID, &principalID, &action, &capability, &outcome, &requestID, &correlationID, &metadata); err != nil {
		t.Fatal(err)
	}
	if projectID != "project_extended" || environment != "production" || generationID != "generation_extended" || principalID != user.Principal.ID || action != "project.read" || capability != access.CapabilityProjectAdmin.String() || outcome != "denied" || requestID == "" || correlationID == "" || metadata != `{"a": 1, "z": 2}` {
		t.Fatalf("canonical audit row = %q/%q/%q/%q/%q/%q/%q/%q/%q/%q", projectID, environment, generationID, principalID, action, capability, outcome, requestID, correlationID, metadata)
	}
	rollbackEvent := canonical
	rollbackEvent.Action = "project.rollback"
	if err := repo.RunAuditedMutation(t.Context(), func(txRepo access.Repository) (access.AuditEventInput, error) {
		if tx, ok := txRepo.(*Repository); ok {
			if err := tx.RecordCanonicalAuditEvent(t.Context(), rollbackEvent); err != nil {
				return access.AuditEventInput{}, err
			}
		}
		return access.AuditEventInput{Action: "project.rollback"}, errors.New("force rollback")
	}); err == nil {
		t.Fatal("rollback mutation unexpectedly committed")
	}
	var rollbackCount int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='project.rollback'`).Scan(&rollbackCount); err != nil {
		t.Fatal(err)
	}
	if rollbackCount != 0 {
		t.Fatalf("rolled-back canonical rows = %d", rollbackCount)
	}
	var missingCodeHash [32]byte
	missingCodeHash[0] = 1
	if err := repo.StoreAuthorizationCode(t.Context(), desktopauth.AuthorizationCode{
		CodeHash: missingCodeHash, PrincipalID: "00000000-0000-7000-8000-000000000002", ClientID: desktopauth.DesktopClientID,
		InstanceID: "instance_extended", ProfileID: "profile_extended", RedirectURI: "http://127.0.0.1/callback",
		CodeChallenge: strings.Repeat("A", 43), ReturnPath: "/", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing-principal desktop code error = %v", err)
	}

	initial, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{
		Email: "bootstrap@example.com", Environment: "production", Now: time.Unix(1, 0).UTC(),
	}, nil)
	if err != nil {
		t.Fatalf("initialize instance: %v", err)
	}
	if initial.PublisherToken == "" || !initial.PublisherTokenExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("initial credentials = %#v", initial)
	}
	var marker string
	if err := db.admin.QueryRow(t.Context(), `SELECT value FROM access.platform_setting WHERE key=$1`, access.InstanceInitializedSetting).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	markerTime, err := time.Parse(time.RFC3339Nano, marker)
	if err != nil || markerTime.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("database initialization marker = %q (%v)", marker, err)
	}
	if initialized, err := repo.Initialized(t.Context()); err != nil || !initialized {
		t.Fatalf("repository initialization marker lookup = %t (%v)", initialized, err)
	}
	if _, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{Email: "second@example.com", Environment: "production", Now: time.Now()}, nil); !errors.Is(err, access.ErrInstanceAlreadyInitialized) {
		t.Fatalf("second initialization error = %v", err)
	}

	projectGraphID, err := graph.NewResourceID("project_extended")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deviceHash := hashHex("device-extended")
	userHash := hashHex("user-extended")
	if err := repo.CreateDeviceAuthorization(t.Context(), access.DeviceAuthorization{
		ID: "da_extended", ClientID: access.AuthoringCLIClientID,
		DeviceCodeHash: deviceHash, UserCodeHash: userHash,
		Scope:  access.AuthoringScope{TargetID: "instance_extended", ProjectID: projectGraphID, Capabilities: []access.Capability{access.CapabilityResourceRead}},
		Status: access.DeviceAuthorizationPending, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute), PollInterval: time.Second,
	}); err != nil {
		t.Fatalf("create device authorization: %v", err)
	}
	if err := repo.ApproveDeviceAuthorization(t.Context(), "da_extended", user.Principal.ID, time.Unix(1, 0)); err != nil {
		t.Fatalf("approve device authorization: %v", err)
	}
	accessHash := hashHex("access-extended")
	refreshHash := hashHex("refresh-extended")
	credential, err := repo.IssueDeviceCredential(t.Context(), access.DeviceCredentialIssue{
		DeviceCodeHash: deviceHash, ClientID: access.AuthoringCLIClientID, Now: time.Unix(1, 0),
		SessionID: "as_extended", CredentialID: "ac_extended", AccessTokenHash: accessHash, RefreshTokenHash: refreshHash,
		AccessExpiresAt: now.Add(15 * time.Minute), RefreshExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("issue device credential with stale caller time: %v", err)
	}
	if credential.Session.CreatedAt.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("session creation time was not database-owned: %v", credential.Session.CreatedAt)
	}

	rotated, err := repo.RotateAuthoringCredential(t.Context(), access.AuthoringCredentialRotation{
		RefreshTokenHash: refreshHash, Now: time.Unix(1, 0), CredentialID: "ac_extended_next",
		AccessTokenHash: hashHex("access-extended-next"), RefreshTokenHashNew: hashHex("refresh-extended-next"),
		AccessExpiresAt: now.Add(30 * time.Minute), RefreshExpiresAt: now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("rotate credential with stale caller time: %v", err)
	}
	if rotated.Session.CreatedAt != credential.Session.CreatedAt {
		t.Fatalf("rotation changed database session creation time: %v -> %v", credential.Session.CreatedAt, rotated.Session.CreatedAt)
	}
	if _, err := repo.AuthoringCredentialByAccessTokenHash(t.Context(), hashHex("access-extended-next"), time.Unix(1, 0)); err != nil {
		t.Fatalf("resolve rotated credential with stale caller time: %v", err)
	}
	replay := access.AuthoringCredentialRotation{
		RefreshTokenHash: refreshHash, CredentialID: "ac_extended_replay",
		AccessTokenHash: hashHex("access-extended-replay"), RefreshTokenHashNew: hashHex("refresh-extended-replay"),
		AccessExpiresAt: now.Add(45 * time.Minute), RefreshExpiresAt: now.Add(3 * time.Hour),
	}
	if _, err := repo.RotateAuthoringCredential(t.Context(), replay); !errors.Is(err, access.ErrAuthoringRefreshReplay) {
		t.Fatalf("old refresh replay error = %v", err)
	}
	var revoked bool
	var replayAudits int
	if err := db.admin.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM access.authoring_session WHERE id='as_extended'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='authoring.refresh.replay' AND resource_id='as_extended'`).Scan(&replayAudits); err != nil {
		t.Fatal(err)
	}
	if !revoked || replayAudits != 1 {
		t.Fatalf("replay containment revoked=%t audit_count=%d", revoked, replayAudits)
	}
	if _, err := repo.RotateAuthoringCredential(t.Context(), replay); !errors.Is(err, access.ErrAuthoringRefreshReplay) {
		t.Fatalf("contained refresh replay error = %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='authoring.refresh.replay' AND resource_id='as_extended'`).Scan(&replayAudits); err != nil {
		t.Fatal(err)
	}
	if replayAudits != 1 {
		t.Fatalf("idempotent replay audit count=%d, want 1", replayAudits)
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.device_authorization SET consumed_at=NULL WHERE id='da_extended'`); err == nil {
		t.Fatal("device consumption rewind accepted")
	}
}

func TestAccessExtendedPostgreSQL18SnapshotAndPublicationAdapters(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_adapter", Kind: graph.KindProject, Name: "adapter"},
		{ID: "dashboard_adapter", Kind: graph.KindDashboard, Name: "dashboard"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := graph.ServingIdentity{ProjectID: "project_adapter", Environment: "production", GenerationID: "generation_adapter"}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("dashboard_adapter", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := access.NewCanonicalGrant(project, subject, resource, access.CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{{ID: "grant_adapter", Canonical: grant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthorizationSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("install authorization snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var snapshots, grants int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM access.authorization_snapshot WHERE project_id=$1 AND environment=$2 AND generation_id=$3`, identity.ProjectID.String(), identity.Environment, identity.GenerationID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM access.authorization_grant WHERE project_id=$1 AND environment=$2 AND generation_id=$3`, identity.ProjectID.String(), identity.Environment, identity.GenerationID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || grants != 1 {
		t.Fatalf("installed snapshot rows = %d snapshots, %d grants", snapshots, grants)
	}
	// A transaction rollback leaves no partial snapshot or child rows.
	rollbackIdentity := identity
	rollbackIdentity.GenerationID = "generation_rollback"
	rollbackSnapshot, err := accesssnapshot.NewAuthorizationSnapshot(rollbackIdentity, project, []accesssnapshot.Grant{{ID: "grant_rollback", Canonical: grant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthorizationSnapshotTx(ctx, tx, rollbackSnapshot); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("install rollback snapshot: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM access.authorization_snapshot WHERE project_id=$1 AND generation_id=$2`, rollbackIdentity.ProjectID.String(), rollbackIdentity.GenerationID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatalf("rolled-back snapshot rows = %d", snapshots)
	}
	// Replaying the exact immutable identity is idempotent.
	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthorizationSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("replay authorization snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Snapshot evidence is immutable: even an administrative writer cannot
	// replace its digest after publication.
	if _, err := db.admin.Exec(ctx, `UPDATE access.authorization_snapshot SET digest=$1 WHERE project_id=$2 AND environment=$3 AND generation_id=$4`, "sha256:"+strings.Repeat("a", 64), identity.ProjectID.String(), identity.Environment, identity.GenerationID); err == nil {
		t.Fatal("authorization snapshot digest tamper unexpectedly succeeded")
	}
	// Reusing an immutable identity with a different digest is rejected.
	conflictGrant, err := access.NewCanonicalGrant(project, subject, resource, access.CapabilityResourceEdit)
	if err != nil {
		t.Fatal(err)
	}
	conflictSnapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{{ID: "grant_conflict", Canonical: conflictGrant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthorizationSnapshotTx(ctx, tx, conflictSnapshot); !errors.Is(err, ErrAuthorizationSnapshotIdentityConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("conflicting snapshot error = %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateDashboardPublicationPrincipalTx(ctx, tx, project.ProjectID(), "public"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("activate dashboard publication principal: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	publicationID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("dashboard_publication:"+project.ProjectID().String()+".public")).String()
	var principalKind string
	if err := db.admin.QueryRow(ctx, `SELECT principal_type FROM access.principal WHERE id=$1::uuid`, publicationID).Scan(&principalKind); err != nil {
		t.Fatal(err)
	}
	if principalKind != "dashboard_publication" {
		t.Fatalf("publication principal type = %q", principalKind)
	}
	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateDashboardPublicationPrincipalTx(ctx, tx, project.ProjectID(), strings.Repeat("x", 513)); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("oversized publication name accepted")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := any(db.runtime).(Tx); ok {
		t.Fatal("pgx pool unexpectedly satisfied transaction-only Tx boundary")
	}
}

func avatarMetadata(principalID, digest string, size int64) avatar.Metadata {
	return avatar.Metadata{PrincipalID: principalID, SHA256: digest, MediaType: "image/png", SizeBytes: size, Width: 256, Height: 256}
}

func hashHex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func projectIDForTest(value string) graph.ResourceID {
	id, _ := graph.NewResourceID(value)
	return id
}
