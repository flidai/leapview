package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type goCacheAction struct {
	Runs struct {
		Steps []goCacheActionStep `yaml:"steps"`
	} `yaml:"runs"`
}

type goCacheActionStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	If   string            `yaml:"if"`
	With map[string]string `yaml:"with"`
}

func TestSetupCIOwnsGoValidationCache(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "actions", "setup-ci", "action.yml"))
	if err != nil {
		t.Fatalf("read setup-ci action: %v", err)
	}

	var action goCacheAction
	if err := yaml.Unmarshal(body, &action); err != nil {
		t.Fatalf("parse setup-ci action: %v", err)
	}

	var setupGo []goCacheActionStep
	var goCaches []goCacheActionStep
	for _, step := range action.Runs.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-go@") {
			setupGo = append(setupGo, step)
		}
		if strings.HasPrefix(step.Uses, "actions/cache@") && strings.Contains(step.With["path"], "steps.go-cache-paths.outputs.") {
			goCaches = append(goCaches, step)
		}
	}
	if len(setupGo) != 1 {
		t.Fatalf("setup-ci must have one setup-go step, found %d", len(setupGo))
	}
	if setupGo[0].ID != "go" {
		t.Fatalf("setup-go step must be id go, got %q", setupGo[0].ID)
	}
	if setupGo[0].With["go-version-file"] != "go.mod" {
		t.Fatalf("setup-go must read the root go.mod, got %q", setupGo[0].With["go-version-file"])
	}
	if setupGo[0].With["cache"] != "false" {
		t.Fatalf("setup-go built-in cache must be disabled, got %q", setupGo[0].With["cache"])
	}

	var resolver []goCacheActionStep
	for _, step := range action.Runs.Steps {
		if step.ID == "go-cache-paths" {
			resolver = append(resolver, step)
		}
	}
	if len(resolver) != 1 {
		t.Fatalf("setup-ci must have one Go cache path resolver, found %d", len(resolver))
	}
	for _, want := range []string{
		"go env GOMODCACHE",
		"go env GOCACHE",
		"ImageOS",
		"/etc/os-release",
		"${ID:-}",
		"${VERSION_ID:-}",
		"GITHUB_OUTPUT",
		"unable to determine the runner image identity",
	} {
		if !strings.Contains(resolver[0].Run, want) {
			t.Errorf("Go cache path resolver missing %q", want)
		}
	}

	if len(goCaches) != 1 {
		t.Fatalf("setup-ci must have one Go cache writer, found %d", len(goCaches))
	}
	goCache := goCaches[0]
	if goCache.If != "" || goCache.Run != "" {
		t.Fatalf("Go cache must not conditionally bypass the cache action")
	}
	if _, ok := goCache.With["restore-keys"]; ok {
		t.Fatal("Go validation cache must not use a fallback restore key")
	}
	path := strings.Split(strings.TrimSpace(goCache.With["path"]), "\n")
	if len(path) != 2 || path[0] != "${{ steps.go-cache-paths.outputs.gomodcache }}" || path[1] != "${{ steps.go-cache-paths.outputs.gocache }}" {
		t.Fatalf("Go cache must contain only GOMODCACHE and GOCACHE, got %q", goCache.With["path"])
	}
	wantKey := "go-validation-v1-${{ github.job }}-${{ runner.os }}-${{ runner.arch }}-${{ steps.go-cache-paths.outputs.image }}-${{ steps.go.outputs.go-version }}-${{ hashFiles('**/go.mod', '**/go.sum', 'Taskfile.yml', '.github/actions/setup-ci/action.yml') }}"
	if got := goCache.With["key"]; got != wantKey {
		t.Fatalf("Go validation cache key = %q, want %q", got, wantKey)
	}
	if !regexp.MustCompile(`^actions/cache@[0-9a-f]{40}$`).MatchString(goCache.Uses) {
		t.Fatalf("Go cache action must be pinned to a commit, got %q", goCache.Uses)
	}
	if strings.Contains(string(body), "cache-hit") {
		t.Fatal("setup-ci must not skip work based on a cache-hit output")
	}
	setupIndex := -1
	resolverIndex := -1
	cacheIndex := -1
	toolIndex := -1
	for index, step := range action.Runs.Steps {
		switch {
		case step.ID == "go":
			setupIndex = index
		case step.ID == "go-cache-paths":
			resolverIndex = index
		case strings.HasPrefix(step.Uses, "actions/cache@") && strings.Contains(step.With["path"], "steps.go-cache-paths.outputs."):
			cacheIndex = index
		case step.Name == "Install pinned CI tools":
			toolIndex = index
		}
	}
	if !(setupIndex >= 0 && setupIndex < resolverIndex && resolverIndex < cacheIndex && cacheIndex < toolIndex) {
		t.Fatalf("setup-ci must set up Go, resolve paths, restore Go cache, then install tools; indexes are setup=%d resolver=%d cache=%d tools=%d", setupIndex, resolverIndex, cacheIndex, toolIndex)
	}
}

func TestValidationWorkflowsKeepStableGoCacheWorkloadIDs(t *testing.T) {
	root := repoRoot(t)
	wantedJobs := map[string][]string{
		"ci.yml":               {"apigen-validation", "go-packages-validation", "go-application-validation", "frontend-validation"},
		"merge-validation.yml": {"apigen-validation", "go-packages-validation", "go-application-validation", "frontend-validation", "full-validation"},
		"nightly.yml":          {"apigen-validation", "go-packages-validation", "go-application-validation", "frontend-validation", "full-validation"},
	}
	for workflowName, jobs := range wantedJobs {
		body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", workflowName))
		if err != nil {
			t.Fatalf("read %s: %v", workflowName, err)
		}
		var workflow struct {
			Jobs map[string]yaml.Node `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(body, &workflow); err != nil {
			t.Fatalf("parse %s: %v", workflowName, err)
		}
		for _, job := range jobs {
			jobNode, ok := workflow.Jobs[job]
			if !ok {
				t.Errorf("%s must keep stable setup-ci workload ID %q", workflowName, job)
				continue
			}
			if !workflowJobUsesSetupCI(&jobNode) {
				t.Errorf("%s job %q must use the shared setup-ci action", workflowName, job)
			}
		}
	}
}

func TestNativeGoCachesRemainSeparateFromValidationCache(t *testing.T) {
	root := repoRoot(t)
	files := []string{
		filepath.Join(root, ".github", "actions", "desktop-preview-candidate", "action.yml"),
		filepath.Join(root, ".github", "workflows", "electron-security-proof.yml"),
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		modes := setupGoCacheModes(t, body)
		if len(modes) == 0 {
			t.Fatalf("%s must retain its native setup-go cache configuration", file)
		}
		for _, mode := range modes {
			if mode != "true" {
				t.Errorf("%s native setup-go cache mode = %q, want true", file, mode)
			}
		}
		if strings.Contains(string(body), "go-validation-v1-") {
			t.Errorf("%s must not populate the validation cache key family", file)
		}
	}
}

func workflowJobUsesSetupCI(node *yaml.Node) bool {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "uses" && node.Content[i+1].Value == "./.github/actions/setup-ci" {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if workflowJobUsesSetupCI(child) {
			return true
		}
	}
	return false
}

func setupGoCacheModes(t *testing.T, body []byte) []string {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatalf("parse setup-go configuration: %v", err)
	}

	var modes []string
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			uses := ""
			cache := ""
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				switch key.Value {
				case "uses":
					uses = value.Value
				case "with":
					if value.Kind == yaml.MappingNode {
						for j := 0; j+1 < len(value.Content); j += 2 {
							if value.Content[j].Value == "cache" {
								cache = value.Content[j+1].Value
							}
						}
					}
				}
			}
			if strings.HasPrefix(uses, "actions/setup-go@") {
				modes = append(modes, cache)
			}
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&document)
	return modes
}
