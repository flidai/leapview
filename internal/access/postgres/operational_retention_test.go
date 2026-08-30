package postgres

import (
	"strings"
	"testing"
	"time"
)

func TestPostgreSQL18OperationalRetentionBoundsStateAndPreservesEvidence(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	ctx := t.Context()
	principalID := "63000000-0000-7000-8000-000000000001"
	now := time.Now().UTC().Truncate(time.Microsecond)
	created := now.Add(-2 * time.Hour)
	cutoff := now.Add(-30 * time.Minute)
	statements := []string{
		`
INSERT INTO access.principal(id, principal_type, status, email, display_name, created_at, updated_at)
VALUES ($1, 'user', 'active', 'retention@example.com', 'retention', $2::timestamptz, $2::timestamptz)`,
		`
INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, created_at, last_seen_at)
VALUES ('63000000-0000-7000-8000-000000000002', $1, decode(repeat('11',32),'hex'), decode(repeat('22',32),'hex'), $2::timestamptz + interval '1 hour', $2::timestamptz, $2::timestamptz)`,
		`
INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, created_at, last_seen_at)
VALUES ('63000000-0000-7000-8000-000000000003', $1, decode(repeat('33',32),'hex'), decode(repeat('44',32),'hex'), clock_timestamp() + interval '1 hour', clock_timestamp(), clock_timestamp())`,
		`
INSERT INTO access.api_token(id, principal_id, name, token_fingerprint, verifier, expires_at, created_at)
VALUES ('63000000-0000-7000-8000-000000000004', $1, 'old', decode(repeat('55',32),'hex'), decode(repeat('66',32),'hex'), $2::timestamptz + interval '1 hour', $2::timestamptz)`,
		`
INSERT INTO access.service_principal_secret(id, service_principal_id, name, secret_fingerprint, verifier, expires_at, created_at)
VALUES ('63000000-0000-7000-8000-000000000005', $1, 'old', decode(repeat('77',32),'hex'), decode(repeat('88',32),'hex'), $2::timestamptz + interval '1 hour', $2::timestamptz)`,
		`
INSERT INTO access.desktop_authorization_code(code_hash, principal_id, client_id, instance_id, profile_id, redirect_uri, code_challenge, return_path, expires_at, created_at)
VALUES (decode(repeat('99',32),'hex'), $1, 'leapview-desktop', 'instance', 'profile', '/', repeat('a',43), '/', $2::timestamptz + interval '5 minutes', $2::timestamptz)`,
		`
INSERT INTO access.device_authorization(id, client_id, device_code_hash, user_code_hash, target_id, project_id, capabilities, status, principal_id, expires_at, poll_interval_seconds, created_at, denied_at)
VALUES ('device-old', 'leapview-cli', repeat('a',64), repeat('b',64), 'target', 'project', '[]', 'denied', $1, $2::timestamptz + interval '1 hour', 5, $2::timestamptz, $2::timestamptz)`,
		`
INSERT INTO access.authoring_session(id, kind, client_id, principal_id, target_id, project_id, capabilities, created_at, expires_at, revoked_at)
VALUES ('authoring-old', 'human_cli', 'cli', $1, 'target', 'project', '[]', $2::timestamptz, $2::timestamptz + interval '1 hour', $2::timestamptz)`,
		`
INSERT INTO access.authoring_credential(id, session_id, access_token_hash, access_expires_at, active, created_at, replaced_at)
VALUES ('credential-old', 'authoring-old', repeat('c',64), $2::timestamptz + interval '1 hour', false, $2::timestamptz, $2::timestamptz)`,
		`
INSERT INTO access.oauth_session(kind, signature, request_id, request_json, active, created_at)
VALUES ('authorize_code', 'old-signature', 'old-request', '{}', false, $2::timestamptz)`,
		`
INSERT INTO access.oauth_client_assertion(jti, expires_at)
VALUES ('old-assertion', $2::timestamptz)`,
	}
	for _, statement := range statements {
		hasPrincipal, hasCreated := strings.Contains(statement, "$1"), strings.Contains(statement, "$2")
		args := make([]any, 0, 2)
		if hasPrincipal {
			args = append(args, principalID)
		}
		if hasCreated && !hasPrincipal {
			statement = strings.ReplaceAll(statement, "$2", "$1")
			args = []any{created}
		} else if hasCreated {
			args = append(args, created)
		}
		if _, err := db.admin.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed operational retention state: %v", err)
		}
	}

	maintenance := NewMaintenance(db.maintenance)
	result, err := maintenance.PruneAuthState(ctx, cutoff, 1000)
	if err != nil {
		t.Fatalf("prune operational auth state: %v", err)
	}
	if result.RequestedLimit != 1000 || !result.RequestedCutoff.Equal(cutoff) || result.Cutoff.After(cutoff) {
		t.Fatalf("invalid operational retention evidence: %#v", result)
	}
	if result.SessionsDeleted != 1 || result.OAuthSessionsDeleted != 1 || result.OAuthAssertionsDeleted != 1 || result.DesktopCodesDeleted != 1 || result.DeviceAuthorizationsDeleted != 1 || result.APITokensDeleted != 1 || result.ServiceSecretsDeleted != 1 || result.AuthoringSessionsDeleted != 1 || result.AuthoringCredentialsDeleted != 1 {
		t.Fatalf("operational retention counts = %#v", result)
	}
	var survivors int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM access.session WHERE id='63000000-0000-7000-8000-000000000003'`).Scan(&survivors); err != nil || survivors != 1 {
		t.Fatalf("active session survivor count=%d err=%v", survivors, err)
	}
	if result.AuthStateFloor.IsZero() {
		t.Fatalf("missing durable operational retention floor: %#v", result)
	}
	// This row is newer than the stale replay cutoff but older than the
	// previously advanced floor. It must survive the replay.
	if _, err := db.admin.Exec(ctx, `
INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, created_at, last_seen_at)
VALUES ('63000000-0000-7000-8000-000000000006', $1, decode(repeat('aa',32),'hex'), decode(repeat('bb',32),'hex'), $2::timestamptz - interval '10 minutes', $2::timestamptz - interval '20 minutes', $2::timestamptz - interval '20 minutes')`, principalID, cutoff); err != nil {
		t.Fatalf("seed stale-replay boundary session: %v", err)
	}
	// A stale policy replay must retain the advanced floor as evidence while
	// using only the older requested cutoff as its deletion predicate.
	older := cutoff.Add(-30 * time.Minute)
	replay, err := maintenance.PruneAuthState(ctx, older, 1000)
	if err != nil {
		t.Fatalf("stale auth retention replay: %v", err)
	}
	if replay.SessionsDeleted != 0 || !replay.Cutoff.Equal(older) || !replay.AuthStateFloor.Equal(result.AuthStateFloor) {
		t.Fatalf("stale replay widened cutoff or changed floor: first=%#v replay=%#v", result, replay)
	}
	var replaySurvivor int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM access.session WHERE id='63000000-0000-7000-8000-000000000006'`).Scan(&replaySurvivor); err != nil || replaySurvivor != 1 {
		t.Fatalf("stale-replay boundary session count=%d err=%v", replaySurvivor, err)
	}
}

func TestPostgreSQL18OperationalRetentionRoleBoundary(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	ctx := t.Context()
	var runtimeDelete, runtimeExecute, maintenanceExecute bool
	if err := db.runtime.QueryRow(ctx, `SELECT has_table_privilege(current_user, 'access.session', 'DELETE'), has_function_privilege(current_user, 'access.prune_auth_state(timestamptz, integer)', 'EXECUTE')`).Scan(&runtimeDelete, &runtimeExecute); err != nil {
		t.Fatal(err)
	}
	if runtimeDelete || runtimeExecute {
		t.Fatalf("runtime retention privileges delete=%t execute=%t", runtimeDelete, runtimeExecute)
	}
	if err := db.maintenance.QueryRow(ctx, `SELECT has_function_privilege(current_user, 'access.prune_auth_state(timestamptz, integer)', 'EXECUTE')`).Scan(&maintenanceExecute); err != nil {
		t.Fatal(err)
	}
	if !maintenanceExecute {
		t.Fatal("maintenance role cannot execute operational retention")
	}
	if _, err := db.runtime.Exec(ctx, `SET access.maintenance='on'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `DELETE FROM access.session`); err == nil {
		t.Fatal("runtime forged maintenance marker and deleted auth state")
	}
}
