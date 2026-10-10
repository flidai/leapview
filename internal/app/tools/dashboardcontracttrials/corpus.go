package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	compiler "github.com/flidai/leapview/internal/project/compiler"
)

// This corpus qualifies benchmark inputs. It never creates or scores agent runs.
var corpusTaskIDs = []string{
	"monthly-create", "ranked-chart", "grain-alias-sort", "scoped-dependent-filter",
	"second-page-reuse", "move-resize", "reordered-definition-edit", "combo-bindings",
	"records-query", "repair-preserving-neighbors",
}

type corpusManifest struct {
	Version     int               `json:"version"`
	Purpose     string            `json:"purpose"`
	SupportRoot string            `json:"supportRoot"`
	Tasks       []corpusTask      `json:"tasks"`
	FrozenFiles map[string]string `json:"frozenFiles"`
}

type corpusTask struct {
	ID                     string           `json:"id"`
	Prompt                 string           `json:"prompt"`
	SeedRoot               string           `json:"seedRoot"`
	OracleRoot             string           `json:"oracleRoot"`
	Coverage               []string         `json:"coverage"`
	SeedCompilerValid      bool             `json:"seedCompilerValid"`
	SeedDiagnosticContains string           `json:"seedDiagnosticContains,omitempty"`
	Negatives              []corpusNegative `json:"negatives"`
}

type corpusNegative struct {
	ID         string `json:"id"`
	SourceRoot string `json:"sourceRoot"`
	Rejects    string `json:"rejects"`
}

type corpusTaskResult struct {
	ID                string   `json:"id"`
	SeedSchema        bool     `json:"seedSchema"`
	SeedCompiler      bool     `json:"seedCompiler"`
	ExpectedSeedValid bool     `json:"expectedSeedValid"`
	SeedDiagnostic    string   `json:"seedDiagnostic,omitempty"`
	OracleSchema      bool     `json:"oracleSchema"`
	OracleCompiler    bool     `json:"oracleCompiler"`
	SeedDiffers       bool     `json:"seedDiffersFromOracle"`
	RejectedNegatives []string `json:"rejectedValidButWrongDocuments"`
	Qualified         bool     `json:"qualified"`
}

type corpusCompilerIdentity struct {
	Commit            string `json:"commit"`
	TrackedDiffSHA256 string `json:"trackedDiffSHA256"`
	ExecutableSHA256  string `json:"executableSHA256"`
	GoVersion         string `json:"goVersion"`
	BuildInfo         string `json:"buildInfo"`
}

type corpusReport struct {
	Kind           string                 `json:"kind"`
	AgentTrials    int                    `json:"agentTrials"`
	ManifestSHA256 string                 `json:"manifestSHA256"`
	SupportSHA256  string                 `json:"supportingResourcesSHA256"`
	InputSHA256    string                 `json:"frozenInputsSHA256"`
	Compiler       corpusCompilerIdentity `json:"compiler"`
	Tasks          []corpusTaskResult     `json:"tasks"`
	Qualified      bool                   `json:"qualified"`
}

func sha256Text(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func corpusPath(base, name string) (string, error) {
	if !filepath.IsLocal(name) || name == "." {
		return "", fmt.Errorf("corpus path must be local: %q", name)
	}
	return filepath.Join(base, filepath.FromSlash(name)), nil
}

func loadCorpusManifest(path string) (corpusManifest, []byte, error) {
	var manifest corpusManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest, nil, fmt.Errorf("manifest must contain one JSON document")
	}
	if manifest.Version != 1 || manifest.Purpose != "deterministic-canonical-v1-qualification" {
		return manifest, nil, fmt.Errorf("unsupported corpus version or purpose")
	}
	expected := make(map[string]bool, len(corpusTaskIDs))
	for _, id := range corpusTaskIDs {
		expected[id] = true
	}
	seen := map[string]bool{}
	for _, task := range manifest.Tasks {
		if !expected[task.ID] || seen[task.ID] {
			return manifest, nil, fmt.Errorf("unexpected or duplicate task %q", task.ID)
		}
		seen[task.ID] = true
		if len(task.Coverage) == 0 || len(task.Negatives) == 0 {
			return manifest, nil, fmt.Errorf("task %s requires coverage and a valid-but-wrong negative", task.ID)
		}
		if task.ID == "repair-preserving-neighbors" {
			if task.SeedCompilerValid || task.SeedDiagnosticContains == "" {
				return manifest, nil, fmt.Errorf("repair must declare the expected invalid seed diagnostic")
			}
		} else if !task.SeedCompilerValid || task.SeedDiagnosticContains != "" {
			return manifest, nil, fmt.Errorf("task %s must start from a compiler-valid seed", task.ID)
		}
	}
	for _, id := range corpusTaskIDs {
		if !seen[id] {
			return manifest, nil, fmt.Errorf("missing required task %s", id)
		}
	}
	return manifest, data, nil
}

// Validate exact recoverable input bytes, including prompts and supporting resources.
// The old discovery pilot's frozen files are intentionally outside this manifest.
func verifyCorpusInputs(base string, manifest corpusManifest) (string, error) {
	if len(manifest.FrozenFiles) == 0 {
		return "", fmt.Errorf("corpus has no frozen inputs")
	}
	var names []string
	for name := range manifest.FrozenFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		path, err := corpusPath(base, name)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("frozen input %s: %w", name, err)
		}
		if sha256Text(data) != manifest.FrozenFiles[name] {
			return "", fmt.Errorf("frozen corpus input changed: %s", name)
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", name, len(data))
		hash.Write(data)
	}
	roots := []string{manifest.SupportRoot}
	for _, task := range manifest.Tasks {
		path, err := corpusPath(base, task.Prompt)
		if err != nil {
			return "", err
		}
		if _, exists := manifest.FrozenFiles[task.Prompt]; !exists {
			return "", fmt.Errorf("unfrozen prompt %s", task.Prompt)
		}
		prompt, err := os.ReadFile(path)
		if err != nil || len(bytes.TrimSpace(prompt)) == 0 {
			return "", fmt.Errorf("missing or empty prompt for %s", task.ID)
		}
		roots = append(roots, task.SeedRoot, task.OracleRoot)
		for _, negative := range task.Negatives {
			if negative.ID == "" || negative.Rejects == "" {
				return "", fmt.Errorf("task %s has an undescribed negative", task.ID)
			}
			roots = append(roots, negative.SourceRoot)
		}
	}
	for _, name := range roots {
		root, err := corpusPath(base, name)
		if err != nil {
			return "", err
		}
		files, _, _, err := sourceFiles(root)
		if err != nil {
			return "", fmt.Errorf("source root %s: %w", name, err)
		}
		if len(files) == 0 {
			return "", fmt.Errorf("empty source root %s", name)
		}
		for _, file := range files {
			key := name + "/" + file
			if _, exists := manifest.FrozenFiles[key]; !exists {
				return "", fmt.Errorf("unfrozen source file %s", key)
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func corpusBuildIdentity(repo string) (corpusCompilerIdentity, error) {
	identity := corpusCompilerIdentity{GoVersion: runtime.Version()}
	for _, entry := range []struct {
		args []string
		set  func([]byte)
	}{
		{[]string{"rev-parse", "HEAD"}, func(data []byte) { identity.Commit = strings.TrimSpace(string(data)) }},
		{[]string{"diff", "HEAD", "--binary"}, func(data []byte) { identity.TrackedDiffSHA256 = sha256Text(data) }},
	} {
		command := exec.Command("git", entry.args...)
		command.Dir = repo
		data, err := command.Output()
		if err != nil {
			return identity, fmt.Errorf("compiler Git identity: %w", err)
		}
		entry.set(data)
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		identity.BuildInfo = info.String()
	}
	path, err := os.Executable()
	if err != nil {
		return identity, err
	}
	file, err := os.Open(path)
	if err != nil {
		return identity, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return identity, err
	}
	identity.ExecutableSHA256 = hex.EncodeToString(hash.Sum(nil))
	return identity, nil
}

func compileCorpusRoot(support, source string) error {
	root, err := os.MkdirTemp("", "dashboard-corpus-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := os.CopyFS(root, os.DirFS(support)); err != nil {
		return err
	}
	// Only dashboard resources vary by task; governed sales resources stay frozen.
	if err := os.CopyFS(filepath.Join(root, "dashboards"), os.DirFS(filepath.Join(source, "dashboards"))); err != nil {
		return err
	}
	_, err = compiler.Compile(root)
	if err != nil {
		return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), root, "<corpus>"))
	}
	return nil
}

func loadCorpusDocument(root string) (document.DashboardDocument, error) {
	return compiler.LoadDashboardDocumentForSourceRoot(filepath.Join(root, "dashboards", "evaluation.yaml"), root)
}

func corpusIntentEqual(actual, expected document.DashboardDocument) (bool, error) {
	got, err := canonical(actual)
	if err != nil {
		return false, err
	}
	want, err := canonical(expected)
	return bytes.Equal(got, want), err
}

func qualifyCorpusTask(base, support string, task corpusTask) (corpusTaskResult, error) {
	result := corpusTaskResult{ID: task.ID, ExpectedSeedValid: task.SeedCompilerValid}
	seedRoot, err := corpusPath(base, task.SeedRoot)
	if err != nil {
		return result, err
	}
	oracleRoot, err := corpusPath(base, task.OracleRoot)
	if err != nil {
		return result, err
	}
	seed, err := loadCorpusDocument(seedRoot)
	if err != nil {
		return result, fmt.Errorf("%s seed schema: %w", task.ID, err)
	}
	result.SeedSchema = true
	err = compileCorpusRoot(support, seedRoot)
	result.SeedCompiler = err == nil
	if err != nil {
		result.SeedDiagnostic = err.Error()
	}
	if result.SeedCompiler != task.SeedCompilerValid {
		return result, fmt.Errorf("%s seed compiler validity differs from expectation: %v", task.ID, err)
	}
	if task.SeedDiagnosticContains != "" && !strings.Contains(result.SeedDiagnostic, task.SeedDiagnosticContains) {
		return result, fmt.Errorf("%s seed lacks expected repair diagnostic %q", task.ID, task.SeedDiagnosticContains)
	}
	oracle, err := loadCorpusDocument(oracleRoot)
	if err != nil {
		return result, fmt.Errorf("%s oracle schema: %w", task.ID, err)
	}
	result.OracleSchema = true
	if err := compileCorpusRoot(support, oracleRoot); err != nil {
		return result, fmt.Errorf("%s oracle compiler: %w", task.ID, err)
	}
	result.OracleCompiler = true
	equal, err := corpusIntentEqual(seed, oracle)
	if err != nil {
		return result, err
	}
	result.SeedDiffers = !equal
	if equal {
		return result, fmt.Errorf("%s seed already equals its oracle", task.ID)
	}
	for _, negative := range task.Negatives {
		root, err := corpusPath(base, negative.SourceRoot)
		if err != nil {
			return result, err
		}
		actual, err := loadCorpusDocument(root)
		if err != nil {
			return result, fmt.Errorf("%s negative %s must be schema-valid: %w", task.ID, negative.ID, err)
		}
		if err := compileCorpusRoot(support, root); err != nil {
			return result, fmt.Errorf("%s negative %s must compile: %w", task.ID, negative.ID, err)
		}
		equal, err := corpusIntentEqual(actual, oracle)
		if err != nil {
			return result, err
		}
		if equal {
			return result, fmt.Errorf("%s oracle accepted valid-but-wrong negative %s", task.ID, negative.ID)
		}
		result.RejectedNegatives = append(result.RejectedNegatives, negative.ID)
	}
	result.Qualified = true
	return result, nil
}

func qualifyCorpus(repo, manifestPath string) (corpusReport, error) {
	report := corpusReport{Kind: "deterministic-canonical-v1-qualification", AgentTrials: 0}
	manifest, data, err := loadCorpusManifest(manifestPath)
	if err != nil {
		return report, err
	}
	report.ManifestSHA256 = sha256Text(data)
	base := filepath.Dir(manifestPath)
	report.InputSHA256, err = verifyCorpusInputs(base, manifest)
	if err != nil {
		return report, err
	}
	support, err := corpusPath(base, manifest.SupportRoot)
	if err != nil {
		return report, err
	}
	_, report.SupportSHA256, _, err = sourceFiles(support)
	if err != nil {
		return report, err
	}
	report.Compiler, err = corpusBuildIdentity(repo)
	if err != nil {
		return report, err
	}
	for _, task := range manifest.Tasks {
		result, err := qualifyCorpusTask(base, support, task)
		report.Tasks = append(report.Tasks, result)
		if err != nil {
			return report, err
		}
	}
	report.Qualified = true
	return report, nil
}
