package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
)

const privacyTestDeployment = "instance_0123456789abcdef0123456789abcdef"

func newPrivacyTestExecutor(t *testing.T, customer string) (*PrivacyExecutor, auditDatabase, PrivacySubjectGraph) {
	t.Helper()
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	for _, statement := range []string{
		`CREATE SCHEMA platform`,
		`CREATE TABLE platform.instance_identity (singleton_id smallint PRIMARY KEY, instance_id text NOT NULL)`,
		`GRANT USAGE ON SCHEMA platform TO leapview_control_runtime`,
		`GRANT SELECT ON platform.instance_identity TO leapview_control_runtime`,
	} {
		if _, err := db.admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.admin.Exec(ctx, `INSERT INTO platform.instance_identity VALUES (1, $1)`, privacyTestDeployment); err != nil {
		t.Fatal(err)
	}
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{Email: "reviewer@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: uuid.NewString(), Kind: access.PrincipalKindUser, Email: "subject@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewPrivacyExecutor(repo, PrivacyBoundary{CustomerID: customer, DeploymentID: privacyTestDeployment})
	if err != nil {
		t.Fatal(err)
	}
	graph := PrivacySubjectGraph{Boundary: executor.boundary, PrincipalID: subject.ID, PrincipalType: "user",
		ApprovedIdentifiers: []PrivacyIdentifier{{Kind: "principal_id", Value: subject.ID}},
		CaseID:              "case-966", CorrelationID: "corr-966", ActorID: actor.ID, Action: PrivacyRestrictAccess}
	return executor, db, graph
}

func privacyDryRun(t *testing.T, e *PrivacyExecutor, graph PrivacySubjectGraph) PrivacyManifest {
	t.Helper()
	graph.DryRun = true
	manifest, err := e.DryRun(t.Context(), graph, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestPrivacyActionPostgresScopeAndDryRun(t *testing.T) {
	executor, db, graph := newPrivacyTestExecutor(t, "customer-a")
	ctx := t.Context()
	if _, err := executor.repo.CreateSession(ctx, graph.PrincipalID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executor.repo.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
		PrincipalID: graph.PrincipalID,
		Name:        "privacy-test",
		Permissions: []access.PermissionPair{},
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	manifest := privacyDryRun(t, executor, graph)
	second := privacyDryRun(t, executor, graph)
	if second.Digest != manifest.Digest || second.Total != manifest.Total {
		t.Fatalf("dry-run drift: %+v vs %+v", manifest, second)
	}
	if manifest.Total < 3 || len(manifest.Records) < 3 {
		t.Fatalf("manifest too small: %+v", manifest)
	}
	if manifest.Digest == "" || manifest.NextCursor != "" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	for _, record := range manifest.Records {
		if record.Boundary != graph.Boundary || record.MatchReason != "approved_principal_id" {
			t.Fatalf("scope leaked: %+v", record)
		}
		if strings.Contains(record.RecordID, "@") {
			t.Fatalf("record exposes email: %+v", record)
		}
	}
	graph.DryRun = true
	page, err := executor.DryRun(ctx, graph, "", 1)
	if err != nil || len(page.Records) != 1 || page.NextCursor == "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	next, err := executor.DryRun(ctx, graph, page.NextCursor, 1)
	if err != nil || len(next.Records) != 1 || next.Records[0] == page.Records[0] || next.Digest != manifest.Digest {
		t.Fatalf("next = %+v, %v", next, err)
	}
	var runs int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.privacy_action_run`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("dry-run wrote state: %d, %v", runs, err)
	}
	wrong := graph
	wrong.Boundary.CustomerID = "customer-b"
	if _, err := executor.DryRun(ctx, wrong, "", 10); !errors.Is(err, ErrPrivacyBoundary) {
		t.Fatalf("wrong customer: %v", err)
	}
	wrong = graph
	wrong.Boundary.DeploymentID = "instance_ffffffffffffffffffffffffffffffff"
	if _, err := executor.DryRun(ctx, wrong, "", 10); !errors.Is(err, ErrPrivacyBoundary) {
		t.Fatalf("wrong deployment: %v", err)
	}
	wrong = graph
	wrong.ActorID = graph.PrincipalID
	if _, err := executor.DryRun(ctx, wrong, "", 10); !errors.Is(err, ErrPrivacyUnauthorized) {
		t.Fatalf("non-admin: %v", err)
	}
	wrong = graph
	wrong.ApprovedIdentifiers[0].Kind = "email"
	if _, err := executor.DryRun(ctx, wrong, "", 10); !errors.Is(err, ErrPrivacyUnsupported) {
		t.Fatalf("email identifier: %v", err)
	}
}

func TestPrivacyActionPostgresExecutionResumeAndIsolation(t *testing.T) {
	executor, db, graph := newPrivacyTestExecutor(t, "customer-a")
	ctx := t.Context()
	other, err := executor.repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: uuid.NewString(), Kind: access.PrincipalKindUser, Email: "other@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.repo.CreateSession(ctx, graph.PrincipalID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.repo.CreateSession(ctx, other.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	groupID := uuid.NewString()
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.access_group(id,name) VALUES ($1,'synthetic-group')`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.principal_group(principal_id,group_id) VALUES ($1,$2)`, graph.PrincipalID, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.local_credential(principal_id,verifier) VALUES ($1,decode(repeat('ab',32),'hex'))`, graph.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.oauth_session(kind,signature,request_id,request_json) VALUES ('access_token','synthetic-secret-token','synthetic-request',jsonb_build_object('session',jsonb_build_object('subject',$1::text)))`, graph.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.authoring_session(id,kind,client_id,principal_id,target_id,project_id,permission_profile,permissions,expires_at)
		VALUES ('synthetic-authoring-session','human_cli','synthetic-client',$1,'target','project','leapview.permissions/v1','[]'::jsonb,clock_timestamp()+interval '1 hour')`, graph.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.authoring_credential(id,session_id,access_token_hash,access_expires_at)
		VALUES ('synthetic-authoring-credential','synthetic-authoring-session',repeat('a',64),clock_timestamp()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	manifest := privacyDryRun(t, executor, graph)
	for _, record := range manifest.Records {
		if strings.Contains(record.RecordID, "synthetic-secret-token") {
			t.Fatalf("OAuth bearer leaked in manifest: %+v", record)
		}
	}
	if _, err := executor.Execute(ctx, graph, strings.Repeat("0", len(manifest.Digest)), 1); !errors.Is(err, ErrPrivacyConflict) {
		t.Fatalf("bad digest: %v", err)
	}
	result, err := executor.Execute(ctx, graph, manifest.Digest, 1)
	if err != nil || result.Status != "pending" || result.Cursor != 1 {
		t.Fatalf("first batch = %+v, %v", result, err)
	}
	firstRun := result.RunID
	for result.Status == "pending" {
		result, err = executor.Execute(ctx, graph, manifest.Digest, 1)
		if err != nil {
			t.Fatal(err)
		}
		if result.RunID != firstRun {
			t.Fatalf("run changed: %s vs %s", result.RunID, firstRun)
		}
	}
	if result.Status != "completed" || result.Cursor < 2 {
		t.Fatalf("final = %+v", result)
	}
	again, err := executor.Execute(ctx, graph, manifest.Digest, 1)
	if err != nil || again.RunID != firstRun || again.Cursor != result.Cursor || again.Status != "completed" {
		t.Fatalf("idempotent result = %+v, %v", again, err)
	}
	w, err := executor.repo.PrincipalByID(ctx, graph.PrincipalID)
	if err != nil || !w.AccessDisabled() {
		t.Fatalf("subject not disabled: %+v, %v", w, err)
	}
	w, err = executor.repo.PrincipalByID(ctx, other.ID)
	if err != nil || w.AccessDisabled() {
		t.Fatalf("other principal changed: %+v, %v", w, err)
	}
	var activeOther, activeSubject int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.session WHERE principal_id=$1 AND revoked_at IS NULL`, other.ID).Scan(&activeOther); err != nil {
		t.Fatal(err)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM access.session WHERE principal_id=$1 AND revoked_at IS NULL`, graph.PrincipalID).Scan(&activeSubject); err != nil {
		t.Fatal(err)
	}
	if activeOther != 1 || activeSubject != 0 {
		t.Fatalf("session isolation: other=%d subject=%d", activeOther, activeSubject)
	}
	var membershipRevoked, localRevoked, authoringRevoked, authoringActive, oauthActive bool
	if err := db.runtime.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.principal_group WHERE principal_id=$1`, graph.PrincipalID).Scan(&membershipRevoked); err != nil {
		t.Fatal(err)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.local_credential WHERE principal_id=$1`, graph.PrincipalID).Scan(&localRevoked); err != nil {
		t.Fatal(err)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.authoring_session WHERE principal_id=$1`, graph.PrincipalID).Scan(&authoringRevoked); err != nil {
		t.Fatal(err)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT active FROM access.authoring_credential WHERE session_id='synthetic-authoring-session'`).Scan(&authoringActive); err != nil {
		t.Fatal(err)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT active FROM access.oauth_session WHERE signature='synthetic-secret-token'`).Scan(&oauthActive); err != nil {
		t.Fatal(err)
	}
	if !membershipRevoked || !localRevoked || !authoringRevoked || authoringActive || oauthActive {
		t.Fatalf("incomplete identity cascade: group=%v local=%v authoring=%v authoringActive=%v oauthActive=%v", membershipRevoked, localRevoked, authoringRevoked, authoringActive, oauthActive)
	}
	var auditCount int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE action='privacy.restrict_access' AND request_id=$1 AND correlation_id=$2 AND principal_id=$3`, graph.CaseID, graph.CorrelationID, graph.ActorID).Scan(&auditCount); err != nil || auditCount < 2 {
		t.Fatalf("minimal audit missing: %d, %v", auditCount, err)
	}
	rows, err := db.runtime.Query(ctx, `SELECT metadata::text, resource_kind, outcome FROM audit.audit_event WHERE action='privacy.restrict_access'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var metadata, store, outcome string
		if err := rows.Scan(&metadata, &store, &outcome); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(metadata, graph.Boundary.CustomerID) || strings.Contains(metadata, graph.Boundary.DeploymentID) || strings.Contains(metadata, "@") || strings.Contains(metadata, "token") || !strings.Contains(metadata, "count") || store == "" || outcome != "success" {
			t.Fatalf("unsafe audit: %q %q %q", metadata, store, outcome)
		}
	}
	rows.Close()
	wrong := graph
	wrong.CorrelationID = "different"
	if _, err := executor.Execute(ctx, wrong, manifest.Digest, 1); !errors.Is(err, ErrPrivacyConflict) {
		t.Fatalf("case reuse: %v", err)
	}
}

func TestPrivacyActionPostgresFailureRollsBackBatch(t *testing.T) {
	executor, db, graph := newPrivacyTestExecutor(t, "customer-a")
	ctx := t.Context()
	manifest := privacyDryRun(t, executor, graph)
	// An injected storage failure must roll back the principal mutation, item
	// transition and success audit together; a later retry must be possible.
	if _, err := db.admin.Exec(ctx, `CREATE FUNCTION access.privacy_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected privacy failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `CREATE TRIGGER privacy_test_fail BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.privacy_test_fail()`); err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(ctx, graph, manifest.Digest, 1)
	if err == nil || result.Status != "incomplete" {
		t.Fatalf("injected failure = %+v, %v", result, err)
	}
	w, err := executor.repo.PrincipalByID(ctx, graph.PrincipalID)
	if err != nil || w.AccessDisabled() {
		t.Fatalf("rolled back subject = %+v, %v", w, err)
	}
	var cursor int64
	if err := db.runtime.QueryRow(ctx, `SELECT cursor FROM access.privacy_action_run WHERE case_id=$1`, graph.CaseID).Scan(&cursor); err != nil || cursor != 0 {
		t.Fatalf("cursor = %d, %v", cursor, err)
	}
	var failedAudit, successAudit int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FILTER (WHERE outcome='failed'), count(*) FILTER (WHERE outcome='success') FROM audit.audit_event WHERE action='privacy.restrict_access' AND request_id=$1`, graph.CaseID).Scan(&failedAudit, &successAudit); err != nil || failedAudit != 1 || successAudit != 0 {
		t.Fatalf("failure audit = failed:%d success:%d err:%v", failedAudit, successAudit, err)
	}
	if _, err := db.admin.Exec(ctx, `DROP TRIGGER privacy_test_fail ON access.principal`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `DROP FUNCTION access.privacy_test_fail()`); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(ctx, graph, manifest.Digest, 1)
	if err != nil || result.Status != "completed" {
		t.Fatalf("retry = %+v, %v", result, err)
	}
}

func TestPrivacyActionPostgresPartialFailureResumes(t *testing.T) {
	executor, db, graph := newPrivacyTestExecutor(t, "customer-a")
	ctx := t.Context()
	groupID := uuid.NewString()
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.access_group(id,name) VALUES ($1,'partial-test-group')`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `INSERT INTO access.principal_group(principal_id,group_id) VALUES ($1,$2)`, graph.PrincipalID, groupID); err != nil {
		t.Fatal(err)
	}
	manifest := privacyDryRun(t, executor, graph)
	first, err := executor.Execute(ctx, graph, manifest.Digest, 1)
	if err != nil || first.Status != "pending" || first.Cursor != 1 {
		t.Fatalf("first batch = %+v, %v", first, err)
	}
	if _, err := db.admin.Exec(ctx, `CREATE FUNCTION access.privacy_test_group_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected group failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `CREATE TRIGGER privacy_test_group_fail BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.privacy_test_group_fail()`); err != nil {
		t.Fatal(err)
	}
	failed, err := executor.Execute(ctx, graph, manifest.Digest, 1)
	if err == nil || failed.Status != "incomplete" || failed.Cursor != 1 || failed.RunID != first.RunID {
		t.Fatalf("partial failure = %+v, %v", failed, err)
	}
	var revoked bool
	if err := db.runtime.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.principal_group WHERE principal_id=$1`, graph.PrincipalID).Scan(&revoked); err != nil || revoked {
		t.Fatalf("group must remain active: %v, %v", revoked, err)
	}
	if _, err := db.admin.Exec(ctx, `DROP TRIGGER privacy_test_group_fail ON access.principal_group`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `DROP FUNCTION access.privacy_test_group_fail()`); err != nil {
		t.Fatal(err)
	}
	final, err := executor.Execute(ctx, graph, manifest.Digest, 1)
	if err != nil || final.Status != "completed" || final.Cursor != 2 || final.RunID != first.RunID {
		t.Fatalf("resumed = %+v, %v", final, err)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.principal_group WHERE principal_id=$1`, graph.PrincipalID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("group was not revoked: %v, %v", revoked, err)
	}
	var failedAudit int
	if err := db.runtime.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE action='privacy.restrict_access' AND request_id=$1 AND outcome='failed'`, graph.CaseID).Scan(&failedAudit); err != nil || failedAudit != 1 {
		t.Fatalf("failure audit = %d, %v", failedAudit, err)
	}
}

func TestPrivacyActionPostgresNonAdminDenied(t *testing.T) {
	executor, _, graph := newPrivacyTestExecutor(t, "customer-a")
	other, err := executor.repo.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: uuid.NewString(), Kind: access.PrincipalKindUser})
	if err != nil {
		t.Fatal(err)
	}
	graph.ActorID = other.ID
	graph.DryRun = true
	if _, err := executor.DryRun(context.Background(), graph, "", 10); !errors.Is(err, ErrPrivacyUnauthorized) {
		t.Fatalf("non-admin: %v", err)
	}
}

func TestPrivacyActionPostgresConcurrentMutationDenied(t *testing.T) {
	executor, db, graph := newPrivacyTestExecutor(t, "customer-a")
	manifest := privacyDryRun(t, executor, graph)
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	var locked string
	if err := tx.QueryRow(t.Context(), `SELECT id::text FROM access.principal WHERE id=$1 FOR UPDATE`, graph.PrincipalID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(t.Context(), graph, manifest.Digest, 1)
	if !errors.Is(err, ErrPrivacyBusy) || result.Status != "incomplete" {
		t.Fatalf("locked subject = %+v, %v", result, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(t.Context(), graph, manifest.Digest, 1)
	if err != nil || result.Status != "completed" {
		t.Fatalf("after unlock = %+v, %v", result, err)
	}
}

func TestPrivacyActionPostgresCrossCustomerCollision(t *testing.T) {
	first, _, firstGraph := newPrivacyTestExecutor(t, "customer-a")
	second, _, secondGraph := newPrivacyTestExecutor(t, "customer-b")
	// The same opaque principal ID exists independently in both deployments.
	// A graph from either customer must be rejected by the other executor.
	if _, err := second.repo.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: firstGraph.PrincipalID, Kind: access.PrincipalKindUser}); err != nil {
		t.Fatal(err)
	}
	secondGraph.PrincipalID = firstGraph.PrincipalID
	secondGraph.ApprovedIdentifiers = []PrivacyIdentifier{{Kind: "principal_id", Value: firstGraph.PrincipalID}}
	if _, err := first.DryRun(t.Context(), PrivacySubjectGraph{Boundary: secondGraph.Boundary, PrincipalID: firstGraph.PrincipalID,
		PrincipalType: "user", ApprovedIdentifiers: firstGraph.ApprovedIdentifiers, CaseID: firstGraph.CaseID,
		CorrelationID: firstGraph.CorrelationID, ActorID: firstGraph.ActorID, Action: PrivacyRestrictAccess, DryRun: true}, "", 10); !errors.Is(err, ErrPrivacyBoundary) {
		t.Fatalf("customer-b graph in a: %v", err)
	}
	secondGraph.DryRun = true
	if _, err := second.DryRun(t.Context(), secondGraph, "", 10); err != nil {
		t.Fatalf("customer-b own graph: %v", err)
	}
	firstGraph.DryRun = true
	if _, err := second.DryRun(t.Context(), firstGraph, "", 10); !errors.Is(err, ErrPrivacyBoundary) {
		t.Fatalf("customer-a graph in b: %v", err)
	}
}
