package app

import (
	"context"
	"errors"
	"testing"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const sourceJourneyCredentialVariable = "LEAPVIEW_DEV_CONNECTION_WAREHOUSE"

type sourceCredentialUpstream struct {
	endpoint analyticsgen.TargetConnectionEndpoint
	password string
	database *postgrestest.Database
	role     string
}

func newSourceCredentialUpstream(t *testing.T, h *postgrestest.Harness) sourceCredentialUpstream {
	t.Helper()
	role := h.EnsureRole(t, postgrestest.Role{Name: "source_journey_reader", Password: "source-journey-password", Login: true})
	db := h.NewDatabase(t, "source_journey")
	h.GrantDatabase(t, db.Name, role, "CONNECT")
	admin, err := pgx.Connect(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err = admin.Exec(t.Context(), `CREATE TABLE public.orders (id BIGINT PRIMARY KEY, amount BIGINT NOT NULL); INSERT INTO public.orders VALUES(1,10),(2,20)`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), `GRANT SELECT ON public.orders TO `+pgx.Identifier{role.Name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(db.PrivateURL(role))
	if err != nil {
		t.Fatal(err)
	}
	port := int32(cfg.Port)
	tlsMode := "require"
	return sourceCredentialUpstream{endpoint: analyticsgen.TargetConnectionEndpoint{Host: &cfg.Host, Port: &port, Database: &cfg.Database, SourceIdentity: &role.Name, TlsMode: &tlsMode}, password: role.Password, database: db, role: role.Name}
}

func (source *sourceCredentialUpstream) rotatePasswordAndData(t *testing.T) {
	t.Helper()
	admin, err := pgx.Connect(t.Context(), source.database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	source.password = "source-journey-rotated-password"
	if _, err = admin.Exec(t.Context(), `ALTER ROLE `+pgx.Identifier{source.role}.Sanitize()+` PASSWORD 'source-journey-rotated-password'`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), `UPDATE public.orders SET amount=amount+5`); err != nil {
		t.Fatal(err)
	}
	// Drop only this disposable source role's sessions: a retained authenticated
	// connection must not let the rebuild pass with the obsolete password.
	if _, err = admin.Exec(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename=$1 AND pid<>pg_backend_pid()`, source.role); err != nil {
		t.Fatal(err)
	}
	old, err := pgx.Connect(t.Context(), source.database.URL(postgrestest.Role{Name: source.role, Password: "source-journey-password", Login: true}))
	if err == nil {
		_ = old.Close(context.Background())
		t.Fatal("upstream still accepted the obsolete password")
	}
	var rejected *pgconn.PgError
	if !errors.As(err, &rejected) || rejected.Code != "28P01" {
		t.Fatal("obsolete password probe failed without PostgreSQL authentication rejection")
	}
}

func (f *sourceCredentialHTTPJourney) createSourceBinding(t *testing.T, token string) {
	t.Helper()
	client := analyticsgen.NewGenClient(f.transport(token))
	_, err := client.ApplyDevelopmentProfile(t.Context(), analyticsgen.GenApplyDevelopmentProfileClientRequest{
		Project: sourceJourneyProject, Target: f.instance,
		Headers: analyticsgen.GenApplyDevelopmentProfileClientHeaders{IdempotencyKey: "source-journey-profile"},
		Body: analyticsgen.DevelopmentProfileApplicationRequest{ApplicationId: "profile_credential_journey", Mode: analyticsgen.DevelopmentProfileApplicationModeNew, SourceDigest: f.snapshot.Digest, GraphDigest: f.config.DevelopmentGraphDigest, ProfileDigest: f.config.DevelopmentProfileDigest,
			Connections: []analyticsgen.DevelopmentProfileConnectionIntent{{LogicalConnection: "connection:warehouse", ConnectorKind: "postgres", AuthenticationMode: analyticsgen.TargetConnectionAuthenticationModeExternalBundle, Endpoint: f.source.endpoint, CredentialReference: &analyticsgen.TargetConnectionCredentialReference{ProjectId: sourceJourneyProject, Environment: f.config.Environment, SecretPath: "/", SecretKey: sourceJourneyCredentialVariable}}}},
	})
	if err != nil {
		t.Fatalf("create source binding: %v", err)
	}
}

func (source sourceCredentialUpstream) profileDigest(t *testing.T, name string) string {
	t.Helper()
	e := source.endpoint
	digest, err := connectionbinding.DevelopmentProfileDigest(name, []connectionbinding.DevelopmentProfileDigestConnection{{ConnectionID: "connection:warehouse", ConnectorKind: "postgres", CredentialVariable: sourceJourneyCredentialVariable, Endpoint: connectionbinding.EndpointConfig{Host: *e.Host, Port: int(*e.Port), Database: *e.Database, SourceIdentity: *e.SourceIdentity, TLSMode: *e.TlsMode}}})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
