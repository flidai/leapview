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
	Client                  cliapi.Client
	HTTPClient              *http.Client
	LoadPlanCatalog         func(string) (localplan.SourceCatalog, error)
	ResolveDevelopmentInput func(string, string) (DevelopmentInput, error)
}

// DevelopmentInput is an already verified, explicit local fixture selection.
// The application owns its manifest format; Managed Data owns planning and staging.
type DevelopmentInput struct {
	Name       string
	SourceRoot string
	Connection string
	From       string
	Manifest   manageddata.Manifest
	Provenance DevelopmentInputProvenance
}

type DevelopmentInputProvenance struct {
	Kind      string `json:"kind"`
	Generator string `json:"generator"`
	Rows      int64  `json:"rows"`
	Bounded   bool   `json:"bounded"`
}

type dataSelection struct {
	SourceRoot       string
	Connection       string
	From             string
	DevelopmentInput *DevelopmentInput
}

type options struct {
	remote      cliapi.RemoteOptions
	environment string
}

// Command constructs the Managed Data command tree.
func Command(ctx context.Context, dependencies Dependencies) *cobra.Command {
	loader := dependencies.LoadPlanCatalog
	if loader == nil {
		loader = func(string) (localplan.SourceCatalog, error) {
			return localplan.SourceCatalog{}, fmt.Errorf("Managed Data source catalog loader is required")
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
	parent.AddCommand(dataPlanCommand(ctx, planner, dependencies))
	parent.AddCommand(dataSyncCommand(ctx, planner, dependencies, opts))
	parent.AddCommand(dataRevisionsCommand(ctx, dependencies, opts))
	return parent
}

func dataPlanCommand(ctx context.Context, planner dataPlanner, dependencies Dependencies) *cobra.Command {
	sourceRoot := "dashboards"
	var connection string
	var from string
	var previousManifestPath string
	var developmentInput string
	projectRoot := "."
	command := &cobra.Command{
		Use:   "plan",
		Short: "Plan a local managed data revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selection, err := resolveDataSelection(cmd, dependencies, projectRoot, developmentInput, sourceRoot, connection, from)
			if err != nil {
				return err
			}
			sourceRoot, connection, from = selection.SourceRoot, selection.Connection, selection.From
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
				SourceRoot: sourceRoot,
				Connection: connection,
				From:       from,
				Previous:   previous,
			})
			if err != nil {
				return err
			}
			if err := verifyDevelopmentInputPlan(selection.DevelopmentInput, result); err != nil {
				return err
			}
			return writeDataPlan(cmd.OutOrStdout(), result, selection.DevelopmentInput)
		},
	}
	command.Flags().StringVar(&sourceRoot, "source-root", sourceRoot, "analytics source root")
	command.Flags().StringVar(&connection, "connection", "", "project-global managed connection")
	command.Flags().StringVar(&from, "from", "", "local filesystem root to ingest")
	command.Flags().StringVar(&previousManifestPath, "previous-manifest", "", "prior managed data manifest path")
	command.Flags().StringVar(&developmentInput, "development-input", "", "verified input declared in .leapview/development-inputs.yaml")
	command.Flags().StringVar(&projectRoot, "project-root", projectRoot, "analytics project root for a declared development input")
	return command
}

func resolveDataSelection(command *cobra.Command, dependencies Dependencies, projectRoot, developmentInput, sourceRoot, connection, from string) (dataSelection, error) {
	if strings.TrimSpace(developmentInput) == "" {
		if command.Flags().Changed("project-root") {
			return dataSelection{}, fmt.Errorf("--project-root requires --development-input")
		}
		return dataSelection{SourceRoot: sourceRoot, Connection: connection, From: from}, nil
	}
	if command.Flags().Changed("connection") || command.Flags().Changed("from") || command.Flags().Changed("source-root") {
		return dataSelection{}, fmt.Errorf("--development-input cannot be combined with --connection, --from, or --source-root")
	}
	if dependencies.ResolveDevelopmentInput == nil {
		return dataSelection{}, fmt.Errorf("development input resolver is required")
	}
	selected, err := dependencies.ResolveDevelopmentInput(projectRoot, developmentInput)
	if err != nil {
		return dataSelection{}, err
	}
	return dataSelection{SourceRoot: selected.SourceRoot, Connection: selected.Connection, From: selected.From, DevelopmentInput: &selected}, nil
}

type dataPlanOutput struct {
	Connection       string                    `json:"connection"`
	Root             string                    `json:"root"`
	Sources          []string                  `json:"sources"`
	RevisionID       string                    `json:"revisionId"`
	DevelopmentInput *developmentInputEvidence `json:"developmentInput,omitempty"`
	Manifest         manageddata.Manifest      `json:"manifest"`
	Diff             dataPlanDiff              `json:"diff"`
}

type developmentInputEvidence struct {
	Name       string                     `json:"name"`
	RevisionID string                     `json:"revisionId"`
	Provenance DevelopmentInputProvenance `json:"provenance"`
}

type dataPlanDiff struct {
	Added     []manageddata.File `json:"added"`
	Changed   []manageddata.File `json:"changed"`
	Removed   []manageddata.File `json:"removed"`
	Unchanged []manageddata.File `json:"unchanged"`
}

func writeDataPlan(out io.Writer, result localplan.Result, selected *DevelopmentInput) error {
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
	if selected != nil {
		document.DevelopmentInput = &developmentInputEvidence{Name: selected.Name, RevisionID: selected.Manifest.RevisionID(), Provenance: selected.Provenance}
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}

func verifyDevelopmentInputPlan(selected *DevelopmentInput, result localplan.Result) error {
	if selected == nil {
		return nil
	}
	if strings.TrimSpace(selected.Name) == "" || strings.TrimSpace(selected.Connection) == "" || strings.TrimSpace(selected.From) == "" || selected.Provenance.Kind == "" || !selected.Provenance.Bounded {
		return fmt.Errorf("development input selection is incomplete")
	}
	if err := selected.Manifest.Validate(manageddata.Limits{}); err != nil {
		return fmt.Errorf("development input manifest: %w", err)
	}
	if result.ConnectionName != selected.Connection || filepath.Clean(result.Root) != filepath.Clean(selected.From) || result.Manifest.RevisionID() != selected.Manifest.RevisionID() {
		return fmt.Errorf("development input changed after validation; rerun the command")
	}
	return nil
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
