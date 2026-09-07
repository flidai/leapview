package compiler

import (
	"fmt"
	"strings"
)

// ResolveDashboardSource returns the authored path for a dashboard name or
// resource ID in sourceRoot. It keeps source-root resolution inside the
// compiler without exposing the mutable source assembly.
func ResolveDashboardSource(sourceRoot, reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", fmt.Errorf("dashboard reference is required")
	}
	assembly, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return "", err
	}
	if path, ok := assembly.DashboardPaths[reference]; ok {
		return path, nil
	}
	for name, id := range assembly.DashboardIDs {
		if id == reference {
			return assembly.DashboardPaths[name], nil
		}
	}
	return "", fmt.Errorf("dashboard %q was not found in source root", reference)
}
