package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/spf13/cobra"
)

func dataSyncCommand(ctx context.Context, planner dataPlanner, dependencies Dependencies, opts *options) *cobra.Command {
	sourceRoot := "dashboards"
	var connection string
	var from string
	var developmentInput string
	projectRoot := "."
	format := "text"
	command := &cobra.Command{
		Use:   "sync",
		Short: "Stage a managed data revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			usingDevelopmentInput := strings.TrimSpace(developmentInput) != ""
			selection, err := resolveDataSelection(cmd, dependencies, projectRoot, developmentInput, sourceRoot, connection, from)
			if err != nil {
				return err
			}
			sourceRoot, connection, from = selection.SourceRoot, selection.Connection, selection.From
			if usingDevelopmentInput && (!cmd.Flags().Changed("target") || !cmd.Flags().Changed("project-id") || strings.TrimSpace(opts.remote.Target) == "" || strings.TrimSpace(opts.remote.ProjectID) == "") {
				return fmt.Errorf("--development-input sync requires explicit --target and --project-id; ambient target profiles are not eligible")
			}
			if strings.TrimSpace(connection) == "" {
				return fmt.Errorf("connection is required")
			}
			if strings.TrimSpace(from) == "" {
				return fmt.Errorf("from is required")
			}
			if dependencies.Client == nil {
				return fmt.Errorf("Managed Data CLI API client is required")
			}
			credentials, err := dependencies.Client.Resolve(ctx, opts.remote.Credentials())
			if err != nil {
				return err
			}
			projectID := strings.TrimSpace(credentials.ProjectID)
			if projectID == "" {
				return fmt.Errorf("target-bound Project identity is required; provide --project-id or use a target profile")
			}
			plan, err := planner.Plan(ctx, localplan.Request{SourceRoot: sourceRoot, Connection: connection, From: from})
			if err != nil {
				return err
			}
			if err := verifyDevelopmentInputPlan(selection.DevelopmentInput, plan); err != nil {
				return err
			}
			environment, err := dependencies.Client.Environment(ctx, credentials, opts.environment)
			if err != nil {
				return err
			}
			if usingDevelopmentInput && environment != "dev" {
				return fmt.Errorf("development inputs may only be staged to a verified dev environment, got %q", environment)
			}
			httpClient := dependencies.HTTPClient
			if httpClient == nil {
				httpClient = http.DefaultClient
			}
			return runDataSync(ctx, dataSyncRequest{
				SourceRoot: sourceRoot, ProjectID: projectID, Connection: connection, ConnectionID: plan.Connection, Root: plan.Root,
				Target: credentials.Target, Token: credentials.Token, Plan: plan, Out: cmd.OutOrStdout(), HTTPClient: httpClient,
				Format: format,
			})
		},
	}
	command.Flags().StringVar(&sourceRoot, "source-root", sourceRoot, "analytics source root")
	command.Flags().StringVar(&connection, "connection", "", "project-global managed connection")
	command.Flags().StringVar(&from, "from", "", "local filesystem root to ingest")
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	command.Flags().StringVar(&developmentInput, "development-input", "", "verified input declared in .leapview/development-inputs.yaml")
	command.Flags().StringVar(&projectRoot, "project-root", projectRoot, "analytics project root for a declared development input")
	command.Flags().StringVar(&opts.environment, "environment", "", "assert the target instance environment")
	opts.remote.AddFlags(command)
	return command
}
