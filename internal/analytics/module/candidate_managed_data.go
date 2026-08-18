package module

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// BindCandidateManagedDataRoots applies target-owned managed-data roots to
// detached semantic models used only during candidate materialization. The
// portable artifact remains unchanged; serving-state preparation performs the
// analogous binding when it extracts the artifact for runtime use.
func BindCandidateManagedDataRoots(models map[string]*semanticmodel.Model, connectionNames map[string]string, roots map[string]string) error {
	for modelID, model := range models {
		if model == nil {
			return fmt.Errorf("semantic model %q is nil", modelID)
		}
		for name, connection := range model.Connections {
			if connection.Kind != "managed" {
				continue
			}
			connectionID := name
			if canonical := strings.TrimSpace(connectionNames[name]); canonical != "" {
				connectionID = canonical
			}
			root := strings.TrimSpace(roots[connectionID])
			if root == "" {
				return fmt.Errorf("managed connection %q in semantic model %q has no resolved candidate root", name, modelID)
			}
			connection.Root = root
			connection.Scope = ""
			model.Connections[name] = connection
		}
	}
	return nil
}
