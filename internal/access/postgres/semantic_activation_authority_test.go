package postgres

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSemanticActivationAuthorityLocksControlStateThroughCommit(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.admin, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	activationTx, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = activationTx.Rollback(t.Context()) })
	registry, control, err := repo.SemanticAttributeActivationAuthorityTx(t.Context(), activationTx)
	if err != nil {
		t.Fatal(err)
	}
	if registry.State.Digest == "" || control.State.Digest == "" {
		t.Fatalf("locked activation authority is incomplete: registry=%#v control=%#v", registry.State, control.State)
	}

	assertBlocked := func(name string, lock func() error) {
		t.Helper()
		if err := lock(); err == nil {
			t.Fatalf("%s mutation lock crossed activation transaction", name)
		} else {
			var postgresErr *pgconn.PgError
			if !errors.As(err, &postgresErr) || postgresErr.Code != "55P03" {
				t.Fatalf("%s lock error = %v, want lock_not_available", name, err)
			}
		}
	}

	registryTx, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registryTx.Exec(t.Context(), `SET LOCAL lock_timeout = '100ms'`); err != nil {
		t.Fatal(err)
	}
	assertBlocked("registry", func() error {
		_, err := accessdb.New(registryTx).LockSemanticAttributeRegistry(t.Context())
		return err
	})
	_ = registryTx.Rollback(t.Context())

	controlTx, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controlTx.Exec(t.Context(), `SET LOCAL lock_timeout = '100ms'`); err != nil {
		t.Fatal(err)
	}
	assertBlocked("control", func() error {
		_, err := accessdb.New(controlTx).LockSemanticAttributeControlState(t.Context())
		return err
	})
	_ = controlTx.Rollback(t.Context())

	if err := activationTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	released, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = released.Rollback(t.Context()) }()
	if _, err := accessdb.New(released).LockSemanticAttributeControlState(t.Context()); err != nil {
		t.Fatalf("activation authority lock survived transaction rollback: %v", err)
	}
}

func TestSemanticActivationAuditRollsBackWithActivationTransaction(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.admin, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "activation-audit@example.com", Password: "activation audit password"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID("semantic_orders"), projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	event := access.CanonicalAuditEvent{
		Identity: projectgraph.ServingIdentity{
			ProjectID: projectgraph.ResourceID("project_demo"), Environment: "production", GenerationID: "generation-1",
		},
		PrincipalID: principal.Principal.ID, Action: "semantic_access.activation_admitted", Resource: resource,
		Capability: access.CapabilityResourceManage, Status: "success", MetadataJSON: `{}`,
	}
	if err := repo.RecordCanonicalAuditEventTx(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='semantic_access.activation_admitted'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back activation audit rows = %d", count)
	}
}
