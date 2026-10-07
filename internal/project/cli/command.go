// Package cli owns command-line adapters for the Project capability.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/developmentinput"
	developmentprofile "github.com/flidai/leapview/internal/project/developmentprofile"
	"github.com/flidai/leapview/internal/project/schema"
	"github.com/spf13/cobra"
)

type options struct {
	sourceRoot   string
	format       string
	schemaFormat string
	schemaOut    string
}

// ValidateCommand constructs the local project validation command.
func ValidateCommand(ctx context.Context) *cobra.Command {
	opts := &options{format: "text"}
	cmd := &cobra.Command{
		Use:   "validate [source-root]",
		Short: "Validate a LeapView source root",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return cliapi.NewUsageError(fmt.Errorf("validate accepts at most one positional source root"))
			}
			if len(args) == 1 {
				if cmd.Flags().Changed("source-root") {
					return cliapi.NewUsageError(fmt.Errorf("choose either --source-root or positional source root, not both"))
				}
				opts.sourceRoot = args[0]
			}
			if opts.format != "text" && opts.format != "json" {
				return cliapi.NewUsageError(fmt.Errorf("validate format must be text or json"))
			}
			return runValidate(ctx, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.sourceRoot, "source-root", "dashboards", "analytics source root")
	cmd.Flags().StringVar(&opts.format, "format", opts.format, "output format: text or json")
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
	diagnostics := validateProject(ctx, opts.sourceRoot)
	response := validateResponse{OK: len(diagnostics) == 0, Diagnostics: diagnostics}
	if opts.format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(response); err != nil {
			return err
		}
		if response.OK {
			return nil
		}
		return cliapi.NewReportedError(fmt.Errorf("validation failed"))
	}
	if response.OK {
		_, err := fmt.Fprintf(out, "ok source-root %s\n", opts.sourceRoot)
		return err
	}
	for _, diagnostic := range diagnostics {
		if _, err := fmt.Fprintln(out, diagnostic.String()); err != nil {
			return fmt.Errorf("write validation diagnostic: %w", err)
		}
	}
	return cliapi.NewReportedError(fmt.Errorf("validation failed"))
}

func validateProject(ctx context.Context, sourceRoot string) []configschema.Diagnostic {
	if _, err := projectcompiler.Compile(sourceRoot); err != nil {
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
	profileSchema, err := developmentprofile.JSONSchema()
	if err != nil {
		return err
	}
	files[developmentprofile.SchemaFilename] = profileSchema
	developmentInputSchema, err := developmentinput.JSONSchema()
	if err != nil {
		return err
	}
	files[developmentinput.SchemaFilename] = developmentInputSchema
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
