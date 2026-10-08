// contracttrials grades the preregistered local authoring pilot through the real
// resource decoder and project compiler. It performs no query execution.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	compiler "github.com/flidai/leapview/internal/project/compiler"
)

type result struct {
	ID             string   `json:"id"`
	ParseAndSchema bool     `json:"parseAndSchema"`
	Compiler       bool     `json:"compiler"`
	Intent         bool     `json:"intent"`
	Representation bool     `json:"representation"`
	Pass           bool     `json:"pass"`
	SourceBytes    int      `json:"sourceBytes"`
	Files          []string `json:"files"`
	ChangedFiles   []string `json:"changedFiles"`
	SourceHash     string   `json:"sourceHash"`
	Diagnostic     string   `json:"diagnostic,omitempty"`
}

func canonical(value document.DashboardDocument) ([]byte, error) {
	if value.Spec.Layout == nil {
		value.Spec.Layout = &document.DashboardLayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}
	}
	value.Spec.Includes = nil
	// Generated runtime DTO indexes visual definitions by identity; JSON map
	// ordering is deterministic. Ordered query/page/component lists stay intact.
	return json.Marshal(value)
}

func sourceFiles(root string) ([]string, string, int, error) {
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".json")) {
			rel, _ := filepath.Rel(root, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		return nil, "", 0, err
	}
	sort.Strings(files)
	h := sha256.New()
	size := 0
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, "", 0, err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(data))
		h.Write(data)
		size += len(data)
	}
	return files, hex.EncodeToString(h.Sum(nil)), size, nil
}

// verifyRecordedInputs prevents regrading edited submissions or changed oracles
// as the preserved first attempts. Hashing alone does not create fresh trials.
func verifyRecordedInputs(base string) error {
	for _, spec := range []struct{ name, key string }{{"freeze.json", "pretrial"}, {"submissions.json", "firstAttemptSourceHashes"}} {
		data, err := os.ReadFile(filepath.Join(base, spec.name))
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		var hashes map[string]string
		if err := json.Unmarshal(fields[spec.key], &hashes); err != nil {
			return err
		}
		if len(hashes) == 0 {
			return fmt.Errorf("%s has no recorded hashes", spec.name)
		}
		for name, want := range hashes {
			if !filepath.IsLocal(name) {
				return fmt.Errorf("nonlocal manifest path %s", name)
			}
			content, err := os.ReadFile(filepath.Join(base, name))
			if err != nil {
				return err
			}
			hash := sha256.Sum256(content)
			if hex.EncodeToString(hash[:]) != want {
				return fmt.Errorf("frozen input changed: %s", name)
			}
		}
		if spec.key == "firstAttemptSourceHashes" {
			files, _, _, err := sourceFiles(filepath.Join(base, "runs"))
			if err != nil {
				return err
			}
			if len(files) != len(hashes) {
				return fmt.Errorf("submission source file set changed")
			}
			for _, name := range files {
				if _, ok := hashes["runs/"+name]; !ok {
					return fmt.Errorf("unrecorded submission file: %s", name)
				}
			}
		}
	}
	return nil
}

func compileRoot(repo, source string) error {
	root, err := os.MkdirTemp("", "dashboard-trial-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	for _, name := range []string{"connections", "sources", "models", "semantic-models"} {
		if err := os.CopyFS(filepath.Join(root, name), os.DirFS(filepath.Join(repo, "dashboards", name))); err != nil {
			return err
		}
	}
	if err := os.CopyFS(filepath.Join(root, "dashboards"), os.DirFS(filepath.Join(source, "dashboards"))); err != nil {
		return err
	}
	_, err = compiler.Compile(root)
	if err != nil {
		return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), root, "<fixture>"))
	}
	return nil
}

func grade(repo, base, id string) (result, error) {
	r := result{ID: id}
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 {
		return r, fmt.Errorf("invalid trial ID %s", id)
	}
	source := filepath.Join(base, "runs", id)
	files, hash, size, err := sourceFiles(filepath.Join(source, "dashboards"))
	if err != nil {
		return r, err
	}
	r.Files = files
	r.SourceHash = hash
	r.SourceBytes = size
	frozenBytes, err := os.ReadFile(filepath.Join(base, "freeze.json"))
	if err != nil {
		return r, err
	}
	var frozen struct {
		SeedSources map[string]string `json:"seedSources"`
	}
	if err := json.Unmarshal(frozenBytes, &frozen); err != nil {
		return r, err
	}
	prefix := "runs/" + id + "/dashboards/"
	observed := map[string]bool{}
	for _, file := range files {
		observed[file] = true
		data, err := os.ReadFile(filepath.Join(source, "dashboards", file))
		if err != nil {
			return r, err
		}
		hash := sha256.Sum256(data)
		if frozen.SeedSources[prefix+file] != hex.EncodeToString(hash[:]) {
			r.ChangedFiles = append(r.ChangedFiles, file)
		}
	}
	for key := range frozen.SeedSources {
		if strings.HasPrefix(key, prefix) {
			file := strings.TrimPrefix(key, prefix)
			if !observed[file] {
				r.ChangedFiles = append(r.ChangedFiles, file)
			}
		}
	}
	sort.Strings(r.ChangedFiles)
	original, err := compiler.LoadDashboardDocument(filepath.Join(source, "dashboards", "evaluation.yaml"))
	if err != nil {
		r.Diagnostic = err.Error()
		return r, nil
	}
	switch parts[0] {
	case "explicit":
		r.Representation = original.Spec.Layout != nil && original.Spec.Includes == nil
	case "omitted":
		r.Representation = original.Spec.Layout == nil && original.Spec.Includes == nil
	case "fragments":
		r.Representation = original.Spec.Includes != nil && len(original.Spec.Visuals) == 0 && len(original.Spec.Pages) == 0
	}
	// The task permits changes within the seeded source arrangement, not extra
	// unused dashboards or renamed equivalent fragment paths.
	expectedFiles := 0
	for key := range frozen.SeedSources {
		if strings.HasPrefix(key, prefix) {
			expectedFiles++
		}
	}
	if expectedFiles != len(files) {
		r.Representation = false
	}
	for _, name := range files {
		if _, ok := frozen.SeedSources[prefix+name]; !ok {
			r.Representation = false
		}
	}
	if parts[0] == "fragments" {
		content, err := os.ReadFile(filepath.Join(source, "dashboards", "evaluation.yaml"))
		if err != nil {
			return r, err
		}
		hash := sha256.Sum256(content)
		if hex.EncodeToString(hash[:]) != frozen.SeedSources[prefix+"evaluation.yaml"] {
			r.Representation = false
		}
	}
	actual, err := compiler.LoadDashboardDocumentForSourceRoot(filepath.Join(source, "dashboards", "evaluation.yaml"), source)
	if err != nil {
		r.Diagnostic = err.Error()
		return r, nil
	}
	r.ParseAndSchema = true
	if err := compileRoot(repo, source); err != nil {
		r.Diagnostic = err.Error()
		return r, nil
	}
	r.Compiler = true
	expected, err := compiler.LoadDashboardDocument(filepath.Join(base, "oracle", parts[1]+".yaml"))
	if err != nil {
		return r, err
	}
	got, err := canonical(actual)
	if err != nil {
		return r, err
	}
	want, err := canonical(expected)
	if err != nil {
		return r, err
	}
	r.Intent = bytes.Equal(got, want)
	r.Pass = r.Intent && r.Representation
	if !r.Intent {
		r.Diagnostic = "Expanded document differs from the frozen task oracle; compilation alone does not establish requested intent."
	} else if !r.Representation {
		r.Diagnostic = "Submission did not preserve its assigned representation."
	}
	return r, nil
}

func qualify(repo, base string) error {
	for _, task := range []string{"create", "grain", "reuse"} {
		path := filepath.Join(base, "oracle", task+".yaml")
		if _, err := compiler.LoadDashboardDocument(path); err != nil {
			return fmt.Errorf("%s schema: %w", task, err)
		}
		temp, err := os.MkdirTemp("", "dashboard-oracle-")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(temp, "dashboards"), 0755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(temp, "dashboards", "evaluation.yaml"), data, 0644); err != nil {
			return err
		}
		err = compileRoot(repo, temp)
		os.RemoveAll(temp)
		if err != nil {
			return fmt.Errorf("%s compiler: %w", task, err)
		}
		fmt.Printf("Qualified %s oracle through generated schema and real project compiler\n", task)
	}
	return nil
}

func main() {
	repo := flag.String("repo", ".", "repository root")
	qualification := flag.Bool("qualify", false, "qualify frozen oracles before trials")
	corpus := flag.String("qualify-corpus", "", "qualify a separate ten-task canonical corpus manifest; zero agent trials")
	output := flag.String("output", "", "write scored results JSON")
	flag.Parse()
	if *corpus != "" {
		if *qualification {
			fmt.Fprintln(os.Stderr, "-qualify and -qualify-corpus are separate modes")
			os.Exit(1)
		}
		path := *corpus
		if !filepath.IsAbs(path) {
			path = filepath.Join(*repo, path)
		}
		report, qualificationError := qualifyCorpus(*repo, path)
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		data = append(data, '\n')
		if *output != "" {
			err = os.WriteFile(*output, data, 0644)
		} else {
			_, err = os.Stdout.Write(data)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if qualificationError != nil {
			fmt.Fprintln(os.Stderr, qualificationError)
			os.Exit(1)
		}
		return
	}
	base := filepath.Join(*repo, "playground", "dashboard-evaluation")
	if *qualification {
		if err := qualify(*repo, base); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := verifyRecordedInputs(base); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	entries, err := os.ReadDir(filepath.Join(base, "runs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var results []result
	for _, entry := range entries {
		if entry.IsDir() {
			r, err := grade(*repo, base, entry.Name())
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			results = append(results, r)
		}
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if *output != "" {
		if err := os.WriteFile(*output, data, 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	} else {
		fmt.Print(string(data))
	}
}
