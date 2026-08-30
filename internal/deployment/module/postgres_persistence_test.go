package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type deploymentDBStub struct{}

func (deploymentDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (deploymentDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (deploymentDBStub) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (deploymentDBStub) Begin(context.Context) (pgx.Tx, error)                   { return nil, nil }

type readOnlyDeploymentDBStub struct{}

func (readOnlyDeploymentDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (readOnlyDeploymentDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (readOnlyDeploymentDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type activationAuditStub struct{}

func (activationAuditStub) AppendActivationAudit(context.Context, postgres.Tx, postgres.ActivationAuditInput) (postgres.AuditEvent, error) {
	return postgres.AuditEvent{}, nil
}
func (activationAuditStub) GetActivationAudit(context.Context, postgres.Tx, postgres.ActivationAuditInput) (postgres.AuditEvent, error) {
	return postgres.AuditEvent{}, nil
}

func TestNewPostgresPersistenceWiresNativeSurfaces(t *testing.T) {
	repository := postgres.NewWithOptions(deploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.validate(); err != nil {
		t.Fatal(err)
	}
	if persistence.Repository != repository || persistence.Candidates == nil || persistence.ProjectClaims == nil || persistence.DeliveryReader == nil || persistence.Activation == nil {
		t.Fatalf("native surfaces were not wired: %#v", persistence)
	}
}

func TestNewPostgresPersistenceRejectsNil(t *testing.T) {
	if _, err := NewPostgresPersistence(nil); err == nil {
		t.Fatal("expected nil repository rejection")
	}
}

func TestNewPostgresPersistenceRejectsNonTransactionalHandle(t *testing.T) {
	repository := postgres.NewWithOptions(readOnlyDeploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	if _, err := NewPostgresPersistence(repository); err == nil {
		t.Fatal("expected non-transactional PostgreSQL handle rejection")
	}
}

func TestBuildProductionNativePersistenceExposesModule(t *testing.T) {
	repository := postgres.NewWithOptions(deploymentDBStub{}, postgres.Options{ActivationAudit: activationAuditStub{}})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Build(t.Context(), Config{Persistence: &persistence, Production: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.NativePersistence() != &persistence {
		t.Fatal("module did not expose native persistence")
	}
	_, err = unsupportedCoordinator{}.Create(t.Context(), apiadapter.CreateRequest{})
	var unsupported *UnsupportedCapabilityError
	if !errors.As(err, &unsupported) {
		t.Fatalf("native HTTP coordinator error = %v, want UnsupportedCapabilityError", err)
	}
}
