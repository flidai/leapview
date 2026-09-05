package cli

import (
	"context"
	"time"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/platform/buildinfo"
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
		Use:           "leapview",
		Short:         "LeapView BI-as-code server and deployment CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       buildinfo.Current().Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.environment = ""
			return runServe(ctx, opts)
		},
	}
	root.AddCommand(serveCommand(ctx, opts))
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
	root.AddCommand(semanticModelOssieCommand(ctx))
	authentication := applicationAuthoringAuthentication{}
	root.AddCommand(accesscli.LoginCommand(ctx, authentication, applicationTargetDiscovery{}, applicationProjectIdentity{}))
	root.AddCommand(accesscli.LogoutCommand(ctx, authentication))
	root.AddCommand(adminCommand(ctx, opts))
	root.AddCommand(healthcheckCommand(ctx, opts))
	normalizeCommandGroups(root)
	annotateCommandDocumentation(root)
	return root
}

// normalizeCommandGroups gives every help-only command group the same
// execution contract: no arguments are accepted, and invoking the group with
// no subcommand prints its help. Without a RunE Cobra treats an unknown
// nested argument as a help request and exits successfully, which makes
// `leapview admin nope` disagree with the root command's invalid-subcommand
// behavior.
func normalizeCommandGroups(root *cobra.Command) {
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		for _, child := range command.Commands() {
			visit(child)
		}
		if command == root || len(command.Commands()) == 0 || command.Runnable() {
			return
		}
		command.Args = cobra.NoArgs
		command.RunE = func(command *cobra.Command, _ []string) error {
			return command.Help()
		}
		if command.Annotations == nil {
			command.Annotations = map[string]string{}
		}
		command.Annotations[documentationHelpGroupAnnotation] = "true"
		if command.Annotations[documentationEffectAnnotation] == "" {
			command.Annotations[documentationEffectAnnotation] = "read"
		}
		if command.Annotations[documentationConfirmationAnnotation] == "" {
			command.Annotations[documentationConfirmationAnnotation] = "never"
		}
	}
	visit(root)
}
