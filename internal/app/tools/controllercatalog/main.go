// Command controllercatalog inventories the constructed controller tree. It
// never executes a command or reads installation state, credentials or Docker.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/ctl"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type catalog struct {
	SchemaVersion int            `json:"schemaVersion"`
	Product       string         `json:"product"`
	Scope         string         `json:"scope"`
	Build         profile        `json:"build"`
	Source        snapshot       `json:"source"`
	Commands      []commandEntry `json:"commands"`
}

// The broker binds the source snapshot before Go compilation starts. Runtime
// binding alone could incorrectly stamp an old binary or an unrelated -root.
var compiledSourceFingerprint string

type profile struct {
	Variant    string   `json:"variant"`
	GOOS       string   `json:"goos"`
	GOARCH     string   `json:"goarch"`
	CGOEnabled bool     `json:"cgoEnabled"`
	Tags       []string `json:"tags"`
}

type snapshot struct {
	Commit            string `json:"commit"`
	WorkingTreeStatus string `json:"workingTreeStatus"`
	TrackedDiffSHA256 string `json:"trackedDiffSHA256"`
	SourceFilesSHA256 string `json:"sourceFilesSHA256"`
}

type commandEntry struct {
	Path               []string    `json:"path"`
	PathText           string      `json:"pathText"`
	Use                string      `json:"use"`
	Aliases            []string    `json:"aliases"`
	Hidden             bool        `json:"hidden"`
	EffectiveHidden    bool        `json:"effectiveHidden"`
	HasHandler         bool        `json:"hasHandler"`
	DisableFlagParsing bool        `json:"disableFlagParsing"`
	Flags              []flagEntry `json:"flags"`
	InheritedFlags     []flagEntry `json:"inheritedFlags"`
}

type flagEntry struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand"`
	Type      string `json:"type"`
	Hidden    bool   `json:"hidden"`
	Required  bool   `json:"required"`
}

func main() {
	root := flag.String("root", ".", "repository to bind to the catalog")
	out := flag.String("out", ".tmp/audit/controller-catalogs", "catalog output directory, relative to repository unless absolute")
	flag.Parse()
	if err := generate(*root, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(root, out string) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("compiled build settings are unavailable")
	}
	build, err := buildProfile(info)
	if err != nil {
		return err
	}
	before, err := sourceSnapshot(root)
	if err != nil {
		return err
	}
	if err := bindCompiledSource(before, compiledSourceFingerprint); err != nil {
		return err
	}
	command, err := ctl.NewCommand(context.Background(), composectl.Options{
		Root: "/audit-controller", DockerBin: "/audit-controller/absent-docker",
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return err
	}
	commands, err := commandCatalog(command)
	if err != nil {
		return err
	}
	after, err := sourceSnapshot(root)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("source changed while generating controller catalog; rerun on a stable checkout")
	}
	encoded, err := json.MarshalIndent(catalog{SchemaVersion: 1, Product: "leapviewctl", Scope: "constructed-explicit-commands", Build: build, Source: before, Commands: commands}, "", "  ")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "leapviewctl-"+build.Variant+".json"), append(encoded, '\n'), 0o644)
}

func buildProfile(info *debug.BuildInfo) (profile, error) {
	build := profile{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Tags: []string{}}
	cgo := ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "CGO_ENABLED":
			cgo = setting.Value
		case "-tags":
			build.Tags = strings.FieldsFunc(setting.Value, func(r rune) bool { return r == ',' || r == ' ' })
		}
	}
	sort.Strings(build.Tags)
	switch {
	case cgo == "0" && len(build.Tags) == 0:
		build.Variant = "standalone"
	case cgo == "1" && reflect.DeepEqual(build.Tags, []string{"duckdb_arrow"}):
		build.Variant, build.CGOEnabled = "host-payload", true
	default:
		return profile{}, fmt.Errorf("unshipped controller build settings: CGO_ENABLED=%q tags=%v; use CGO=0 without tags or CGO=1 with duckdb_arrow", cgo, build.Tags)
	}
	return build, nil
}

func commandCatalog(root *cobra.Command) ([]commandEntry, error) {
	if root == nil {
		return nil, fmt.Errorf("controller root is required")
	}
	rows, seen := []commandEntry{}, map[string]bool{}
	var visit func(*cobra.Command, []string, bool) error
	visit = func(command *cobra.Command, path []string, ancestorHidden bool) error {
		key := strings.Join(path, " ")
		if seen[key] {
			return fmt.Errorf("duplicate controller command path %q", key)
		}
		seen[key] = true
		aliases := append([]string{}, command.Aliases...)
		sort.Strings(aliases)
		rows = append(rows, commandEntry{
			Path: append([]string{}, path...), PathText: key, Use: command.Use, Aliases: aliases,
			Hidden: command.Hidden, EffectiveHidden: ancestorHidden || command.Hidden,
			// Cobra considers help-only RunE callbacks runnable too. Retain the
			// declared handler without inferring product effects or journey proof.
			HasHandler: command.Runnable(), DisableFlagParsing: command.DisableFlagParsing,
			Flags: catalogFlags(command.LocalFlags()), InheritedFlags: catalogFlags(command.InheritedFlags()),
		})
		children := append([]*cobra.Command{}, command.Commands()...)
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err := visit(child, append(append([]string{}, path...), child.Name()), ancestorHidden || command.Hidden); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, []string{}, false); err != nil {
		return nil, err
	}
	return rows, nil
}

func catalogFlags(flags *pflag.FlagSet) []flagEntry {
	rows := []flagEntry{}
	flags.VisitAll(func(flag *pflag.Flag) {
		rows = append(rows, flagEntry{Name: flag.Name, Shorthand: flag.Shorthand, Type: flag.Value.Type(),
			Hidden: flag.Hidden, Required: len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0})
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func git(root string, args ...string) ([]byte, error) {
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("git %v: %w", args, err)
	}
	return output, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sourceFingerprint(source snapshot) string {
	return digest([]byte(strings.Join([]string{source.Commit, digest([]byte(source.WorkingTreeStatus)), source.TrackedDiffSHA256, source.SourceFilesSHA256}, "\n") + "\n"))
}

func bindCompiledSource(source snapshot, stamp string) error {
	if stamp == "" {
		return fmt.Errorf("controller generator lacks pre-compilation source binding; run task audit:controller-catalogs")
	}
	if stamp != sourceFingerprint(source) {
		return fmt.Errorf("controller generator source changed during compilation or differs from -root; rerun task audit:controller-catalogs")
	}
	return nil
}

func sourceSnapshot(root string) (snapshot, error) {
	var source snapshot
	for _, input := range []struct {
		args []string
		set  func([]byte)
	}{
		{[]string{"rev-parse", "HEAD"}, func(b []byte) { source.Commit = strings.TrimSpace(string(b)) }},
		{[]string{"status", "--porcelain=v1", "-z"}, func(b []byte) { source.WorkingTreeStatus = string(b) }},
		{[]string{"diff", "--no-ext-diff", "--binary", "HEAD"}, func(b []byte) { source.TrackedDiffSHA256 = digest(b) }},
	} {
		b, err := git(root, input.args...)
		if err != nil {
			return snapshot{}, err
		}
		input.set(b)
	}
	paths, err := git(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return snapshot{}, err
	}
	names := strings.Split(strings.TrimSuffix(string(paths), "\x00"), "\x00")
	sort.Strings(names)
	hash, seen := sha256.New(), map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		path := filepath.Join(root, name)
		stat, err := os.Lstat(path)
		state, checksum := "missing", ""
		if err != nil && !os.IsNotExist(err) {
			return snapshot{}, err
		}
		if err == nil {
			var data []byte
			switch {
			case stat.Mode()&os.ModeSymlink != 0:
				state = "symlink"
				var target string
				target, err = os.Readlink(path)
				data = []byte(target)
			case stat.Mode().IsRegular():
				state = "present"
				data, err = os.ReadFile(path)
			default:
				state = "non_file"
			}
			if err != nil {
				return snapshot{}, err
			}
			if data != nil {
				checksum = digest(data)
			}
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%s\n", name, state, checksum)
	}
	source.SourceFilesSHA256 = hex.EncodeToString(hash.Sum(nil))
	return source, nil
}
