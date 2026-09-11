package app

import (
	"context"
	"database/sql"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	appaccesspostgres "github.com/flidai/leapview/internal/app/accesspostgres"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func authorizationPolicyUpgradeMigrationDB(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "authorization-policy-migration", Login: true})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "authorization-policy-runtime", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "authorization_policy_migration")
	h.GrantDatabase(t, database.Name, owner, "CREATE")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+database.Name+" OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator"); err != nil {
		t.Fatal(err)
	}
	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	provider, err := platformmigrations.NewProvider(migrationDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 8); err != nil {
		t.Fatalf("apply migrations through 008: %v", err)
	}
	seedAuthorizationPolicyUpgradeFixture(t, admin)
	// The supported upgrade boundary applies the pending Goose migration and
	// then reconciles cross-capability role policy before runtime starts.
	if err := postgresbaseline.Apply(ctx, migrationDB); err != nil {
		t.Fatalf("apply migrations 009-010 and product role policy: %v", err)
	}
	runtimeDB, err := pgxpool.New(ctx, database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	return admin, runtimeDB
}

func TestPostgresAuthorizationPolicyMigrationUpgradeRunsAsRuntime(t *testing.T) {
	admin, runtime := authorizationPolicyUpgradeMigrationDB(t)
	scope := access.AuthorizationPolicyScope{TargetID: authorizationPolicyUpgradeTargetID, ProjectID: authorizationPolicyUpgradeProjectID, Environment: authorizationPolicyUpgradeEnvironment}
	var runtimeHeadInsert, runtimeHeadUpdate, runtimeHistoryInsert, runtimeHistoryUpdate, backupSelect, backupInsert bool
	if err := admin.QueryRow(t.Context(), `
		SELECT has_table_privilege('leapview_control_runtime', 'access.authorization_policy', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'access.authorization_policy', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'access.authorization_policy_revision', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'access.authorization_policy_revision', 'UPDATE'),
		       has_table_privilege('leapview_control_backup', 'access.authorization_policy_revision', 'SELECT'),
		       has_table_privilege('leapview_control_backup', 'access.authorization_policy_revision', 'INSERT')`).
		Scan(&runtimeHeadInsert, &runtimeHeadUpdate, &runtimeHistoryInsert, &runtimeHistoryUpdate, &backupSelect, &backupInsert); err != nil {
		t.Fatal(err)
	}
	if !runtimeHeadInsert || !runtimeHeadUpdate || !runtimeHistoryInsert || runtimeHistoryUpdate || !backupSelect || backupInsert {
		t.Fatalf("authorization policy migration ACLs runtime head insert/update=%t/%t history insert/update=%t/%t backup select/insert=%t/%t", runtimeHeadInsert, runtimeHeadUpdate, runtimeHistoryInsert, runtimeHistoryUpdate, backupSelect, backupInsert)
	}
	targets := deploymentpostgres.New(runtime)
	states := servingstatepostgres.New(runtime)
	policies, err := accesspostgres.NewAuthorizationPolicyRepository(runtime, scope)
	if err != nil {
		t.Fatal(err)
	}
	begin := func(ctx context.Context) (accesspostgres.Tx, error) { return runtime.Begin(ctx) }
	if err := appaccesspostgres.InitializeActiveTargetAuthorizationPolicy(t.Context(), begin, targets, states, policies, scope.TargetID, scope.Environment); err != nil {
		t.Fatalf("startup authorization policy upgrade under runtime role: %v", err)
	}
	policy, err := policies.AuthorizationPolicy(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 1 || len(policy.RoleBindings) != 1 || policy.RoleBindings[0].ID != authorizationPolicyUpgradeBindingID || policy.RoleBindings[0].Subject.ID != authorizationPolicyUpgradePrincipalID {
		t.Fatalf("runtime-upgraded policy = %#v, want revision 1 with exact active binding", policy)
	}
	var sourceGeneration string
	if err := admin.QueryRow(t.Context(), `SELECT source_generation_id FROM access.authorization_policy_revision WHERE target_id=$1 AND project_id=$2 AND environment=$3 AND revision=1`, scope.TargetID, scope.ProjectID, scope.Environment).Scan(&sourceGeneration); err != nil {
		t.Fatal(err)
	}
	if sourceGeneration != authorizationPolicyUpgradeGenerationID {
		t.Fatalf("migration-upgraded source generation = %q, want %q", sourceGeneration, authorizationPolicyUpgradeGenerationID)
	}
}

func TestPostgresAuthorizationPolicyMigrationUpgradeFailsClosedOnMalformedServingEvidence(t *testing.T) {
	admin, runtime := authorizationPolicyUpgradeMigrationDB(t)
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.bundle DISABLE TRIGGER bundle_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE serving_state.bundle SET access_policy_json='{"unknown":true}'::jsonb WHERE generation_id=$1::uuid`, authorizationPolicyUpgradeGenerationID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.bundle ENABLE TRIGGER bundle_immutable`); err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: authorizationPolicyUpgradeTargetID, ProjectID: authorizationPolicyUpgradeProjectID, Environment: authorizationPolicyUpgradeEnvironment}
	targets := deploymentpostgres.New(runtime)
	states := servingstatepostgres.New(runtime)
	policies, err := accesspostgres.NewAuthorizationPolicyRepository(runtime, scope)
	if err != nil {
		t.Fatal(err)
	}
	begin := func(ctx context.Context) (accesspostgres.Tx, error) { return runtime.Begin(ctx) }
	if err := appaccesspostgres.InitializeActiveTargetAuthorizationPolicy(t.Context(), begin, targets, states, policies, scope.TargetID, scope.Environment); err == nil {
		t.Fatal("malformed serving authorization evidence unexpectedly upgraded")
	}
	var policyRows int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM access.authorization_policy WHERE target_id=$1 AND project_id=$2 AND environment=$3`, scope.TargetID, scope.ProjectID, scope.Environment).Scan(&policyRows); err != nil {
		t.Fatal(err)
	}
	if policyRows != 0 {
		t.Fatalf("malformed serving evidence left %d authorization policy heads", policyRows)
	}
}
