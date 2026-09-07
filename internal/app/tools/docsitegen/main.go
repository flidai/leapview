// Command docsitegen composes authored navigation with generated reference
// catalogs into the runtime catalog and search index used by the public site.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	docsearch "github.com/flidai/leapview/internal/app/site/search"
	"gopkg.in/yaml.v3"
)

func main() {
	navigation := flag.String("navigation", "docs/navigation.yaml", "authored documentation navigation manifest")
	catalog := flag.String("catalog", "docs/catalog.json", "generated runtime catalog")
	search := flag.String("search", "docs/"+docsearch.Filename, "generated documentation search index")
	check := flag.Bool("check", false, "verify generated artifacts are current without changing them")
	flag.Parse()
	var err error
	if *check {
		err = checkGenerated(*navigation, *catalog, *search)
	} else {
		err = generate(*navigation, *catalog, *search)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate documentation site catalog: %v\n", err)
		os.Exit(1)
	}
}

type navigationManifest struct {
	Sections []sectionSpec `yaml:"sections"`
}

type sectionSpec struct {
	ID        string         `yaml:"id"`
	Title     string         `yaml:"title"`
	Summary   string         `yaml:"summary"`
	Documents []documentSpec `yaml:"documents"`
	Groups    []groupSpec    `yaml:"groups"`
}

type groupSpec struct {
	ID         string          `yaml:"id"`
	Title      string          `yaml:"title"`
	Summary    string          `yaml:"summary"`
	Documents  []documentSpec  `yaml:"documents"`
	Collection *collectionSpec `yaml:"collection"`
}

type collectionSpec struct {
	Kind       string        `yaml:"kind"`
	Catalog    string        `yaml:"catalog"`
	SourceDir  string        `yaml:"sourceDir"`
	SlugPrefix string        `yaml:"slugPrefix"`
	Index      *documentSpec `yaml:"index"`
}

type documentSpec struct {
	Slug            string `yaml:"slug" json:"slug"`
	Title           string `yaml:"title" json:"title"`
	Type            string `yaml:"type" json:"type"`
	NavigationTitle string `yaml:"navigationTitle" json:"navigationTitle,omitempty"`
	Summary         string `yaml:"summary" json:"summary"`
	Source          string `yaml:"source" json:"source"`
	Breadcrumb      string `yaml:"breadcrumb" json:"breadcrumb,omitempty"`
	Generated       bool   `yaml:"generated,omitempty" json:"generated,omitempty"`
}

type generatedCatalog struct {
	Sections []generatedSection `json:"sections"`
}

type generatedSection struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Summary   string           `json:"summary"`
	Href      string           `json:"href"`
	Documents []documentSpec   `json:"documents,omitempty"`
	Groups    []generatedGroup `json:"groups,omitempty"`
}

type generatedGroup struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Summary   string         `json:"summary,omitempty"`
	Href      string         `json:"href"`
	Documents []documentSpec `json:"documents"`
}

type referenceCatalog struct {
	Documents []struct {
		Slug    string `json:"slug"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"documents"`
}

type visualCatalog struct {
	Overview struct {
		Source     string `json:"source"`
		Title      string `json:"title"`
		Breadcrumb string `json:"breadcrumb"`
	} `json:"overview"`
	Documents []struct {
		Source     string `json:"source"`
		Title      string `json:"title"`
		Breadcrumb string `json:"breadcrumb"`
	} `json:"documents"`
}

func generate(navigationPath, catalogPath, searchPath string) error {
	contents, err := os.ReadFile(navigationPath)
	if err != nil {
		return fmt.Errorf("read navigation: %w", err)
	}
	var manifest navigationManifest
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode navigation: %w", err)
	}
	root := filepath.Dir(navigationPath)
	catalog := generatedCatalog{Sections: make([]generatedSection, 0, len(manifest.Sections))}
	search := make([]docsearch.Document, 0)
	seenSlugs := map[string]struct{}{}
	seenSources := map[string]struct{}{}

	for _, section := range manifest.Sections {
		if section.ID == "" || section.Title == "" {
			return fmt.Errorf("documentation section requires id and title")
		}
		generated := generatedSection{ID: section.ID, Title: section.Title, Summary: section.Summary}
		for _, document := range section.Documents {
			if err := addDocument(root, section.Title, "", document, seenSlugs, seenSources, &generated.Documents, &search); err != nil {
				return err
			}
		}
		for _, group := range section.Groups {
			if group.ID == "" || group.Title == "" {
				return fmt.Errorf("documentation group in %s requires id and title", section.ID)
			}
			documents := append([]documentSpec(nil), group.Documents...)
			if group.Collection != nil {
				collectionDocuments, err := loadCollection(root, *group.Collection)
				if err != nil {
					return fmt.Errorf("load %s collection: %w", group.ID, err)
				}
				documents = append(documents, collectionDocuments...)
			}
			generatedGroup := generatedGroup{ID: group.ID, Title: group.Title, Summary: group.Summary}
			for _, document := range documents {
				if err := addDocument(root, section.Title, group.Title, document, seenSlugs, seenSources, &generatedGroup.Documents, &search); err != nil {
					return err
				}
			}
			generatedGroup.Href = firstDocumentHref(generatedGroup.Documents)
			generated.Groups = append(generated.Groups, generatedGroup)
		}
		generated.Href = firstDocumentHref(generated.Documents)
		if generated.Href == "" {
			for _, group := range generated.Groups {
				if group.Href != "" {
					generated.Href = group.Href
					break
				}
			}
		}
		if generated.Href == "" {
			return fmt.Errorf("documentation section %s has no documents", section.ID)
		}
		catalog.Sections = append(catalog.Sections, generated)
	}
	if err := validateNoOrphanMarkdown(root, seenSources); err != nil {
		return err
	}
	if err := validateInternalLinks(root, seenSources, seenSlugs); err != nil {
		return err
	}
	if err := validateYAMLExamples(root, seenSources); err != nil {
		return err
	}
	if err := writeJSON(catalogPath, catalog); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(catalogPath), "llms.txt"), []byte(renderLLMs(catalog)), 0o644); err != nil {
		return fmt.Errorf("write llms.txt: %w", err)
	}
	return docsearch.Build(searchPath, search)
}

func checkGenerated(navigationPath, catalogPath, searchPath string) error {
	temporary, err := os.MkdirTemp("", "leapview-docsite-check-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(temporary)

	generatedCatalog := filepath.Join(temporary, "catalog.json")
	generatedSearch := filepath.Join(temporary, docsearch.Filename)
	if err := generate(navigationPath, generatedCatalog, generatedSearch); err != nil {
		return err
	}
	for _, artifact := range []struct {
		current   string
		generated string
	}{
		{current: catalogPath, generated: generatedCatalog},
		{current: searchPath, generated: generatedSearch},
		{current: filepath.Join(filepath.Dir(catalogPath), "llms.txt"), generated: filepath.Join(temporary, "llms.txt")},
	} {
		current, err := os.ReadFile(artifact.current)
		if err != nil {
			return fmt.Errorf("read generated artifact %s: %w", artifact.current, err)
		}
		expected, err := os.ReadFile(artifact.generated)
		if err != nil {
			return fmt.Errorf("read expected artifact %s: %w", artifact.generated, err)
		}
		if !bytes.Equal(current, expected) {
			return fmt.Errorf("%s is out of date; run task docs:generate", artifact.current)
		}
	}
	return nil
}

func renderLLMs(catalog generatedCatalog) string {
	var out strings.Builder
	out.WriteString("# LeapView\n\n")
	out.WriteString("> Dashboards-as-code BI with generated, machine-readable CLI, API, configuration, and visual documentation.\n\n")
	out.WriteString("## Agent entry points\n\n")
	out.WriteString("- [Documentation MCP](/mcp): read-only Streamable HTTP MCP with docs_catalog, docs_search, and docs_read tools.\n")
	out.WriteString("- [Agent tool manifest](/docs/agent-tools/manifest.json): generated names, privileges, defaults, annotations, and input/output schemas.\n")
	out.WriteString("- [CLI manifest](/docs/cli/manifest.json): versioned command syntax, arguments, flags, defaults, side effects, and confirmation behavior.\n")
	out.WriteString("- [API operation manifest](/docs/api/operations.json): versioned operation IDs, routes, authorization, safety metadata, and request/response schemas.\n")
	out.WriteString("- [OpenAPI schema](/docs/openapi.yaml): complete generated OpenAPI contract.\n\n")
	out.WriteString("Focused slices are available at `/docs/agent-tools/tools/{name}.json|md`, `/docs/cli/commands/{id}.json|md`, and `/docs/api/operations/{operationId}.json|md`. Prefer search and focused reads over loading complete manifests. Public documentation tools never execute tools, commands, or API operations.\n\n")
	out.WriteString("## Documentation\n")
	for _, section := range catalog.Sections {
		out.WriteString("\n### " + section.Title + "\n\n")
		for _, document := range section.Documents {
			writeLLMsDocument(&out, document)
		}
		for _, group := range section.Groups {
			out.WriteString("\n#### " + group.Title + "\n\n")
			for _, document := range group.Documents {
				writeLLMsDocument(&out, document)
			}
		}
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

func writeLLMsDocument(out *strings.Builder, document documentSpec) {
	out.WriteString("- [" + document.Title + "](/docs/" + document.Slug + ")")
	if document.Summary != "" {
		out.WriteString(": " + strings.ReplaceAll(strings.TrimSpace(document.Summary), "\n", " "))
	}
	out.WriteByte('\n')
}

func addDocument(root, section, group string, document documentSpec, seenSlugs, seenSources map[string]struct{}, output *[]documentSpec, search *[]docsearch.Document) error {
	document.Slug = strings.Trim(document.Slug, "/")
	if document.Slug == "" || document.Title == "" || document.Source == "" {
		return fmt.Errorf("documentation entry requires slug, title, and source")
	}
	if err := validateDocumentType(document); err != nil {
		return err
	}
	if _, ok := seenSlugs[document.Slug]; ok {
		return fmt.Errorf("duplicate documentation slug %q", document.Slug)
	}
	seenSlugs[document.Slug] = struct{}{}
	path := filepath.Join(root, filepath.FromSlash(document.Source))
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read documentation source %s: %w", document.Source, err)
	}
	heading := documentHeading(contents)
	if heading == "" {
		return fmt.Errorf("documentation source %s is missing an h1", document.Source)
	}
	if heading != document.Title {
		return fmt.Errorf("documentation title %q does not match h1 %q in %s", document.Title, heading, document.Source)
	}
	if err := validateDocumentStructure(document, contents); err != nil {
		return err
	}
	if document.Breadcrumb == "" {
		document.Breadcrumb = document.Title
	}
	seenSources[filepath.ToSlash(document.Source)] = struct{}{}
	*output = append(*output, document)
	*search = append(*search, docsearch.Document{Slug: document.Slug, Title: document.Title, Summary: document.Summary, Section: section, Category: group, Body: string(contents), Generated: document.Generated})
	return nil
}

var supportedDocumentTypes = map[string]struct{}{
	"landing":     {},
	"tutorial":    {},
	"how-to":      {},
	"explanation": {},
	"reference":   {},
}

func validateDocumentType(document documentSpec) error {
	if document.Type == "" {
		return fmt.Errorf("documentation entry %q requires type", document.Slug)
	}
	if _, ok := supportedDocumentTypes[document.Type]; !ok {
		return fmt.Errorf("documentation entry %q has unsupported type %q", document.Slug, document.Type)
	}
	if document.Generated && document.Type != "reference" {
		return fmt.Errorf("generated documentation entry %q must have type reference", document.Slug)
	}
	return nil
}

func validateDocumentStructure(document documentSpec, contents []byte) error {
	headings := documentSectionHeadings(contents)
	switch document.Type {
	case "landing":
		if fencedCodeLine.Match(contents) {
			return fmt.Errorf("landing %s must not contain fenced code", document.Slug)
		}
		destinations := map[string]struct{}{}
		for _, match := range internalDocumentationLink.FindAllSubmatch(contents, -1) {
			target := string(match[1])
			if target != "/docs" && target != "/docs/search" {
				destinations[target] = struct{}{}
			}
		}
		if len(destinations) < 2 {
			return fmt.Errorf("landing %s must link to at least two documentation destinations", document.Slug)
		}
	case "tutorial":
		for _, required := range []string{"Before you begin", "Troubleshooting", "Next steps"} {
			if !containsHeading(headings, required) {
				return fmt.Errorf("tutorial %s is missing required section %q", document.Slug, required)
			}
		}
		if !containsHeadingPrefix(headings, "Verify") {
			return fmt.Errorf("tutorial %s requires a verification section", document.Slug)
		}
	case "how-to":
		verificationHeadings := append([]string{document.Title}, headings...)
		if !containsAnyHeadingTerm(verificationHeadings, "validat", "verif", "test", "troubleshoot", "diagnos", "check", "inspect", "monitor", "observ", "review") {
			return fmt.Errorf("how-to %s requires a validation, verification, test, or troubleshooting section", document.Slug)
		}
	case "reference":
		if !document.Generated && !fencedCodeLine.Match(contents) && !markdownTableDelimiter.Match(contents) && !markdownListLine.Match(contents) {
			return fmt.Errorf("reference %s requires a list, table, or fenced code block", document.Slug)
		}
	}
	return nil
}

func documentSectionHeadings(contents []byte) []string {
	inFence := false
	fence := ""
	headings := make([]string, 0)
	for _, rawLine := range strings.Split(string(contents), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if !inFence {
				inFence = true
				fence = marker
			} else if marker == fence {
				inFence = false
				fence = ""
			}
			continue
		}
		if !inFence && strings.HasPrefix(line, "## ") {
			headings = append(headings, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
		}
	}
	return headings
}

func containsHeading(headings []string, expected string) bool {
	for _, heading := range headings {
		if heading == expected {
			return true
		}
	}
	return false
}

func containsHeadingPrefix(headings []string, prefix string) bool {
	for _, heading := range headings {
		if strings.HasPrefix(heading, prefix) {
			return true
		}
	}
	return false
}

func containsAnyHeadingTerm(headings []string, terms ...string) bool {
	for _, heading := range headings {
		words := strings.FieldsFunc(strings.ToLower(heading), func(character rune) bool {
			return !unicode.IsLetter(character) && !unicode.IsNumber(character)
		})
		for _, word := range words {
			for _, term := range terms {
				if strings.HasPrefix(word, term) {
					return true
				}
			}
		}
	}
	return false
}

func documentHeading(contents []byte) string {
	inFence := false
	fence := ""
	for _, rawLine := range strings.Split(string(contents), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if !inFence {
				inFence = true
				fence = marker
			} else if marker == fence {
				inFence = false
				fence = ""
			}
			continue
		}
		if !inFence && strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func validateNoOrphanMarkdown(root string, seenSources map[string]struct{}) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := seenSources[relative]; !ok {
			return fmt.Errorf("orphaned documentation source %s", relative)
		}
		return nil
	})
}

var internalDocumentationLink = regexp.MustCompile(`\]\((/docs(?:/[^\s)#?]+)?)`)
var yamlCodeFence = regexp.MustCompile("(?ms)```ya?ml(?:[ \\t]+[^\\n]*)?\\n(.*?)\\n```")
var fencedCodeLine = regexp.MustCompile("(?m)^[ \\t]*(?:```|~~~)")
var markdownTableDelimiter = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t]*:?-{3,}:?[ \t]*\|`)
var markdownListLine = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+] |[0-9]+\. )`)

func validateInternalLinks(root string, seenSources, seenSlugs map[string]struct{}) error {
	machineLinks, err := loadMachineDocumentationLinks(root)
	if err != nil {
		return err
	}
	for source := range seenSources {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(source)))
		if err != nil {
			return err
		}
		for _, match := range internalDocumentationLink.FindAllSubmatch(contents, -1) {
			target := string(match[1])
			if target == "/docs" || target == "/docs/search" || target == "/docs/openapi.yaml" {
				continue
			}
			if _, ok := machineLinks[target]; ok {
				continue
			}
			if strings.HasPrefix(target, "/docs/schemas/") {
				name := strings.TrimPrefix(target, "/docs/schemas/")
				if _, err := os.Stat(filepath.Join(root, "reference", "config", "schemas", filepath.Base(name))); err == nil && name == filepath.Base(name) {
					continue
				}
			}
			slug := strings.TrimPrefix(target, "/docs/")
			if _, ok := seenSlugs[slug]; ok {
				continue
			}
			return fmt.Errorf("broken documentation link %s in %s", target, source)
		}
	}
	return nil
}

func loadMachineDocumentationLinks(root string) (map[string]struct{}, error) {
	links := map[string]struct{}{}
	agentToolPath := filepath.Join(root, "reference", "agent-tools", "manifest.json")
	if contents, err := os.ReadFile(agentToolPath); err == nil {
		var manifest struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(contents, &manifest); err != nil {
			return nil, fmt.Errorf("decode %s: %w", agentToolPath, err)
		}
		links["/docs/agent-tools/manifest.json"] = struct{}{}
		for _, tool := range manifest.Tools {
			links["/docs/agent-tools/tools/"+tool.Name+".json"] = struct{}{}
			links["/docs/agent-tools/tools/"+tool.Name+".md"] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	cliPath := filepath.Join(root, "reference", "cli", "manifest.json")
	if contents, err := os.ReadFile(cliPath); err == nil {
		var manifest struct {
			Commands []struct {
				ID   string   `json:"id"`
				Path []string `json:"path"`
			} `json:"commands"`
		}
		if err := json.Unmarshal(contents, &manifest); err != nil {
			return nil, fmt.Errorf("decode %s: %w", cliPath, err)
		}
		links["/docs/cli/manifest.json"] = struct{}{}
		for _, command := range manifest.Commands {
			links["/docs/cli/commands/"+command.ID+".json"] = struct{}{}
			links["/docs/cli/commands/"+command.ID+".md"] = struct{}{}
			if len(command.Path) > 1 {
				links["/docs/cli/"+command.ID] = struct{}{}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	apiPath := filepath.Join(root, "api", "operations.json")
	if contents, err := os.ReadFile(apiPath); err == nil {
		var manifest struct {
			Operations []struct {
				OperationID string `json:"operationId"`
			} `json:"operations"`
		}
		if err := json.Unmarshal(contents, &manifest); err != nil {
			return nil, fmt.Errorf("decode %s: %w", apiPath, err)
		}
		links["/docs/api/operations.json"] = struct{}{}
		for _, operation := range manifest.Operations {
			links["/docs/api/operations/"+operation.OperationID+".json"] = struct{}{}
			links["/docs/api/operations/"+operation.OperationID+".md"] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return links, nil
}

func validateYAMLExamples(root string, seenSources map[string]struct{}) error {
	for source := range seenSources {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(source)))
		if err != nil {
			return err
		}
		for index, match := range yamlCodeFence.FindAllSubmatch(contents, -1) {
			var value any
			if err := yaml.Unmarshal(match[1], &value); err != nil {
				return fmt.Errorf("invalid YAML example %d in %s: %w", index+1, source, err)
			}
		}
	}
	return nil
}

func loadCollection(root string, collection collectionSpec) ([]documentSpec, error) {
	documents := make([]documentSpec, 0)
	if collection.Index != nil {
		documents = append(documents, *collection.Index)
	}
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(collection.Catalog)))
	if err != nil {
		return nil, err
	}
	switch collection.Kind {
	case "catalog":
		var catalog referenceCatalog
		if err := json.Unmarshal(contents, &catalog); err != nil {
			return nil, err
		}
		for _, document := range catalog.Documents {
			documents = append(documents, documentSpec{
				Slug:      joinSlug(collection.SlugPrefix, document.Slug),
				Title:     document.Title,
				Type:      "reference",
				Summary:   document.Summary,
				Source:    filepath.ToSlash(filepath.Join(collection.SourceDir, document.Slug+".md")),
				Generated: true,
			})
		}
	case "visual":
		var catalog visualCatalog
		if err := json.Unmarshal(contents, &catalog); err != nil {
			return nil, err
		}
		if collection.Index == nil {
			documents = append(documents, visualDocument(collection, catalog.Overview.Source, catalog.Overview.Title, catalog.Overview.Breadcrumb))
		}
		for _, document := range catalog.Documents {
			documents = append(documents, visualDocument(collection, document.Source, document.Title, document.Breadcrumb))
		}
	default:
		return nil, fmt.Errorf("unsupported collection kind %q", collection.Kind)
	}
	return documents, nil
}

func visualDocument(collection collectionSpec, source, title, breadcrumb string) documentSpec {
	return documentSpec{
		Slug:       joinSlug(collection.SlugPrefix, source),
		Title:      title,
		Type:       "reference",
		Summary:    "Configuration and query shape for the " + title + " visual.",
		Source:     filepath.ToSlash(filepath.Join(collection.SourceDir, source+".md")),
		Breadcrumb: breadcrumb,
		Generated:  true,
	}
}

func joinSlug(prefix, slug string) string {
	return strings.Trim(strings.Trim(prefix, "/")+"/"+strings.Trim(slug, "/"), "/")
}

func firstDocumentHref(documents []documentSpec) string {
	if len(documents) == 0 {
		return ""
	}
	return "/docs/" + documents[0].Slug
}

func writeJSON(path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
