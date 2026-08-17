package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

type dataPlanner interface {
	Plan(context.Context, localplan.Request) (localplan.Result, error)
}

// Dependencies are application facilities required by Managed Data commands.
type Dependencies struct {
	Client          cliapi.Client
	HTTPClient      *http.Client
	LoadPlanProject func(string) (localplan.Project, error)
	LoadProjectID   func(string) (string, error)
}

type options struct {
	remote      cliapi.RemoteOptions
	environment string
}

// Command constructs the Managed Data command tree.
func Command(ctx context.Context, dependencies Dependencies) *cobra.Command {
	loader := dependencies.LoadPlanProject
	if loader == nil {
		loader = func(string) (localplan.Project, error) {
			return localplan.Project{}, fmt.Errorf("Managed Data project plan loader is required")
		}
	}
	return dataCommandWithOptions(ctx, localplan.NewService(loader), dependencies, &options{})
}

func dataCommandWithPlanner(ctx context.Context, planner dataPlanner) *cobra.Command {
	return dataCommandWithOptions(ctx, planner, Dependencies{}, &options{})
}

func dataCommandWithOptions(ctx context.Context, planner dataPlanner, dependencies Dependencies, opts *options) *cobra.Command {
	parent := &cobra.Command{
		Use:          "data",
		Short:        "Manage project-global data revisions",
		SilenceUsage: true,
	}
	parent.AddCommand(dataPlanCommand(ctx, planner))
	parent.AddCommand(dataSyncCommand(ctx, planner, dependencies, opts))
	parent.AddCommand(dataRevisionsCommand(ctx, dependencies, opts))
	return parent
}

func dataPlanCommand(ctx context.Context, planner dataPlanner) *cobra.Command {
	var projectPath string
	var connection string
	var from string
	var previousManifestPath string
	command := &cobra.Command{
		Use:   "plan",
		Short: "Plan a local managed data revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(connection) == "" {
				return fmt.Errorf("connection is required")
			}
			if strings.TrimSpace(from) == "" {
				return fmt.Errorf("from is required")
			}
			var previous *manageddata.Manifest
			if previousManifestPath != "" {
				manifest, err := readManagedDataManifest(previousManifestPath)
				if err != nil {
					return fmt.Errorf("previous manifest: %w", err)
				}
				previous = &manifest
			}
			result, err := planner.Plan(ctx, localplan.Request{
				ProjectPath: projectPath,
				Connection:  connection,
				From:        from,
				Previous:    previous,
			})
			if err != nil {
				return err
			}
			return writeDataPlan(cmd.OutOrStdout(), result)
		},
	}
	command.Flags().StringVar(&projectPath, "project", filepath.Join("dashboards", "leapview.yaml"), "project path")
	command.Flags().StringVar(&connection, "connection", "", "project-global managed connection")
	command.Flags().StringVar(&from, "from", "", "local filesystem root to ingest")
	command.Flags().StringVar(&previousManifestPath, "previous-manifest", "", "prior managed data manifest path")
	return command
}

type dataPlanOutput struct {
	Connection string               `json:"connection"`
	Root       string               `json:"root"`
	Sources    []string             `json:"sources"`
	RevisionID string               `json:"revisionId"`
	Manifest   manageddata.Manifest `json:"manifest"`
	Diff       dataPlanDiff         `json:"diff"`
}

type dataPlanDiff struct {
	Added     []manageddata.File `json:"added"`
	Changed   []manageddata.File `json:"changed"`
	Removed   []manageddata.File `json:"removed"`
	Unchanged []manageddata.File `json:"unchanged"`
}

func writeDataPlan(out io.Writer, result localplan.Result) error {
	connection := result.ConnectionName
	if connection == "" {
		connection = result.Connection
	}
	document := dataPlanOutput{
		Connection: connection,
		Root:       result.Root,
		Sources:    append([]string{}, result.Sources...),
		RevisionID: result.Manifest.RevisionID(),
		Manifest:   result.Manifest,
		Diff: dataPlanDiff{
			Added:     append([]manageddata.File{}, result.Diff.Added...),
			Changed:   append([]manageddata.File{}, result.Diff.Changed...),
			Removed:   append([]manageddata.File{}, result.Diff.Removed...),
			Unchanged: append([]manageddata.File{}, result.Diff.Unchanged...),
		},
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}

func readManagedDataManifest(name string) (manageddata.Manifest, error) {
	file, err := os.Open(name)
	if err != nil {
		return manageddata.Manifest{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest manageddata.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return manageddata.Manifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return manageddata.Manifest{}, fmt.Errorf("must contain exactly one JSON object")
		}
		return manageddata.Manifest{}, err
	}
	if err := manifest.Validate(manageddata.Limits{}); err != nil {
		return manageddata.Manifest{}, err
	}
	return manifest, nil
}
