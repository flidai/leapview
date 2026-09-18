package postgres

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	durableGrantTestInstance = "instance_0123456789abcdef0123456789abcdef"
	durableGrantTestProject  = "durable_grants_test"
	durableGrantTestUID      = "00000000-0000-7000-8000-000000000101"
	durableGrantTestUID2     = "00000000-0000-7000-8000-000000000102"
	durableGrantIssuer       = "00000000-0000-7000-8000-000000000110"
	durableGrantRecipient    = "00000000-0000-7000-8000-000000000111"
	durableGrantExecution    = "00000000-0000-7000-8000-000000000112"
	durableGrantGroup        = "00000000-0000-7000-8000-000000000113"
	durableGrantTokenDigest  = "0123456789abcdef0123456789abcdef"
)

func installDurableGrantMigration(t *testing.T, db auditDatabase) {
	t.Helper()
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `
		CREATE SCHEMA project;
		CREATE TABLE project.resource_uid_registry (
			instance_id text NOT NULL,
			project_id text NOT NULL,
			resource_uid uuid NOT NULL,
			authored_resource_id text NOT NULL,
			resource_kind text NOT NULL,
			state text NOT NULL,
			UNIQUE (instance_id, project_id, resource_uid)
		);
		GRANT USAGE ON SCHEMA project TO leapview_control_owner, leapview_control_migrator, leapview_control_runtime, leapview_control_readonly;
		GRANT USAGE, CREATE ON SCHEMA access TO leapview_control_owner;
		GRANT ALL ON project.resource_uid_registry TO leapview_control_owner, leapview_control_migrator;
		GRANT SELECT ON project.resource_uid_registry TO leapview_control_runtime, leapview_control_readonly;
		GRANT SELECT, REFERENCES ON access.principal, access.access_group, access.session, access.api_token TO leapview_control_owner;
		GRANT EXECUTE ON FUNCTION access.valid_permission_pairs(text, jsonb) TO leapview_control_owner;
	`); err != nil {
		t.Fatalf("create resource UID fixture: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `
		INSERT INTO project.resource_uid_registry(instance_id, project_id, resource_uid, authored_resource_id, resource_kind, state)
		VALUES ($1,$2,$3::uuid,'dashboard_test','dashboard','active'),
		       ($1,$2,$4::uuid,'pipeline_test','pipeline','active')
	`, durableGrantTestInstance, durableGrantTestProject, durableGrantTestUID, durableGrantTestUID2); err != nil {
		t.Fatalf("seed resource UID fixture: %v", err)
	}
	parts := make([]string, 0, 2)
	for _, migrationName := range []string{"026_durable_authority_grants.sql", "027_resource_share_no_onward_delegation.sql"} {
		contents, readErr := fs.ReadFile(platformmigrations.MigrationFS(), migrationName)
		if readErr != nil {
			t.Fatalf("read %s: %v", migrationName, readErr)
		}
		up := string(contents)
		if down := strings.Index(up, "\n-- +goose Down"); down >= 0 {
			up = up[:down]
		}
		parts = append(parts, up)
	}
	up := strings.Join(parts, "\n")
	conn, err := db.admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		t.Fatalf("set migration role: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply migration 026: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit migration 026: %v", err)
	}
}

func seedDurableGrantPrincipals(t *testing.T, db auditDatabase) {
	t.Helper()
	for _, item := range []struct{ id, email string }{
		{durableGrantIssuer, "durable-grant-issuer@example.com"},
		{durableGrantRecipient, "durable-grant-recipient@example.com"},
		{durableGrantExecution, "durable-grant-execution@example.com"},
	} {
		if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.principal(id, principal_type, status, email, display_name) VALUES ($1::uuid,'user','active',$2,$2)`, item.id, item.email); err != nil {
			t.Fatalf("seed principal %s: %v", item.id, err)
		}
	}
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.access_group(id, name, provider) VALUES ($1::uuid,'durable-grant-group','local')`, durableGrantGroup); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func durableGrantPostgresInput(t *testing.T, issuer access.GrantIssuerEvidence, target access.DurableGrantTarget, recipient access.SubjectRef, idempotency string, issued, expires time.Time, permissions, ceiling []access.PermissionPair) access.ResourceShareGrantInput {
	t.Helper()
	return access.ResourceShareGrantInput{
		Target: target, Issuer: issuer, Recipient: recipient, Permissions: permissions, IssuancePermissions: ceiling,
		IssuedAt: issued, ExpiresAt: expires, IdempotencyKey: idempotency,
	}
}

func TestDurableGrantPostgreSQLResourceShareLifecycle(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	installDurableGrantMigration(t, db)
	seedDurableGrantPrincipals(t, db)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	target := access.DurableGrantTarget{InstanceID: durableGrantTestInstance, ProjectID: durableGrantTestProject, ResourceUID: durableGrantTestUID, ResourceID: "dashboard_test", ResourceKind: projectgraph.KindDashboard}
	resource, err := access.NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	share, err := access.NewExactPermissionPair(access.ActionResourceShare, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionDashboardRead, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{
		PrincipalID: durableGrantIssuer, Name: "durable-grant-issuer", Permissions: []access.PermissionPair{share, read}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create typed issuer token: %v", err)
	}
	issuer := access.GrantIssuerEvidence{PrincipalID: durableGrantIssuer, Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: token.ID, Fingerprint: token.TokenFingerprint}}
	issued := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	expires := issued.Add(time.Hour)
	principal, err := access.NewSubjectRef(access.SubjectKindPrincipal, durableGrantRecipient)
	if err != nil {
		t.Fatal(err)
	}
	input := durableGrantPostgresInput(t, issuer, target, principal, "share-replay", issued, expires, []access.PermissionPair{read}, []access.PermissionPair{share, read})
	onward := input
	onward.IdempotencyKey = "share-onward-rejected"
	onward.AllowOnwardDelegation = true
	if _, err := repo.CreateResourceShareGrant(t.Context(), onward); !errors.Is(err, access.ErrGrantNoOnwardDelegation) {
		t.Fatalf("repository onward-delegating share error = %v, want ErrGrantNoOnwardDelegation", err)
	}
	grant, err := repo.CreateResourceShareGrant(t.Context(), input)
	if err != nil {
		t.Fatalf("create resource share: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `ALTER TABLE access.resource_share_grant DISABLE TRIGGER resource_share_grant_immutable`); err != nil {
		t.Fatalf("disable immutable trigger for schema check: %v", err)
	}
	_, schemaErr := db.admin.Exec(t.Context(), `UPDATE access.resource_share_grant SET allow_onward_delegation=true WHERE id=$1`, grant.ID)
	if _, err := db.admin.Exec(t.Context(), `ALTER TABLE access.resource_share_grant ENABLE TRIGGER resource_share_grant_immutable`); err != nil {
		t.Fatalf("restore immutable trigger after schema check: %v", err)
	}
	var pgErr *pgconn.PgError
	if schemaErr == nil || !errors.As(schemaErr, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("resource-share onward schema error = %v, want check violation", schemaErr)
	}
	replayed, err := repo.CreateResourceShareGrant(t.Context(), input)
	if err != nil {
		t.Fatalf("idempotent resource share replay: %v", err)
	}
	if replayed.ID != grant.ID || replayed.Fingerprint != grant.Fingerprint {
		t.Fatalf("replay changed grant identity: first=%#v replay=%#v", grant, replayed)
	}
	group, err := access.NewSubjectRef(access.SubjectKindGroup, durableGrantGroup)
	if err != nil {
		t.Fatal(err)
	}
	conflict := durableGrantPostgresInput(t, issuer, target, group, "share-replay", issued, expires, []access.PermissionPair{read}, []access.PermissionPair{share, read})
	if _, err := repo.CreateResourceShareGrant(t.Context(), conflict); !errors.Is(err, access.ErrGrantIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	groupGrant, err := repo.CreateResourceShareGrant(t.Context(), durableGrantPostgresInput(t, issuer, target, group, "share-group", issued, expires, []access.PermissionPair{read}, []access.PermissionPair{share, read}))
	if err != nil {
		t.Fatalf("create group share: %v", err)
	}
	if got, err := repo.CurrentResourceShareGrant(t.Context(), groupGrant.ID, durableGrantGroup); err != nil || got.Recipient.Kind != access.SubjectKindGroup {
		t.Fatalf("resolve group share: grant=%#v err=%v", got, err)
	}
	// UID mismatch is a domain error, not a raw foreign-key escape.
	wrongUID := input
	wrongUID.IdempotencyKey = "share-wrong-uid"
	wrongUID.Target.ResourceUID = "00000000-0000-7000-8000-000000000199"
	if _, err := repo.CreateResourceShareGrant(t.Context(), wrongUID); !errors.Is(err, access.ErrGrantResourceUIDMismatch) {
		t.Fatalf("wrong resource UID error = %v", err)
	}
	// Issuer loss does not retroactively revoke an immutable ordinary share.
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp() WHERE id=$1::uuid`, durableGrantIssuer); err != nil {
		t.Fatalf("disable issuer: %v", err)
	}
	if _, err := repo.CurrentResourceShareGrant(t.Context(), grant.ID, durableGrantRecipient); err != nil {
		t.Fatalf("issuer loss revoked ordinary share: %v", err)
	}
	if err := repo.RevokeResourceShareGrant(t.Context(), grant.ID, durableGrantRecipient, "security review"); err != nil {
		t.Fatalf("revoke share: %v", err)
	}
	if _, err := repo.CurrentResourceShareGrant(t.Context(), grant.ID, durableGrantRecipient); !errors.Is(err, access.ErrGrantRevoked) {
		t.Fatalf("revoked share resolution error = %v", err)
	}
	// A registry tombstone makes the exact old UID unusable even though the
	// authored resource ID remains recognizable.
	if _, err := db.admin.Exec(t.Context(), `UPDATE project.resource_uid_registry SET state='tombstoned' WHERE resource_uid=$1::uuid`, durableGrantTestUID); err != nil {
		t.Fatalf("tombstone resource: %v", err)
	}
	if _, err := repo.CurrentResourceShareGrant(t.Context(), groupGrant.ID, durableGrantGroup); !errors.Is(err, access.ErrGrantResourceInactive) {
		t.Fatalf("tombstoned UID resolution error = %v", err)
	}
	// A privileged fixture mutation simulates a damaged legacy row. The read
	// path must reject it rather than projecting an untyped permission grant.
	var permissionConstraint string
	if err := db.admin.QueryRow(t.Context(), `SELECT conname FROM pg_constraint WHERE conrelid='access.resource_share_grant'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%valid_permission_pairs%' LIMIT 1`).Scan(&permissionConstraint); err != nil {
		t.Fatalf("find permission constraint: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `ALTER TABLE access.resource_share_grant DROP CONSTRAINT `+permissionConstraint); err != nil {
		t.Fatalf("drop permission constraint: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `ALTER TABLE access.resource_share_grant DISABLE TRIGGER resource_share_grant_immutable`); err != nil {
		t.Fatalf("disable immutable trigger: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.resource_share_grant SET permissions='{"tampered":true}'::jsonb WHERE id=$1`, grant.ID); err != nil {
		t.Fatalf("tamper durable share fixture: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `ALTER TABLE access.resource_share_grant ENABLE TRIGGER resource_share_grant_immutable`); err != nil {
		t.Fatalf("restore immutable trigger: %v", err)
	}
	if _, err := repo.ResourceShareGrant(t.Context(), grant.ID); !errors.Is(err, access.ErrInvalidDurableGrant) {
		t.Fatalf("malformed persisted permissions error = %v", err)
	}
}

func TestDurableGrantPostgreSQLExecutionEvidenceAndMutationGuard(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	installDurableGrantMigration(t, db)
	seedDurableGrantPrincipals(t, db)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	target := access.DurableGrantTarget{InstanceID: durableGrantTestInstance, ProjectID: durableGrantTestProject, ResourceUID: durableGrantTestUID2, ResourceID: "pipeline_test", ResourceKind: projectgraph.KindPipeline}
	resource, err := access.NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	run, err := access.NewExactPermissionPair(access.ActionPipelineRun, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewExactPermissionPair(access.ActionWorkloadDelegate, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: durableGrantIssuer, Name: "execution-issuer", Permissions: []access.PermissionPair{run, delegate}, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	issuer := access.GrantIssuerEvidence{PrincipalID: durableGrantIssuer, Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: token.ID, Fingerprint: token.TokenFingerprint}}
	in := access.ExecutionGrantInput{Target: target, Issuer: issuer, ExecutionPrincipalID: durableGrantExecution, Permissions: []access.PermissionPair{run}, IssuancePermissions: []access.PermissionPair{run, delegate}, WorkflowID: "workflow", WorkflowRevision: "revision-1", ClosureDigest: "sha256:" + strings.Repeat("a", 64), BindingDigest: "sha256:" + strings.Repeat("b", 64), DestinationDigest: "sha256:" + strings.Repeat("c", 64), TriggerDigest: "sha256:" + strings.Repeat("d", 64), IssuedAt: time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond), ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), IdempotencyKey: "execution-1"}
	grant, err := repo.CreateExecutionGrant(t.Context(), in)
	if err != nil {
		t.Fatalf("create execution grant: %v", err)
	}
	current, err := repo.CurrentExecutionGrant(t.Context(), grant.ID, durableGrantExecution)
	if err != nil || current.ClosureDigest != in.ClosureDigest || current.BindingDigest != in.BindingDigest || current.DestinationDigest != in.DestinationDigest || current.TriggerDigest != in.TriggerDigest {
		t.Fatalf("execution evidence resolution: grant=%#v err=%v", current, err)
	}
	selected, err := repo.SelectCurrentExecutionGrantID(t.Context(), target.InstanceID, target.ProjectID, target.ResourceID)
	if err != nil || selected != grant.ID {
		t.Fatalf("current execution grant selection: id=%q err=%v, want %q", selected, err, grant.ID)
	}
	if _, err := db.runtime.Exec(t.Context(), `UPDATE access.execution_grant SET closure_digest=$2 WHERE id=$1`, grant.ID, "sha256:"+strings.Repeat("e", 64)); err == nil {
		t.Fatal("execution closure drift unexpectedly mutated immutable grant")
	}
	// Expiry is evaluated at use, while revocation is a monotonic mutation.
	expired := in
	expired.ID = "execution-expired"
	expired.IdempotencyKey = "execution-expired"
	expired.IssuedAt = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	expired.ExpiresAt = expired.IssuedAt.Add(time.Minute)
	expired.ClosureDigest = "sha256:" + strings.Repeat("f", 64)
	old, err := repo.CreateExecutionGrant(t.Context(), expired)
	if err != nil {
		t.Fatalf("create expired execution grant: %v", err)
	}
	if _, err := repo.CurrentExecutionGrant(t.Context(), old.ID, durableGrantExecution); !errors.Is(err, access.ErrGrantExpired) {
		t.Fatalf("expired execution resolution error = %v", err)
	}
}
