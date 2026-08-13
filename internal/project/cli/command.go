// Package cli owns command-line adapters for the Project capability.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/platform/cliapi"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/schema"
	"github.com/flidai/leapview/internal/workspace"
	"github.com/spf13/cobra"
)

type options struct {
	remote       cliapi.RemoteOptions
	catalog      string
	environment  string
	workspaceID  string
	jsonOutput   bool
	schemaFormat string
	schemaOut    string
}

// ActiveWorkspaceGraphLoader supplies the deployed Workspace state that Project
// planning compares with locally compiled configuration.
type ActiveWorkspaceGraphLoader interface {
	LoadActiveWorkspaceGraph(context.Context, cliapi.Credentials, string, string) (workspace.AssetGraph, error)
}

// ValidateCommand constructs the local project validation command.
func ValidateCommand(ctx context.Context) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "validate [project]",
		Short: "Validate a configuration-as-code project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("validate accepts at most one positional project")
			}
			if len(args) == 1 {
				if cmd.Flags().Changed("project") {
					return fmt.Errorf("choose either --project or positional project, not both")
				}
				opts.catalog = args[0]
			}
			return runValidate(ctx, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.catalog, "project", filepath.Join("dashboards", "leapview.yaml"), "project path")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON diagnostics")
	return cmd
}

// PlanCommand constructs the local or active-state project plan command.
func PlanCommand(ctx context.Context, loader ActiveWorkspaceGraphLoader) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "plan [project]",
		Short: "Emit a deterministic configuration-as-code plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("plan accepts at most one positional project")
			}
			if len(args) == 1 {
				if cmd.Flags().Changed("project") {
					return fmt.Errorf("choose either --project or positional project, not both")
				}
				opts.catalog = args[0]
			}
			return runPlan(ctx, loader, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.catalog, "project", filepath.Join("dashboards", "leapview.yaml"), "project path")
	cmd.Flags().StringVar(&opts.remote.Target, "target", "", "LeapView server URL for active deployment diff")
	cmd.Flags().StringVar(&opts.remote.Token, "token", "", "API token")
	cmd.Flags().StringVar(&opts.environment, "environment", "", "assert the target instance environment for active diff")
	cmd.Flags().StringVar(&opts.workspaceID, "workspace", opts.workspaceID, "workspace id for active diff")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON plan")
	return cmd
}

// SchemaCommand constructs project schema export commands.
func SchemaCommand() *cobra.Command {
	opts := &options{}
	parent := &cobra.Command{
		Use:   "schema",
		Short: "Inspect LeapView YAML schemas",
	}
	export := &cobra.Command{
		Use:   "export",
		Short: "Export generated schema artifacts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSchemaExport(opts)
		},
	}
	export.Flags().StringVar(&opts.schemaFormat, "format", "json-schema", "schema output format")
	export.Flags().StringVar(&opts.schemaOut, "out", filepath.Join("schemas", "json"), "output directory")
	parent.AddCommand(export)
	return parent
}

type validateResponse struct {
	OK          bool                      `json:"ok"`
	Diagnostics []configschema.Diagnostic `json:"diagnostics"`
}

func runValidate(ctx context.Context, opts *options, out io.Writer) error {
	diagnostics := validateProject(ctx, opts.catalog)
	response := validateResponse{OK: len(diagnostics) == 0, Diagnostics: diagnostics}
	if opts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(response); err != nil {
			return err
		}
		if response.OK {
			return nil
		}
		return fmt.Errorf("validation failed")
	}
	if response.OK {
		fmt.Fprintf(out, "ok %s\n", opts.catalog)
		return nil
	}
	for _, diagnostic := range diagnostics {
		fmt.Fprintln(out, diagnostic.String())
	}
	return fmt.Errorf("validation failed")
}

func runPlan(ctx context.Context, loader ActiveWorkspaceGraphLoader, opts *options, out io.Writer) error {
	var plan workspacecompiler.ProjectPlan
	var err error
	if opts.remote.Target != "" {
		if strings.TrimSpace(opts.workspaceID) == "" {
			return fmt.Errorf("plan --target requires --workspace")
		}
		active, err := fetchActiveWorkspaceGraph(ctx, loader, opts)
		if err != nil {
			return err
		}
		plan, err = workspacecompiler.PlanProjectAgainstGraph(opts.catalog, opts.workspaceID, active)
	} else {
		plan, err = workspacecompiler.PlanProject(opts.catalog)
	}
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(plan)
	}
	if err := renderProjectPlan(out, plan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func renderProjectPlan(out io.Writer, plan workspacecompiler.ProjectPlan) error {
	fmt.Fprintf(out, "project %s\n", plan.Project)
	for _, workspace := range plan.Workspaces {
		fmt.Fprintf(out, "workspace %s\n", workspace.ID)
		fmt.Fprintf(out, "  connections %s\n", strings.Join(workspace.Connections, ","))
		fmt.Fprintf(out, "  sources %s\n", strings.Join(workspace.Sources, ","))
		fmt.Fprintf(out, "  model_tables %s\n", strings.Join(workspace.ModelTables, ","))
		fmt.Fprintf(out, "  semantic_models %s\n", strings.Join(workspace.SemanticModels, ","))
		fmt.Fprintf(out, "  dashboards %s\n", strings.Join(workspace.Dashboards, ","))
		fmt.Fprintf(out, "  workspace_groups %s\n", strings.Join(workspace.WorkspaceGroups, ","))
		fmt.Fprintf(out, "  workspace_role_bindings %s\n", strings.Join(workspace.WorkspaceRoleBindings, ","))
		if len(workspace.Changes) > 0 || len(workspace.DependencyChanges) > 0 {
			fmt.Fprintf(out, "  changes +%d ~%d -%d dependencies %d\n", workspace.Summary.Added, workspace.Summary.Changed, workspace.Summary.Removed, workspace.Summary.DependencyChanges)
			for _, change := range workspace.Changes {
				fmt.Fprintf(out, "    %s %s", change.Action, change.ID)
				annotations := planChangeAnnotations(change)
				if annotations != "" {
					fmt.Fprintf(out, " [%s]", annotations)
				}
				fmt.Fprintln(out)
			}
			for _, change := range workspace.DependencyChanges {
				fmt.Fprintf(out, "    %s dependency %s -> %s (%s)", change.Action, change.From, change.To, change.Type)
				if change.Breaking {
					fmt.Fprint(out, " [breaking]")
				}
				fmt.Fprintln(out)
			}
		}
	}
	return nil
}

func fetchActiveWorkspaceGraph(ctx context.Context, loader ActiveWorkspaceGraphLoader, opts *options) (workspace.AssetGraph, error) {
	return fetchActiveWorkspaceGraphFor(ctx, loader, opts, opts.workspaceID)
}

func fetchActiveWorkspaceGraphFor(ctx context.Context, loader ActiveWorkspaceGraphLoader, opts *options, workspaceID string) (workspace.AssetGraph, error) {
	if loader == nil {
		return workspace.AssetGraph{}, fmt.Errorf("Project active Workspace graph loader is required")
	}
	return loader.LoadActiveWorkspaceGraph(ctx, opts.remote.Credentials(), opts.environment, workspaceID)
}

func planChangeAnnotations(change workspacecompiler.ProjectPlanChange) string {
	parts := []string{}
	if change.Breaking {
		parts = append(parts, "breaking")
	}
	if change.MaterializationImpact {
		parts = append(parts, "refresh")
	}
	if change.AccessImpact {
		parts = append(parts, "access")
	}
	return strings.Join(parts, ",")
}

func validateProject(ctx context.Context, projectPath string) []configschema.Diagnostic {
	if _, err := workspacecompiler.CompileProject(projectPath, workspacecompiler.Options{}); err != nil {
		return configschema.Diagnostics(err)
	}
	if err := ctx.Err(); err != nil {
		return configschema.Diagnostics(err)
	}
	return nil
}

func runSchemaExport(opts *options) error {
	return ExportSchema(opts.schemaFormat, opts.schemaOut)
}

// ExportSchema writes the Project capability's generated schema artifacts.
func ExportSchema(format, outDir string) error {
	if format != "json-schema" {
		return fmt.Errorf("unsupported schema format %q", format)
	}
	files, err := configschema.JSONSchemaFiles()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
