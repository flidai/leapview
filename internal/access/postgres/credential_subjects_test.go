package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCredentialSubjectsHoldMembershipAndGroupUntilCommit(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "credential-groups@example.test", DisplayName: "Credential groups"})
	if err != nil {
		t.Fatal(err)
	}
	group, membership := uuid.NewString(), uuid.NewString()
	if _, err = db.admin.Exec(t.Context(), `INSERT INTO access.access_group(id,name) VALUES($1::uuid,'Credential group')`, group); err != nil {
		t.Fatal(err)
	}
	if _, err = db.admin.Exec(t.Context(), `INSERT INTO access.principal_group(membership_id,principal_id,group_id) VALUES($1::uuid,$2::uuid,$3::uuid)`, membership, principal.ID, group); err != nil {
		t.Fatal(err)
	}
	held, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	subjects, err := LockCredentialAuthorizationSubjectsTx(t.Context(), held, principal.ID)
	if err != nil || len(subjects) != 2 {
		t.Fatalf("current subjects: %v %v", subjects, err)
	}
	for _, mutation := range []struct{ name, sql, id string }{
		{"membership", `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE membership_id=$1::uuid`, membership},
		{"group", `UPDATE access.access_group SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, group},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			revoke, err := db.admin.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer revoke.Rollback(context.Background())
			if _, err = revoke.Exec(t.Context(), `SET LOCAL lock_timeout='100ms'`); err != nil {
				t.Fatal(err)
			}
			_, err = revoke.Exec(t.Context(), mutation.sql, mutation.id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
				t.Fatalf("revocation crossed active credential authority: %v", err)
			}
		})
	}
	if err = held.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.admin.Exec(t.Context(), `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE membership_id=$1::uuid`, membership); err != nil {
		t.Fatal(err)
	}
	current, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer current.Rollback(context.Background())
	subjects, err = LockCredentialAuthorizationSubjectsTx(t.Context(), current, principal.ID)
	if err != nil || len(subjects) != 1 {
		t.Fatalf("revoked membership retained: %v %v", subjects, err)
	}
}
