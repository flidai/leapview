package cli

import (
	"context"
	"time"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	addr               string
	production         bool
	environment        string
	target             string
	token              string
	pageID             string
	schemaFormat       string
	schemaOut          string
	backupOut          string
	restoreFrom        string
	restoreBefore      string
	confirmRestore     bool
	databaseOnly       bool
	auditDays          int
	queryDays          int
	archivedAgentDays  int
	authStateDays      int
	autoApprove        bool
	apply              bool
	healthcheckURL     string
	healthcheckTimeout time.Duration
}

func Execute(ctx context.Context) error {
	return NewCommand(ctx).ExecuteContext(ctx)
}

// NewCommand constructs the LeapView CLI command tree for execution and documentation.
func NewCommand(ctx context.Context) *cobra.Command {
	opts := &rootOptions{}
	root := &cobra.Command{
		Use:   "leapview",
		Short: "LeapView BI-as-code server and deployment CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.environment = ""
			return runServe(ctx, opts)
		},
	}
	root.AddCommand(serveCommand(ctx, opts))
	root.AddCommand(evaluationCommand(ctx, opts))
	root.AddCommand(versionCommand())
	root.AddCommand(devCommand(ctx))
	root.AddCommand(publishCommand(ctx))
	root.AddCommand(buildCommand(ctx))
	root.AddCommand(rollbackCommand(ctx))
	root.AddCommand(deployCommand(ctx, opts))
	root.AddCommand(validateCommand(ctx, opts))
	root.AddCommand(planCommand(ctx, opts))
	root.AddCommand(schemaCommand(opts))
	root.AddCommand(configCommand())
	root.AddCommand(dataCommand(ctx, opts))
	root.AddCommand(apiCommand(ctx, opts))
	root.AddCommand(agentCommand(ctx, opts))
	root.AddCommand(searchCommand(ctx, opts))
	root.AddCommand(dashboardsCommand(ctx, opts))
	root.AddCommand(semanticModelsCommand(ctx, opts))
	authentication := applicationAuthoringAuthentication{}
	root.AddCommand(accesscli.LoginCommand(ctx, authentication, applicationTargetDiscovery{}, applicationProjectIdentity{}))
	root.AddCommand(accesscli.LogoutCommand(ctx, authentication))
	root.AddCommand(adminCommand(ctx, opts))
	root.AddCommand(healthcheckCommand(ctx, opts))
	annotateCommandDocumentation(root)
	return root
}
