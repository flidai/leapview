package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	agentGuidance      bool
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

// Run executes the public CLI, renders one failure diagnostic, and returns the
// process exit code. Signal cancellation is allowed to unwind command lifecycle
// handlers before its conventional status is returned.
func Run(ctx context.Context) int {
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	root := NewCommand(runContext)
	signals := make(chan os.Signal, 1)
	interrupts := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case received := <-signals:
			interrupts <- received
			cancel()
		case <-done:
		}
	}()
	command, err := root.ExecuteContextC(runContext)
	close(done)
	var received os.Signal
	select {
	case received = <-interrupts:
	default:
	}
	if command == nil {
		command = root
	}
	return runExitCode(command, err, received)
}

func runExitCode(command *cobra.Command, err error, received os.Signal) int {
	if received != nil && (command == nil || command.CommandPath() != "leapview serve") {
		return interruptExitCode(received)
	}
	return renderCLIError(command, err)
}

// NewCommand constructs the LeapView CLI command tree for execution and documentation.
func NewCommand(ctx context.Context) *cobra.Command {
	opts := &rootOptions{}
	root := &cobra.Command{
		Use:           "leapview",
		Short:         "LeapView BI-as-code server and deployment CLI",
		Long:          "LeapView compiles governed dashboards from source and serves them through a BI platform. Use the CLI to author projects, plan and deliver changes, explore data, manage access, and operate the server. Running leapview without a subcommand prints grouped help.",
		Example:       "  leapview init ./analytics\n  cd ./analytics && leapview dev\n  leapview validate\n  leapview help deploy",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       buildinfo.Current().Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.agentGuidance {
				return writeAgentGuidance(cmd)
			}
			return cmd.Help()
		},
	}
	root.InitDefaultVersionFlag()
	root.Flags().BoolVar(&opts.agentGuidance, "llms", false, "print offline agent guidance and the public command catalog")
	root.PersistentFlags().Bool("no-input", false, "disable terminal prompts and automatic browser opening")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return cliapi.NewUsageError(err)
	})
	authentication := applicationAuthoringAuthentication{}
	root.AddGroup(
		&cobra.Group{ID: "authoring", Title: "Authoring:"},
		&cobra.Group{ID: "delivery", Title: "Delivery:"},
		&cobra.Group{ID: "dataquery", Title: "Data and Query:"},
		&cobra.Group{ID: "access", Title: "Access:"},
		&cobra.Group{ID: "operations", Title: "Operations:"},
		&cobra.Group{ID: "reference", Title: "Reference:"},
	)
	root.SetHelpCommandGroupID("reference")
	root.SetCompletionCommandGroupID("reference")
	addGroup := func(id string, commands ...*cobra.Command) {
		for _, command := range commands {
			command.GroupID = id
			root.AddCommand(command)
		}
	}
	addGroup("operations", serveCommand(ctx, opts), configCommand(), healthcheckCommand(ctx, opts), doctorCommand(ctx), adminCommand(ctx, opts))
	addGroup("authoring", initCommand(), devCommand(ctx), validateCommand(ctx, opts), semanticModelOssieCommand(ctx))
	addGroup("delivery", publishCommand(ctx), buildCommand(ctx), rollbackCommand(ctx), deployCommand(ctx, opts), planCommand(ctx, opts))
	addGroup("dataquery", dataCommand(ctx, opts), apiCommand(ctx, opts), agentCommand(ctx, opts), searchCommand(ctx, opts), dashboardsCommand(ctx, opts), semanticModelsCommand(ctx, opts))
	addGroup("access", accesscli.LoginCommand(ctx, authentication, applicationTargetDiscovery{}, applicationProjectIdentity{profiles: cliapi.NewProfileStore(clientConfigPath())}), accesscli.LogoutCommand(ctx, authentication), bootstrapProjectCommand(ctx, opts), acknowledgeProjectClaimPublisherCommand(ctx, opts))
	addGroup("reference", versionCommand(), schemaCommand(opts))
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var initializeHelpFlags func(*cobra.Command)
	initializeHelpFlags = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		command.InitDefaultHelpFlag()
		for _, child := range command.Commands() {
			initializeHelpFlags(child)
		}
	}
	initializeHelpFlags(root)
	if help, _, err := root.Find([]string{"help"}); err == nil {
		help.Args = func(_ *cobra.Command, args []string) error {
			_, remaining, err := root.Find(args)
			if err != nil {
				return err
			}
			if len(remaining) > 0 {
				return fmt.Errorf("unknown help topic %q; run leapview help for available commands", strings.Join(args, " "))
			}
			return nil
		}
	}
	normalizeCommandGroups(root)
	registerCLICompletions(root)
	annotateCommandDocumentation(root)
	return root
}

// normalizeCommandGroups gives runnable commands explicit positional
// validators and makes help-only groups print their help when invoked alone.
// Without a RunE Cobra treats an unknown nested argument as a help request
// and exits successfully, which makes `leapview admin nope` disagree with the
// root command's invalid-subcommand behavior.
func normalizeCommandGroups(root *cobra.Command) {
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		defer func() {
			if command.Args == nil {
				return
			}
			validateArgs := command.Args
			command.Args = func(command *cobra.Command, args []string) error {
				if err := validateArgs(command, args); err != nil {
					return cliapi.NewUsageError(err)
				}
				return nil
			}
		}()
		for _, child := range command.Commands() {
			visit(child)
		}
		if command == root || command.Name() == "help" {
			return
		}
		if command.Runnable() && command.Args == nil {
			switch command.CommandPath() {
			case "leapview validate", "leapview semantic-model ossie import", "leapview semantic-model ossie export":
				command.Args = cobra.MaximumNArgs(1)
			default:
				command.Args = cobra.NoArgs
			}
		}
		if len(command.Commands()) == 0 || command.Runnable() {
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
