// Command dependencyreport produces and validates a content-bound
// dependency security evidence document.  The document deliberately contains
// normalized scanner results rather than raw command output: it is safe to
// retain as a CI artifact and can be checked again against the exact checkout.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SchemaVersion        = "leapview.dependency-clearance/v1"
	defaultWaiver        = "security/dependency-waivers.json"
	goScannerMemoryLimit = "4GiB"
)

var requiredFiles = []string{
	"go.mod",
	"go.sum",
	"bun.lock",
	"desktop/bun.lock",
	"pkg/apigen/typespec/package-lock.json",
}

var requiredGraphs = []string{"root-bun", "desktop-bun", "apigen-npm", "go"}

type SourceEvidence struct {
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
}

type ToolIdentity struct {
	RuntimeName          string   `json:"runtime_name"`
	RuntimeVersion       string   `json:"runtime_version"`
	ScannerName          string   `json:"scanner_name"`
	ScannerVersion       string   `json:"scanner_version"`
	DatabaseLastModified string   `json:"database_last_modified,omitempty"`
	Command              []string `json:"command"`
	Environment          []string `json:"environment,omitempty"`
}

type ToolchainEvidence struct {
	NodeVersion string `json:"node_version"`
	TaskVersion string `json:"task_version"`
}

type Finding struct {
	Advisory     string `json:"advisory"`
	Dependency   string `json:"dependency"`
	Severity     string `json:"severity"`
	Reachability string `json:"reachability,omitempty"`
	Title        string `json:"title,omitempty"`
	URL          string `json:"url,omitempty"`
	PackageCount int    `json:"package_count,omitempty"`
}

type ScanResult struct {
	Status             string         `json:"status"`
	PackageCount       int            `json:"package_count"`
	VulnerablePackages int            `json:"vulnerable_package_count"`
	AdvisoryNotices    int            `json:"non_reachable_advisory_count,omitempty"`
	SeverityCounts     map[string]int `json:"severity_counts"`
	Findings           []Finding      `json:"findings,omitempty"`
	Notices            []Finding      `json:"non_reachable_notices,omitempty"`
	Error              string         `json:"error,omitempty"`
}

type GraphResult struct {
	ID       string       `json:"id"`
	Manager  string       `json:"manager"`
	Manifest string       `json:"manifest"`
	Lockfile string       `json:"lockfile"`
	Identity ToolIdentity `json:"identity"`
	Result   ScanResult   `json:"result"`
}

// Waiver is intentionally typed and reviewable. Every field is required so an
// exception cannot become a permanent, ownerless suppression.
type Waiver struct {
	Advisory            string    `json:"advisory"`
	Dependency          string    `json:"dependency"`
	Owner               string    `json:"owner"`
	Reachability        string    `json:"reachability"`
	CompensatingControl string    `json:"compensating_control"`
	Created             time.Time `json:"created"`
	Expiry              time.Time `json:"expiry"`
}

type Counts struct {
	Packages        int            `json:"packages"`
	Vulnerabilities int            `json:"vulnerabilities"`
	AdvisoryNotices int            `json:"non_reachable_advisories"`
	Severity        map[string]int `json:"severity"`
}

type Clearance struct {
	Cleared bool     `json:"cleared"`
	Reasons []string `json:"reasons,omitempty"`
}

type Report struct {
	SchemaVersion string            `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Source        SourceEvidence    `json:"source"`
	Toolchain     ToolchainEvidence `json:"toolchain"`
	Digests       map[string]string `json:"digests"`
	Graphs        []GraphResult     `json:"graphs"`
	Waivers       []Waiver          `json:"waivers,omitempty"`
	Counts        Counts            `json:"counts"`
	Clearance     Clearance         `json:"clearance"`
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner func(context.Context, string, string, ...string) (CommandResult, error)

type Dependencies struct {
	Run Runner
	Now func() time.Time
}

type Config struct {
	Root           string
	Output         string
	Waivers        string
	AllowDirty     bool
	ExpectedCommit string
}

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: dependencyreport <report|check> [flags]")
	}
	deps := Dependencies{Run: execRunner, Now: func() time.Time { return time.Now().UTC() }}
	switch args[0] {
	case "report":
		fs := flag.NewFlagSet("report", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		cfg := Config{}
		fs.StringVar(&cfg.Root, "root", ".", "repository root")
		fs.StringVar(&cfg.Output, "output", "dependency-security-report.json", "report output path")
		fs.StringVar(&cfg.Waivers, "waivers", defaultWaiver, "JSON waiver file (optional)")
		fs.BoolVar(&cfg.AllowDirty, "allow-dirty", false, "allow a dirty checkout (report remains uncleared)")
		fs.StringVar(&cfg.ExpectedCommit, "expected-commit", "", "require this source commit")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return generate(context.Background(), cfg, deps)
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		root := "."
		input := "dependency-security-report.json"
		allowDirty := false
		expectedCommit := ""
		fs.StringVar(&root, "root", ".", "repository root")
		fs.StringVar(&input, "input", input, "report input path")
		fs.BoolVar(&allowDirty, "allow-dirty", false, "allow a dirty checkout")
		fs.StringVar(&expectedCommit, "expected-commit", "", "require this source commit")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		data, err := os.ReadFile(input)
		if err != nil {
			return fmt.Errorf("read report: %w", err)
		}
		report, err := decodeReport(data)
		if err != nil {
			return err
		}
		if err := validateReport(context.Background(), report, Config{Root: root, Output: input, AllowDirty: allowDirty, ExpectedCommit: expectedCommit}, deps); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (want report or check)", args[0])
	}
}

func execRunner(ctx context.Context, dir, name string, args ...string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if name == "go" && containsArgument(args, "golang.org/x/vuln/cmd/govulncheck@v1.5.0") {
		cmd.Env = append(cmd.Environ(), "GOMEMLIMIT="+goScannerMemoryLimit)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

func containsArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func generate(ctx context.Context, cfg Config, deps Dependencies) (err error) {
	if strings.TrimSpace(cfg.Output) == "" {
		return errors.New("report output path is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return err
	}
	cfg.Root = root
	output, err := filepath.Abs(cfg.Output)
	if err != nil {
		return err
	}
	cfg.Output = output
	// A failed run must not leave an older clean document that an operator could
	// accidentally upload. Remove it only after resolving the exact target.
	defer func() {
		if err != nil {
			_ = os.Remove(output)
		}
	}()
	report, err := collectReport(ctx, cfg, deps)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeAtomic(output, data); err != nil {
		return err
	}
	return nil
}

func collectReport(ctx context.Context, cfg Config, deps Dependencies) (Report, error) {
	if deps.Run == nil {
		deps.Run = execRunner
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	source, err := gitSource(ctx, cfg.Root, cfg.Output, deps.Run)
	if err != nil {
		return Report{}, err
	}
	if source.Dirty && !cfg.AllowDirty {
		return Report{}, errors.New("dependency report requires a clean checkout (use --allow-dirty only for local diagnostics)")
	}
	if cfg.ExpectedCommit != "" && source.Commit != cfg.ExpectedCommit {
		return Report{}, fmt.Errorf("source commit mismatch: got %s, want %s", source.Commit, cfg.ExpectedCommit)
	}
	if err := ensureGraphFiles(cfg.Root); err != nil {
		return Report{}, err
	}
	digests, err := fileDigests(cfg.Root)
	if err != nil {
		return Report{}, err
	}
	waivers, err := readWaivers(cfg.Root, cfg.Waivers)
	if err != nil {
		return Report{}, err
	}
	if err := validateWaivers(waivers, deps.Now()); err != nil {
		return Report{}, err
	}
	toolchain, err := captureToolchain(ctx, cfg.Root, deps.Run)
	if err != nil {
		return Report{}, err
	}
	graphs := scanGraphs(ctx, cfg.Root, deps.Run)
	for _, graph := range graphs {
		if graph.Result.Status == "scanner_error" {
			return Report{}, fmt.Errorf("scanner failed for %s: %s", graph.ID, graph.Result.Error)
		}
	}
	report := Report{SchemaVersion: SchemaVersion, GeneratedAt: deps.Now().UTC(), Source: source, Toolchain: toolchain, Digests: digests, Graphs: graphs, Waivers: waivers}
	report.Counts = summarize(graphs)
	report.Clearance = clearance(graphs, waivers, source, deps.Now())
	if err := validateWaiversUsed(graphs, waivers); err != nil {
		return Report{}, err
	}
	if !report.Clearance.Cleared {
		return Report{}, fmt.Errorf("dependency clearance failed: %s", strings.Join(report.Clearance.Reasons, "; "))
	}
	return report, nil
}

func scanGraphs(ctx context.Context, root string, run Runner) []GraphResult {
	graphs := make([]GraphResult, 0, len(requiredGraphs))
	for _, graph := range requiredGraphs {
		var result GraphResult
		switch graph {
		case "root-bun":
			result = scanBun(ctx, root, "root-bun", "package.json", "bun.lock", run)
		case "desktop-bun":
			result = scanBun(ctx, filepath.Join(root, "desktop"), "desktop-bun", "package.json", "bun.lock", run)
		case "apigen-npm":
			result = scanNPM(ctx, root, run)
		case "go":
			result = scanGo(ctx, root, run)
		}
		graphs = append(graphs, result)
	}
	return graphs
}

func captureToolchain(ctx context.Context, root string, run Runner) (ToolchainEvidence, error) {
	node, err := run(ctx, root, "node", "--version")
	if err != nil || node.ExitCode != 0 || strings.TrimSpace(string(node.Stdout)) == "" {
		return ToolchainEvidence{}, errors.New("node --version failed")
	}
	task, err := run(ctx, root, "task", "--version")
	if err != nil || task.ExitCode != 0 || strings.TrimSpace(string(task.Stdout)) == "" {
		return ToolchainEvidence{}, errors.New("task --version failed")
	}
	return ToolchainEvidence{NodeVersion: strings.TrimSpace(string(node.Stdout)), TaskVersion: strings.TrimSpace(string(task.Stdout))}, nil
}

func gitSource(ctx context.Context, root, output string, run Runner) (SourceEvidence, error) {
	commitResult, err := run(ctx, root, "git", "rev-parse", "HEAD")
	if err != nil || commitResult.ExitCode != 0 {
		return SourceEvidence{}, errors.New("cannot determine source commit")
	}
	commit := strings.TrimSpace(string(commitResult.Stdout))
	if !isCommit(commit) {
		return SourceEvidence{}, errors.New("git returned malformed source commit")
	}
	statusResult, err := run(ctx, root, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil || statusResult.ExitCode != 0 {
		return SourceEvidence{}, errors.New("cannot determine checkout state")
	}
	outputRel, _ := filepath.Rel(root, output)
	outputRel = filepath.ToSlash(outputRel)
	dirty := false
	for _, line := range strings.Split(strings.TrimSpace(string(statusResult.Stdout)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path := line
		if len(path) > 3 {
			path = strings.TrimSpace(path[3:])
		}
		if strings.HasPrefix(path, "\"") && strings.HasSuffix(path, "\"") {
			path = strings.Trim(path, "\"")
		}
		if filepath.ToSlash(path) == outputRel {
			continue
		}
		dirty = true
		break
	}
	return SourceEvidence{Commit: commit, Dirty: dirty}, nil
}

func isCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func fileDigests(root string) (map[string]string, error) {
	digests := make(map[string]string, len(requiredFiles))
	for _, name := range requiredFiles {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("read required dependency graph %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		digests[name] = hex.EncodeToString(sum[:])
	}
	return digests, nil
}

func ensureGraphFiles(root string) error {
	files := []string{
		"package.json", "bun.lock",
		"desktop/package.json", "desktop/bun.lock",
		"pkg/apigen/typespec/package.json", "pkg/apigen/typespec/package-lock.json",
		"go.mod", "go.sum",
	}
	for _, name := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("missing dependency graph file %s: %w", name, err)
		}
	}
	return nil
}

func readWaivers(root, name string) ([]Waiver, error) {
	if name == "" {
		name = defaultWaiver
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(path))
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && name == defaultWaiver {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read waiver file: %w", err)
	}
	var list []Waiver
	if err := json.Unmarshal(data, &list); err == nil {
		return list, nil
	}
	var envelope struct {
		Waivers []Waiver `json:"waivers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("malformed waiver file: %w", err)
	}
	return envelope.Waivers, nil
}

func validateWaivers(waivers []Waiver, now time.Time) error {
	for i, waiver := range waivers {
		if strings.TrimSpace(waiver.Advisory) == "" || strings.TrimSpace(waiver.Dependency) == "" || strings.TrimSpace(waiver.Owner) == "" || strings.TrimSpace(waiver.Reachability) == "" || strings.TrimSpace(waiver.CompensatingControl) == "" {
			return fmt.Errorf("waiver %d is missing a required field", i)
		}
		if waiver.Created.IsZero() || waiver.Expiry.IsZero() {
			return fmt.Errorf("waiver %d must include created and expiry", i)
		}
		if !waiver.Expiry.After(now) {
			return fmt.Errorf("waiver %d has expired", i)
		}
		if waiver.Created.After(now.Add(5 * time.Minute)) {
			return fmt.Errorf("waiver %d is dated in the future", i)
		}
		if !waiver.Expiry.After(waiver.Created) {
			return fmt.Errorf("waiver %d expiry must be after created", i)
		}
	}
	return nil
}

func scanBun(ctx context.Context, dir, id, manifest, lockfile string, run Runner) GraphResult {
	result := GraphResult{ID: id, Manager: "bun", Manifest: manifest, Lockfile: lockfile}
	version, versionErr := run(ctx, dir, "bun", "--version")
	if versionErr != nil || version.ExitCode != 0 {
		result.Result = scannerError("bun version command failed")
		return result
	}
	runtimeVersion := strings.TrimSpace(string(version.Stdout))
	audit, err := run(ctx, dir, "bun", "audit", "--json")
	result.Identity = ToolIdentity{RuntimeName: "bun", RuntimeVersion: runtimeVersion, ScannerName: "bun audit", ScannerVersion: runtimeVersion, Command: []string{"bun", "audit", "--json"}}
	if err != nil {
		result.Result = scannerError(err.Error())
		return result
	}
	if len(bytes.TrimSpace(audit.Stdout)) == 0 {
		result.Result = scannerError("bun audit returned empty output")
		return result
	}
	findings, packages, err := parseBun(audit.Stdout)
	if err != nil {
		result.Result = scannerError("malformed bun audit output: " + err.Error())
		return result
	}
	result.Result = makeResult(findings, packages, audit.ExitCode)
	return result
}

func parseBun(data []byte) ([]Finding, int, error) {
	var groups map[string][]struct {
		ID       any    `json:"id"`
		Severity string `json:"severity"`
		Title    string `json:"title"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &groups); err != nil {
		return nil, 0, err
	}
	var findings []Finding
	for dependency, advisories := range groups {
		for _, advisory := range advisories {
			id := normalizeAdvisoryID(advisory.ID)
			if id == "<nil>" || id == "" {
				id = dependency
			}
			findings = append(findings, Finding{Advisory: id, Dependency: dependency, Severity: normalizeSeverity(advisory.Severity), Title: advisory.Title, URL: advisory.URL})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Dependency+findings[i].Advisory < findings[j].Dependency+findings[j].Advisory
	})
	return findings, len(groups), nil
}

func normalizeAdvisoryID(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func scanNPM(ctx context.Context, root string, run Runner) GraphResult {
	result := GraphResult{ID: "apigen-npm", Manager: "npm", Manifest: "pkg/apigen/typespec/package.json", Lockfile: "pkg/apigen/typespec/package-lock.json"}
	version, err := run(ctx, root, "npm", "--version")
	if err != nil || version.ExitCode != 0 {
		result.Result = scannerError("npm version command failed")
		return result
	}
	runtimeVersion := strings.TrimSpace(string(version.Stdout))
	audit, err := run(ctx, root, "npm", "--prefix", "pkg/apigen/typespec", "audit", "--json")
	result.Identity = ToolIdentity{RuntimeName: "npm", RuntimeVersion: runtimeVersion, ScannerName: "npm audit", ScannerVersion: runtimeVersion, Command: []string{"npm", "--prefix", "pkg/apigen/typespec", "audit", "--json"}}
	if err != nil {
		result.Result = scannerError(err.Error())
		return result
	}
	findings, packages, total, err := parseNPM(audit.Stdout)
	if err != nil {
		result.Result = scannerError("malformed npm audit output: " + err.Error())
		return result
	}
	result.Result = makeResult(findings, packages, audit.ExitCode)
	if total > result.Result.PackageCount {
		result.Result.PackageCount = total
	}
	return result
}

func parseNPM(data []byte) ([]Finding, int, int, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &envelope); err != nil {
		return nil, 0, 0, err
	}
	if _, ok := envelope["vulnerabilities"]; !ok {
		return nil, 0, 0, errors.New("npm audit output omitted vulnerabilities")
	}
	if _, ok := envelope["metadata"]; !ok {
		return nil, 0, 0, errors.New("npm audit output omitted metadata")
	}
	var report struct {
		Vulnerabilities map[string]struct {
			Name     string            `json:"name"`
			Severity string            `json:"severity"`
			Via      []json.RawMessage `json:"via"`
		} `json:"vulnerabilities"`
		Metadata struct {
			Dependencies struct {
				Total int `json:"total"`
			} `json:"dependencies"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &report); err != nil {
		return nil, 0, 0, err
	}
	if report.Vulnerabilities == nil {
		return nil, 0, 0, errors.New("npm audit output omitted vulnerabilities")
	}
	var findings []Finding
	for dependency, vulnerability := range report.Vulnerabilities {
		foundAdvisory := false
		for _, raw := range vulnerability.Via {
			var advisory struct {
				Source   any    `json:"source"`
				Title    string `json:"title"`
				URL      string `json:"url"`
				Severity string `json:"severity"`
			}
			if json.Unmarshal(raw, &advisory) != nil || advisory.Source == nil {
				continue
			}
			id := normalizeAdvisoryID(advisory.Source)
			findings = append(findings, Finding{Advisory: id, Dependency: dependency, Severity: normalizeSeverity(advisory.Severity), Title: advisory.Title, URL: advisory.URL})
			foundAdvisory = true
		}
		if !foundAdvisory {
			findings = append(findings, Finding{Advisory: dependency, Dependency: dependency, Severity: normalizeSeverity(vulnerability.Severity)})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Dependency+findings[i].Advisory < findings[j].Dependency+findings[j].Advisory
	})
	return findings, len(report.Vulnerabilities), report.Metadata.Dependencies.Total, nil
}

func scanGo(ctx context.Context, root string, run Runner) GraphResult {
	result := GraphResult{ID: "go", Manager: "go", Manifest: "go.mod", Lockfile: "go.sum"}
	audit, err := run(ctx, root, "go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.5.0", "-json", "./...")
	if err != nil {
		result.Result = scannerError(err.Error())
		return result
	}
	findings, notices, packages, identity, err := parseGo(audit.Stdout)
	if err != nil {
		result.Result = scannerError("malformed govulncheck output: " + err.Error())
		return result
	}
	result.Identity = identity
	result.Result = makeResult(findings, packages, audit.ExitCode)
	result.Result.Notices = notices
	result.Result.AdvisoryNotices = len(notices)
	return result
}

func parseGo(data []byte) ([]Finding, []Finding, int, ToolIdentity, error) {
	identity := ToolIdentity{
		RuntimeName: "go",
		ScannerName: "govulncheck",
		Command:     []string{"go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.5.0", "-json", "./..."},
		Environment: []string{"GOMEMLIMIT=" + goScannerMemoryLimit},
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	observed := map[string]Finding{}
	packages := map[string]bool{}
	seenConfig := false
	seenGraph := false
	for {
		var envelope map[string]json.RawMessage
		if err := decoder.Decode(&envelope); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, 0, identity, err
		}
		if raw, ok := envelope["config"]; ok {
			var config struct {
				ScannerName    string `json:"scanner_name"`
				ScannerVersion string `json:"scanner_version"`
				DBLastModified string `json:"db_last_modified"`
				GoVersion      string `json:"go_version"`
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				return nil, nil, 0, identity, err
			}
			identity.ScannerName, identity.ScannerVersion = config.ScannerName, config.ScannerVersion
			identity.DatabaseLastModified, identity.RuntimeVersion = config.DBLastModified, config.GoVersion
			seenConfig = true
		}
		if raw, ok := envelope["SBOM"]; ok {
			var sbom struct {
				GoVersion string `json:"go_version"`
				Modules   []struct {
					Path string `json:"path"`
				} `json:"modules"`
			}
			if err := json.Unmarshal(raw, &sbom); err != nil {
				return nil, nil, 0, identity, err
			}
			if identity.RuntimeVersion == "" {
				identity.RuntimeVersion = sbom.GoVersion
			}
			for _, module := range sbom.Modules {
				if module.Path != "" {
					packages[module.Path] = true
				}
			}
			seenGraph = true
		}
		if raw, ok := envelope["finding"]; ok {
			var finding struct {
				OSV   string `json:"osv"`
				Trace []struct {
					Module   string `json:"module"`
					Version  string `json:"version"`
					Package  string `json:"package"`
					Function string `json:"function"`
				} `json:"trace"`
			}
			if err := json.Unmarshal(raw, &finding); err != nil {
				return nil, nil, 0, identity, err
			}
			if finding.OSV == "" {
				return nil, nil, 0, identity, errors.New("finding has no advisory")
			}
			dependency := ""
			reachability := "required"
			for _, frame := range finding.Trace {
				if dependency == "" && frame.Module != "" {
					dependency = frame.Module
				}
				if frame.Function != "" {
					reachability = "called"
				} else if frame.Package != "" && reachability != "called" {
					reachability = "imported"
				}
			}
			if dependency == "" {
				dependency = "unknown"
			}
			key := finding.OSV + "\x00" + dependency
			candidate := Finding{Advisory: finding.OSV, Dependency: dependency, Severity: "unknown", Reachability: reachability}
			if previous, exists := observed[key]; !exists || reachabilityRank(candidate.Reachability) > reachabilityRank(previous.Reachability) {
				observed[key] = candidate
			}
		}
	}
	if !seenConfig || !seenGraph {
		return nil, nil, 0, identity, errors.New("govulncheck output omitted config or SBOM")
	}
	var findings, notices []Finding
	for _, finding := range observed {
		if finding.Reachability == "called" {
			findings = append(findings, finding)
		} else {
			notices = append(notices, finding)
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Dependency+findings[i].Advisory < findings[j].Dependency+findings[j].Advisory
	})
	sort.Slice(notices, func(i, j int) bool {
		return notices[i].Dependency+notices[i].Advisory < notices[j].Dependency+notices[j].Advisory
	})
	return findings, notices, len(packages), identity, nil
}

func reachabilityRank(value string) int {
	switch value {
	case "called":
		return 3
	case "imported":
		return 2
	default:
		return 1
	}
}

func scannerError(message string) ScanResult {
	return ScanResult{Status: "scanner_error", SeverityCounts: map[string]int{}, Error: message}
}

func makeResult(findings []Finding, packages, exitCode int) ScanResult {
	counts := map[string]int{}
	for _, finding := range findings {
		counts[normalizeSeverity(finding.Severity)]++
	}
	status := "passed"
	if len(findings) > 0 {
		status = "vulnerable"
	} else if exitCode != 0 {
		status = "scanner_error"
	}
	return ScanResult{Status: status, PackageCount: packages, VulnerablePackages: uniqueDependencies(findings), SeverityCounts: counts, Findings: findings}
}

func normalizeSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "high", "moderate", "medium", "low", "info", "unknown":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func uniqueDependencies(findings []Finding) int {
	seen := map[string]bool{}
	for _, finding := range findings {
		seen[finding.Dependency] = true
	}
	return len(seen)
}

func summarize(graphs []GraphResult) Counts {
	counts := Counts{Severity: map[string]int{}}
	for _, graph := range graphs {
		counts.Packages += graph.Result.PackageCount
		counts.Vulnerabilities += len(graph.Result.Findings)
		counts.AdvisoryNotices += len(graph.Result.Notices)
		for severity, count := range graph.Result.SeverityCounts {
			counts.Severity[severity] += count
		}
	}
	return counts
}

func clearance(graphs []GraphResult, waivers []Waiver, source SourceEvidence, now time.Time) Clearance {
	var reasons []string
	if source.Dirty {
		reasons = append(reasons, "checkout is dirty")
	}
	if err := validateWaivers(waivers, now); err != nil {
		reasons = append(reasons, err.Error())
	}
	for _, graph := range graphs {
		if graph.Result.Status == "scanner_error" {
			reasons = append(reasons, graph.ID+": "+graph.Result.Error)
			continue
		}
		for _, finding := range graph.Result.Findings {
			if !waived(finding, waivers) {
				reasons = append(reasons, graph.ID+": unwaived "+finding.Advisory+" ("+finding.Dependency+")")
			}
		}
	}
	sort.Strings(reasons)
	return Clearance{Cleared: len(reasons) == 0, Reasons: reasons}
}

func waived(finding Finding, waivers []Waiver) bool {
	for _, waiver := range waivers {
		if waiver.Advisory == finding.Advisory && waiver.Dependency == finding.Dependency {
			return true
		}
	}
	return false
}

func validateWaiversUsed(graphs []GraphResult, waivers []Waiver) error {
	for index, waiver := range waivers {
		used := false
		for _, graph := range graphs {
			for _, finding := range graph.Result.Findings {
				if finding.Advisory == waiver.Advisory && finding.Dependency == waiver.Dependency {
					used = true
					break
				}
			}
			if used {
				break
			}
		}
		if !used {
			return fmt.Errorf("waiver %d does not match an observed finding", index)
		}
	}
	return nil
}

func decodeReport(data []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("malformed dependency report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Report{}, errors.New("malformed dependency report: multiple JSON values")
		}
		return Report{}, fmt.Errorf("malformed dependency report: %w", err)
	}
	if report.SchemaVersion != SchemaVersion {
		return Report{}, fmt.Errorf("unsupported report schema %q", report.SchemaVersion)
	}
	return report, nil
}

func validateReport(ctx context.Context, report Report, cfg Config, deps Dependencies) error {
	if deps.Run == nil {
		deps.Run = execRunner
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if report.SchemaVersion != SchemaVersion || report.GeneratedAt.IsZero() {
		return errors.New("report schema or timestamp is invalid")
	}
	if report.GeneratedAt.After(deps.Now().Add(5 * time.Minute)) {
		return errors.New("report timestamp is in the future")
	}
	source, err := gitSource(ctx, cfg.Root, cfg.Output, deps.Run)
	if err != nil {
		return err
	}
	if source.Commit != report.Source.Commit {
		return fmt.Errorf("source commit mismatch: report %s, checkout %s", report.Source.Commit, source.Commit)
	}
	if cfg.ExpectedCommit != "" && report.Source.Commit != cfg.ExpectedCommit {
		return fmt.Errorf("expected commit mismatch: report %s, want %s", report.Source.Commit, cfg.ExpectedCommit)
	}
	if source.Dirty && !cfg.AllowDirty {
		return errors.New("cannot validate dependency report from a dirty checkout")
	}
	if report.Source.Dirty && !cfg.AllowDirty {
		return errors.New("report was generated from a dirty checkout")
	}
	if err := ensureGraphFiles(cfg.Root); err != nil {
		return err
	}
	digests, err := fileDigests(cfg.Root)
	if err != nil {
		return err
	}
	if len(report.Digests) != len(requiredFiles) {
		return errors.New("report is missing dependency graph digests")
	}
	for _, name := range requiredFiles {
		if report.Digests[name] != digests[name] {
			return fmt.Errorf("dependency graph digest mismatch for %s", name)
		}
	}
	if err := validateWaivers(report.Waivers, deps.Now()); err != nil {
		return err
	}
	if err := validateWaiversUsed(report.Graphs, report.Waivers); err != nil {
		return err
	}
	if len(report.Graphs) != len(requiredGraphs) {
		return errors.New("report is missing dependency scan graphs")
	}
	seen := map[string]bool{}
	for _, graph := range report.Graphs {
		if seen[graph.ID] {
			return fmt.Errorf("duplicate dependency graph %s", graph.ID)
		}
		seen[graph.ID] = true
		if graph.Result.Status == "scanner_error" {
			return fmt.Errorf("scanner failed for %s: %s", graph.ID, graph.Result.Error)
		}
		for _, finding := range graph.Result.Findings {
			if !waived(finding, report.Waivers) {
				return fmt.Errorf("unwaived finding %s in %s", finding.Advisory, graph.ID)
			}
		}
	}
	for _, graph := range requiredGraphs {
		if !seen[graph] {
			return fmt.Errorf("report is missing dependency graph %s", graph)
		}
	}
	if !reflect.DeepEqual(report.Counts, summarize(report.Graphs)) {
		return errors.New("report severity or package counts are inconsistent")
	}
	toolchain, err := captureToolchain(ctx, cfg.Root, deps.Run)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(report.Toolchain, toolchain) {
		return errors.New("toolchain identity changed since report generation")
	}
	freshGraphs := scanGraphs(ctx, cfg.Root, deps.Run)
	for _, graph := range freshGraphs {
		if graph.Result.Status == "scanner_error" {
			return fmt.Errorf("scanner failed for %s: %s", graph.ID, graph.Result.Error)
		}
	}
	if !reflect.DeepEqual(report.Graphs, freshGraphs) {
		return errors.New("dependency scan results changed since report generation")
	}
	if expected := clearance(report.Graphs, report.Waivers, report.Source, report.GeneratedAt); !reflect.DeepEqual(report.Clearance, expected) {
		return errors.New("report clearance is inconsistent with scan results")
	}
	if !report.Clearance.Cleared {
		return errors.New("report does not contain clearance")
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dependency-report-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
