//go:build integration

package duckdb

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/stretchr/testify/require"
)

func TestLiveQuackTargetRuntimePool(t *testing.T) {
	uri := strings.TrimSpace(os.Getenv("LEAPVIEW_TEST_QUACK_URL"))
	token := os.Getenv("LEAPVIEW_TEST_QUACK_TOKEN")
	if uri == "" || token == "" {
		t.Skip("set LEAPVIEW_TEST_QUACK_URL and LEAPVIEW_TEST_QUACK_TOKEN to run the live Quack probe")
	}
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(uri, "quack:"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_quack_integration", TargetID: "lvinst_integration", ConnectionID: "lakehouse",
		ConnectorKind: "quack", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope:    connectionbinding.BindingScope{ProjectID: "integration", Environment: "test"},
		Endpoint: connectionbinding.EndpointConfig{Host: host, Port: port, TLSMode: "require"},
		CredentialReference: connectionbinding.CredentialReference{
			ProjectID: "integration", Environment: "test", SecretPath: "/integration/quack", SecretKey: "token",
		},
		Enabled: true, Now: time.Now(),
	})
	require.NoError(t, err)
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"token": token}, "live", time.Now(), time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	defer snapshot.Destroy()

	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open: NewIsolatedTargetRuntimeOpener(),
		Limits: TargetRuntimeLimits{
			MemoryMaxBytes: 128 << 20, TempMaxBytes: 64 << 20, MaxThreads: 1,
		},
		RequireTLS: true,
	})
	require.NoError(t, err)
	pool, err := factory.Prepare(context.Background(), binding, snapshot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, pool.HealthCheck(context.Background()))

	prepared, ok := pool.(*targetRuntimePool)
	require.True(t, ok)
	session, ok := prepared.session.(*isolatedTargetRuntimeSession)
	require.True(t, ok)
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{
		"lakehouse": prepared.connection,
	}}
	relation, err := SourceRelation(model, semanticmodel.Source{
		Connection: "lakehouse", Object: "oeducklake.src_nocodb.jobs",
	})
	require.NoError(t, err)
	var rowCount int64
	require.NoError(t, session.connection.QueryRowContext(
		context.Background(), "SELECT count(*) FROM ("+relation+")",
	).Scan(&rowCount))
	if rowCount <= 0 {
		t.Fatalf("live Quack jobs source row count = %d, want a non-empty source", rowCount)
	}
}
