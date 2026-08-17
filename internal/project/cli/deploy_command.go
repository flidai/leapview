package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

// DeployOptions are the Project-owned inputs to deployment orchestration.
type DeployOptions struct {
	ProjectPath string
	Credentials cliapi.Credentials
	Environment string
}

// DeployOperations performs the cross-capability release/deployment workflow
// assembled by the application.
type DeployOperations interface {
	Deploy(context.Context, DeployOptions, io.Writer) error
}

// DeployCommand constructs the project deployment command.
func DeployCommand(ctx context.Context, client cliapi.Client, operations DeployOperations) *cobra.Command {
	values := DeployOptions{ProjectPath: filepath.Join("dashboards", "leapview.yaml")}
	command := &cobra.Command{
		Use:   "deploy",
		Short: "Atomically deploy a configuration-as-code project",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if client == nil {
				return fmt.Errorf("Project CLI API client is required")
			}
			if operations == nil {
				return fmt.Errorf("Project deploy operations are required")
			}
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
	command.Flags().StringVar(&values.ProjectPath, "project", values.ProjectPath, "project path")
	command.Flags().StringVar(&values.Environment, "environment", "", "assert the target instance environment")
	return command
}
