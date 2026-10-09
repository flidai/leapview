//go:build integration

package duckdb

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativePostgreSQLSourceReadDenialAndRecovery(t *testing.T) {
	harness := postgrestest.StartTLS(t)
	const password = "owned-native-postgresql-fixture"
	reader := harness.EnsureRole(t, postgrestest.Role{Name: "fixture_reader", Password: password, Login: true})
	database := harness.NewDatabase(t, "")
	harness.GrantDatabase(t, database.Name, reader, "CONNECT")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	for _, statement := range []string{
		"CREATE TABLE public.fixture_rows (id BIGINT, value TEXT)",
		"INSERT INTO public.fixture_rows VALUES (1, 'x')",
		"GRANT USAGE ON SCHEMA public TO fixture_reader",
		"GRANT SELECT ON public.fixture_rows TO fixture_reader",
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	endpoint, err := url.Parse(database.URL(reader))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	// The native PostgreSQL extension uses libpq's standard CA input. The
	// target-owned connection still requires full server identity verification.
	t.Setenv("PGSSLROOTCERT", harness.RootCertPath())
	db := openNativeConnectorDB(t, "postgres")
	source := semanticmodel.Source{Connection: "local", Object: "public.fixture_rows"}
	connection := semanticmodel.Connection{Kind: "postgres", Host: endpoint.Hostname(), Port: port, Database: database.Name, Username: reader.Name, SSLMode: "verify-full", Auth: semanticmodel.ConnectionAuth{"password": password}}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(ctx, db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
	if _, err := db.ExecContext(ctx, "INSERT INTO conn_local.public.fixture_rows VALUES (2, 'denied')"); err == nil {
		t.Fatal("target-owned PostgreSQL source attachment allowed a write")
	}
	if _, err := db.ExecContext(ctx, "DETACH conn_local"); err != nil {
		t.Fatal(err)
	}
	connection.Auth = semanticmodel.ConnectionAuth{"password": "incorrect-owned-fixture-password"}
	model.Connections["local"] = connection
	if err := prepareRefreshSourceAccess(ctx, db, model, nil); err == nil {
		t.Fatal("native PostgreSQL accepted an incorrect scoped credential")
	}
	connection.Auth = semanticmodel.ConnectionAuth{"password": password}
	model.Connections["local"] = connection
	if err := prepareRefreshSourceAccess(ctx, db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
}
