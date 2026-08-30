package app

import (
	"context"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
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

func deploymentPostgresAuthorities() DeploymentPostgresAuthorities {
	return DeploymentPostgresAuthorities{
		Access: accesspostgres.New(), Events: eventspostgres.New(),
		Jobs: jobspostgres.NewRepository(nil), Operations: operationpostgres.New(nil),
	}
}

func TestNewDeploymentPostgresPersistenceBuildsNativeBundle(t *testing.T) {
	persistence, err := NewDeploymentPostgresPersistence(deploymentPostgresDBStub{}, deploymentPostgresAuthorities())
	if err != nil {
		t.Fatal(err)
	}
	if persistence.Repository == nil || persistence.Candidates == nil || persistence.ProjectClaims == nil || persistence.DeliveryReader == nil || persistence.Activation == nil || persistence.Events == nil || persistence.Audit == nil || persistence.Workflow == nil || persistence.Operations == nil {
		t.Fatalf("native deployment bundle is incomplete: %#v", persistence)
	}
	if !persistence.Repository.Configured() {
		t.Fatal("native deployment repository is not configured")
	}
}

func TestNewDeploymentPostgresPersistenceRejectsMissingAuthority(t *testing.T) {
	base := deploymentPostgresAuthorities()
	cases := []struct {
		name string
		edit func(*DeploymentPostgresAuthorities)
	}{
		{name: "control", edit: func(_ *DeploymentPostgresAuthorities) {}},
		{name: "access", edit: func(a *DeploymentPostgresAuthorities) { a.Access = nil }},
		{name: "event", edit: func(a *DeploymentPostgresAuthorities) { a.Events = nil }},
		{name: "jobs", edit: func(a *DeploymentPostgresAuthorities) { a.Jobs = nil }},
		{name: "operation", edit: func(a *DeploymentPostgresAuthorities) { a.Operations = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authorities := base
			tc.edit(&authorities)
			control := deploymentPostgresDBStub{}
			if tc.name == "control" {
				control = deploymentPostgresDBStub{}
				if _, err := NewDeploymentPostgresPersistence(nil, authorities); err == nil || !strings.Contains(err.Error(), "control pool") {
					t.Fatalf("missing control error = %v", err)
				}
				return
			}
			if _, err := NewDeploymentPostgresPersistence(control, authorities); err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("missing %s error = %v", tc.name, err)
			}
		})
	}
}
