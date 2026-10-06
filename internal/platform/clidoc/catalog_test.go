package clidoc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildIsDeterministicAndIncludesOnlyPublicContract(t *testing.T) {
	root := &cobra.Command{Use: "leapview"}
	root.PersistentFlags().Bool("no-input", false, "disable prompts")
	root.PersistentFlags().String("internal-secret", "", "hidden")
	if err := root.PersistentFlags().MarkHidden("internal-secret"); err != nil {
		t.Fatal(err)
	}
	visible := &cobra.Command{Use: "inspect <name>", Args: cobra.ExactArgs(1), RunE: func(*cobra.Command, []string) error { t.Fatal("catalog executed command"); return nil }, Annotations: map[string]string{EffectAnnotation: "read", ConfirmationAnnotation: "never"}}
	visible.Flags().String("format", "text", "result format")
	visible.Flags().String("private", "", "hidden")
	if err := visible.Flags().MarkHidden("private"); err != nil {
		t.Fatal(err)
	}
	root.AddCommand(visible, &cobra.Command{Use: "hidden", Hidden: true})
	first, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("catalog changed between builds:\n%s\n%s", a, b)
	}
	if len(first.Commands) != 2 || first.Commands[0].ID != "root" || first.Commands[1].ID != "inspect" {
		t.Fatalf("commands: %#v", first.Commands)
	}
	for _, hidden := range []string{"internal-secret", "private", "hidden"} {
		if strings.Contains(string(a), hidden) {
			t.Fatalf("leaked %q: %s", hidden, a)
		}
	}
	command := first.Commands[1]
	if len(command.Options) != 1 || len(command.InheritedOptions) != 1 {
		t.Fatalf("options: %#v", command)
	}
	if command.Output.DefaultFormat != "text" || command.Output.Modes[1].Framing != "document" {
		t.Fatalf("output: %#v", command.Output)
	}
}

func TestBuildPreservesEventAndArtifactFraming(t *testing.T) {
	root := &cobra.Command{Use: "leapview"}
	login := &cobra.Command{Use: "login", Annotations: map[string]string{FramingAnnotation: "events"}}
	login.Flags().String("format", "text", "output")
	artifact := &cobra.Command{Use: "export"}
	artifact.Flags().String("format", "json-schema", "artifact encoding")
	root.AddCommand(login, artifact)
	manifest, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range manifest.Commands {
		if command.ID == "login" && command.Output.Modes[1].Framing != "events" {
			t.Fatalf("login output: %#v", command.Output)
		}
		if command.ID == "export" && command.Output.Modes[0].Format != "json-schema" {
			t.Fatalf("artifact output: %#v", command.Output)
		}
	}
}

func TestJSONOnlyFormatDoesNotAdvertiseArtifactEncodings(t *testing.T) {
	root := &cobra.Command{Use: "leapview"}
	initialize := &cobra.Command{Use: "initialize"}
	initialize.Flags().String("format", "json", "output format (json)")
	root.AddCommand(initialize)
	manifest, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	output := manifest.Commands[1].Output
	if output.DefaultFormat != "json" || len(output.Modes) != 1 || output.Modes[0].Format != "json" || output.Modes[0].Framing != "document" {
		t.Fatalf("JSON-only contract: %#v", output)
	}
}
