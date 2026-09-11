package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyticsGeneratedAPIUsesExplorationOwnedVisualizationContracts(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "analytics", "api", "gen", "request_models.gen.go"))
	if err != nil {
		t.Fatalf("read generated Analytics request models: %v (run task api:generate first)", err)
	}
	if strings.Contains(string(body), "internal/dashboard/visualization/ir") {
		t.Fatal("generated Analytics request models import dashboard visualization IR; authored exploration formats must remain local")
	}
}
