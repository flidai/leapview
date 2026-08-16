package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunnableCommandsDeclareDocumentationSafety(t *testing.T) {
	root := NewCommand(context.Background())
	seen := map[string]struct{}{}
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		seen[command.CommandPath()] = struct{}{}
		if command.Runnable() {
			if command.Annotations[documentationEffectAnnotation] == "" {
				t.Errorf("runnable command %q has no documentation effect", command.CommandPath())
			}
			if command.Annotations[documentationConfirmationAnnotation] == "" {
				t.Errorf("runnable command %q has no documentation confirmation", command.CommandPath())
			}
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
	for path := range documentedCommandSafety {
		if _, ok := seen[path]; !ok {
			t.Errorf("documentation safety declares unknown command %q", path)
		}
	}
}

func TestRootHelpExposesCanonicalDeploymentLifecycle(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })

	help := func(args ...string) string {
		os.Args = append([]string{"leapview"}, args...)
		return captureStdout(t, func() {
			if err := Execute(context.Background()); err != nil {
				t.Fatalf("Execute(%v) error = %v", args, err)
			}
		})
	}

	output := help("--help")
	if !strings.Contains(output, "\n  deploy ") {
		t.Fatalf("root help missing atomic deploy command:\n%s", output)
	}
	if !strings.Contains(output, "\n  dev ") {
		t.Fatalf("root help missing dev command:\n%s", output)
	}
	if !strings.Contains(output, "\n  publish ") {
		t.Fatalf("root help missing publish command:\n%s", output)
	}
	if !strings.Contains(output, "\n  version ") {
		t.Fatalf("root help missing version command:\n%s", output)
	}
	command := NewCommand(context.Background())
	if found, _, err := command.Find([]string{"deploy"}); err != nil || found == command {
		t.Fatalf("root command does not resolve atomic deploy path: command=%v err=%v", found, err)
	}
	if found, _, err := command.Find([]string{"search"}); err != nil || found == command {
		t.Fatalf("root command does not resolve project-wide search: command=%v err=%v", found, err)
	}
	if found, _, err := command.Find([]string{"workspaces"}); err == nil || found != nil {
		t.Fatalf("removed workspace command is still registered: command=%v err=%v", found, err)
	}
}

func TestVersionReportsDevelopmentIdentityAsJSON(t *testing.T) {
	command := NewCommand(context.Background())
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"version", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"product": "leapview"`,
		`"version": "development"`,
		`"development": true`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("version output missing %s:\n%s", want, output.String())
		}
	}
}

func TestDeployCommandUsesTargetOwnedAtomicCandidatePreparation(t *testing.T) {
	command := deployCommand(context.Background(), &rootOptions{})
	if command.Name() != "deploy" {
		t.Fatalf("command name = %q, want deploy", command.Name())
	}
	if !strings.Contains(strings.ToLower(command.Short), "atomically") || !strings.Contains(strings.ToLower(command.Short), "project") {
		t.Fatalf("deploy short help = %q, want atomic project scope", command.Short)
	}
	if command.Flags().Lookup("revision") != nil {
		t.Fatal("deploy command still exposes client-owned managed revision pins")
	}
	for _, removed := range []string{"connection", "workspace"} {
		if command.Flags().Lookup(removed) != nil {
			t.Fatalf("deploy command still exposes removed --%s targeting flag", removed)
		}
	}
}

func TestAgentCommandIsGlobal(t *testing.T) {
	command := agentCommand(context.Background(), &rootOptions{})
	if command.PersistentFlags().Lookup("workspace") != nil {
		t.Fatal("global agent command still exposes --workspace")
	}
}
