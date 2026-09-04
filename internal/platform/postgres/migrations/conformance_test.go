package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestBaselinePostgreSQL18 applies the clean baseline to a real PostgreSQL 18
// server.  CI can make container availability mandatory with
// LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1; local runs skip when Docker is not
// available, matching the existing platform PostgreSQL conformance tests.
func TestBaselinePostgreSQL18(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "migration-conformance", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "leapview_control")
	h.GrantDatabase(t, database.Name, owner, "CREATE")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, `
		ALTER DATABASE leapview_control OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDB.Close()
	riverPool, err := pgxpool.New(ctx, database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	defer riverPool.Close()
	if err := platformmigrations.ApplyRiver(ctx, riverPool); err != nil {
		t.Fatalf("apply River schema: %v", err)
	}
	if err := postgresbaseline.Apply(ctx, migrationDB); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	// Explicit migration replays are safe: Goose applies no DDL twice while
	// LeapView still reconciles its role policy.
	if err := postgresbaseline.Apply(ctx, migrationDB); err != nil {
		t.Fatalf("reapply baseline: %v", err)
	}

	var schemaCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.schemata
		WHERE schema_name = ANY($1::text[])`, []string{"access", "delivery", "refresh", "event", "audit", "lineage", "agent", "ducklake", "physical_pool", "dashboard"}).Scan(&schemaCount); err != nil {
		t.Fatal(err)
	}
	if schemaCount != 10 {
		t.Fatalf("capability schema count = %d, want 10", schemaCount)
	}
	var schemaOwner, capabilityOwner string
	if err := db.QueryRow(ctx, `SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = 'platform'`).Scan(&schemaOwner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = 'access.principal'::regclass`).Scan(&capabilityOwner); err != nil {
		t.Fatal(err)
	}
	if schemaOwner != owner.Name || capabilityOwner != owner.Name {
		t.Fatalf("baseline ownership = schema %q/capability %q, want %q", schemaOwner, capabilityOwner, owner.Name)
	}
	var agentExists bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'agent')`).Scan(&agentExists); err != nil {
		t.Fatal(err)
	}
	if !agentExists {
		t.Fatal("agent capability namespace is missing from the clean baseline")
	}

	var version int64
	var applied bool
	if err := db.QueryRow(ctx, "SELECT version_id, is_applied FROM public.goose_db_version ORDER BY id DESC LIMIT 1").Scan(&version, &applied); err != nil {
		t.Fatal(err)
	}
	if version != postgresbaseline.BaselineRevision || !applied {
		t.Fatalf("Goose baseline identity = %d/applied=%t", version, applied)
	}
	var registryProfile, registryDigest string
	var registryRevision int64
	if err := db.QueryRow(ctx,
		"SELECT profile, registry_revision, registry_digest FROM access.semantic_attribute_registry WHERE singleton",
	).Scan(&registryProfile, &registryRevision, &registryDigest); err != nil {
		t.Fatal(err)
	}
	if registryProfile != "leapview.semantic-access/v1" || registryRevision != 0 || !strings.HasPrefix(registryDigest, "sha256:") {
		t.Fatalf("attribute registry state = %q/%d/%q", registryProfile, registryRevision, registryDigest)
	}

	var canUpdateAudit, canUpdateGoose, canReadGoose, canUpdateEvent, canDeleteEvent, canUpdateLineage, canInsertLineageRevision, canPublishLineage, canUpdateServingBundle, backupInsert, backupSelect, backupCursor, backupProject, readonlyCursor, readonlyJobs, readonlyJobView bool
	var physicalRuntimeSelect, physicalRuntimeInsert, physicalRuntimeUpdate, physicalRuntimeDelete, physicalReadonlySelect, physicalReadonlyInsert, physicalBackupSelect, physicalBackupInsert, physicalMaintenanceSelect, physicalMaintenanceLeaseWrite, physicalMaintenanceAdmissionWrite bool
	var readonlySession, readonlyCredential, readonlyToken, readonlyServiceSecret, readonlyDesktopCode, readonlyDeviceAuth, readonlyAuthoringCredential bool
	var dashboardRuntimeSessionInsert, dashboardRuntimeSessionDelete, dashboardRuntimeUsageInsert, dashboardRuntimeUsageDelete, dashboardRuntimeAppearanceInsert, dashboardRuntimeAppearanceDelete bool
	var dashboardMaintenanceSessionDelete, dashboardMaintenanceUsageDelete, dashboardMaintenanceAppearanceInsert bool
	var dashboardReadonlySessionInsert, dashboardReadonlyUsageInsert, dashboardReadonlyAppearanceUpdate bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'audit.audit_event', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'public.goose_db_version', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'public.goose_db_version', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'event.event_log', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'event.event_log', 'DELETE'),
		       has_table_privilege('leapview_control_runtime', 'lineage.graphs', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'lineage.revisions', 'INSERT'),
		       has_function_privilege('leapview_control_runtime', 'lineage.publish_revision(text,text,text)', 'EXECUTE'),
		       has_table_privilege('leapview_control_runtime', 'serving_state.bundle', 'UPDATE'),
		       has_table_privilege('leapview_control_backup', 'audit.audit_event', 'INSERT'),
		       has_table_privilege('leapview_control_backup', 'audit.audit_event', 'SELECT'),
		       has_table_privilege('leapview_control_backup', 'platform.api_cursor_signing_keys', 'SELECT'),
		       has_table_privilege('leapview_control_backup', 'project.project_identity', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'platform.api_cursor_signing_keys', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'jobs.job_history', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'jobs.event', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.session', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.local_credential', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.api_token', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.service_principal_secret', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.desktop_authorization_code', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.device_authorization', 'SELECT'),
	       has_table_privilege('leapview_control_readonly', 'access.authoring_credential', 'SELECT'),
	       has_table_privilege('leapview_control_runtime', 'physical_pool.physical_pools', 'SELECT'),
	       has_table_privilege('leapview_control_runtime', 'physical_pool.physical_pools', 'INSERT'),
	       has_table_privilege('leapview_control_runtime', 'physical_pool.physical_pool_admissions', 'UPDATE'),
	       has_table_privilege('leapview_control_runtime', 'physical_pool.namespace_deletion_leases', 'DELETE'),
	       has_table_privilege('leapview_control_readonly', 'physical_pool.physical_pools', 'SELECT'),
	       has_table_privilege('leapview_control_readonly', 'physical_pool.physical_pools', 'INSERT'),
	       has_table_privilege('leapview_control_backup', 'physical_pool.physical_pool_admissions', 'SELECT'),
	       has_table_privilege('leapview_control_backup', 'physical_pool.physical_pool_admissions', 'INSERT'),
	       has_table_privilege('leapview_control_maintenance', 'physical_pool.physical_pools', 'SELECT'),
	       has_table_privilege('leapview_control_maintenance', 'physical_pool.namespace_deletion_leases', 'UPDATE'),
	       has_table_privilege('leapview_control_maintenance', 'physical_pool.physical_pool_admissions', 'INSERT'),
	       has_table_privilege('leapview_control_runtime', 'dashboard.view_session', 'INSERT'),
	       has_table_privilege('leapview_control_runtime', 'dashboard.view_session', 'DELETE'),
	       has_table_privilege('leapview_control_runtime', 'dashboard.view_day', 'INSERT'),
	       has_table_privilege('leapview_control_runtime', 'dashboard.view_day', 'DELETE'),
	       has_table_privilege('leapview_control_runtime', 'dashboard.appearance_override', 'INSERT'),
	       has_table_privilege('leapview_control_runtime', 'dashboard.appearance_override', 'DELETE'),
	       has_table_privilege('leapview_control_maintenance', 'dashboard.view_session', 'DELETE'),
	       has_table_privilege('leapview_control_maintenance', 'dashboard.view_day', 'DELETE'),
	       has_table_privilege('leapview_control_maintenance', 'dashboard.appearance_override', 'INSERT'),
	       has_table_privilege('leapview_control_readonly', 'dashboard.view_session', 'INSERT'),
	       has_table_privilege('leapview_control_readonly', 'dashboard.view_day', 'INSERT'),
	       has_table_privilege('leapview_control_readonly', 'dashboard.appearance_override', 'UPDATE')`).
		Scan(&canUpdateAudit, &canUpdateGoose, &canReadGoose, &canUpdateEvent, &canDeleteEvent, &canUpdateLineage, &canInsertLineageRevision, &canPublishLineage, &canUpdateServingBundle, &backupInsert, &backupSelect, &backupCursor, &backupProject, &readonlyCursor, &readonlyJobs, &readonlyJobView, &readonlySession, &readonlyCredential, &readonlyToken, &readonlyServiceSecret, &readonlyDesktopCode, &readonlyDeviceAuth, &readonlyAuthoringCredential, &physicalRuntimeSelect, &physicalRuntimeInsert, &physicalRuntimeUpdate, &physicalRuntimeDelete, &physicalReadonlySelect, &physicalReadonlyInsert, &physicalBackupSelect, &physicalBackupInsert, &physicalMaintenanceSelect, &physicalMaintenanceLeaseWrite, &physicalMaintenanceAdmissionWrite, &dashboardRuntimeSessionInsert, &dashboardRuntimeSessionDelete, &dashboardRuntimeUsageInsert, &dashboardRuntimeUsageDelete, &dashboardRuntimeAppearanceInsert, &dashboardRuntimeAppearanceDelete, &dashboardMaintenanceSessionDelete, &dashboardMaintenanceUsageDelete, &dashboardMaintenanceAppearanceInsert, &dashboardReadonlySessionInsert, &dashboardReadonlyUsageInsert, &dashboardReadonlyAppearanceUpdate); err != nil {
		t.Fatal(err)
	}
	if canUpdateAudit || canUpdateGoose || !canReadGoose || canUpdateEvent || canDeleteEvent || canUpdateLineage || canInsertLineageRevision || !canPublishLineage || canUpdateServingBundle || backupInsert || !backupSelect || !backupCursor || !backupProject || readonlyCursor || !readonlyJobs || readonlyJobView || readonlySession || readonlyCredential || readonlyToken || readonlyServiceSecret || readonlyDesktopCode || readonlyDeviceAuth || readonlyAuthoringCredential || !physicalRuntimeSelect || physicalRuntimeInsert || physicalRuntimeUpdate || physicalRuntimeDelete || !physicalReadonlySelect || physicalReadonlyInsert || !physicalBackupSelect || physicalBackupInsert || !physicalMaintenanceSelect || !physicalMaintenanceLeaseWrite || physicalMaintenanceAdmissionWrite || !dashboardRuntimeSessionInsert || dashboardRuntimeSessionDelete || !dashboardRuntimeUsageInsert || dashboardRuntimeUsageDelete || !dashboardRuntimeAppearanceInsert || dashboardRuntimeAppearanceDelete || !dashboardMaintenanceSessionDelete || !dashboardMaintenanceUsageDelete || dashboardMaintenanceAppearanceInsert || dashboardReadonlySessionInsert || dashboardReadonlyUsageInsert || dashboardReadonlyAppearanceUpdate {
		t.Fatalf("least-privilege grants leaked: audit update=%t Goose update/read=%t/%t event update=%t event delete=%t lineage update/insert-revision/publish=%t/%t/%t serving bundle update=%t backup insert=%t backup select=%t backup cursor=%t backup project=%t readonly cursor=%t readonly jobs=%t readonly job view=%t readonly credentials=%t/%t/%t/%t/%t/%t/%t physical runtime select/write=%t/%t/%t/%t readonly select/insert=%t/%t backup select/insert=%t/%t maintenance select/lease-write/admission-write=%t/%t/%t dashboard runtime session insert/delete=%t/%t usage insert/delete=%t/%t appearance insert/delete=%t/%t maintenance session/usage delete=%t/%t appearance insert=%t readonly session/usage insert=%t/%t appearance update=%t", canUpdateAudit, canUpdateGoose, canReadGoose, canUpdateEvent, canDeleteEvent, canUpdateLineage, canInsertLineageRevision, canPublishLineage, canUpdateServingBundle, backupInsert, backupSelect, backupCursor, backupProject, readonlyCursor, readonlyJobs, readonlyJobView, readonlySession, readonlyCredential, readonlyToken, readonlyServiceSecret, readonlyDesktopCode, readonlyDeviceAuth, readonlyAuthoringCredential, physicalRuntimeSelect, physicalRuntimeInsert, physicalRuntimeUpdate, physicalRuntimeDelete, physicalReadonlySelect, physicalReadonlyInsert, physicalBackupSelect, physicalBackupInsert, physicalMaintenanceSelect, physicalMaintenanceLeaseWrite, physicalMaintenanceAdmissionWrite, dashboardRuntimeSessionInsert, dashboardRuntimeSessionDelete, dashboardRuntimeUsageInsert, dashboardRuntimeUsageDelete, dashboardRuntimeAppearanceInsert, dashboardRuntimeAppearanceDelete, dashboardMaintenanceSessionDelete, dashboardMaintenanceUsageDelete, dashboardMaintenanceAppearanceInsert, dashboardReadonlySessionInsert, dashboardReadonlyUsageInsert, dashboardReadonlyAppearanceUpdate)
	}
	var runtimePublicUsage, readonlyPublicUsage, backupPublicUsage bool
	if err := db.QueryRow(ctx, `
		SELECT has_schema_privilege('leapview_control_runtime', 'public', 'USAGE'),
		       has_schema_privilege('leapview_control_readonly', 'public', 'USAGE'),
		       has_schema_privilege('leapview_control_backup', 'public', 'USAGE')`).
		Scan(&runtimePublicUsage, &readonlyPublicUsage, &backupPublicUsage); err != nil {
		t.Fatal(err)
	}
	if !runtimePublicUsage || !readonlyPublicUsage || !backupPublicUsage {
		t.Fatalf("Goose schema visibility missing: runtime=%t readonly=%t backup=%t", runtimePublicUsage, readonlyPublicUsage, backupPublicUsage)
	}
	var runtimeTargetTableUpdate, runtimeTargetLockColumn, runtimeTargetRevisionColumn bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'delivery.delivery_target', 'UPDATE'),
		       has_column_privilege('leapview_control_runtime', 'delivery.delivery_target', 'updated_at', 'UPDATE'),
		       has_column_privilege('leapview_control_runtime', 'delivery.delivery_target', 'target_revision', 'UPDATE')`).
		Scan(&runtimeTargetTableUpdate, &runtimeTargetLockColumn, &runtimeTargetRevisionColumn); err != nil {
		t.Fatal(err)
	}
	if runtimeTargetTableUpdate || !runtimeTargetLockColumn || runtimeTargetRevisionColumn {
		t.Fatalf("delivery target row-lock capability leaked: table=%t updated_at=%t target_revision=%t", runtimeTargetTableUpdate, runtimeTargetLockColumn, runtimeTargetRevisionColumn)
	}
	var runtimeCatalogIdentitySelect, runtimeDeliveryAttemptSelect, runtimeDeliverySealSelect, runtimeServingBundleSelect bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'ducklake.catalog_identity', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'delivery.delivery_build_attempt', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'delivery.delivery_snapshot_seal', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'serving_state.bundle', 'SELECT')`).
		Scan(&runtimeCatalogIdentitySelect, &runtimeDeliveryAttemptSelect, &runtimeDeliverySealSelect, &runtimeServingBundleSelect); err != nil {
		t.Fatal(err)
	}
	if !runtimeCatalogIdentitySelect || !runtimeDeliveryAttemptSelect || !runtimeDeliverySealSelect || !runtimeServingBundleSelect {
		t.Fatalf("native delivery identity reads missing: catalog_identity=%t delivery_attempt=%t delivery_seal=%t serving_bundle=%t", runtimeCatalogIdentitySelect, runtimeDeliveryAttemptSelect, runtimeDeliverySealSelect, runtimeServingBundleSelect)
	}
	var runtimeRootSelect, runtimeRootInsert, runtimeRootUpdate, runtimeRootDelete, runtimeRootLock, runtimeRootRetire, runtimeRootExpire, runtimeRootMaintain, maintenanceRootSelect, maintenanceRootLock, maintenanceRootExpire, maintenanceRootMaintain bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'delivery.delivery_retention_root', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'delivery.delivery_retention_root', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'delivery.delivery_retention_root', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'delivery.delivery_retention_root', 'DELETE'),
		       has_function_privilege('leapview_control_runtime', 'delivery.lock_retention_root(uuid)', 'EXECUTE'),
		       has_function_privilege('leapview_control_runtime', 'delivery.retire_retention_root(uuid)', 'EXECUTE'),
		       has_function_privilege('leapview_control_runtime', 'delivery.expire_retention_root(uuid,interval)', 'EXECUTE'),
		       has_function_privilege('leapview_control_runtime', 'delivery.maintain_retention_roots(text,text,interval,integer)', 'EXECUTE'),
		       has_table_privilege('leapview_control_maintenance', 'delivery.delivery_retention_root', 'SELECT'),
		       has_function_privilege('leapview_control_maintenance', 'delivery.lock_retention_root(uuid)', 'EXECUTE'),
		       has_function_privilege('leapview_control_maintenance', 'delivery.expire_retention_root(uuid,interval)', 'EXECUTE'),
		       has_function_privilege('leapview_control_maintenance', 'delivery.maintain_retention_roots(text,text,interval,integer)', 'EXECUTE')`).
		Scan(&runtimeRootSelect, &runtimeRootInsert, &runtimeRootUpdate, &runtimeRootDelete, &runtimeRootLock, &runtimeRootRetire, &runtimeRootExpire, &runtimeRootMaintain, &maintenanceRootSelect, &maintenanceRootLock, &maintenanceRootExpire, &maintenanceRootMaintain); err != nil {
		t.Fatal(err)
	}
	if !runtimeRootSelect || !runtimeRootInsert || runtimeRootUpdate || runtimeRootDelete || !runtimeRootLock || !runtimeRootRetire || runtimeRootExpire || runtimeRootMaintain || !maintenanceRootSelect || !maintenanceRootLock || !maintenanceRootExpire || !maintenanceRootMaintain {
		t.Fatalf("delivery retention-root capability leaked: runtime select/insert/update/delete/lock=%t/%t/%t/%t/%t retire/expire/maintain=%t/%t/%t maintenance select/lock/expire/maintain=%t/%t/%t/%t", runtimeRootSelect, runtimeRootInsert, runtimeRootUpdate, runtimeRootDelete, runtimeRootLock, runtimeRootRetire, runtimeRootExpire, runtimeRootMaintain, maintenanceRootSelect, maintenanceRootLock, maintenanceRootExpire, maintenanceRootMaintain)
	}
	var runtimeRetentionSelect, runtimeRetentionInsert, runtimeRetentionUpdate, runtimeRetentionDelete, runtimeRetentionExecute bool
	var readonlyRetentionExecute, maintenanceRetentionExecute, backupRetentionExecute bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'ducklake.snapshot_retention', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'ducklake.snapshot_retention', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'ducklake.snapshot_retention', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'ducklake.snapshot_retention', 'DELETE'),
		       has_function_privilege('leapview_control_runtime', 'ducklake.admit_snapshot_retention_from_seal(uuid)', 'EXECUTE'),
		       has_function_privilege('leapview_control_readonly', 'ducklake.admit_snapshot_retention_from_seal(uuid)', 'EXECUTE'),
		       has_function_privilege('leapview_control_maintenance', 'ducklake.admit_snapshot_retention_from_seal(uuid)', 'EXECUTE'),
		       has_function_privilege('leapview_control_backup', 'ducklake.admit_snapshot_retention_from_seal(uuid)', 'EXECUTE')`).Scan(&runtimeRetentionSelect, &runtimeRetentionInsert, &runtimeRetentionUpdate, &runtimeRetentionDelete, &runtimeRetentionExecute, &readonlyRetentionExecute, &maintenanceRetentionExecute, &backupRetentionExecute); err != nil {
		t.Fatal(err)
	}
	if !runtimeRetentionSelect || runtimeRetentionInsert || runtimeRetentionUpdate || runtimeRetentionDelete || !runtimeRetentionExecute || readonlyRetentionExecute || maintenanceRetentionExecute || backupRetentionExecute {
		t.Fatalf("snapshot retention capability leaked: runtime select/write=%t/%t/%t/%t execute=%t readonly/maintenance/backup execute=%t/%t/%t", runtimeRetentionSelect, runtimeRetentionInsert, runtimeRetentionUpdate, runtimeRetentionDelete, runtimeRetentionExecute, readonlyRetentionExecute, maintenanceRetentionExecute, backupRetentionExecute)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, source, operation, action, capability, outcome,
		     aggregate_key, aggregate_sequence, intent_digest)
		VALUES ('00000000-0000-0000-0000-000000000002', 'schema', 'test',
		        'schema.test', '', 'success', 'schema:test', 0,
		        'sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE audit.audit_event SET action = 'tampered' WHERE audit_id = '00000000-0000-0000-0000-000000000002'`); err == nil {
		t.Fatal("audit append-only trigger did not reject an update")
	}
	const retentionLockTarget = "target:runtime-retention-lock"
	const retentionLockRoot = "00000000-0000-0000-0000-000000000005"
	if _, err := db.Exec(ctx, `
		INSERT INTO delivery.delivery_target(target_id, project_id, environment)
		VALUES ($1, 'project:runtime-retention-lock', 'dev')`, retentionLockTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO delivery.delivery_retention_root(root_id, target_id, root_kind, state)
		VALUES ($1::uuid, $2, 'query', 'live')`, retentionLockRoot, retentionLockTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO event.event_log
		    (event_id, scope_id, aggregate_type, aggregate_id, aggregate_version,
		     event_type, schema_version, occurred_at, payload)
		VALUES ('01900000-0000-7000-8000-000000000004', 'schema', 'test',
		        'schema:test', 1, 'schema.test', 1,
		        clock_timestamp() - interval '2 hours', '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	// Exercise the append path as the non-superuser runtime role.  Trigger
	// execution must remain available even though the trigger function itself
	// is not callable by PUBLIC.
	runtimeConn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeConn.Release()
	if _, err := runtimeConn.Exec(ctx, `SET ROLE leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	var lockedTarget, lockedKind, lockedState string
	if err := runtimeConn.QueryRow(ctx, `SELECT target_id,root_kind,state FROM delivery.lock_retention_root($1::uuid)`, retentionLockRoot).Scan(&lockedTarget, &lockedKind, &lockedState); err != nil {
		t.Fatalf("runtime retention-root lock capability: %v", err)
	}
	if lockedTarget != retentionLockTarget || lockedKind != "query" || lockedState != "live" {
		t.Fatalf("runtime retention-root lock identity = %q/%q/%q", lockedTarget, lockedKind, lockedState)
	}
	if _, err := runtimeConn.Exec(ctx, `UPDATE delivery.delivery_retention_root SET state = state WHERE root_id = $1::uuid`, retentionLockRoot); err == nil {
		t.Fatal("runtime retention-root UPDATE unexpectedly succeeded")
	}
	if _, err := runtimeConn.Exec(ctx, `
		INSERT INTO dashboard.view_session
		    (id, project_id, publication_id, principal_or_client, dashboard_id,
		     serving_state_id, stream_instance_id, key_json, version, state_json, expires_at)
		VALUES ('dvs_privilege_runtime', 'project:privilege', '', 'principal:runtime',
		        'dashboard:runtime', 'state:runtime', 'stream:runtime',
		        '{"projectId":"project:privilege"}'::jsonb, 1, '{}'::jsonb,
		        clock_timestamp() + interval '1 hour')`); err != nil {
		t.Fatalf("runtime dashboard session insert: %v", err)
	}
	if _, err := runtimeConn.Exec(ctx, `
		INSERT INTO dashboard.view_day
		    (project_id, dashboard_id, principal_id, viewed_on, page_id, first_viewed_at, last_viewed_at)
		VALUES ('project:privilege', 'dashboard:runtime', 'principal:runtime', current_date,
		        'page:runtime', clock_timestamp(), clock_timestamp())`); err != nil {
		t.Fatalf("runtime dashboard usage insert: %v", err)
	}
	if _, err := runtimeConn.Exec(ctx, `
		INSERT INTO dashboard.appearance_override
		    (project_id, dashboard_id, icon, color, updated_by)
		VALUES ('project:privilege', 'dashboard:runtime', 'layout-dashboard', 'blue', 'principal:runtime')`); err != nil {
		t.Fatalf("runtime dashboard appearance insert: %v", err)
	}
	if _, err := runtimeConn.Exec(ctx, `DELETE FROM dashboard.view_session WHERE id = 'dvs_privilege_runtime'`); err == nil {
		t.Fatal("runtime dashboard session delete unexpectedly succeeded")
	}
	if _, err := runtimeConn.Exec(ctx, `DELETE FROM dashboard.view_day WHERE project_id = 'project:privilege'`); err == nil {
		t.Fatal("runtime dashboard usage delete unexpectedly succeeded")
	}
	if _, err := runtimeConn.Exec(ctx, `DELETE FROM dashboard.appearance_override WHERE project_id = 'project:privilege'`); err == nil {
		t.Fatal("runtime dashboard appearance delete unexpectedly succeeded")
	}
	if _, err := runtimeConn.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, source, operation, action, capability, outcome,
		     aggregate_key, aggregate_sequence, intent_digest)
		VALUES ('00000000-0000-0000-0000-000000000003', 'runtime', 'test',
		        'runtime.test', '', 'success', 'runtime:test', 0,
		        'sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err != nil {
		t.Fatalf("runtime audit append: %v", err)
	}
	if _, err := runtimeConn.Exec(ctx, `DELETE FROM event.event_log WHERE event_id = '01900000-0000-7000-8000-000000000004'`); err == nil {
		t.Fatal("runtime direct event delete unexpectedly succeeded")
	}
	if err := runtimeConn.QueryRow(ctx, `SELECT event.prune_event_log(clock_timestamp(), 10)`).Scan(new(int64)); err == nil {
		t.Fatal("runtime event prune unexpectedly succeeded")
	}
	maintenanceConn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenanceConn.Release()
	if _, err := maintenanceConn.Exec(ctx, `SET ROLE leapview_control_maintenance`); err != nil {
		t.Fatal(err)
	}
	var dashboardDeleted int64
	if _, err := db.Exec(ctx, `
		INSERT INTO dashboard.view_session
		    (id, project_id, publication_id, principal_or_client, dashboard_id,
		     serving_state_id, stream_instance_id, key_json, version, state_json, expires_at)
		VALUES ('dvs_privilege_expired', 'project:privilege', '', 'principal:expired',
		        'dashboard:expired', 'state:expired', 'stream:expired',
		        '{"projectId":"project:privilege"}'::jsonb, 1, '{}'::jsonb,
		        clock_timestamp() - interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	if err := maintenanceConn.QueryRow(ctx, `
		WITH victims AS (
			SELECT id FROM dashboard.view_session
			WHERE expires_at <= clock_timestamp()
			ORDER BY expires_at, id LIMIT 1
		)
		DELETE FROM dashboard.view_session AS s USING victims
		WHERE s.id = victims.id
		RETURNING 1`).Scan(new(int)); err != nil {
		t.Fatalf("maintenance bounded session delete: %v", err)
	} else {
		dashboardDeleted++
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO dashboard.view_day
		    (project_id, dashboard_id, principal_id, viewed_on, page_id, first_viewed_at, last_viewed_at)
		VALUES ('project:privilege', 'dashboard:expired', 'principal:expired', current_date - 100,
		        'page:expired', clock_timestamp() - interval '2 hours', clock_timestamp() - interval '2 hours')`); err != nil {
		t.Fatal(err)
	}
	if err := maintenanceConn.QueryRow(ctx, `
		WITH victims AS (
			SELECT project_id, dashboard_id, principal_id, viewed_on
			FROM dashboard.view_day
			WHERE viewed_on < current_date - 90
			ORDER BY viewed_on, project_id, dashboard_id, principal_id LIMIT 1
		)
		DELETE FROM dashboard.view_day AS d USING victims
		WHERE d.project_id = victims.project_id AND d.dashboard_id = victims.dashboard_id
		  AND d.principal_id = victims.principal_id AND d.viewed_on = victims.viewed_on
		RETURNING 1`).Scan(new(int)); err != nil {
		t.Fatalf("maintenance bounded usage delete: %v", err)
	} else {
		dashboardDeleted++
	}
	if dashboardDeleted != 2 {
		t.Fatalf("maintenance bounded dashboard deletes = %d, want 2", dashboardDeleted)
	}
	if _, err := maintenanceConn.Exec(ctx, `DELETE FROM dashboard.appearance_override WHERE project_id = 'project:privilege'`); err == nil {
		t.Fatal("maintenance dashboard appearance delete unexpectedly succeeded")
	}
	var pruned int64
	if err := maintenanceConn.QueryRow(ctx, `SELECT event.prune_event_log(clock_timestamp(), 10)`).Scan(&pruned); err != nil {
		t.Fatalf("maintenance event prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("runtime event prune removed %d rows, want 1", pruned)
	}
	var remaining int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_log WHERE event_id = '01900000-0000-7000-8000-000000000004'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("pruned event still exists (%d rows)", remaining)
	}
	readonlyConn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer readonlyConn.Release()
	if _, err := readonlyConn.Exec(ctx, `SET ROLE leapview_control_readonly`); err != nil {
		t.Fatal(err)
	}
	if _, err := readonlyConn.Exec(ctx, `UPDATE dashboard.appearance_override SET color = 'red' WHERE project_id = 'project:privilege'`); err == nil {
		t.Fatal("readonly dashboard appearance update unexpectedly succeeded")
	}

	// PostgreSQL enforces the JSON document boundary at the persistence edge.
	_, err = db.Exec(ctx, `
		INSERT INTO access.principal (id, principal_type, status, attributes)
		VALUES ('00000000-0000-0000-0000-000000000001', 'user', 'active', $1::jsonb)`, `{"oversized":"`+strings.Repeat("x", 20000)+`"}`)
	if err == nil {
		t.Fatal("oversized principal attributes unexpectedly accepted")
	}
}
