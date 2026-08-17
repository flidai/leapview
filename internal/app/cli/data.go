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
		LoadPlanProject: loadManagedDataPlanProject,
		LoadProjectID:   loadProjectID,
	})
}

func loadManagedDataPlanProject(path string) (localplan.Project, error) {
	project, err := projectcompiler.LoadProject(path)
	if err != nil {
		return localplan.Project{}, err
	}
	projection := localplan.Project{
		Connections: make(map[string]localplan.Connection, len(project.Connections)),
		Sources:     make(map[string]localplan.Source, len(project.Sources)),
	}
	for name, connection := range project.Connections {
		stableID := project.ConnectionIDs[name]
		if stableID == "" || stableID != strings.TrimSpace(stableID) {
			return localplan.Project{}, fmt.Errorf("connection %q has no canonical stable ID", name)
		}
		if _, err := projectgraph.NewResourceID(stableID); err != nil {
			return localplan.Project{}, fmt.Errorf("connection %q has invalid stable ID %q: %w", name, stableID, err)
		}
		projection.Connections[name] = localplan.Connection{ID: stableID, Kind: connection.Kind, Root: connection.Root, Scope: connection.Scope}
	}
	for name, source := range project.Sources {
		projection.Sources[name] = localplan.Source{Connection: source.Connection, Path: source.Path, Format: source.Format}
	}
	return projection, nil
}

func loadProjectID(path string) (string, error) {
	project, err := projectcompiler.LoadProject(path)
	if err != nil {
		return "", err
	}
	return project.ID.String(), nil
}
