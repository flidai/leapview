package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/ctl"
	"github.com/spf13/cobra"
)

func TestCatalogRetainsHiddenCommandsFlagsAliasesAndHandlersWithoutExecution(t *testing.T) {
	fatal := func(*cobra.Command, []string) error { t.Fatal("catalog executed command"); return nil }
	root := &cobra.Command{Use: "leapviewctl", PersistentPreRunE: fatal}
	root.PersistentFlags().String("endpoint", "local", "endpoint")
	hidden := &cobra.Command{Use: "private", Hidden: true, RunE: fatal}
	child := &cobra.Command{Use: "upload <path>", Aliases: []string{"put", "add"}, RunE: fatal, DisableFlagParsing: true}
	child.Flags().String("token", "private-input", "private-input")
	if err := child.MarkFlagRequired("token"); err != nil {
		t.Fatal(err)
	}
	if err := child.Flags().MarkHidden("token"); err != nil {
		t.Fatal(err)
	}
	hidden.AddCommand(child)
	root.AddCommand(hidden)
	rows, err := commandCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("catalog lost commands: %#v", rows)
	}
	if rows[0].HasHandler || !rows[1].Hidden || !rows[1].HasHandler || !rows[2].EffectiveHidden || rows[2].Hidden {
		t.Fatalf("wrong command capabilities: %#v", rows)
	}
	if !reflect.DeepEqual(rows[2].Path, []string{"private", "upload"}) || !reflect.DeepEqual(rows[2].Aliases, []string{"add", "put"}) || !rows[2].DisableFlagParsing {
		t.Fatalf("wrong command identity: %#v", rows[2])
	}
	if len(rows[2].Flags) != 1 || !rows[2].Flags[0].Hidden || !rows[2].Flags[0].Required || rows[2].Flags[0].Name != "token" {
		t.Fatalf("wrong local flags: %#v", rows[2].Flags)
	}
	if len(rows[2].InheritedFlags) != 1 || rows[2].InheritedFlags[0].Name != "endpoint" {
		t.Fatalf("lost inherited flags: %#v", rows[2].InheritedFlags)
	}
	first, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), "private-input") {
		t.Fatal("catalog exposed flag value/default/description")
	}
	if rowAt(t, rows, "private upload").HasHandler != true {
		t.Fatal("catalog lost declared handler")
	}
	again, err := commandCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("catalog bytes changed on repeat traversal")
	}
}

func TestCatalogRejectsDuplicatePathsAndNilRoot(t *testing.T) {
	if _, err := commandCatalog(nil); err == nil {
		t.Fatal("nil root accepted")
	}
	root := &cobra.Command{Use: "leapviewctl"}
	root.AddCommand(&cobra.Command{Use: "same"}, &cobra.Command{Use: "same"})
	if _, err := commandCatalog(root); err == nil {
		t.Fatal("duplicate command path accepted")
	}
}

func TestCompiledSourceBindingRejectsUnstampedAndStaleCatalogs(t *testing.T) {
	source := snapshot{Commit: strings.Repeat("a", 40), WorkingTreeStatus: " M source.go\x00", TrackedDiffSHA256: strings.Repeat("b", 64), SourceFilesSHA256: strings.Repeat("c", 64)}
	stamp := sourceFingerprint(source)
	if err := bindCompiledSource(source, stamp); err != nil {
		t.Fatal(err)
	}
	if err := bindCompiledSource(source, ""); err == nil {
		t.Fatal("unstamped generator accepted")
	}
	for _, changed := range []snapshot{
		{Commit: strings.Repeat("d", 40), WorkingTreeStatus: source.WorkingTreeStatus, TrackedDiffSHA256: source.TrackedDiffSHA256, SourceFilesSHA256: source.SourceFilesSHA256},
		{Commit: source.Commit, WorkingTreeStatus: "changed", TrackedDiffSHA256: source.TrackedDiffSHA256, SourceFilesSHA256: source.SourceFilesSHA256},
		{Commit: source.Commit, WorkingTreeStatus: source.WorkingTreeStatus, TrackedDiffSHA256: strings.Repeat("e", 64), SourceFilesSHA256: source.SourceFilesSHA256},
		{Commit: source.Commit, WorkingTreeStatus: source.WorkingTreeStatus, TrackedDiffSHA256: source.TrackedDiffSHA256, SourceFilesSHA256: strings.Repeat("f", 64)},
	} {
		if err := bindCompiledSource(changed, stamp); err == nil {
			t.Fatal("stale or unrelated checkout accepted")
		}
	}
}

func TestSourceSnapshotTracksContentsDeletionAndSymlinksWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.go", "package initial")
	write("deleted.go", "package deleted")
	for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=Catalog Test", "-c", "user.email=catalog@example.invalid", "commit", "-m", "fixture"}} {
		if _, err := git(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func() snapshot {
		t.Helper()
		source, err := sourceSnapshot(root)
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	initial := read()
	if initial.WorkingTreeStatus != "" || initial != read() {
		t.Fatal("clean source snapshot was not stable")
	}
	write("tracked.go", "package changed")
	first := read()
	write("tracked.go", "package changedagain")
	second := read()
	if first.WorkingTreeStatus != second.WorkingTreeStatus || first.TrackedDiffSHA256 == second.TrackedDiffSHA256 || first.SourceFilesSHA256 == second.SourceFilesSHA256 {
		t.Fatal("tracked contents were not fingerprinted")
	}
	write("untracked.go", "package untracked")
	first = read()
	write("untracked.go", "package untrackedagain")
	second = read()
	if first.WorkingTreeStatus != second.WorkingTreeStatus || first.SourceFilesSHA256 == second.SourceFilesSHA256 {
		t.Fatal("untracked contents were not fingerprinted")
	}
	if err := os.Symlink("/outside/private-file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	first = read()
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside/other-private-file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	second = read()
	if first.WorkingTreeStatus != second.WorkingTreeStatus || first.SourceFilesSHA256 == second.SourceFilesSHA256 {
		t.Fatal("symlink text was not fingerprinted")
	}
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	if deleted := read(); deleted.SourceFilesSHA256 == second.SourceFilesSHA256 || !strings.Contains(deleted.WorkingTreeStatus, "deleted.go") {
		t.Fatal("deleted source was not fingerprinted")
	}
}

func TestBuildProfileUsesCompiledSettingsAndRejectsUnshippedVariants(t *testing.T) {
	for _, tc := range []struct{ cgo, tags, variant string }{{"0", "", "standalone"}, {"1", "duckdb_arrow", "host-payload"}} {
		profile, err := buildProfile(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "CGO_ENABLED", Value: tc.cgo}, {Key: "-tags", Value: tc.tags}}})
		if err != nil {
			t.Fatal(err)
		}
		if profile.Variant != tc.variant || profile.GOOS == "" || profile.GOARCH == "" {
			t.Fatalf("wrong compiled profile: %#v", profile)
		}
	}
	for _, settings := range [][]debug.BuildSetting{nil, {{Key: "CGO_ENABLED", Value: "1"}}, {{Key: "CGO_ENABLED", Value: "0"}, {Key: "-tags", Value: "duckdb_arrow"}}, {{Key: "CGO_ENABLED", Value: "1"}, {Key: "-tags", Value: "duckdb_arrow,extra"}}} {
		if _, err := buildProfile(&debug.BuildInfo{Settings: settings}); err == nil {
			t.Fatalf("unshipped settings accepted: %#v", settings)
		}
	}
}

func TestActualControllerTreeRetainsHostMaintenanceAndHiddenWorker(t *testing.T) {
	root, err := ctl.NewCommand(context.Background(), composectl.Options{Root: t.TempDir(), DockerBin: "/absent/docker", Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := commandCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	worker, upgrade, migrate := rowAt(t, rows, "qualify client-worker"), rowAt(t, rows, "host upgrade"), rowAt(t, rows, "host upgrade migrate-copy")
	if !worker.Hidden || !worker.HasHandler || !migrate.HasHandler {
		t.Fatalf("lost controller capabilities: %#v %#v", worker, migrate)
	}
	// Both shipped builds contain the path. The tagged build adds the direct
	// transition handler and its flags; the standalone build keeps a group.
	transition := false
	for _, flag := range upgrade.Flags {
		if flag.Name == "operation-id" {
			transition = true
		}
	}
	if upgrade.HasHandler != transition {
		t.Fatalf("host upgrade handler does not match build-specific flags: %#v", upgrade)
	}
}

func rowAt(t *testing.T, rows []commandEntry, path string) commandEntry {
	t.Helper()
	for _, row := range rows {
		if row.PathText == path {
			return row
		}
	}
	t.Fatalf("missing constructed path %q", path)
	return commandEntry{}
}
