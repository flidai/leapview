package cli

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/developmentinput"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/spf13/cobra"
)

func dataCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	return manageddatacli.Command(ctx, manageddatacli.Dependencies{
		Client:                  capabilityAPIClient{},
		HTTPClient:              http.DefaultClient,
		LoadPlanCatalog:         loadManagedDataPlanCatalog,
		ResolveDevelopmentInput: resolveDevelopmentInput,
	})
}

func resolveDevelopmentInput(projectRoot, name string) (manageddatacli.DevelopmentInput, error) {
	selected, err := developmentinput.Load(projectRoot, name)
	if err != nil {
		return manageddatacli.DevelopmentInput{}, err
	}
	manifest := manageddata.Manifest{Files: make([]manageddata.File, len(selected.Files))}
	for index, file := range selected.Files {
		manifest.Files[index] = manageddata.File{Path: file.Path, Size: file.SizeBytes, SHA256: file.SHA256}
	}
	if err := manifest.Validate(manageddata.Limits{}); err != nil {
		return manageddatacli.DevelopmentInput{}, fmt.Errorf("development input manifest: %w", err)
	}
	return manageddatacli.DevelopmentInput{
		Name:       selected.Name,
		SourceRoot: filepath.Join(selected.ProjectRoot, "dashboards"),
		Connection: selected.Connection,
		From:       selected.Root,
		Manifest:   manifest,
		Provenance: manageddatacli.DevelopmentInputProvenance{Kind: selected.Provenance.Kind, Generator: selected.Provenance.Generator, Rows: selected.Provenance.Rows, Bounded: selected.Provenance.Bounded},
	}, nil
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
