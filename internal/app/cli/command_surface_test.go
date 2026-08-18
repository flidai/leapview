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
	for _, lifecycle := range []string{"plan", "build", "publish", "rollback"} {
		if !strings.Contains(output, "\n  "+lifecycle+" ") {
			t.Fatalf("root help missing canonical %s command:\n%s", lifecycle, output)
		}
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
		t.Fatalf("root command does not resolve deprecated deploy path: command=%v err=%v", found, err)
	}
	if found, _, err := command.Find([]string{"search"}); err != nil || found == command {
		t.Fatalf("root command does not resolve project-wide search: command=%v err=%v", found, err)
	}
	if found, _, err := command.Find([]string{"workspaces"}); err == nil {
		t.Fatalf("removed workspace command is still registered: command=%v err=%v", found, err)
	}
}

func TestDocumentationDoesNotAdvertiseWorkspaceCommands(t *testing.T) {
	for path := range documentedCommandSafety {
		if strings.Contains(path, "workspace") {
			t.Fatalf("documentation safety advertises removed workspace command %q", path)
		}
	}
}

func TestDeliveryPoolBootstrapDocumentsWriteEffectAndExplicitConfirmation(t *testing.T) {
	command := NewCommand(context.Background())
	found, _, err := command.Find([]string{"admin", "delivery", "pool", "bootstrap"})
	if err != nil {
		t.Fatalf("find delivery pool bootstrap: %v", err)
	}
	if got := found.Annotations[documentationEffectAnnotation]; got != "write" {
		t.Fatalf("bootstrap effect = %q, want write", got)
	}
	if got := found.Annotations[documentationConfirmationAnnotation]; got != "required" {
		t.Fatalf("bootstrap confirmation = %q, want required", got)
	}
	for _, flag := range []string{"pool", "evidence", "apply"} {
		if found.Flags().Lookup(flag) == nil {
			t.Fatalf("bootstrap command missing --%s", flag)
		}
	}
}

func TestQualificationLocalPoolBootstrapIsHiddenAndRequiresConfirmation(t *testing.T) {
	command := NewCommand(context.Background())
	found, _, err := command.Find([]string{"admin", "delivery", "pool", "qualify"})
	if err != nil {
		t.Fatalf("find qualification pool bootstrap: %v", err)
	}
	if !found.Hidden {
		t.Fatal("qualification-only local pool bootstrap must stay hidden")
	}
	if got := found.Annotations[documentationEffectAnnotation]; got != "write" {
		t.Fatalf("bootstrap effect = %q, want write", got)
	}
	if got := found.Annotations[documentationConfirmationAnnotation]; got != "required" {
		t.Fatalf("bootstrap confirmation = %q, want required", got)
	}
	if found.Flags().Lookup("apply") == nil {
		t.Fatal("qualification pool bootstrap missing --apply")
	}
}

func TestDeliveryRepairDocumentsConditionalDestructiveEffect(t *testing.T) {
	command := NewCommand(context.Background())
	found, _, err := command.Find([]string{"admin", "delivery", "repair"})
	if err != nil {
		t.Fatalf("find delivery repair: %v", err)
	}
	if got := found.Annotations[documentationEffectAnnotation]; got != "destructive" {
		t.Fatalf("repair effect = %q, want destructive", got)
	}
	if got := found.Annotations[documentationConfirmationAnnotation]; got != "conditional" {
		t.Fatalf("repair confirmation = %q, want conditional", got)
	}
	if found.Flags().Lookup("apply") == nil {
		t.Fatal("repair command missing --apply")
	}
}

func TestDeliveryAuditDocumentsReadOnlyEffect(t *testing.T) {
	command := NewCommand(context.Background())
	found, _, err := command.Find([]string{"admin", "delivery", "audit"})
	if err != nil {
		t.Fatalf("find delivery audit: %v", err)
	}
	if got := found.Annotations[documentationEffectAnnotation]; got != "read" {
		t.Fatalf("audit effect = %q, want read", got)
	}
	if got := found.Annotations[documentationConfirmationAnnotation]; got != "never" {
		t.Fatalf("audit confirmation = %q, want never", got)
	}
	if found.Flags().Lookup("pool-id") == nil {
		t.Fatal("audit command missing --pool-id")
	}
	if found.Flags().Lookup("apply") != nil {
		t.Fatal("audit command unexpectedly exposes --apply")
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
	if !strings.Contains(strings.ToLower(command.Short), "deprecated") || command.Deprecated == "" {
		t.Fatalf("deploy command is not marked deprecated: short=%q deprecated=%q", command.Short, command.Deprecated)
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
