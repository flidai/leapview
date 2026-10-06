package cli

import (
	"strings"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/clidoc"
	"github.com/spf13/cobra"
)

const (
	documentationEffectAnnotation       = clidoc.EffectAnnotation
	documentationConfirmationAnnotation = clidoc.ConfirmationAnnotation
	documentationHelpGroupAnnotation    = clidoc.HelpGroupAnnotation
)

type commandSafety struct {
	effect       string
	confirmation string
	output       string
	framing      string
}

func annotateCommandDocumentation(root *cobra.Command) {
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		if safety, ok := documentedCommandSafety[command.CommandPath()]; ok {
			if command.Annotations == nil {
				command.Annotations = map[string]string{}
			}
			command.Annotations[documentationEffectAnnotation] = safety.effect
			command.Annotations[documentationConfirmationAnnotation] = safety.confirmation
			if safety.output != "" {
				command.Annotations[clidoc.OutputAnnotation] = safety.output
			}
			if safety.framing != "" {
				command.Annotations[clidoc.FramingAnnotation] = safety.framing
			}
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func registerCLICompletions(root *cobra.Command) {
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		if command.Flags().Lookup("target") != nil {
			_ = command.RegisterFlagCompletionFunc("target", completeLocalTargets)
		}
		if command.Flags().Lookup("format") != nil {
			_ = command.RegisterFlagCompletionFunc("format", completeFormats(command.CommandPath()))
		}
		switch command.CommandPath() {
		case "leapview api call", "leapview api describe":
			command.ValidArgsFunction = completeAPIOperations
		case "leapview logout":
			command.ValidArgsFunction = completeLocalTargets
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func completeLocalTargets(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	names, err := cliapi.NewProfileStore(clientConfigPath()).ProfileNames()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	matches := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func completeAPIOperations(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	contracts := sortedAPIOperationContracts()
	operations := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		if strings.HasPrefix(contract.OperationID, prefix) {
			operations = append(operations, contract.OperationID)
		}
	}
	return operations, cobra.ShellCompDirectiveNoFileComp
}

func completeFormats(commandPath string) cobra.CompletionFunc {
	formats := []string{"text", "json"}
	switch commandPath {
	case "leapview admin initialize":
		formats = []string{"json"}
	case "leapview semantic-model ossie export":
		formats = []string{"json", "yaml"}
	case "leapview schema export":
		formats = []string{"json-schema"}
	}
	return func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		matches := make([]string, 0, len(formats))
		for _, format := range formats {
			if strings.HasPrefix(format, prefix) {
				matches = append(matches, format)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	}
}

var documentedCommandSafety = map[string]commandSafety{
	"leapview":                                     {effect: "read", confirmation: "never"},
	"leapview completion":                          {effect: "read", confirmation: "never"},
	"leapview completion bash":                     {effect: "read", confirmation: "never"},
	"leapview completion fish":                     {effect: "read", confirmation: "never"},
	"leapview completion powershell":               {effect: "read", confirmation: "never"},
	"leapview completion zsh":                      {effect: "read", confirmation: "never"},
	"leapview help":                                {effect: "read", confirmation: "never"},
	"leapview admin initialize":                    {effect: "write", confirmation: "never"},
	"leapview admin maintenance":                   {effect: "destructive", confirmation: "conditional"},
	"leapview admin delivery pool bootstrap":       {effect: "write", confirmation: "required"},
	"leapview admin delivery pool upgrade":         {effect: "write", confirmation: "required"},
	"leapview admin delivery pool qualify":         {effect: "local-write", confirmation: "never"},
	"leapview agent ask":                           {effect: "write", confirmation: "never"},
	"leapview agent conversations":                 {effect: "read", confirmation: "never"},
	"leapview agent tools":                         {effect: "read", confirmation: "never"},
	"leapview api call":                            {effect: "dynamic", confirmation: "never", output: "raw"},
	"leapview api describe":                        {effect: "read", confirmation: "never", output: "json"},
	"leapview api list":                            {effect: "read", confirmation: "never"},
	"leapview acknowledge-project-claim-publisher": {effect: "write", confirmation: "never"},
	"leapview bootstrap-project":                   {effect: "write", confirmation: "never"},
	"leapview config validate":                     {effect: "read", confirmation: "never"},
	"leapview dashboards describe":                 {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards export":                   {effect: "local-write", confirmation: "never", output: "yaml", framing: "document-or-files"},
	"leapview dashboards filter":                   {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards filter-options":           {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards list":                     {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards page":                     {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards query-page":               {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards visual":                   {effect: "read", confirmation: "never", output: "json"},
	"leapview dashboards visual-data":              {effect: "read", confirmation: "never", output: "json"},
	"leapview data plan":                           {effect: "read", confirmation: "never", output: "json"},
	"leapview data revisions current":              {effect: "read", confirmation: "never"},
	"leapview data revisions list":                 {effect: "read", confirmation: "never"},
	"leapview data sync":                           {effect: "write", confirmation: "never"},
	"leapview build":                               {effect: "write", confirmation: "conditional"},
	"leapview deploy":                              {effect: "write", confirmation: "conditional"},
	"leapview dev":                                 {effect: "write", confirmation: "never", framing: "document-or-events"},
	"leapview dev logs":                            {effect: "read", confirmation: "never"},
	"leapview dev reset":                           {effect: "destructive", confirmation: "required"},
	"leapview dev status":                          {effect: "read", confirmation: "never"},
	"leapview dev stop":                            {effect: "destructive", confirmation: "conditional"},
	"leapview healthcheck":                         {effect: "read", confirmation: "never"},
	"leapview init":                                {effect: "local-write", confirmation: "never"},
	"leapview login":                               {effect: "local-write", confirmation: "never", framing: "events"},
	"leapview logout":                              {effect: "destructive", confirmation: "never"},
	"leapview plan":                                {effect: "write", confirmation: "conditional"},
	"leapview publish":                             {effect: "write", confirmation: "conditional"},
	"leapview rollback":                            {effect: "write", confirmation: "conditional"},
	"leapview schema export":                       {effect: "local-write", confirmation: "never", output: "json-schema", framing: "files"},
	"leapview search":                              {effect: "read", confirmation: "never"},
	"leapview semantic-models dataset":             {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models datasets":            {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models describe":            {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models explain-preview":     {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models explain-query":       {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models fields":              {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models list":                {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models preview":             {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-models query":               {effect: "read", confirmation: "never", output: "json"},
	"leapview semantic-model ossie import":         {effect: "read", confirmation: "never", output: "yaml"},
	"leapview semantic-model ossie export":         {effect: "read", confirmation: "never", output: "json,yaml", framing: "document-or-file"},
	"leapview serve":                               {effect: "local-write", confirmation: "never"},
	"leapview validate":                            {effect: "read", confirmation: "never"},
	"leapview version":                             {effect: "read", confirmation: "never"},
}
