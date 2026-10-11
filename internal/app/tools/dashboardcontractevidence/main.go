// Command dashboardcontractevidence records real project compiler outcomes for
// exact Playground fixture sources. It never executes queries or deploys.
package main

import (
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

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
)

type EvidenceDocument struct {
	FormatVersion int               `json:"formatVersion"`
	CompileOnly   bool              `json:"compileOnly"`
	Metadata      Metadata          `json:"metadata"`
	Fixtures      []FixtureEvidence `json:"fixtures"`
}
type BuildFingerprint struct {
	GoVersion    string            `json:"goVersion"`
	GOOS         string            `json:"goos"`
	GOARCH       string            `json:"goarch"`
	Tags         string            `json:"tags"`
	Settings     map[string]string `json:"settings"`
	Dependencies []*debug.Module   `json:"dependencies"`
}
type Metadata struct {
	DiagnosticNormalization string           `json:"diagnosticNormalization"`
	CompilerCommit          string           `json:"compilerCommit"`
	CompilerVersion         string           `json:"compilerVersion"`
	CompilerFingerprint     string           `json:"compilerFingerprint"`
	CompilerSourceDigest    string           `json:"compilerSourceDigest"`
	Build                   BuildFingerprint `json:"build"`
	TrackedSourceCount      int              `json:"trackedSourceCount"`
	UntrackedSourcePaths    []string         `json:"untrackedSourcePaths"`
	ModifiedSourcePaths     []string         `json:"modifiedSourcePaths"`
	BundledResourcesDigest  string           `json:"bundledResourcesDigest"`
	SchemaDigest            string           `json:"schemaDigest"`
}

func main() {
	repo := flag.String("repo", ".", "LeapView repository root")
	update := flag.Bool("update", false, "explicitly update playground/dashboard-contract-evidence.json; otherwise verify existing evidence")
	check := flag.Bool("check", false, "verify stable evidence inputs/results; recorded Git provenance is informational")
	flag.Parse()
	if *update && *check {
		fatal(fmt.Errorf("choose either -update or -check"))
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		fatal(err)
	}
	metadata, err := fingerprint(root)
	if err != nil {
		fatal(err)
	}
	fixtures, err := compileCorpus(root)
	if err != nil {
		fatal(err)
	}
	current := EvidenceDocument{FormatVersion: 1, CompileOnly: true, Metadata: metadata, Fixtures: fixtures}
	content, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		fatal(err)
	}
	content = append(content, '\n')
	path := filepath.Join(root, "playground/dashboard-contract-evidence.json")
	if *update {
		if err := os.WriteFile(path, content, 0644); err != nil {
			fatal(err)
		}
		fmt.Printf("Updated %s with %d exact-source compiler fixtures\n", path, len(fixtures))
		return
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	var recorded EvidenceDocument
	if err := json.Unmarshal(existing, &recorded); err != nil {
		fatal(err)
	}
	if err := compareEvidence(recorded, current); err != nil {
		fatal(err)
	}
	fmt.Printf("Verified %d exact-source compiler fixtures\n", len(fixtures))
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
func command(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

// Fingerprint the actual local dependency source/embedded inputs selected by
// go list, including ignored generated files and new untracked implementation.
// HEAD alone cannot identify a dirty compiler build. External module checksums
// and relevant build settings are included separately; volatile temp paths,
// timestamps and binary paths are excluded.
func fingerprint(repo string) (Metadata, error) {
	metadata := Metadata{CompilerVersion: projectartifact.CompilerVersion, DiagnosticNormalization: "temporary source-root paths removed; complete diagnostic sibling subtrees sorted", UntrackedSourcePaths: []string{}, ModifiedSourcePaths: []string{}}
	head, err := command(repo, "git", "rev-parse", "HEAD")
	if err != nil {
		return metadata, err
	}
	metadata.CompilerCommit = strings.TrimSpace(string(head))
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return metadata, fmt.Errorf("Go build information unavailable")
	}
	build := BuildFingerprint{GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Settings: map[string]string{}, Dependencies: info.Deps}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "-tags", "CGO_ENABLED", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS", "GOAMD64", "GOARM64", "GOEXPERIMENT":
			build.Settings[setting.Key] = setting.Value
		}
	}
	build.Tags = build.Settings["-tags"]
	if build.Tags != "duckdb_arrow" {
		return metadata, fmt.Errorf("generate evidence with exactly -tags duckdb_arrow (got %q)", build.Tags)
	}
	metadata.Build = build
	trackedRaw, err := command(repo, "git", "ls-files", "-z")
	if err != nil {
		return metadata, err
	}
	tracked := map[string]bool{}
	for _, path := range strings.Split(string(trackedRaw), "\x00") {
		tracked[path] = true
	}
	changedRaw, err := command(repo, "git", "diff", "HEAD", "--name-only", "-z")
	if err != nil {
		return metadata, err
	}
	changed := map[string]bool{}
	for _, path := range strings.Split(string(changedRaw), "\x00") {
		changed[path] = true
	}
	cmd := exec.Command("go", "list", "-deps", "-json", "-tags", "duckdb_arrow", "./internal/app/tools/dashboardcontractevidence")
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		return metadata, fmt.Errorf("enumerate compiler dependency sources: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	files := map[string][]byte{}
	for {
		var pkg struct {
			Dir                                                             string
			GoFiles, CgoFiles, CFiles, CXXFiles, HFiles, SFiles, EmbedFiles []string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return metadata, err
		}
		relative, err := filepath.Rel(repo, pkg.Dir)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		names := append(pkg.GoFiles, pkg.CgoFiles...)
		names = append(names, pkg.CFiles...)
		names = append(names, pkg.CXXFiles...)
		names = append(names, pkg.HFiles...)
		names = append(names, pkg.SFiles...)
		names = append(names, pkg.EmbedFiles...)
		for _, name := range names {
			path := filepath.Join(relative, name)
			content, err := os.ReadFile(filepath.Join(repo, path))
			if err != nil {
				return metadata, err
			}
			files[filepath.ToSlash(path)] = content
		}
	}
	// These files bind external dependency resolution and contract inputs even
	// when the generated schema itself is embedded by another Go package.
	for _, path := range []string{"go.mod", "go.sum", "api/dashboard/main.tsp", "api/visualization/main.tsp", "schemas/json/dashboard-document.schema.json"} {
		content, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			return metadata, err
		}
		files[path] = content
	}
	inventory := sourceInventory(files)
	metadata.CompilerSourceDigest = jsonDigest(inventory)
	for _, file := range inventory {
		if tracked[file.Path] {
			metadata.TrackedSourceCount++
		} else {
			metadata.UntrackedSourcePaths = append(metadata.UntrackedSourcePaths, file.Path)
		}
		if changed[file.Path] {
			metadata.ModifiedSourcePaths = append(metadata.ModifiedSourcePaths, file.Path)
		}
	}
	sort.Slice(metadata.Build.Dependencies, func(i, j int) bool { return metadata.Build.Dependencies[i].Path < metadata.Build.Dependencies[j].Path })
	metadata.CompilerFingerprint = jsonDigest(struct {
		SourceDigest string
		Build        BuildFingerprint
	}{metadata.CompilerSourceDigest, metadata.Build})
	support, err := supportingFiles(repo)
	if err != nil {
		return metadata, err
	}
	metadata.BundledResourcesDigest = jsonDigest(sourceInventory(support))
	metadata.SchemaDigest = digest(files["schemas/json/dashboard-document.schema.json"])
	return metadata, nil
}

// Git commit and index classification describe the recording event. They are
// not compiler inputs, so staging/committing unchanged sources cannot stale
// evidence or make the artifact depend on a commit containing its own hash.
func compareEvidence(recorded, current EvidenceDocument) error {
	recordedBuild, currentBuild := recorded.Metadata.Build, current.Metadata.Build
	recordedBuild.Dependencies, currentBuild.Dependencies = nil, nil
	if !reflect.DeepEqual(recordedBuild, currentBuild) {
		return fmt.Errorf("evidence environment mismatch: recorded %s %s/%s tags=%s; current %s %s/%s tags=%s; recorded evidence is not a result for this build environment", recordedBuild.GoVersion, recordedBuild.GOOS, recordedBuild.GOARCH, recordedBuild.Tags, currentBuild.GoVersion, currentBuild.GOOS, currentBuild.GOARCH, currentBuild.Tags)
	}
	for _, value := range []*EvidenceDocument{&recorded, &current} {
		value.Metadata.CompilerCommit = ""
		value.Metadata.TrackedSourceCount = 0
		value.Metadata.UntrackedSourcePaths = nil
		value.Metadata.ModifiedSourcePaths = nil
	}
	left, err := json.Marshal(recorded)
	if err != nil {
		return err
	}
	right, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if string(left) != string(right) {
		return fmt.Errorf("compiler evidence inputs or outcomes changed; run go run -tags duckdb_arrow ./internal/app/tools/dashboardcontractevidence -update after reviewing the changes")
	}
	return nil
}
