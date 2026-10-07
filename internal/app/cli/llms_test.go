package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/clidoc"
)

func TestAgentGuidanceIsOfflineAndMatchesPublicCommandCatalog(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "missing", "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "invalid")
	t.Setenv("PATH", directory)
	t.Setenv("LEAPVIEW_API_TOKEN", "")
	t.Setenv("LEAPVIEW_TARGET", "https://unreachable.invalid")
	var first string
	for iteration := range 2 {
		root := NewCommand(context.Background())
		root.SetArgs([]string{"--llms"})
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 || !strings.HasPrefix(stdout.String(), "# LeapView CLI agent guide\n") {
			t.Fatalf("unexpected streams: stdout %q, stderr %q", stdout.String(), stderr.String())
		}
		if iteration == 0 {
			first = stdout.String()
		} else if stdout.String() != first {
			t.Fatal("agent guidance changes between identical invocations")
		}
		catalog, err := clidoc.Build(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range catalog.Commands {
			if !strings.Contains(stdout.String(), "`"+strings.ReplaceAll(command.Usage, "|", "\\|")+"`") {
				t.Errorf("missing public command %s", command.ID)
			}
		}
		for _, private := range []string{"admin project-claim", "admin delivery pool qualify", "--json"} {
			// The migration instruction names the removed flag once, but the
			// command catalog must never advertise it as an option.
			catalogSection := strings.Split(stdout.String(), "## Public command catalog")[1]
			if strings.Contains(catalogSection, private) {
				t.Errorf("catalog exposes private or removed contract %q", private)
			}
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("offline guidance changed local state: entries %v, error %v", entries, err)
	}
}

func TestAgentGuidanceRejectsExecutableSubcommandsBeforeEffects(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("LEAPVIEW_CLI_CONFIG", filepath.Join(directory, "cli.json"))
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "invalid")
	for _, arguments := range [][]string{{"--llms", "serve"}, {"serve", "--llms"}, {"--llms", "init", filepath.Join(directory, "project")}, {"dev", "--llms"}} {
		root := NewCommand(context.Background())
		root.SetArgs(arguments)
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "llms") {
			t.Errorf("arguments %v: error %v, want root-only flag rejection", arguments, err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid guidance invocation changed state: entries %v, error %v", entries, err)
	}
}

func TestPublicCommandOutputMetadataMatchesFixedAndStreamingContracts(t *testing.T) {
	catalog, err := clidoc.Build(NewCommand(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]clidoc.OutputMode{
		"api-call":          {Format: "raw", Framing: "bytes"},
		"api-describe":      {Format: "json", Framing: "document"},
		"dashboards-list":   {Format: "json", Framing: "document"},
		"dashboards-export": {Format: "yaml", Framing: "document-or-files"},
		"data-plan":         {Format: "json", Framing: "document"},
		"schema-export":     {Format: "json-schema", Framing: "files"},
		"login":             {Format: "json", Framing: "events"},
		"dev":               {Format: "json", Framing: "document-or-events"},
	}
	for _, command := range catalog.Commands {
		want, ok := expected[command.ID]
		if !ok {
			continue
		}
		found := false
		for _, mode := range command.Output.Modes {
			found = found || mode == want
		}
		if !found {
			t.Errorf("%s output %v does not include %v", command.ID, command.Output, want)
		}
		delete(expected, command.ID)
	}
	if len(expected) != 0 {
		t.Errorf("missing commands: %v", expected)
	}
}
