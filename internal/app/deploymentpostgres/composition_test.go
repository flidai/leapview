package deploymentpostgres

import (
	"context"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgresql "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type deploymentPostgresDBStub struct{}

func (deploymentPostgresDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (deploymentPostgresDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (deploymentPostgresDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (deploymentPostgresDBStub) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }

func deploymentPostgresAuthorities() Authorities {
	lineage, err := NewActivationLineageVerifier(lineagepostgres.New(deploymentPostgresDBStub{}))
	if err != nil {
		panic(err)
	}
	return Authorities{
		Access: accesspostgres.New(), Events: eventspostgres.New(),
		Jobs: jobspostgres.NewRepository(nil), Operations: operationpostgres.New(nil),
		Lineage:           lineage,
		ApprovalAuthorize: deploymentpostgresql.ApprovalAuthorizerFunc(func(context.Context, deploymentpostgresql.ApprovalAuthorizationInput) error { return nil }),
	}
}

func TestNewPersistenceBuildsNativeBundle(t *testing.T) {
	persistence, err := NewPersistence(deploymentPostgresDBStub{}, deploymentPostgresAuthorities())
	if err != nil {
		t.Fatal(err)
	}
	if persistence.Repository == nil || persistence.Candidates == nil || persistence.ProjectClaims == nil || persistence.DeliveryReader == nil || persistence.Activation == nil || persistence.Events == nil || persistence.Audit == nil || persistence.Workflow == nil || persistence.Operations == nil || persistence.Approval == nil {
		t.Fatalf("native deployment bundle is incomplete: %#v", persistence)
	}
	if !persistence.Repository.Configured() || !persistence.Repository.EventCapable() {
		t.Fatal("native deployment repository is not configured")
	}

	// Keep this test tied to the module's public persistence contract rather
	// than to the concrete repository implementation details.
	var _ deploymentmodule.Persistence = persistence
}

func TestNewPersistenceRejectsMissingAuthority(t *testing.T) {
	base := deploymentPostgresAuthorities()
	cases := []struct {
		name string
		edit func(*Authorities)
	}{
		{name: "control", edit: func(_ *Authorities) {}},
		{name: "access", edit: func(a *Authorities) { a.Access = nil }},
		{name: "event", edit: func(a *Authorities) { a.Events = nil }},
		{name: "jobs", edit: func(a *Authorities) { a.Jobs = nil }},
		{name: "operation", edit: func(a *Authorities) { a.Operations = nil }},
		{name: "lineage", edit: func(a *Authorities) { a.Lineage = nil }},
		{name: "approval", edit: func(a *Authorities) { a.ApprovalAuthorize = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authorities := base
			tc.edit(&authorities)
			control := deploymentPostgresDBStub{}
			if tc.name == "control" {
				if _, err := NewPersistence(nil, authorities); err == nil || !strings.Contains(err.Error(), "control pool") {
					t.Fatalf("missing control error = %v", err)
				}
				return
			}
			if _, err := NewPersistence(control, authorities); err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("missing %s error = %v", tc.name, err)
			}
		})
	}
}
