package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

const dashboardPath = "dashboards/executive-sales.yaml"

type SourceFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type FixtureEvidence struct {
	ID                   string                    `json:"id"`
	Label                string                    `json:"label"`
	Source               string                    `json:"source"`
	SourceDigest         string                    `json:"sourceDigest"`
	SourceRootDigest     string                    `json:"sourceRootDigest"`
	SourceFiles          []SourceFile              `json:"sourceFiles"`
	FragmentPaths        []string                  `json:"fragmentPaths,omitempty"`
	Valid                bool                      `json:"valid"`
	Issues               []configschema.Diagnostic `json:"issues"`
	ResolvedIntent       *ResolvedIntent           `json:"resolvedIntent,omitempty"`
	ResolvedIntentDigest string                    `json:"resolvedIntentDigest,omitempty"`
	BundleDigest         string                    `json:"bundleDigest,omitempty"`
}
type ResolvedIntent struct {
	DashboardID   string                      `json:"dashboardId"`
	SemanticModel string                      `json:"semanticModel"`
	Layout        *dashboarddefinition.Layout `json:"layout"`
	Pages         []PageIntent                `json:"pages"`
	Visuals       []VisualIntent              `json:"visuals"`
}
type PageIntent struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Grid         dashboard.PageGrid `json:"grid"`
	ReadingOrder []string           `json:"readingOrder"`
	Components   []ComponentIntent  `json:"components"`
	Height       int                `json:"height"`
}
type ComponentIntent struct {
	ID        string                  `json:"id"`
	Kind      string                  `json:"kind"`
	Visual    string                  `json:"visual,omitempty"`
	Placement dashboard.PagePlacement `json:"placement"`
}
type VisualIntent struct {
	ID               string                                          `json:"id"`
	Mark             string                                          `json:"mark"`
	Query            visualizationdefinition.QueryBinding            `json:"query"`
	SecondaryQueries map[string]visualizationdefinition.QueryBinding `json:"secondaryQueries,omitempty"`
}
type sourceCase struct {
	id, label, source string
	fragments         map[string]string
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func jsonDigest(value any) string {
	content, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return digest(content)
}

// Every source root uses every bundled connection/source/model/semantic-model
// resource, not a handwritten TypeScript approximation of the semantic graph.
func supportingFiles(repo string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, directory := range []string{"connections", "sources", "models", "semantic-models"} {
		base := filepath.Join(repo, "dashboards", directory)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(filepath.Join(repo, "dashboards"), path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(relative)] = content
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}
func sourceInventory(files map[string][]byte) []SourceFile {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	inventory := make([]SourceFile, 0, len(paths))
	for _, path := range paths {
		inventory = append(inventory, SourceFile{Path: path, Digest: digest(files[path])})
	}
	return inventory
}
func guideSource(repo string) (string, error) {
	guide, err := os.ReadFile(filepath.Join(repo, "docs/articles/build/dashboard.md"))
	if err != nil {
		return "", err
	}
	_, block, ok := strings.Cut(string(guide), "```yaml\n")
	if !ok {
		return "", fmt.Errorf("dashboard guide has no YAML block")
	}
	source, _, ok := strings.Cut(block, "```")
	if !ok {
		return "", fmt.Errorf("dashboard guide YAML is not closed")
	}
	return source, nil
}
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	panic("fixture missing mapping key: " + key)
}
func deleteMapping(node *yaml.Node, key string) {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
	panic("fixture missing mapping key: " + key)
}
func encodeNode(node *yaml.Node) string {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		panic(err)
	}
	if err := encoder.Close(); err != nil {
		panic(err)
	}
	return out.String()
}
func mutateSource(source string, mutate func(*yaml.Node)) string {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(source), &node); err != nil {
		panic(err)
	}
	mutate(mappingValue(node.Content[0], "spec"))
	return encodeNode(&node)
}

func corpusCases(repo string) ([]sourceCase, error) {
	corrected, err := guideSource(repo)
	if err != nil {
		return nil, err
	}
	dimension := "dimensions:\n          - dimension: purchase_date\n            grain: month\n            alias: purchase_month"
	if strings.Count(corrected, dimension) != 1 {
		return nil, fmt.Errorf("guide monthly dimension changed; update fixture mutations deliberately")
	}
	original := strings.Replace(corrected, dimension, "dimensions: [purchase_month]", 1)
	component := func(spec *yaml.Node, index int) *yaml.Node {
		return mappingValue(mappingValue(spec, "pages").Content[0], "components").Content[index]
	}
	cases := []sourceCase{
		{id: "original-guide", label: "Original guide: unknown dimension", source: original},
		{id: "corrected-monthly", label: "Corrected monthly guide", source: corrected},
		{id: "zero-span", label: "Zero column span", source: mutateSource(corrected, func(spec *yaml.Node) {
			mappingValue(mappingValue(component(spec, 0), "placement"), "columnSpan").Value = "0"
		})},
		{id: "out-of-grid", label: "Positive span outside the grid", source: mutateSource(corrected, func(spec *yaml.Node) {
			mappingValue(mappingValue(component(spec, 0), "placement"), "columnSpan").Value = "13"
		})},
		{id: "overlap", label: "Overlapping components", source: mutateSource(corrected, func(spec *yaml.Node) { mappingValue(mappingValue(component(spec, 1), "placement"), "row").Value = "1" })},
		{id: "missing-visual", label: "Missing visual reference", source: mutateSource(corrected, func(spec *yaml.Node) { mappingValue(component(spec, 0), "visual").Value = "missing-visual" })},
		{id: "duplicate-visual-id", label: "Duplicate visual identity", source: mutateSource(corrected, func(spec *yaml.Node) {
			visuals := mappingValue(spec, "visuals")
			mappingValue(visuals.Content[1], "id").Value = mappingValue(visuals.Content[0], "id").Value
		})},
		{id: "omitted-defaults", label: "Omitted layout defaults", source: mutateSource(corrected, func(spec *yaml.Node) { deleteMapping(spec, "layout") })},
		{id: "explicit-defaults", label: "Explicit layout defaults", source: corrected},
		{id: "reordered-definitions", label: "Independent visuals reordered", source: mutateSource(corrected, func(spec *yaml.Node) {
			visuals := mappingValue(spec, "visuals")
			visuals.Content[0], visuals.Content[1] = visuals.Content[1], visuals.Content[0]
		})},
	}
	fragments := map[string]string{}
	confined := mutateSource(corrected, func(spec *yaml.Node) {
		for _, collection := range []string{"visuals", "pages"} {
			value := mappingValue(spec, collection)
			fragment := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: collection}, value}}
			fragments["dashboards/fragments/"+collection+".yaml"] = encodeNode(fragment)
			for i := 0; i < len(spec.Content); i += 2 {
				if spec.Content[i].Value == collection {
					spec.Content[i+1] = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				}
			}
		}
		var includes yaml.Node
		if err := yaml.Unmarshal([]byte("visuals: [fragments/visuals.yaml]\npages: [fragments/pages.yaml]\n"), &includes); err != nil {
			panic(err)
		}
		spec.Content = append(spec.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "includes"}, includes.Content[0])
	})
	cases = append(cases, sourceCase{id: "confined-fragments", label: "Confined visual and page fragments", source: confined, fragments: fragments})
	return cases, nil
}
func compileCorpus(repo string) ([]FixtureEvidence, error) {
	support, err := supportingFiles(repo)
	if err != nil {
		return nil, err
	}
	cases, err := corpusCases(repo)
	if err != nil {
		return nil, err
	}
	reports := make([]FixtureEvidence, 0, len(cases))
	for _, fixture := range cases {
		report, err := compileCase(support, fixture)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}
func compileCase(support map[string][]byte, fixture sourceCase) (FixtureEvidence, error) {
	files := make(map[string][]byte, len(support)+1+len(fixture.fragments))
	for path, content := range support {
		files[path] = content
	}
	files[dashboardPath] = []byte(fixture.source)
	fragmentPaths := make([]string, 0, len(fixture.fragments))
	for path, content := range fixture.fragments {
		files[path] = []byte(content)
		fragmentPaths = append(fragmentPaths, path)
	}
	sort.Strings(fragmentPaths)
	inventory := sourceInventory(files)
	report := FixtureEvidence{ID: fixture.id, Label: fixture.label, Source: fixture.source, SourceDigest: digest([]byte(fixture.source)), SourceRootDigest: jsonDigest(inventory), SourceFiles: inventory, FragmentPaths: fragmentPaths, Issues: []configschema.Diagnostic{}}
	root, err := os.MkdirTemp("", "contract-evidence-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(root)
	for _, file := range inventory {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return report, err
		}
		if err := os.WriteFile(path, files[file.Path], 0644); err != nil {
			return report, err
		}
	}
	bundle, compileErr := projectcompiler.Compile(root)
	if compileErr != nil {
		report.Issues = configschema.Diagnostics(compileErr)
		for i := range report.Issues {
			diagnostic := &report.Issues[i]
			diagnostic.File = strings.ReplaceAll(diagnostic.File, root+string(filepath.Separator), "")
			diagnostic.Message = strings.ReplaceAll(diagnostic.Message, root+string(filepath.Separator), "")
			diagnostic.Message = strings.ReplaceAll(diagnostic.Message, root, "<source-root>")
			diagnostic.Message = stableDiagnosticMessage(diagnostic.Message)
		}
		return report, nil
	}
	definition, ok := bundle.DashboardDefinitions()["dashboard:executive-sales"]
	if !ok {
		return report, fmt.Errorf("fixture %s: compiler omitted dashboard", fixture.id)
	}
	intent := ResolvedIntent{DashboardID: definition.ID, SemanticModel: definition.SemanticModel, Layout: definition.Layout, Pages: make([]PageIntent, 0, len(definition.Pages)), Visuals: make([]VisualIntent, 0, len(definition.Visualizations))}
	for _, page := range definition.Pages {
		item := PageIntent{ID: page.ID, Title: page.Title, Grid: page.Grid, Height: page.Height, ReadingOrder: []string{}, Components: []ComponentIntent{}}
		for _, component := range page.Visuals {
			item.ReadingOrder = append(item.ReadingOrder, component.ID)
			item.Components = append(item.Components, ComponentIntent{ID: component.ID, Kind: component.Kind, Visual: component.Visual, Placement: component.Placement})
		}
		intent.Pages = append(intent.Pages, item)
	}
	ids := make([]string, 0, len(definition.Visualizations))
	for id := range definition.Visualizations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visual := definition.Visualizations[id]
		raw, err := json.Marshal(visual.Spec)
		if err != nil {
			return report, err
		}
		var spec map[string]any
		if err := json.Unmarshal(raw, &spec); err != nil {
			return report, err
		}
		mark, _ := spec["mark"].(string)
		if mark == "" {
			mark, _ = spec["kind"].(string)
		}
		intent.Visuals = append(intent.Visuals, VisualIntent{ID: id, Mark: mark, Query: visual.Query, SecondaryQueries: visual.SecondaryQueries})
	}
	report.Valid = true
	report.ResolvedIntent = &intent
	report.ResolvedIntentDigest = jsonDigest(intent)
	report.BundleDigest = bundle.Digest()
	return report, nil
}

// The JSON Schema library renders object-property causes in Go map order.
// Preserve the complete message tree while sorting sibling subtrees, so
// recording the same actual error does not create random snapshot changes.
func stableDiagnosticMessage(message string) string {
	lines := strings.Split(message, "\n")
	if len(lines) < 2 {
		return message
	}
	type node struct {
		line     string
		indent   int
		children []*node
	}
	root := &node{indent: -1}
	stack := []*node{root}
	for _, line := range lines[1:] {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		child := &node{line: line, indent: indent}
		parent := stack[len(stack)-1]
		parent.children = append(parent.children, child)
		stack = append(stack, child)
	}
	var render func(*node) string
	render = func(value *node) string {
		children := make([]string, 0, len(value.children))
		for _, child := range value.children {
			children = append(children, render(child))
		}
		sort.Strings(children)
		if value == root {
			return strings.Join(children, "\n")
		}
		if len(children) == 0 {
			return value.line
		}
		return value.line + "\n" + strings.Join(children, "\n")
	}
	return lines[0] + "\n" + render(root)
}
