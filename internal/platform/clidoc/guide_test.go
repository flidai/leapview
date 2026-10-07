package clidoc

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type brokenWriter struct{ err error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAgentGuideUsesCatalogAndPropagatesWriteFailure(t *testing.T) {
	manifest := Manifest{SchemaVersion: SchemaVersion, Commands: []Command{{ID: "root", Usage: "leapview [flags]", Options: []Option{{Name: "llms", Type: "bool", Default: "false", Description: "Print agent guidance"}}, Output: Output{DefaultFormat: "text", Modes: []OutputMode{{Format: "text", Framing: "lines"}}}}, {ID: "inspect", Usage: "leapview inspect <name> [flags]", Effect: "read", Confirmation: "never", Output: Output{DefaultFormat: "json", Modes: []OutputMode{{Format: "json", Framing: "document"}}}}}}
	var out bytes.Buffer
	if err := WriteAgentGuide(&out, manifest, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Binary version: `1.2.3`", "leapview inspect <name> [flags]", "json (document)", "--llms", "130 client interruption by SIGINT", "Successfully drained serve shutdown exits 0", "not a read-only diagnostic"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	failure := errors.New("closed output")
	if err := WriteAgentGuide(brokenWriter{failure}, manifest, "1.2.3"); !errors.Is(err, failure) {
		t.Fatalf("write error: %v", err)
	}
}
