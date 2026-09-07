package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type authoringReplayFaultDB struct {
	*pgxpool.Pool
	failSQL    string
	failCommit bool
}

func (d *authoringReplayFaultDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &authoringReplayFaultTx{Tx: tx, failSQL: d.failSQL, failCommit: d.failCommit}, nil
}

type authoringReplayFaultTx struct {
	pgx.Tx
	failSQL    string
	failCommit bool
}

func (tx *authoringReplayFaultTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if tx.failSQL != "" && strings.Contains(sql, tx.failSQL) {
		return pgconn.CommandTag{}, errors.New("injected authoring replay failure")
	}
	return tx.Tx.Exec(ctx, sql, arguments...)
}

func (tx *authoringReplayFaultTx) Commit(ctx context.Context) error {
	if tx.failCommit {
		return errors.New("injected authoring replay commit failure")
	}
	return tx.Tx.Commit(ctx)
}

func TestPostgres18AuthoringRefreshReplayFailuresRollbackAtomically(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	const (
		principalID = "0198f2c0-7c7a-7f00-8a11-000000000201"
		sessionID   = "authoring_replay_failure"
	)
	refreshHash := hashHex("authoring-replay-failure-refresh")
	now := time.Now().UTC()
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES ($1::uuid,'user','active','replay-failure@example.com','Replay Failure')`, principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.authoring_session(id,kind,client_id,principal_id,target_id,project_id,capabilities,created_at,expires_at) VALUES ($1,'human_cli','leapview-cli',$2::uuid,'instance_replay_failure','project_replay_failure','["RESOURCE_READ"]'::jsonb,$3,$4)`, sessionID, principalID, now, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.authoring_credential(id,session_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,active,created_at,replaced_at) VALUES ('authoring_replay_old',$1,$2,$3,$4,$5,false,$6,$6)`, sessionID, hashHex("authoring-replay-failure-access"), refreshHash, now.Add(time.Hour), now.Add(3*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	rotation := access.AuthoringCredentialRotation{
		RefreshTokenHash: refreshHash, CredentialID: "authoring_replay_replacement",
		AccessTokenHash: hashHex("authoring-replay-failure-new-access"), RefreshTokenHashNew: hashHex("authoring-replay-failure-new-refresh"),
		AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(3 * time.Hour),
	}
	for _, test := range []struct {
		name       string
		failSQL    string
		failCommit bool
	}{
		{name: "revoke", failSQL: "UPDATE access.authoring_session"},
		{name: "audit", failSQL: "INSERT INTO audit.audit_event"},
		{name: "commit", failCommit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, err := NewAccess(&authoringReplayFaultDB{Pool: db.runtime, failSQL: test.failSQL, failCommit: test.failCommit}, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.RotateAuthoringCredential(t.Context(), rotation); err == nil || errors.Is(err, access.ErrAuthoringRefreshReplay) {
				t.Fatalf("injected %s error = %v, want surfaced operational failure", test.name, err)
			}
			var revoked bool
			var audits int
			if err := db.admin.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM access.authoring_session WHERE id=$1`, sessionID).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='authoring.refresh.replay' AND resource_id=$1`, sessionID).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if revoked || audits != 0 {
				t.Fatalf("failed %s containment committed revoked=%t audits=%d", test.name, revoked, audits)
			}
		})
	}
}
