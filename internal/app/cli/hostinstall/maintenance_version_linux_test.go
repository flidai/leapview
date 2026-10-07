//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/buildinfo"
)

func TestNativeRuntimeVersionUsesAdvertisedJSONContract(t *testing.T) {
	for _, test := range []struct {
		name, help string
		arguments  []string
	}{
		{"legacy", "Flags:\n      --json   emit machine-readable JSON\n", []string{"--json"}},
		{"current", "Flags:\n      --format string   output format: text or json\n", []string{"--format", "json"}},
		{"both", "Flags:\n      --json   legacy JSON\n      --format string   output format\n", []string{"--format", "json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls [][]string
			identity := buildinfo.Identity{Revision: strings.Repeat("a", 40)}
			e := NativeEffects{execute: func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, args)
				if args[len(args)-1] == "--help" {
					return test.help, nil
				}
				want := append([]string{"exec", "installed", "leapview", "version"}, test.arguments...)
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("unsupported command: %v", args)
				}
				encoded, _ := json.Marshal(identity)
				return string(encoded), nil
			}}
			got, err := e.appVersion(t.Context(), "installed")
			if err != nil || got != identity || len(calls) != 2 {
				t.Fatalf("version discovery failed: %+v, %v, %v", got, err, calls)
			}
		})
	}
}

func TestNativeRuntimeVersionRejectsUnsupportedAndInvalidOutputWithoutFallback(t *testing.T) {
	for _, test := range []struct {
		name, help, output      string
		helpError, versionError error
		calls                   int
	}{
		{name: "help failure", helpError: errors.New("Docker disconnected"), calls: 1},
		{name: "unsupported flags", help: "Flags:\n      --yaml\n", calls: 1},
		{name: "wrong format type", help: "Flags:\n      --format int\n", calls: 1},
		{name: "execution failure", help: "      --json\n", versionError: errors.New("runtime failed"), calls: 2},
		{name: "malformed JSON", help: "      --json\n", output: "text version", calls: 2},
		{name: "missing dirty", help: "      --json\n", output: `{"revision":"` + strings.Repeat("a", 40) + `"}`, calls: 2},
		{name: "dirty", help: "      --json\n", output: `{"dirty":true,"revision":"` + strings.Repeat("a", 40) + `"}`, calls: 2},
		{name: "invalid revision", help: "      --json\n", output: `{"dirty":false,"revision":"unknown"}`, calls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			e := NativeEffects{execute: func(_ context.Context, args ...string) (string, error) {
				calls++
				if args[len(args)-1] == "--help" {
					return test.help, test.helpError
				}
				return test.output, test.versionError
			}}
			if _, err := e.appVersion(t.Context(), "installed"); err == nil || calls != test.calls {
				t.Fatalf("invalid contract accepted or retried: %v, calls %d", err, calls)
			}
		})
	}
}
