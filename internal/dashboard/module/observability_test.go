package module

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
)

type testDashboardTelemetry struct{}

func (testDashboardTelemetry) DashboardRefreshStarted(string)                                   {}
func (testDashboardTelemetry) DashboardRefreshFinished(string, string, int, map[string]float64) {}
func (testDashboardTelemetry) DashboardRefreshEventObserved(string, string)                     {}
func (testDashboardTelemetry) VisualizationFrameObserved(string, int, int, int)                 {}
func (testDashboardTelemetry) DashboardCacheObserved(string)                                    {}
func (testDashboardTelemetry) DashboardCacheObservationObserved(dataquery.CacheObservation)     {}
func (testDashboardTelemetry) SpatialTileObserved(string, string, string, int64, int64, int, int, bool) {
}

func TestBuildWiresProgressiveObservers(t *testing.T) {
	module, err := Build(t.Context(), Config{HTTP: HTTPConfig{
		Telemetry: testDashboardTelemetry{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := module.HTTP()
	if handler.RefreshEventObserved == nil {
		t.Fatal("dashboard refresh event observer is not configured")
	}
	if handler.CacheObserved == nil {
		t.Fatal("dashboard cache observer is not configured")
	}
	if handler.Broker == nil || module.publicBroker == nil {
		t.Fatal("dashboard build did not configure a local broker")
	}
	if handler.Broker != module.publicBroker {
		t.Fatal("dashboard handler and module use different local brokers")
	}
	if handler.CacheObservationObserved == nil {
		t.Fatal("typed dashboard cache observer is not configured")
	}
}
