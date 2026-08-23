// Package securitypolicy validates the repository's dependency-security
// coverage contract.  The contract is deliberately kept in the repository so
// that adding a new build surface cannot silently bypass dependency updates.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	coverageFile    = ".security/coverage.yaml"
	exceptionsFile  = ".security/exceptions.yaml"
	dependabotFile  = ".github/dependabot.yml"
	maxExceptionAge = 90 * 24 * time.Hour
)

// Coverage is the machine-readable list of maintained security surfaces.
type Coverage struct {
	Version  int       `yaml:"version"`
	Surfaces []Surface `yaml:"surfaces"`
}

// Surface describes one repository file or root. Path is repository-relative
// and uses slash separators on every platform.
type Surface struct {
	Path        string   `yaml:"path"`
	Kind        string   `yaml:"kind"`
	Updater     Updater  `yaml:"updater"`
	Scanners    []string `yaml:"scanners"`
	Images      []string `yaml:"images,omitempty"`
	UpdaterOnly bool     `yaml:"updater-only,omitempty"`
}

type Updater struct {
	Ecosystem string `yaml:"ecosystem"`
	Directory string `yaml:"directory"`
}

// Exceptions is intentionally empty by default. A non-empty exception must
// identify one narrow scanner finding and have a short, dated owner-approved
// rationale.
type Exceptions struct {
	Version    int         `yaml:"version"`
	Exceptions []Exception `yaml:"exceptions"`
}

type Exception struct {
	ID        string `yaml:"id"`
	Scanner   string `yaml:"scanner"`
	Rule      string `yaml:"rule"`
	Resource  string `yaml:"resource"`
	Owner     string `yaml:"owner"`
	Rationale string `yaml:"rationale"`
	Created   string `yaml:"created"`
	Expires   string `yaml:"expires"`
}

type dependabotConfig struct {
	Version int                `yaml:"version"`
	Updates []dependabotUpdate `yaml:"updates"`
}

type dependabotUpdate struct {
	PackageEcosystem      string                     `yaml:"package-ecosystem"`
	Directory             string                     `yaml:"directory"`
	Schedule              map[string]any             `yaml:"schedule"`
	OpenPullRequestsLimit *int                       `yaml:"open-pull-requests-limit"`
	Groups                map[string]dependabotGroup `yaml:"groups"`
}

type dependabotGroup struct {
	Patterns []string `yaml:"patterns"`
}

var (
	stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	actionSHARef    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	usesLinePattern = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*([^\s#]+)`)
	knownLockNames  = map[string]bool{
		"bun.lock": true, "bun.lockb": true, "package-lock.json": true,
		"pnpm-lock.yaml": true, "yarn.lock": true,
	}
)

// ValidateRepository validates all three repository-owned contracts and
// Dependabot coverage. now is injectable to make expiry checks hermetic.
func ValidateRepository(root string, now time.Time) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("repository root is required")
	}
	coverage, err := readYAML[Coverage](filepath.Join(root, coverageFile))
	if err != nil {
		return fmt.Errorf("read %s: %w", coverageFile, err)
	}
	if err := validateCoverage(root, coverage); err != nil {
		return err
	}
	exceptions, err := readYAML[Exceptions](filepath.Join(root, exceptionsFile))
	if err != nil {
		return fmt.Errorf("read %s: %w", exceptionsFile, err)
	}
	if err := validateExceptions(exceptions, now); err != nil {
		return err
	}
	dependabot, err := readYAML[dependabotConfig](filepath.Join(root, dependabotFile))
	if err != nil {
		return fmt.Errorf("read %s: %w", dependabotFile, err)
	}
	if err := validateUpdaters(coverage, dependabot); err != nil {
		return err
	}
	if err := validatePinnedActions(root, coverage); err != nil {
		return err
	}
	return nil
}

func validatePinnedActions(root string, coverage Coverage) error {
	for _, surface := range coverage.Surfaces {
		if surface.Kind != "github-actions" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(surface.Path)))
		if err != nil {
			return fmt.Errorf("read GitHub Actions surface %q: %w", surface.Path, err)
		}
		for index, line := range strings.Split(string(data), "\n") {
			match := usesLinePattern.FindStringSubmatch(line)
			if len(match) != 2 {
				continue
			}
			uses := strings.Trim(match[1], `"'`)
			if strings.HasPrefix(uses, "./") {
				continue
			}
			if strings.HasPrefix(uses, "docker://") {
				parts := strings.Split(uses, "@sha256:")
				if len(parts) == 2 && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(parts[1]) {
					continue
				}
				return fmt.Errorf("GitHub Actions surface %q line %d uses an unpinned container action %q", surface.Path, index+1, uses)
			}
			at := strings.LastIndexByte(uses, '@')
			if at <= 0 || !actionSHARef.MatchString(uses[at+1:]) {
				return fmt.Errorf("GitHub Actions surface %q line %d must pin %q to a full commit SHA", surface.Path, index+1, uses)
			}
		}
	}
	return nil
}

func readYAML[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return value, errors.New("multiple YAML documents are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return value, err
	}
	return value, nil
}

func validateCoverage(root string, coverage Coverage) error {
	if coverage.Version != 1 {
		return fmt.Errorf("coverage version must be 1, got %d", coverage.Version)
	}
	if len(coverage.Surfaces) == 0 {
		return errors.New("coverage surfaces must not be empty")
	}
	discovered, err := discoverSurfaces(root)
	if err != nil {
		return fmt.Errorf("discover security surfaces: %w", err)
	}
	seen := make(map[string]bool, len(coverage.Surfaces))
	covered := make(map[string]bool, len(coverage.Surfaces))
	for i, surface := range coverage.Surfaces {
		where := fmt.Sprintf("coverage surface %d", i+1)
		path, err := normalizeRepoPath(surface.Path)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if seen[path] {
			return fmt.Errorf("duplicate coverage path %q", path)
		}
		seen[path] = true
		if surface.Path != path {
			return fmt.Errorf("%s path must be normalized as %q", where, path)
		}
		if !isKnownKind(surface.Kind) {
			return fmt.Errorf("%s has unknown kind %q", where, surface.Kind)
		}
		if _, ok := discovered[coverageKey(path, surface.Kind)]; !ok {
			return fmt.Errorf("coverage path %q (%s) is not a maintained security surface", path, surface.Kind)
		}
		covered[coverageKey(path, surface.Kind)] = true
		if err := validateUpdater(surface.Updater, where); err != nil {
			return err
		}
		if err := validateSurfaceUpdater(surface, where); err != nil {
			return err
		}
		if len(surface.Scanners) == 0 && !surface.UpdaterOnly {
			return fmt.Errorf("%s must name at least one applicable scanner", where)
		}
		if surface.UpdaterOnly && len(surface.Scanners) != 0 {
			return fmt.Errorf("%s cannot be updater-only while naming scanners", where)
		}
		if surface.UpdaterOnly && surface.Kind != "js-package" {
			return fmt.Errorf("%s updater-only is only valid for a package without a lockfile", where)
		}
		seenScanners := map[string]bool{}
		for _, scanner := range surface.Scanners {
			scanner = strings.TrimSpace(scanner)
			if scanner == "" {
				return fmt.Errorf("%s has an empty scanner", where)
			}
			if seenScanners[scanner] {
				return fmt.Errorf("%s lists scanner %q more than once", where, scanner)
			}
			seenScanners[scanner] = true
		}
		if surface.Kind == "dockerfile" {
			if len(surface.Images) != len(uniqueSorted(surface.Images)) {
				return fmt.Errorf("%s lists a Docker image more than once", where)
			}
			actual, err := dockerImages(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				return fmt.Errorf("read Dockerfile %q: %w", path, err)
			}
			if err := compareStrings("Dockerfile "+path+" images", actual, surface.Images); err != nil {
				return err
			}
		} else if len(surface.Images) != 0 {
			return fmt.Errorf("coverage surface %q has images but is not a Dockerfile", path)
		}
	}
	for key := range discovered {
		path, kind := splitCoverageKey(key)
		if !covered[key] {
			return fmt.Errorf("maintained %s %q is missing from coverage", kind, path)
		}
	}
	return nil
}

func validateSurfaceUpdater(surface Surface, where string) error {
	wantEcosystem := map[string]string{
		"go-module":      "gomod",
		"js-package":     "npm",
		"js-lock":        "npm",
		"terraform-root": "terraform",
		"dockerfile":     "docker",
		"github-actions": "github-actions",
	}[surface.Kind]
	if surface.Updater.Ecosystem != wantEcosystem {
		return fmt.Errorf("%s updater ecosystem %q does not apply to kind %q", where, surface.Updater.Ecosystem, surface.Kind)
	}
	if surface.Kind == "github-actions" {
		if surface.Updater.Directory != "/" {
			return fmt.Errorf("%s GitHub Actions updater directory must be /", where)
		}
		return nil
	}
	directory := filepath.ToSlash(filepath.Dir(surface.Path))
	if directory == "." {
		directory = ""
	}
	wantDirectory := "/" + directory
	if surface.Kind == "terraform-root" {
		wantDirectory = "/" + surface.Path
	}
	if surface.Updater.Directory != wantDirectory {
		return fmt.Errorf("%s updater directory %q does not cover path (want %q)", where, surface.Updater.Directory, wantDirectory)
	}
	return nil
}

func validateUpdater(updater Updater, where string) error {
	if strings.TrimSpace(updater.Ecosystem) == "" || strings.TrimSpace(updater.Directory) == "" {
		return fmt.Errorf("%s must specify updater ecosystem and directory", where)
	}
	if updater.Directory[0] != '/' || strings.Contains(updater.Directory, "..") || filepath.Clean(updater.Directory) != updater.Directory {
		return fmt.Errorf("%s updater directory must be a normalized absolute repository path", where)
	}
	switch updater.Ecosystem {
	case "gomod", "npm", "docker", "terraform", "github-actions":
	default:
		return fmt.Errorf("%s has unsupported updater ecosystem %q", where, updater.Ecosystem)
	}
	return nil
}

func validateExceptions(contract Exceptions, now time.Time) error {
	if contract.Version != 1 {
		return fmt.Errorf("exceptions version must be 1, got %d", contract.Version)
	}
	if contract.Exceptions == nil {
		return errors.New("exceptions must be an explicit list (use [] when empty)")
	}
	seen := make(map[string]bool, len(contract.Exceptions))
	now = now.UTC()
	for i, exception := range contract.Exceptions {
		where := fmt.Sprintf("exception %d", i+1)
		if !stableIDPattern.MatchString(exception.ID) {
			return fmt.Errorf("%s id must be a stable non-empty identifier", where)
		}
		if seen[exception.ID] {
			return fmt.Errorf("duplicate exception id %q", exception.ID)
		}
		seen[exception.ID] = true
		for field, value := range map[string]string{
			"scanner": exception.Scanner, "rule": exception.Rule, "resource": exception.Resource,
			"owner": exception.Owner, "rationale": exception.Rationale,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s %s is required", where, field)
			}
			if isOverbroad(value) {
				return fmt.Errorf("%s %s is overbroad", where, field)
			}
		}
		created, err := parseExceptionDate(exception.Created)
		if err != nil {
			return fmt.Errorf("%s created: %w", where, err)
		}
		expires, err := parseExceptionDate(exception.Expires)
		if err != nil {
			return fmt.Errorf("%s expires: %w", where, err)
		}
		if created.After(now) {
			return fmt.Errorf("%s created date is in the future", where)
		}
		if !expires.After(created) {
			return fmt.Errorf("%s expires must be after created", where)
		}
		if expires.After(created.Add(maxExceptionAge)) {
			return fmt.Errorf("%s exceeds the 90-day maximum", where)
		}
		if !expires.After(now) {
			return fmt.Errorf("%s is expired", where)
		}
	}
	return nil
}

func parseExceptionDate(value string) (time.Time, error) {
	if strings.TrimSpace(value) != value || value == "" {
		return time.Time{}, errors.New("must be an ISO date (YYYY-MM-DD)")
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, errors.New("must be an ISO date (YYYY-MM-DD)")
	}
	return parsed.UTC(), nil
}

func isOverbroad(value string) bool {
	if strings.ContainsAny(value, "*?[]%") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all", "any", "everything", "global":
		return true
	default:
		return false
	}
}

func validateUpdaters(coverage Coverage, config dependabotConfig) error {
	if config.Version != 2 {
		return fmt.Errorf("Dependabot version must be 2, got %d", config.Version)
	}
	wanted := map[string]bool{}
	for _, surface := range coverage.Surfaces {
		wanted[updaterKey(surface.Updater)] = true
	}
	got := map[string]bool{}
	for i, update := range config.Updates {
		where := fmt.Sprintf("Dependabot update %d", i+1)
		if err := validateUpdater(Updater{Ecosystem: update.PackageEcosystem, Directory: update.Directory}, where); err != nil {
			return err
		}
		key := updaterKey(Updater{Ecosystem: update.PackageEcosystem, Directory: update.Directory})
		if got[key] {
			return fmt.Errorf("duplicate Dependabot updater %q", key)
		}
		got[key] = true
		if update.OpenPullRequestsLimit == nil || *update.OpenPullRequestsLimit < 1 || *update.OpenPullRequestsLimit > 10 {
			return fmt.Errorf("%s must set open-pull-requests-limit between 1 and 10", where)
		}
		if len(update.Schedule) == 0 {
			return fmt.Errorf("%s must set a schedule", where)
		}
		if len(update.Groups) == 0 {
			return fmt.Errorf("%s must define a bounded dependency group", where)
		}
		for name, group := range update.Groups {
			if strings.TrimSpace(name) == "" || len(group.Patterns) == 0 {
				return fmt.Errorf("%s has an empty dependency group", where)
			}
		}
	}
	for key := range wanted {
		if !got[key] {
			return fmt.Errorf("missing Dependabot updater for %q", key)
		}
	}
	for key := range got {
		if !wanted[key] {
			return fmt.Errorf("Dependabot updater %q has no covered security surface", key)
		}
	}
	return nil
}

func discoverSurfaces(root string) (map[string]bool, error) {
	discovered := map[string]bool{}
	terraformDirs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			base := filepath.Base(rel)
			if base == ".git" || base == "node_modules" || base == "vendor" || base == ".terraform" {
				return fs.SkipDir
			}
			return nil
		}
		if rel == "." || strings.HasPrefix(rel, ".git/") {
			return nil
		}
		base := filepath.Base(rel)
		switch {
		case base == "go.mod":
			discovered[coverageKey(rel, "go-module")] = true
		case base == "package.json":
			discovered[coverageKey(rel, "js-package")] = true
		case knownLockNames[base]:
			discovered[coverageKey(rel, "js-lock")] = true
		case strings.HasSuffix(base, ".tf"):
			terraformDirs[filepath.ToSlash(filepath.Dir(rel))] = true
		case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile."):
			discovered[coverageKey(rel, "dockerfile")] = true
		case (strings.HasPrefix(rel, ".github/workflows/") || strings.HasPrefix(rel, ".github/actions/") || strings.HasPrefix(rel, ".github/examples/")) && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
			discovered[coverageKey(rel, "github-actions")] = true
		}
		return nil
	})
	for dir := range terraformDirs {
		discovered[coverageKey(dir, "terraform-root")] = true
	}
	if err != nil {
		return nil, err
	}
	return discovered, nil
}

func dockerImages(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var stages = map[string]bool{}
	var images []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "FROM ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "FROM "))
		if len(fields) == 0 {
			return nil, errors.New("FROM has no image")
		}
		if fields[0] == "--platform=$BUILDPLATFORM" || strings.HasPrefix(fields[0], "--platform=") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			return nil, errors.New("FROM has no image")
		}
		image := fields[0]
		if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") {
			stages[strings.ToLower(fields[2])] = true
		}
		if image == "scratch" || stages[strings.ToLower(image)] {
			continue
		}
		images = append(images, image)
	}
	return uniqueSorted(images), nil
}

func compareStrings(label string, actual, declared []string) error {
	actual = uniqueSorted(actual)
	declared = uniqueSorted(declared)
	if len(actual) != len(declared) {
		return fmt.Errorf("%s inventory mismatch: declared %v, found %v", label, declared, actual)
	}
	for i := range actual {
		if actual[i] != declared[i] {
			return fmt.Errorf("%s inventory mismatch: declared %v, found %v", label, declared, actual)
		}
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeRepoPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return "", errors.New("path must be a non-empty relative slash path")
	}
	clean := path
	for strings.Contains(clean, "//") {
		clean = strings.ReplaceAll(clean, "//", "/")
	}
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") || strings.HasPrefix(clean, "./") {
		return "", errors.New("path must not escape repository root")
	}
	return clean, nil
}

func isKnownKind(kind string) bool {
	switch kind {
	case "go-module", "js-package", "js-lock", "terraform-root", "dockerfile", "github-actions":
		return true
	default:
		return false
	}
}

func coverageKey(path, kind string) string { return kind + "\x00" + path }

func splitCoverageKey(key string) (string, string) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return key, "surface"
	}
	return parts[1], parts[0]
}

func updaterKey(updater Updater) string { return updater.Ecosystem + "\x00" + updater.Directory }
