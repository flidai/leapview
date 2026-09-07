package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/spf13/cobra"
)

func dataCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	return manageddatacli.Command(ctx, manageddatacli.Dependencies{
		Client:          capabilityAPIClient{},
		HTTPClient:      http.DefaultClient,
		LoadPlanCatalog: loadManagedDataPlanCatalog,
	})
}

func loadManagedDataPlanCatalog(path string) (localplan.SourceCatalog, error) {
	project, err := projectcompiler.LoadSourceRoot(path)
	if err != nil {
		return localplan.SourceCatalog{}, err
	}
	projection := localplan.SourceCatalog{
		Connections: make(map[string]localplan.Connection, len(project.Connections)),
		Sources:     make(map[string]localplan.Source, len(project.Sources)),
	}
	for name, connection := range project.Connections {
		stableID := project.ConnectionIDs[name]
		if stableID == "" || stableID != strings.TrimSpace(stableID) {
			return localplan.SourceCatalog{}, fmt.Errorf("connection %q has no canonical stable ID", name)
		}
		if _, err := projectgraph.NewResourceID(stableID); err != nil {
			return localplan.SourceCatalog{}, fmt.Errorf("connection %q has invalid stable ID %q: %w", name, stableID, err)
		}
		projection.Connections[name] = localplan.Connection{ID: stableID, Kind: connection.Kind, Root: connection.Root, Scope: connection.Scope}
	}
	for name, source := range project.Sources {
		projection.Sources[name] = localplan.Source{Connection: source.Connection, Path: source.Path, Format: source.Format}
	}
	return projection, nil
}
