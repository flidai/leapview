package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// DeployOptions are the Project-owned inputs to deployment orchestration.
type DeployOptions struct {
	SourceRoot  string
	Credentials cliapi.Credentials
	Environment string
	// Intent is "new" or "resume" when explicitly selected. Empty permits
	// the application adapter to enter the guided interactive flow.
	Intent string
	// OperationHandle is the stable human-readable descriptor handle.
	OperationHandle string
	Format          string
	// ConfirmPlan must equal the retained plan digest to authorize expensive
	// build/publication work. Interactive bare deploy may obtain this exact
	// value from ConfirmationReader after displaying the plan review.
	ConfirmPlan        string
	Interactive        bool
	ConfirmationReader io.Reader
	ConfirmationWriter io.Writer
}

// DeployOperations performs the cross-capability release/deployment workflow
// assembled by the application.
type DeployOperations interface {
	Deploy(context.Context, DeployOptions, io.Writer) error
}

// DeployCommand constructs the project deployment command.
func DeployCommand(ctx context.Context, client cliapi.Client, operations DeployOperations) *cobra.Command {
	values := DeployOptions{SourceRoot: "dashboards"}
	command := &cobra.Command{
		Use:   "deploy",
		Short: "Review and deliver an exact target-owned deployment operation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if client == nil {
				return fmt.Errorf("Project CLI API client is required")
			}
			if operations == nil {
				return fmt.Errorf("Project deploy operations are required")
			}
			if command.Flags().Changed("new") && command.Flags().Changed("resume") {
				return writeDeploySelectionCommandError(command, values.Format, "CONFLICTING_INTENT_FLAGS", "--new and --resume are mutually exclusive")
			}
			if values.OperationHandle != "" && !command.Flags().Changed("new") && !command.Flags().Changed("resume") {
				return writeDeploySelectionCommandError(command, values.Format, "OPERATION_INTENT_REQUIRED", "--operation requires explicit --new or --resume in noninteractive mode")
			}
			if values.Format != "text" && values.Format != "json" {
				return fmt.Errorf("deploy format must be text or json")
			}
			switch {
			case command.Flags().Changed("new"):
				values.Intent = "new"
			case command.Flags().Changed("resume"):
				values.Intent = "resume"
			default:
				values.Intent = ""
			}
			values.ConfirmationReader = command.InOrStdin()
			values.ConfirmationWriter = command.OutOrStdout()
			values.Interactive = values.Format != "json" && deployInputIsTerminal(values.ConfirmationReader) && (values.Intent == "" || (values.Intent == "resume" && values.OperationHandle == ""))
			credentials, err := client.Resolve(ctx, values.Credentials)
			if err != nil {
				return err
			}
			values.Credentials = credentials
			return operations.Deploy(ctx, values, command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&values.Credentials.Target, "target", "", "LeapView server URL")
	command.Flags().StringVar(&values.Credentials.Token, "token", "", "API token")
	command.Flags().StringVar(&values.SourceRoot, "source-root", values.SourceRoot, "analytics source root")
	command.Flags().StringVar(&values.Credentials.ProjectID, "project-id", "", "target-bound Project identity")
	command.Flags().StringVar(&values.Environment, "environment", "", "assert the target instance environment")
	command.Flags().Bool("new", false, "start a fresh deployment operation")
	command.Flags().Bool("resume", false, "resume a retained deployment operation")
	command.Flags().StringVar(&values.OperationHandle, "operation", "", "stable deployment operation handle")
	command.Flags().StringVar(&values.Format, "format", "text", "output format: text or json")
	command.Flags().StringVar(&values.ConfirmPlan, "confirm-plan", "", "exact retained plan digest authorizing build and publication")
	return command
}

func deployInputIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func writeDeploySelectionCommandError(command *cobra.Command, format, code, detail string) error {
	if format != "json" {
		return fmt.Errorf("deployment operation selection failed (%s): %s", code, detail)
	}
	if err := WriteDeploymentSelectionError(command.OutOrStdout(), format, code, "", detail); err != nil {
		return err
	}
	return &DeploymentSelectionError{SchemaVersion: 1, Code: code, Detail: detail}
}
