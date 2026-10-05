//go:build integration && duckdb_arrow

package module

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	"github.com/flidai/leapview/internal/platform/outbound"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestCredentialProbeChecksRealPostgresPasswordWithoutActivation(t *testing.T) {
	harness := postgrestest.StartTLS(t)
	role := harness.EnsureRole(t, postgrestest.Role{Name: "credential_probe_user", Password: "saved-probe-password", Login: true})
	database := harness.NewDatabase(t, "credential_probe")
	harness.GrantDatabase(t, database.Name, role, "CONNECT")
	config, err := pgx.ParseConfig(database.PrivateURL(role))
	if err != nil {
		t.Fatal(err)
	}
	binding := modulePoolBinding(t, time.Now().UTC())
	binding.Endpoint.Host = config.Host
	binding.Endpoint.Port = int(config.Port)
	binding.Endpoint.Database = config.Database
	binding.Endpoint.SourceIdentity = role.Name
	binding.Endpoint.TLSMode = "require"
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	catalog := &moduleBindingCatalog{binding: binding}
	factory, err := analyticsduckdb.NewTargetRuntimePoolFactory(analyticsduckdb.TargetRuntimePoolFactoryConfig{
		Open:       analyticsduckdb.NewIsolatedTargetRuntimeOpener(),
		Limits:     analyticsduckdb.TargetRuntimeLimits{MemoryMaxBytes: 64 << 20, TempMaxBytes: 16 << 20, MaxThreads: 1},
		RequireTLS: true, DestinationPolicy: outbound.New(outbound.ExplicitPrivate, outbound.Options{}),
		ExtensionAdmission: newModuleTestExtensionAdmission(t, "postgres"),
	})
	if err != nil {
		t.Fatal(err)
	}
	module := &Module{
		connectionBindings: catalog, connectionFactory: factory,
		targetID: binding.TargetID.String(), targetEnvironment: binding.Scope.Environment,
		targetClass: connectionbinding.TargetProduction, production: true,
	}
	if err := module.ProbeCredential(t.Context(), binding, uuid.NewString(), map[string]string{"password": role.Password}); err != nil {
		t.Fatalf("valid password probe: %v", err)
	}
	const wrongPassword = "invalid-probe-password"
	err = module.ProbeCredential(t.Context(), binding, uuid.NewString(), map[string]string{"password": wrongPassword})
	if !errors.Is(err, ErrCredentialProbeFailed) {
		t.Fatalf("wrong password probe = %v, want failed", err)
	}
	if strings.Contains(err.Error(), wrongPassword) || strings.Contains(err.Error(), role.Password) {
		t.Fatal("probe error exposed a password")
	}
	if module.connectionPools != nil || !reflect.DeepEqual(catalog.binding, binding) {
		t.Fatal("isolated credential probe activated a pool or changed binding state")
	}
}
