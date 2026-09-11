package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	appaccesspostgres "github.com/flidai/leapview/internal/app/accesspostgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authorizationPolicyUpgradeTargetID      = "target-upgrade"
	authorizationPolicyUpgradeProjectID     = "project-upgrade"
	authorizationPolicyUpgradeEnvironment   = "production"
	authorizationPolicyUpgradeGenerationID  = "80000000-0000-0000-0000-000000000001"
	authorizationPolicyUpgradePlanID        = "80000000-0000-0000-0000-000000000002"
	authorizationPolicyUpgradeCandidateID   = "80000000-0000-0000-0000-000000000003"
	authorizationPolicyUpgradeAttemptID     = "80000000-0000-0000-0000-000000000004"
	authorizationPolicyUpgradeSealID        = "80000000-0000-0000-0000-000000000005"
	authorizationPolicyUpgradePublicationID = "80000000-0000-0000-0000-000000000006"
	authorizationPolicyUpgradeBindingID     = "binding-owner"
	authorizationPolicyUpgradePrincipalID   = "70000000-0000-0000-0000-000000000001"
)

func authorizationPolicyUpgradeDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func authorizationPolicyUpgradeDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "authorization_policy_upgrade")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rollback := func() { _ = tx.Rollback(t.Context()) }
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		rollback()
		t.Fatalf("apply access schema: %v", err)
	}
	if _, err := tx.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		rollback()
		t.Fatalf("apply event schema: %v", err)
	}
	if err := deploymentpostgres.ApplySchema(t.Context(), tx); err != nil {
		rollback()
		t.Fatalf("apply delivery schema: %v", err)
	}
	if err := servingstatepostgres.ApplySchema(t.Context(), tx); err != nil {
		rollback()
		t.Fatalf("apply serving-state schema: %v", err)
	}
	if err := ducklakepostgres.ApplySchema(t.Context(), tx); err != nil {
		rollback()
		t.Fatalf("apply DuckLake schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedAuthorizationPolicyUpgradeFixture(t *testing.T, db *pgxpool.Pool) {
	seedAuthorizationPolicyUpgradeFixtureWithPolicy(t, db, `{"roleBindings":{"binding-owner":{"id":"binding-owner","name":"Existing owner","role":"owner","subject":{"kind":"principal","principalId":"70000000-0000-0000-0000-000000000001"}}}}`)
}

func seedAuthorizationPolicyUpgradeFixtureWithPolicy(t *testing.T, db *pgxpool.Pool, policyJSON string) {
	t.Helper()
	ctx := t.Context()
	targets := deploymentpostgres.New(db)
	if _, err := targets.CreateTarget(ctx, deploymentpostgres.TargetInput{
		TargetID: authorizationPolicyUpgradeTargetID, ProjectID: authorizationPolicyUpgradeProjectID, Environment: authorizationPolicyUpgradeEnvironment,
	}); err != nil {
		t.Fatalf("create delivery target: %v", err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed authorization policy upgrade fixture: %v", err)
		}
	}
	planDigest := authorizationPolicyUpgradeDigest('a')
	artifactDigest := authorizationPolicyUpgradeDigest('b')
	graphDigest := authorizationPolicyUpgradeDigest('c')
	configDigest := authorizationPolicyUpgradeDigest('d')
	securityDigest := authorizationPolicyUpgradeDigest('e')
	rootDigest := authorizationPolicyUpgradeDigest('f')
	requestDigest := authorizationPolicyUpgradeDigest('1')
	exec(`INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_digest,qualification_required,approval_required,approval_policy_revision,plan_document)
VALUES($1::uuid,$2,1,$3,$4,$5,$6,$7,$8,false,false,1,'{}'::jsonb)`, authorizationPolicyUpgradePlanID, authorizationPolicyUpgradeTargetID, planDigest, graphDigest, configDigest, securityDigest, artifactDigest, planDigest)
	exec(`INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,status,candidate_revision,artifact_digest)
VALUES($1::uuid,$2,$3::uuid,'building',1,$4)`, authorizationPolicyUpgradeCandidateID, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradePlanID, artifactDigest)
	exec(`INSERT INTO delivery.delivery_build_attempt(attempt_id,plan_id,candidate_id,owner_id,physical_pool_id,catalog_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity,snapshot_id,commit_marker,finished_at)
VALUES($1::uuid,$2::uuid,$3::uuid,'builder','upgrade-pool','upgrade-catalog',1,$4,$5,'committed','candidate/upgrade',clock_timestamp()+interval '1 hour','upgrade-session',1,'{"committed":true}'::jsonb,clock_timestamp())`, authorizationPolicyUpgradeAttemptID, authorizationPolicyUpgradePlanID, authorizationPolicyUpgradeCandidateID, requestDigest, planDigest)
	exec(`INSERT INTO delivery.delivery_snapshot_seal(seal_id,attempt_id,candidate_id,physical_pool_id,tenant_domain,region,encryption_domain,object_namespace,catalog_database,catalog_id,catalog_uuid,catalog_version,ducklake_snapshot_id,relation_namespace,relation_manifest_digest,closure_digest,object_root,object_root_digest,artifact_root,artifact_root_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,request_digest,plan_digest,compatibility_digest,serving_artifact_id,serving_artifact_digest,duckdb_version,runtime_version,ducklake_extension_version,ducklake_spec_version,catalog_schema_version,qualification_evidence)
VALUES($1::uuid,$2::uuid,$3::uuid,'upgrade-pool','upgrade-tenant','test-region','upgrade-encryption','objects/upgrade','ducklake','upgrade-catalog',$4::uuid,1,1,'candidate/upgrade',$5,$6,'objects/upgrade',$7,'artifacts/upgrade',$8,$9,$10,$11,$12,$13,$14,'artifact-'||substr($15,8),$15,'1','runtime','1','1','1','{}'::jsonb)`, authorizationPolicyUpgradeSealID, authorizationPolicyUpgradeAttemptID, authorizationPolicyUpgradeCandidateID, "80000000-0000-0000-0000-000000000007", authorizationPolicyUpgradeDigest('2'), authorizationPolicyUpgradeDigest('3'), rootDigest, rootDigest, graphDigest, configDigest, securityDigest, requestDigest, planDigest, artifactDigest, artifactDigest)
	exec(`UPDATE delivery.delivery_candidate
SET snapshot_seal_id=$2::uuid,status='qualified',qualification_digest=$3,qualified_at=clock_timestamp()
WHERE candidate_id=$1::uuid`, authorizationPolicyUpgradeCandidateID, authorizationPolicyUpgradeSealID, authorizationPolicyUpgradeDigest('4'))
	exec(`INSERT INTO delivery.delivery_generation(generation_id,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision)
VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6,'artifacts/upgrade',$7,$8,$9,$10,$11,1)`, authorizationPolicyUpgradeGenerationID, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradeCandidateID, authorizationPolicyUpgradeSealID, authorizationPolicyUpgradePlanID, planDigest, rootDigest, artifactDigest, graphDigest, configDigest, securityDigest)
	exec(`INSERT INTO delivery.delivery_publication(publication_id,target_id,generation_id,candidate_id,snapshot_seal_id,expected_target_revision,result_target_revision,actor_id,state,request_digest,committed_at)
VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,1,1,'upgrade-test','committed',$6,clock_timestamp())`, authorizationPolicyUpgradePublicationID, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradeGenerationID, authorizationPolicyUpgradeCandidateID, authorizationPolicyUpgradeSealID, requestDigest)
	exec(`INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id)
VALUES($1,$2::uuid,$3::uuid)`, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradeGenerationID, authorizationPolicyUpgradePublicationID)
	exec(`INSERT INTO serving_state.bundle(generation_id,project_id,environment,artifact_id,artifact_digest,compiled_graph_digest,artifact_format,artifact_locator,storage_security_domain,artifact_content_type,artifact_metadata_digest,manifest_json,project_digest,access_policy_json,dashboard_publications_json,dashboard_appearances_json,size_bytes,created_by)
VALUES($1::uuid,$2,$3,'artifact-'||substr($4,8),$4,$5,'tar.gz','serving-artifacts/'||substr($4,8)||'.tar.gz','upgrade-runtime','application/gzip',$6,'{}'::jsonb,$7,$8::jsonb,'{}'::jsonb,'{}'::jsonb,1,'upgrade-test')`, authorizationPolicyUpgradeGenerationID, authorizationPolicyUpgradeProjectID, authorizationPolicyUpgradeEnvironment, artifactDigest, graphDigest, authorizationPolicyUpgradeDigest('6'), authorizationPolicyUpgradeDigest('7'), policyJSON)
}

func TestInitializeActiveTargetAuthorizationPolicyImportsExactActiveScope(t *testing.T) {
	db := authorizationPolicyUpgradeDB(t)
	seedAuthorizationPolicyUpgradeFixture(t, db)
	targets := deploymentpostgres.New(db)
	states := servingstatepostgres.New(db)
	scope := access.AuthorizationPolicyScope{TargetID: authorizationPolicyUpgradeTargetID, ProjectID: authorizationPolicyUpgradeProjectID, Environment: authorizationPolicyUpgradeEnvironment}
	policies, err := accesspostgres.NewAuthorizationPolicyRepository(db, scope)
	if err != nil {
		t.Fatal(err)
	}
	begin := func(ctx context.Context) (accesspostgres.Tx, error) { return db.Begin(ctx) }
	if err := appaccesspostgres.InitializeActiveTargetAuthorizationPolicy(t.Context(), begin, targets, states, policies, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradeEnvironment); err != nil {
		t.Fatal(err)
	}
	// An already initialized target is an exact replay/no-op, not a second
	// revision or a second immutable history row.
	if err := appaccesspostgres.InitializeActiveTargetAuthorizationPolicy(t.Context(), begin, targets, states, policies, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradeEnvironment); err != nil {
		t.Fatalf("authorization policy upgrade replay: %v", err)
	}
	policy, err := policies.AuthorizationPolicy(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 1 || len(policy.RoleBindings) != 1 {
		t.Fatalf("initialized policy = %#v, want revision 1 with one binding", policy)
	}
	binding := policy.RoleBindings[0]
	if binding.ID != authorizationPolicyUpgradeBindingID || binding.Name != "Existing owner" || binding.Role != access.ProjectRoleOwner || binding.Subject.Kind != access.SubjectKindPrincipal || binding.Subject.ID != authorizationPolicyUpgradePrincipalID {
		t.Fatalf("initialized binding = %#v, want exact active serving binding", binding)
	}
	var sourceGeneration string
	if err := db.QueryRow(t.Context(), `SELECT source_generation_id FROM access.authorization_policy_revision WHERE target_id=$1 AND project_id=$2 AND environment=$3 AND revision=1`, scope.TargetID, scope.ProjectID, scope.Environment).Scan(&sourceGeneration); err != nil {
		t.Fatal(err)
	}
	if sourceGeneration != authorizationPolicyUpgradeGenerationID {
		t.Fatalf("source generation = %q, want %q", sourceGeneration, authorizationPolicyUpgradeGenerationID)
	}
	var revisionCount int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM access.authorization_policy_revision WHERE target_id=$1 AND project_id=$2 AND environment=$3`, scope.TargetID, scope.ProjectID, scope.Environment).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 1 {
		t.Fatalf("policy history rows = %d, want 1", revisionCount)
	}
}

func TestInitializeActiveTargetAuthorizationPolicyLeavesLegacyEmptyPolicyUninitialized(t *testing.T) {
	db := authorizationPolicyUpgradeDB(t)
	seedAuthorizationPolicyUpgradeFixtureWithPolicy(t, db, `{}`)
	targets := deploymentpostgres.New(db)
	states := servingstatepostgres.New(db)
	scope := access.AuthorizationPolicyScope{TargetID: authorizationPolicyUpgradeTargetID, ProjectID: authorizationPolicyUpgradeProjectID, Environment: authorizationPolicyUpgradeEnvironment}
	policies, err := accesspostgres.NewAuthorizationPolicyRepository(db, scope)
	if err != nil {
		t.Fatal(err)
	}
	begin := func(ctx context.Context) (accesspostgres.Tx, error) { return db.Begin(ctx) }
	if err := appaccesspostgres.InitializeActiveTargetAuthorizationPolicy(t.Context(), begin, targets, states, policies, authorizationPolicyUpgradeTargetID, authorizationPolicyUpgradeEnvironment); err != nil {
		t.Fatal(err)
	}
	if _, err := policies.AuthorizationPolicy(t.Context(), scope); !errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
		t.Fatalf("empty legacy policy initialization error = %v, want policy to remain absent for explicit bootstrap", err)
	}
	const principalID = "70000000-0000-0000-0000-000000000009"
	if _, err := db.Exec(t.Context(), `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, principalID); err != nil {
		t.Fatalf("create bootstrap principal: %v", err)
	}
	binding := access.RoleBinding{
		ID: "project-bootstrap-owner", Name: "Project bootstrap owner",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
		Role:    access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin),
	}
	policy, err := policies.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{
		Scope: scope, Binding: binding, ExpectedRevision: 0, IdempotencyKey: "project-bootstrap-owner-" + principalID,
	})
	if err != nil {
		t.Fatalf("establish bootstrap policy: %v", err)
	}
	if policy.Revision != 1 || len(policy.RoleBindings) != 1 || policy.RoleBindings[0].ID != binding.ID {
		t.Fatalf("bootstrap policy = %+v, want owner binding at revision 1", policy)
	}
}

func TestAuthorizationPolicyUpgradeShareLockBlocksActivationUpdate(t *testing.T) {
	db := authorizationPolicyUpgradeDB(t)
	seedAuthorizationPolicyUpgradeFixture(t, db)
	targets := deploymentpostgres.New(db)
	shareTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer shareTx.Rollback(t.Context())
	if _, err := targets.TargetForShareTx(t.Context(), shareTx, authorizationPolicyUpgradeTargetID); err != nil {
		t.Fatal(err)
	}
	activationTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer activationTx.Rollback(t.Context())
	updateStarted := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		close(updateStarted)
		_, updateErr := activationTx.Exec(t.Context(), `UPDATE delivery.delivery_target SET target_revision=target_revision+1,updated_at=clock_timestamp() WHERE target_id=$1`, authorizationPolicyUpgradeTargetID)
		updateDone <- updateErr
	}()
	<-updateStarted
	select {
	case updateErr := <-updateDone:
		t.Fatalf("activation-style target update ran before share-lock release: %v", updateErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err := shareTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatalf("activation-style target update after share-lock release: %v", err)
	}
	if err := activationTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRow(t.Context(), `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1`, authorizationPolicyUpgradeTargetID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("target revision after activation-style update = %d, want 2", revision)
	}
}

func TestAuthorizationRoleBindingsFromServingPolicyRejectsUnsupportedOrAmbiguousEvidence(t *testing.T) {
	for _, encoded := range []string{
		`{"grants":{"grant":{"id":"grant"}}}`,
		`{"roleBindings":{"key":{"id":"other","name":"Viewer","role":"viewer","subject":{"kind":"principal","principalId":"principal"}}}}`,
		`{"roleBindings":{"key":{"id":"key","name":"Viewer","role":"viewer","subject":{"kind":"principal","principalId":"principal","email":"invented@example.com"}}}}`,
		`{"unknown":true}`,
	} {
		if _, err := projectmodule.DecodeAuthorizationRoleBindingsJSON(encoded); err == nil {
			t.Fatalf("accepted unsupported active serving policy %s", encoded)
		}
	}
}
