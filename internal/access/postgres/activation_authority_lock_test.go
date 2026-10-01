package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCredentialActivationHoldsRevocationLocksThroughCallerRollback(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	fixture := newActivationAuthorityFixture(t, db, 40, access.SubjectKindGroup, false,
		[]access.PermissionPair{mustActivationPair(t, 40, access.ActionConnectionManage), mustActivationPair(t, 40, access.ActionConnectionUse), mustActivationPair(t, 40, access.ActionConnectionRead)},
		[]access.PermissionPair{mustActivationPair(t, 40, access.ActionConnectionManage), mustActivationPair(t, 40, access.ActionConnectionUse), mustActivationPair(t, 40, access.ActionConnectionRead)})
	ctx := t.Context()
	caller, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Rollback(context.Background())
	issuer := access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}
	if err := fixture.repo.AuthorizeCredentialActivationTx(ctx, caller, fixture.scope, fixture.snapshot, issuer, []access.PermissionPair{fixture.manage, fixture.use}); err != nil {
		t.Fatalf("session authority check failed: %v", err)
	}
	for _, mutation := range []struct {
		name string
		sql  string
		args []any
	}{
		{"principal disable", `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp() WHERE id=$1::uuid`, []any{fixture.principal.ID}},
		{"policy update", `UPDATE access.authorization_policy SET revision=revision+1 WHERE target_id=$1 AND project_id=$2 AND environment=$3`, []any{fixture.scope.TargetID, fixture.scope.ProjectID, fixture.scope.Environment}},
		{"group revoke", `UPDATE access.access_group SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, []any{fixture.group.ID}},
		{"membership removal", `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND group_id=$2::uuid`, []any{fixture.principal.ID, fixture.group.ID}},
		{"session revoke", `UPDATE access.session SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, []any{fixture.session.ID}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			expectActivationLockTimeout(t, db, mutation.sql, mutation.args...)
		})
	}
	if err := caller.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp() WHERE id=$1::uuid`, fixture.principal.ID); err != nil {
		t.Fatalf("principal mutation remained blocked after caller rollback: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `UPDATE access.principal SET status='active', disabled_at=NULL WHERE id=$1::uuid`, fixture.principal.ID); err != nil {
		t.Fatal(err)
	}

	apiCaller, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer apiCaller.Rollback(context.Background())
	issuer.Credential = fixture.token
	if err := fixture.repo.AuthorizeCredentialActivationTx(ctx, apiCaller, fixture.scope, fixture.snapshot, issuer, []access.PermissionPair{fixture.manage, fixture.use}); err != nil {
		t.Fatalf("API token authority check failed: %v", err)
	}
	expectActivationLockTimeout(t, db, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, fixture.token.ID)
}

func expectActivationLockTimeout(t *testing.T, db auditDatabase, statement string, args ...any) {
	t.Helper()
	tx, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `SET LOCAL lock_timeout = '100ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), statement, args...)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("mutation error = %v, want lock timeout 55P03", err)
	}
}
