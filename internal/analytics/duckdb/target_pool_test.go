package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/extension"
	"github.com/stretchr/testify/require"
)

// targetPoolTestExtensionAdmission supplies canonical, platform-independent
// evidence to a recording session. The fake session never opens these paths;
// real-load coverage uses newDuckDBTestExtensionAdmission in source tests.
type targetPoolTestExtensionAdmission struct{}

var _ extension.Admission = targetPoolTestExtensionAdmission{}
var _ extension.Preparation = targetPoolTestExtensionAdmission{}

func (targetPoolTestExtensionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	switch name {
	case "postgres", "httpfs", "quack":
		digest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		identity, err := (extension.Identity{
			DuckDBVersion: "test-runtime", ExtensionVersion: "test-fixture", GOOS: "test", GOARCH: "test",
			Platform: "test-test", Name: name, Digest: digest, SupportProfile: "test-fixture",
		}).Canonical()
		if err != nil {
			return extension.AdmittedExtension{}, err
		}
		return extension.AdmittedExtension{
			Name: name, Identity: identity, Version: "test-fixture", ExtensionVersion: "test-fixture",
			DuckDBVersion: "test-runtime", GOOS: "test", GOARCH: "test", Platform: "test-test",
			SupportProfile: "test-fixture", Digest: digest,
			Path: "/test/extensions/" + extension.ArtifactFilenameStem(name) + ".duckdb_extension", Origin: "reviewed-local-test-fixture",
			Provenance: "attest:target-pool-test", Signature: "sig:target-pool-test",
		}, nil
	default:
		return extension.AdmittedExtension{}, fmt.Errorf("test extension %q was not admitted", name)
	}
}

func (a targetPoolTestExtensionAdmission) PrepareExtensions(ctx context.Context, names []string) ([]extension.Evidence, error) {
	evidence := make([]extension.Evidence, 0, len(names))
	for _, name := range names {
		admitted, err := a.AdmitExtension(ctx, name)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, admitted.Evidence())
	}
	return evidence, nil
}

func TestTargetRuntimePoolFactoryPreparesOnlyConnectorOwnedReadOnlyProbe(t *testing.T) {
	session := &recordingTargetSession{}
	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open: func(context.Context) (TargetRuntimeSession, error) { return session, nil },
		Limits: TargetRuntimeLimits{
			MemoryMaxBytes: 64 << 20, TempMaxBytes: 16 << 20, MaxThreads: 1,
		},
		RequireTLS: true, ExtensionAdmission: targetPoolTestExtensionAdmission{},
	})
	require.NoError(t, err)
	binding := testDuckDBTargetBinding(t)
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"password": "source-secret"},
		"secret-1:v4", time.Now(), time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	pool, err := factory.Prepare(context.Background(), binding, snapshot)
	require.NoError(t, err)
	if err := pool.HealthCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(session.statements, "\n")
	for _, required := range []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET memory_limit = '67108864B'",
		"SET max_temp_directory_size = '16777216B'",
		"SET threads = 1",
		loadExtensionStatement("/test/extensions/" + extension.ArtifactFilenameStem("postgres") + ".duckdb_extension"),
		"CREATE OR REPLACE TEMPORARY SECRET leapview_warehouse",
		"HOST 'warehouse.internal'",
		"PASSWORD 'source-secret'",
		"ATTACH '' AS conn_warehouse (TYPE postgres, READ_ONLY, SECRET leapview_warehouse)",
		"SET lock_configuration = true",
		"SELECT 1",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("runtime statements missing %q:\n%s", required, joined)
		}
	}
	for _, forbidden := range []string{"PERSISTENT SECRET", "READ_WRITE", "postgres_execute", "mysql_execute"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("runtime statements contain forbidden capability %q:\n%s", forbidden, joined)
		}
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if !session.closed {
		t.Fatal("runtime session was not closed")
	}
}

func TestTargetRuntimePoolFactoryPreparesScopedQuackProbe(t *testing.T) {
	session := &recordingTargetSession{}
	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open: func(context.Context) (TargetRuntimeSession, error) { return session, nil },
		Limits: TargetRuntimeLimits{
			MemoryMaxBytes: 64 << 20, TempMaxBytes: 16 << 20, MaxThreads: 1,
		},
		RequireTLS: true, ExtensionAdmission: targetPoolTestExtensionAdmission{},
	})
	require.NoError(t, err)
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"token": "source-secret"},
		"secret-1:v6", time.Now(), time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	pool, err := factory.Prepare(context.Background(), testQuackTargetBinding(t), snapshot)
	require.NoError(t, err)
	require.NoError(t, pool.HealthCheck(context.Background()))

	joined := strings.Join(session.statements, "\n")
	for _, required := range []string{
		"LOAD '/test/extensions/httpfs.duckdb_extension'",
		"LOAD '/test/extensions/quack.duckdb_extension'",
		"CREATE OR REPLACE TEMPORARY SECRET leapview_lakehouse (TYPE quack, TOKEN 'source-secret', SCOPE 'quack:quack.example.com:443')",
		"FROM quack_query('quack:quack.example.com:443', 'SELECT 1')",
		"SET lock_configuration = true",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Quack runtime statements missing %q:\n%s", required, joined)
		}
	}
	if strings.Index(joined, "LOAD httpfs") > strings.Index(joined, "LOAD quack") {
		t.Fatalf("Quack dependency extension was loaded after Quack:\n%s", joined)
	}
	for _, forbidden := range []string{"ATTACH", "PERSISTENT SECRET", "PROVIDER", "HOST '"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Quack runtime statements contain forbidden capability %q:\n%s", forbidden, joined)
		}
	}
	if got := strings.Count(joined, "FROM quack_query("); got != 2 {
		t.Fatalf("Quack probes = %d, want prepare and health-check probes:\n%s", got, joined)
	}
	require.NoError(t, pool.Close())
}

func TestTargetRuntimePoolFactoryRejectsUnboundedOrUnsupportedEndpointsBeforeOpeningRuntime(t *testing.T) {
	opened := 0
	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open: func(context.Context) (TargetRuntimeSession, error) {
			opened++
			return &recordingTargetSession{}, nil
		},
		Limits:     TargetRuntimeLimits{MemoryMaxBytes: 1, TempMaxBytes: 1, MaxThreads: 1},
		RequireTLS: true, ExtensionAdmission: targetPoolTestExtensionAdmission{},
	})
	require.NoError(t, err)
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"password": "source-secret"},
		"secret-1:v4", time.Now(), time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*connectionbinding.TargetBinding){
		"unsupported_connector": func(binding *connectionbinding.TargetBinding) {
			binding.ConnectorKind = "s3"
			binding.Endpoint.ObjectScope = "s3://warehouse/"
		},
		"missing_host": func(binding *connectionbinding.TargetBinding) {
			binding.Endpoint.Host = ""
		},
		"missing_database": func(binding *connectionbinding.TargetBinding) {
			binding.Endpoint.Database = ""
		},
		"missing_identity": func(binding *connectionbinding.TargetBinding) {
			binding.Endpoint.SourceIdentity = ""
		},
		"insecure_transport": func(binding *connectionbinding.TargetBinding) {
			binding.Endpoint.TLSMode = "disable"
		},
	} {
		t.Run(name, func(t *testing.T) {
			binding := testDuckDBTargetBinding(t)
			mutate(&binding)
			if _, err := factory.Prepare(context.Background(), binding, snapshot); err == nil {
				t.Fatal("Prepare() accepted an unbounded target")
			}
		})
	}
	if opened != 0 {
		t.Fatalf("opened runtimes for rejected targets = %d", opened)
	}
}

func TestTargetRuntimePoolResolvesTargetOwnedConnectionAfterProviderSnapshotIsDestroyed(t *testing.T) {
	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open: func(context.Context) (TargetRuntimeSession, error) {
			return &recordingTargetSession{}, nil
		},
		Limits:     TargetRuntimeLimits{MemoryMaxBytes: 1, TempMaxBytes: 1, MaxThreads: 1},
		RequireTLS: true, ExtensionAdmission: targetPoolTestExtensionAdmission{},
	})
	require.NoError(t, err)
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"password": "source-secret"},
		"secret-1:v4", time.Now(), time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	pool, err := factory.Prepare(t.Context(), testDuckDBTargetBinding(t), snapshot)
	require.NoError(t, err)
	snapshot.Destroy()
	resolver, ok := pool.(analyticsruntime.ConnectionResolver)
	if !ok {
		t.Fatal("validated target pool does not expose an Analytics connection resolver")
	}
	resolved, err := resolver.Resolve(
		t.Context(),
		"warehouse",
		semanticmodel.Connection{Kind: "postgres"},
	)
	require.NoError(t, err)
	if resolved.Host != "warehouse.internal" || resolved.Database != "analytics" ||
		resolved.Auth["password"] != "source-secret" {
		t.Fatalf("resolved target connection = %#v", resolved)
	}
	clear(resolved.Auth)
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(
		t.Context(),
		"warehouse",
		semanticmodel.Connection{Kind: "postgres"},
	); !errors.Is(err, connectionbinding.ErrProviderUnavailable) {
		t.Fatalf("Resolve() after pool close error = %v", err)
	}
}

func TestTargetRuntimePoolHealthAndCloseAreIdempotentAndPropagateOnlyInternally(t *testing.T) {
	sourceErr := errors.New("driver included source-secret")
	session := &recordingTargetSession{failOn: "SELECT 1", err: sourceErr}
	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open:       func(context.Context) (TargetRuntimeSession, error) { return session, nil },
		Limits:     TargetRuntimeLimits{MemoryMaxBytes: 1, TempMaxBytes: 1, MaxThreads: 1},
		RequireTLS: true, ExtensionAdmission: targetPoolTestExtensionAdmission{},
	})
	require.NoError(t, err)
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"password": "source-secret"},
		"secret-1:v4", time.Now(), time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	pool, err := factory.Prepare(context.Background(), testDuckDBTargetBinding(t), snapshot)
	require.NoError(t, err)
	if err := pool.HealthCheck(context.Background()); !errors.Is(err, sourceErr) {
		t.Fatalf("HealthCheck() error = %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if session.closeCalls != 1 {
		t.Fatalf("session close calls = %d", session.closeCalls)
	}
}

func TestIsolatedTargetRuntimeOpenerCreatesPrivateSingleConnectionSession(t *testing.T) {
	open := NewIsolatedTargetRuntimeOpener()
	session, err := open(context.Background())
	require.NoError(t, err)
	if _, err := session.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

type recordingTargetSession struct {
	statements []string
	failOn     string
	err        error
	closed     bool
	closeCalls int
}

func (session *recordingTargetSession) ExecContext(
	_ context.Context,
	statement string,
	_ ...any,
) (sql.Result, error) {
	session.statements = append(session.statements, statement)
	if session.failOn != "" && strings.Contains(statement, session.failOn) {
		return nil, session.err
	}
	return nil, nil
}

func (session *recordingTargetSession) Close() error {
	session.closeCalls++
	session.closed = true
	return nil
}
