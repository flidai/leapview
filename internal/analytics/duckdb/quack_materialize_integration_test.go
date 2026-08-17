//go:build integration && duckdb_arrow

package duckdb

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

func TestLiveQuackSourceMaterialization(t *testing.T) {
	uri := strings.TrimSpace(os.Getenv("LEAPVIEW_TEST_QUACK_URL"))
	token := os.Getenv("LEAPVIEW_TEST_QUACK_TOKEN")
	if uri == "" || token == "" {
		t.Skip("set LEAPVIEW_TEST_QUACK_URL and LEAPVIEW_TEST_QUACK_TOKEN to run the live Quack materialization")
	}
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(uri, "quack:"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	connection := semanticmodel.Connection{
		Kind: "quack", Host: host, Port: port, SSLMode: "require",
		Auth: semanticmodel.ConnectionAuth{"token": token},
	}
	model := &semanticmodel.Model{
		Name: "operations",
		Connections: map[string]semanticmodel.Connection{
			"quack": {Kind: "quack"},
		},
		Sources: map[string]semanticmodel.Source{
			"quack_jobs": {Connection: "quack", Object: "oeducklake.src_nocodb.jobs"},
		},
	}

	environment, err := analyticsducklake.Open(context.Background(), analyticsducklake.Config{
		RootDir: t.TempDir(), MaxConnections: 2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Close()) })
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	workloadLease, err := controller.Acquire(t.Context(), workload.Request{
		Class: workload.Refresh, Operation: "quack-live-materialize",
	})
	require.NoError(t, err)
	t.Cleanup(workloadLease.Release)
	analyticalLease, err := environment.Acquire(workloadLease.Context())
	require.NoError(t, err)
	t.Cleanup(analyticalLease.Release)

	runtime := NewSourceRuntimeWithConnectionResolver(environment, quackIntegrationConnectionResolver{
		connection: connection,
	})
	prepared, err := runtime.Prepare(analyticalLease.Context(), model)
	require.NoError(t, err)
	require.NoError(t, prepared.Close())
}

type quackIntegrationConnectionResolver struct {
	connection semanticmodel.Connection
}

func (resolver quackIntegrationConnectionResolver) Resolve(
	ctx context.Context,
	_ string,
	_ semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	if err := ctx.Err(); err != nil {
		return semanticmodel.Connection{}, err
	}
	return resolver.connection, nil
}

var _ analyticsruntime.ConnectionResolver = quackIntegrationConnectionResolver{}
