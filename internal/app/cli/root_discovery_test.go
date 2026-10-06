package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

func TestRootHelpAndVersionDoNotLoadEnvironmentConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing", "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "many")

	command := NewCommand(context.Background())
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("constructing command tree touched client state: stat error = %v", err)
	}
	command.SetArgs([]string{"--help"})
	var help strings.Builder
	command.SetOut(&help)
	command.SetErr(&help)
	if err := command.Execute(); err != nil {
		t.Fatalf("root help with malformed unrelated environment: %v", err)
	}
	for _, title := range []string{"Authoring:", "Delivery:", "Data and Query:", "Access:", "Operations:", "Reference:"} {
		if !strings.Contains(help.String(), title) {
			t.Errorf("root help missing %q group:\n%s", title, help.String())
		}
	}

	bareRoot := NewCommand(context.Background())
	bareRoot.SetArgs([]string{})
	var bareHelp strings.Builder
	bareRoot.SetOut(&bareHelp)
	bareRoot.SetErr(&bareHelp)
	if err := bareRoot.Execute(); err != nil {
		t.Fatalf("bare root with malformed unrelated environment: %v", err)
	}
	if !strings.Contains(bareHelp.String(), "Authoring:") {
		t.Fatalf("bare root did not print grouped help:\n%s", bareHelp.String())
	}

	version := NewCommand(context.Background())
	version.SetArgs([]string{"--version"})
	var versionOutput strings.Builder
	version.SetOut(&versionOutput)
	version.SetErr(&versionOutput)
	if err := version.Execute(); err != nil {
		t.Fatalf("root version with malformed unrelated environment: %v", err)
	}
}

func TestRunnableCommandsDeclarePositionalValidators(t *testing.T) {
	root := NewCommand(context.Background())
	if root.Flags().Lookup("version") == nil {
		t.Fatal("root --version flag is missing before execution")
	}
	if root.Flags().Lookup("help") == nil {
		t.Fatal("root --help flag is missing before execution")
	}
	deploy, _, err := root.Find([]string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Flags().Lookup("help") == nil {
		t.Fatal("command --help flag is missing before execution")
	}
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		if command.Runnable() && command.Args == nil {
			t.Errorf("runnable command %q has no positional validator", command.CommandPath())
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
	if err := root.Args(root, []string{"unexpected"}); err == nil {
		t.Fatal("root command accepted a positional argument")
	}
	for _, path := range [][]string{{"validate"}, {"semantic-model", "ossie", "import"}, {"semantic-model", "ossie", "export"}} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if err := command.Args(command, []string{"source"}); err != nil {
			t.Errorf("%s rejected its optional positional argument: %v", command.CommandPath(), err)
		}
		if err := command.Args(command, []string{"source", "extra"}); err == nil {
			t.Errorf("%s accepted too many positional arguments", command.CommandPath())
		}
	}
	completion := NewCommand(context.Background())
	completion.SetArgs([]string{"completion", "nope"})
	if err := completion.Execute(); err == nil {
		t.Fatal("completion help group accepted an unknown nested argument")
	}
}

func TestHelpRejectsUnknownTopicsAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"help", "nope"}, {"help", "deploy", "extra"}, {"help", "admin", "nope"}} {
		root := NewCommand(context.Background())
		root.SetArgs(args)
		var output strings.Builder
		root.SetOut(&output)
		root.SetErr(&output)
		if err := root.Execute(); err == nil {
			t.Errorf("help selection %v succeeded: %s", args, output.String())
		}
	}
	for _, args := range [][]string{{"help"}, {"help", "deploy"}, {"help", "admin", "initialize"}} {
		root := NewCommand(context.Background())
		root.SetArgs(args)
		var output strings.Builder
		root.SetOut(&output)
		root.SetErr(&output)
		if err := root.Execute(); err != nil {
			t.Errorf("valid help selection %v failed: %v", args, err)
		}
	}
}

func TestRootOfflineCompletionsReadOnlyLocalCatalogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", path)
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "many")
	store := cliapi.NewProfileStore(path)
	for _, name := range []string{"staging", "production"} {
		if err := store.Put(name, cliapi.TargetProfile{
			Origin: "http://localhost:8080", InstanceID: "instance", ProjectID: "project", CredentialAccount: "account",
		}); err != nil {
			t.Fatalf("save target %q: %v", name, err)
		}
	}

	root := NewCommand(context.Background())
	call, _, err := root.Find([]string{"api", "call"})
	if err != nil {
		t.Fatal(err)
	}
	operations, directive := call.ValidArgsFunction(call, nil, "")
	if directive&cobra.ShellCompDirectiveNoFileComp == 0 || len(operations) == 0 {
		t.Fatalf("API operation completion = %v, directive %v", operations, directive)
	}
	firstOperation := sortedAPIOperationContracts()[0].OperationID
	if !containsString(operations, firstOperation) {
		t.Fatalf("API operation completion omitted %q", firstOperation)
	}
	completionCommand := NewCommand(context.Background())
	completionCommand.SetArgs([]string{"__complete", "api", "call", ""})
	var completionOutput strings.Builder
	completionCommand.SetOut(&completionOutput)
	completionCommand.SetErr(&completionOutput)
	if err := completionCommand.Execute(); err != nil {
		t.Fatalf("execute API completion with malformed unrelated environment: %v", err)
	}
	if !strings.Contains(completionOutput.String(), firstOperation) {
		t.Fatalf("API completion omitted %q:\n%s", firstOperation, completionOutput.String())
	}

	deploy, _, err := root.Find([]string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	targetCompletion, ok := deploy.GetFlagCompletionFunc("target")
	if !ok {
		t.Fatal("deploy --target has no completion function")
	}
	targets, _ := targetCompletion(deploy, nil, "p")
	if len(targets) != 1 || targets[0] != "production" {
		t.Fatalf("local target completion = %v, want [production]", targets)
	}

	formatCompletion, ok := deploy.GetFlagCompletionFunc("format")
	if !ok {
		t.Fatal("deploy --format has no completion function")
	}
	formats, _ := formatCompletion(deploy, nil, "j")
	if len(formats) != 1 || formats[0] != "json" {
		t.Fatalf("format completion = %v, want [json]", formats)
	}
	initialize, _, err := root.Find([]string{"admin", "initialize"})
	if err != nil {
		t.Fatal(err)
	}
	initializeFormatCompletion, ok := initialize.GetFlagCompletionFunc("format")
	if !ok {
		t.Fatal("admin initialize --format has no completion function")
	}
	initializeFormats, _ := initializeFormatCompletion(initialize, nil, "")
	if len(initializeFormats) != 1 || initializeFormats[0] != "json" {
		t.Fatalf("initialize format completion = %v, want [json]", initializeFormats)
	}
}

func TestCapabilityAPIClientReturnsConfigurationError(t *testing.T) {
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "many")
	_, err := (capabilityAPIClient{}).Resolve(context.Background(), cliapi.Credentials{})
	if err == nil || !strings.Contains(err.Error(), "LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING") {
		t.Fatalf("Resolve() error = %v, want malformed configuration error", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
