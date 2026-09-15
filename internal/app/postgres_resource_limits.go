package app

import (
	"github.com/flidai/leapview/internal/analytics/gates"
	"github.com/flidai/leapview/internal/app/config"
)

func nativeCandidateGateBounds(production bool) gates.Bounds {
	bounds := gates.Bounds{MaxRows: 10000, MaxQueries: 128, MaxMillis: 5000}
	if !production {
		// Local showcase projects include full-size datasets. Keep qualification
		// bounded while allowing their exact-key checks to scan the physical data.
		bounds.MaxMillis = 120000
	}
	return bounds
}

func duckDBReadConnections(cfg config.Config) int {
	connections := cfg.WorkloadConfig().MaxRunning
	if cfg.DuckDBNodeMaxThreads > 0 {
		connections = min(connections, cfg.DuckDBNodeMaxThreads)
	}
	return max(1, connections)
}
