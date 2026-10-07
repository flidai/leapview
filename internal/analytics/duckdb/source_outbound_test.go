package duckdb

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

// Exercise the actual source-reading session, not the isolated target probe.
func TestSourceRuntimeGuardsHTTPReads(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, "id\n1\n")
	}))
	defer server.Close()
	for _, guarded := range []bool{false, true} {
		t.Run(fmt.Sprintf("guarded=%v", guarded), func(t *testing.T) {
			requests.Store(0)
			admission := newDuckDBTestExtensionAdmission(t, "ducklake", "httpfs")
			environment, err := analyticsducklake.Open(t.Context(), analyticsducklake.Config{
				RootDir: t.TempDir(), MaxConnections: 2, ExtensionAdmission: admission, GuardOutbound: guarded,
			})
			require.NoError(t, err)
			defer environment.Close()
			controller, err := workload.New(workload.DefaultConfig())
			require.NoError(t, err)
			defer controller.Close()
			lease, err := controller.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "guarded-source", EstimatedMemoryBytes: 1})
			require.NoError(t, err)
			defer lease.Release()
			analyticalLease, err := environment.Acquire(lease.Context())
			require.NoError(t, err)
			defer analyticalLease.Release()
			if guarded {
				session, err := environment.Session(analyticalLease.Context())
				require.NoError(t, err)
				_, err = session.ExecContext(analyticalLease.Context(), "SET http_proxy = ''")
				require.Error(t, err, "the source session must not be able to remove its proxy")
			}
			runtime := NewSourceRuntime(environment)
			runtime.extensionAdmission = admission
			model := &semanticmodel.Model{
				Connections: map[string]semanticmodel.Connection{"web": {Kind: "http", Scope: server.URL}},
				Sources:     map[string]semanticmodel.Source{"rows": {Connection: "web", Path: "rows.csv", Format: "csv", EffectivePathLocation: testCSVPathLocationWithHeader("rows.csv", true)}},
			}
			_, err = SourceRelation(model, model.Sources["rows"])
			require.NoError(t, err)
			prepared, err := runtime.Prepare(analyticalLease.Context(), model)
			if prepared != nil {
				defer prepared.Close()
			}
			if guarded {
				require.Error(t, err)
				require.Zero(t, requests.Load(), "blocked source must never receive a request")
			} else {
				require.NoError(t, err)
				require.Positive(t, requests.Load(), "development control must actually read the source")
			}
		})
	}
}
