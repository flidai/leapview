package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func connectionBindingDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "binding-runtime", Login: true})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "binding-readonly", Login: true})
	database := h.NewDatabase(t, "connection_binding_test")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, readonly, "CONNECT")
	p, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func testBinding(t *testing.T) connectionbinding.TargetBinding {
	t.Helper()
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "warehouse.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: "infisical-project", Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse"},
		Enabled:             true, Now: time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

type accessAuditAdapter struct {
	repository *accesspostgres.AuditRepository
}

func (a accessAuditAdapter) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	_, err := a.repository.RecordAuditEvent(ctx, tx, intent)
	return err
}

func newAccessAuditAdapter() AuditRepository {
	return accessAuditAdapter{repository: accesspostgres.New()}
}

func TestRepositoryPostgreSQL18ExactReplayAndOptimisticConcurrency(t *testing.T) {
	db := connectionBindingDB(t)
	repository := New(db)
	ctx := t.Context()
	binding := testBinding(t)
	if err := repository.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(ctx, binding); !errors.Is(err, connectionbinding.ErrIncompatibleBinding) {
		t.Fatalf("exact replay = %v, want incompatible binding", err)
	}
	loaded, err := repository.Binding(ctx, binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != binding.ID || loaded.Revision != 1 || loaded.Endpoint.Host != binding.Endpoint.Host {
		t.Fatalf("loaded binding = %#v", loaded)
	}
	first, err := binding.MarkValidated("provider:v1", binding.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	configuration := binding.Configuration()
	configuration.Endpoint.Host = "warehouse-next.internal"
	second, err := binding.UpdateConfiguration(configuration, binding.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, candidate := range []connectionbinding.TargetBinding{first, second} {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repository.Save(ctx, candidate, binding.Revision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, connectionbinding.ErrIncompatibleBinding):
			conflict++
		default:
			t.Fatalf("concurrent save error = %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent saves success=%d conflict=%d, want 1/1", success, conflict)
	}
}

func TestRepositoryAllowsConnectorAndAuthenticationModeRotation(t *testing.T) {
	db := connectionBindingDB(t)
	repository := New(db)
	binding := testBinding(t)
	if err := repository.Create(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	configuration := binding.Configuration()
	configuration.ConnectorKind = "mysql"
	configuration.AuthenticationMode = connectionbinding.AuthenticationNone
	configuration.CredentialReference = connectionbinding.CredentialReference{}
	updated, err := binding.UpdateConfiguration(configuration, binding.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConnectorKind != "mysql" || updated.AuthenticationMode != connectionbinding.AuthenticationNone {
		t.Fatalf("updated binding = %#v", updated)
	}
	if _, err := repository.Save(t.Context(), updated, binding.Revision); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConnectorKind != "mysql" || loaded.AuthenticationMode != connectionbinding.AuthenticationNone {
		t.Fatalf("loaded rotated binding = %#v", loaded)
	}
}

func TestRepositoryProductionMutationCommitsBindingAndAuditTogether(t *testing.T) {
	db := connectionBindingDB(t)
	audit := newAccessAuditAdapter()
	repository, err := NewWithAudit(db, audit)
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	intent := access.AuditIntent{EventID: "01900000-0000-7000-8000-000000000021", Source: "analytics.connectionbinding", Operation: "createTargetConnectionBinding", PrincipalID: "", Action: "created", ResourceKind: "connection", ResourceID: binding.ConnectionID.String(), Capability: access.CapabilityResourceManage, Outcome: "success", AggregateKey: "connection_binding:" + binding.ID.String(), AggregateSequence: 1, MetadataJSON: `{"schemaVersion":1}`}
	if err := repository.Create(connectionbinding.WithAuditIntent(t.Context(), intent), binding); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit count = %d, want 1", count)
	}
}

func TestRepositoryProductionInternalSaveWithoutIntentRemainsUsable(t *testing.T) {
	db := connectionBindingDB(t)
	repository, err := NewWithAudit(db, newAccessAuditAdapter())
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	if err := repository.Create(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	updated, err := binding.MarkValidated("provider:v1", binding.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Save(t.Context(), updated, binding.Revision); err != nil {
		t.Fatalf("internal no-intent Save() = %v", err)
	}
}

type failingAuditRepository struct{ err error }

func (f failingAuditRepository) RecordAuditEvent(context.Context, Tx, access.AuditIntent) error {
	return f.err
}

func TestRepositoryProductionAuditFailureRollsBackBinding(t *testing.T) {
	db := connectionBindingDB(t)
	repository, err := NewWithAudit(db, failingAuditRepository{err: errors.New("audit unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	intent := access.AuditIntent{EventID: "01900000-0000-7000-8000-000000000022", Source: "analytics.connectionbinding", Operation: "createTargetConnectionBinding", Action: "created", ResourceKind: "connection", ResourceID: binding.ConnectionID.String(), Capability: access.CapabilityResourceManage, Outcome: "success", AggregateKey: "connection_binding:" + binding.ID.String(), AggregateSequence: 1, MetadataJSON: `{"schemaVersion":1}`}
	if err := repository.Create(connectionbinding.WithAuditIntent(t.Context(), intent), binding); err == nil {
		t.Fatal("audit failure unexpectedly committed binding")
	}
	if _, err := New(db).Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID); !errors.Is(err, connectionbinding.ErrBindingNotFound) {
		t.Fatalf("binding after audit rollback = %v", err)
	}
}

func TestRepositoryAuditedInternalPoolMutationIsRejectedBeforeSQL(t *testing.T) {
	db := connectionBindingDB(t)
	repository, err := NewWithAudit(db, newAccessAuditAdapter())
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	intent := access.AuditIntent{
		EventID: "01900000-0000-7000-8000-000000000024", Source: "analytics.connectionbinding",
		Operation: "createTargetConnectionBinding", Action: "created", ResourceKind: "connection",
		ResourceID: binding.ConnectionID.String(), Capability: access.CapabilityResourceManage,
		Outcome: "success", AggregateKey: "connection_binding:" + binding.ID.String(), AggregateSequence: 1,
		MetadataJSON: `{"schemaVersion":1}`,
	}
	// createTx retains a DBTX parameter for the unaudited pool-backed rotation
	// path. An audit intent must be rejected before that pool can execute SQL.
	if err := repository.createTx(connectionbinding.WithAuditIntent(t.Context(), intent), db, binding); !errors.Is(err, connectionbinding.ErrAdministrationAuditUnavailable) {
		t.Fatalf("audited pool mutation error = %v, want audit unavailable", err)
	}
	if _, err := New(db).Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID); !errors.Is(err, connectionbinding.ErrBindingNotFound) {
		t.Fatalf("binding after audited pool rejection = %v", err)
	}
}

func TestRepositoryMutationTxMethodsRejectNilReceiverAndTransaction(t *testing.T) {
	binding := connectionbinding.TargetBinding{}
	var repository *Repository
	if err := repository.CreateTx(context.Background(), nil, binding); !errors.Is(err, connectionbinding.ErrProviderUnavailable) {
		t.Fatalf("nil receiver CreateTx error = %v, want provider unavailable", err)
	}
	if _, err := repository.SaveTx(context.Background(), nil, binding, 1); !errors.Is(err, connectionbinding.ErrProviderUnavailable) {
		t.Fatalf("nil receiver SaveTx error = %v, want provider unavailable", err)
	}
}

func TestRepositoryPostgreSQLHealthEvidenceConstraints(t *testing.T) {
	db := connectionBindingDB(t)
	insert := func(id, target string, enabled bool, version, health, reason string, lastValidatedAt any) error {
		_, err := db.Exec(t.Context(), `
INSERT INTO connection_binding.target_connection_binding (
    id, target_id, connection_id, connector_kind, authentication_mode,
    project_id, environment, endpoint_json, enabled, validated_version,
    health, health_reason, last_validated_at, created_at, updated_at, revision
) VALUES ($1, $2, 'warehouse', 'postgres', 'none', 'sales', 'prod', '{}'::jsonb,
          $3, $4, $5, $6, $7::timestamptz,
          '2026-07-29T15:00:00Z'::timestamptz, '2026-07-29T15:00:00Z'::timestamptz, 1)`,
			id, target, enabled, version, health, reason, lastValidatedAt)
		return err
	}
	for _, test := range []struct {
		name, id, target, version, health, reason string
		enabled                                   bool
		lastValidatedAt                           any
	}{
		{name: "healthy requires version", id: "binding_health_1", target: "lvinst_health_1", enabled: true, health: "healthy", lastValidatedAt: "2026-07-29T15:00:00Z"},
		{name: "healthy requires timestamp", id: "binding_health_2", target: "lvinst_health_2", enabled: true, version: "provider:v1", health: "healthy"},
		{name: "degraded requires canonical diagnostic", id: "binding_health_3", target: "lvinst_health_3", enabled: true, health: "degraded", reason: "provider-unavailable"},
		{name: "pending cannot retain reason", id: "binding_health_4", target: "lvinst_health_4", enabled: true, health: "pending", reason: "PENDING"},
		{name: "disabled must be disabled", id: "binding_health_5", target: "lvinst_health_5", enabled: true, health: "disabled"},
		{name: "target id grammar", id: "binding_health_6", target: "lvinst health", enabled: true, health: "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := insert(test.id, test.target, test.enabled, test.version, test.health, test.reason, test.lastValidatedAt); err == nil {
				t.Fatal("invalid health evidence unexpectedly inserted")
			}
		})
	}
}

func TestRepositoryPostgreSQLIdentityEndpointAndCredentialConstraints(t *testing.T) {
	db := connectionBindingDB(t)
	tests := []struct {
		name, id, connectionID, connector, authentication, projectID, environment         string
		endpoint, credentialProject, credentialEnvironment, credentialPath, credentialKey string
	}{
		{name: "binding id grammar", id: "binding invalid", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{}`},
		{name: "connection id grammar", id: "binding_constraint_2", connectionID: "warehouse invalid", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{}`},
		{name: "registered connector", id: "binding_constraint_3", connectionID: "warehouse", connector: "not_registered", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{}`},
		{name: "project id grammar", id: "binding_constraint_4", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales invalid", environment: "prod", endpoint: `{}`},
		{name: "environment grammar", id: "binding_constraint_5", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod invalid", endpoint: `{}`},
		{name: "endpoint port", id: "binding_constraint_6", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{"port":65536}`},
		{name: "endpoint unknown field", id: "binding_constraint_7", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{"unknown":"value"}`},
		{name: "endpoint secret option", id: "binding_constraint_8", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{"options":{"password":"redacted"}}`},
		{name: "external credentials complete", id: "binding_constraint_9", connectionID: "warehouse", connector: "postgres", authentication: "external_bundle", projectID: "sales", environment: "prod", endpoint: `{}`},
		{name: "external credential path", id: "binding_constraint_10", connectionID: "warehouse", connector: "postgres", authentication: "external_bundle", projectID: "sales", environment: "prod", endpoint: `{}`, credentialProject: "credentials", credentialEnvironment: "prod", credentialPath: "relative", credentialKey: "warehouse"},
		{name: "nonexternal credential", id: "binding_constraint_11", connectionID: "warehouse", connector: "postgres", authentication: "none", projectID: "sales", environment: "prod", endpoint: `{}`, credentialProject: "credentials", credentialEnvironment: "prod", credentialPath: "/leapview/sales", credentialKey: "warehouse"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetID := fmt.Sprintf("lvinst_constraint_%d", index+1)
			_, err := db.Exec(t.Context(), `
INSERT INTO connection_binding.target_connection_binding (
    id, target_id, connection_id, connector_kind, authentication_mode,
    project_id, environment, endpoint_json,
    credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
    enabled, health, created_at, updated_at, revision
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12,
          true, 'pending', clock_timestamp(), clock_timestamp(), 1)`,
				test.id, targetID, test.connectionID, test.connector, test.authentication,
				test.projectID, test.environment, test.endpoint,
				test.credentialProject, test.credentialEnvironment, test.credentialPath, test.credentialKey,
			)
			if err == nil {
				t.Fatal("invalid connection binding unexpectedly inserted")
			}
		})
	}
}

func TestRepositoryPostgreSQLHealthEvidenceRoundTrip(t *testing.T) {
	db := connectionBindingDB(t)
	repository := New(db)
	binding := testBinding(t)
	if err := repository.Create(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Health != connectionbinding.HealthPending || loaded.ValidatedVersion != "" || !loaded.LastValidatedAt.IsZero() {
		t.Fatalf("pending binding = %#v", loaded)
	}

	validated, err := loaded.MarkValidated("provider:v1", loaded.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Save(t.Context(), validated, loaded.Revision); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Health != connectionbinding.HealthHealthy || loaded.ValidatedVersion != "provider:v1" || loaded.LastValidatedAt.IsZero() {
		t.Fatalf("healthy binding = %#v", loaded)
	}

	degraded, err := loaded.MarkDegraded("PROVIDER_UNAVAILABLE", loaded.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Save(t.Context(), degraded, loaded.Revision); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Health != connectionbinding.HealthDegraded || loaded.HealthReason != "PROVIDER_UNAVAILABLE" || loaded.ValidatedVersion != "provider:v1" {
		t.Fatalf("degraded binding = %#v", loaded)
	}

	disabled, err := loaded.Disable(loaded.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Save(t.Context(), disabled, loaded.Revision); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Health != connectionbinding.HealthDisabled || loaded.Enabled || loaded.HealthReason != "" {
		t.Fatalf("disabled binding = %#v", loaded)
	}
}

func TestRepositoryWithTxPreservesCallerRollbackForBindingAndAudit(t *testing.T) {
	db := connectionBindingDB(t)
	repository, err := NewWithAudit(db, newAccessAuditAdapter())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	intent := access.AuditIntent{EventID: "01900000-0000-7000-8000-000000000023", Source: "analytics.connectionbinding", Operation: "createTargetConnectionBinding", Action: "created", ResourceKind: "connection", ResourceID: binding.ConnectionID.String(), Capability: access.CapabilityResourceManage, Outcome: "success", AggregateKey: "connection_binding:" + binding.ID.String(), AggregateSequence: 1, MetadataJSON: `{"schemaVersion":1}`}
	if err := repository.WithTx(tx).Create(connectionbinding.WithAuditIntent(t.Context(), intent), binding); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).Binding(t.Context(), binding.Scope, binding.TargetID, binding.ConnectionID); !errors.Is(err, connectionbinding.ErrBindingNotFound) {
		t.Fatalf("binding after caller rollback = %v", err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("audit rows after caller rollback = %d, want 0", count)
	}
}

func TestRepositoryPostgreSQLRoleBoundary(t *testing.T) {
	t.Helper()
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "binding-runtime", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "binding-readonly", Login: true})
	database := h.NewDatabase(t, "connection_binding_roles")
	h.GrantDatabase(t, database.Name, runtimeRole, "CONNECT")
	h.GrantDatabase(t, database.Name, readonlyRole, "CONNECT")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	readonlyDB, err := pgxpool.New(t.Context(), database.URL(readonlyRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyDB.Close)
	if err := runtimeDB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := readonlyDB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	if err := New(runtimeDB).Create(t.Context(), binding); err != nil {
		t.Fatalf("runtime create = %v", err)
	}
	if _, err := readonlyDB.Exec(t.Context(), `INSERT INTO connection_binding.target_connection_binding (id,target_id,connection_id,connector_kind,authentication_mode,project_id,environment,endpoint_json,enabled,health,created_at,updated_at,revision) VALUES ('binding_readonly','lvinst_prod','reporting','postgres','none','sales','prod','{}',true,'pending',clock_timestamp(),clock_timestamp(),1)`); err == nil {
		t.Fatal("readonly role unexpectedly inserted a binding")
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM connection_binding.target_connection_binding`); err == nil {
		t.Fatal("runtime role unexpectedly deleted binding history")
	}
}

func TestRepositoryPostgreSQLTransactionRollback(t *testing.T) {
	db := connectionBindingDB(t)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	if err := New(tx).CreateTx(context.Background(), tx, binding); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).Binding(t.Context(), binding.Scope, binding.TargetID, projectgraph.ResourceID("warehouse")); !errors.Is(err, connectionbinding.ErrBindingNotFound) {
		t.Fatalf("rolled-back binding lookup = %v", err)
	}
}
