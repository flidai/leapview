package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	docsearch "github.com/flidai/leapview/internal/app/site/search"
)

func TestGenerateBuildsUnifiedCatalogFromArticlesAndGeneratedCollections(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start here
    summary: Learn LeapView.
    documents:
      - slug: getting-started
        title: Getting started
        type: explanation
        navigationTitle: Overview
        summary: Run the sample project.
        source: articles/getting-started.md
  - id: reference
    title: Reference
    summary: Exact generated contracts.
    groups:
      - id: cli
        title: CLI
        collection:
          kind: catalog
          catalog: reference/cli/catalog.json
          sourceDir: reference/cli
          slugPrefix: cli
          index:
            slug: cli
            title: CLI command reference
            type: reference
            summary: Command index.
            source: reference/cli/index.md
`)
	writeFixture(t, root, "articles/getting-started.md", "# Getting started\n")
	writeFixture(t, root, "reference/cli/index.md", "# CLI command reference\n\n| Command | Purpose |\n| --- | --- |\n| deploy | Deploy a project |\n")
	writeFixture(t, root, "reference/cli/deploy.md", "# leapview deploy\n")
	writeFixture(t, root, "reference/cli/catalog.json", `{"documents":[{"slug":"deploy","title":"leapview deploy","summary":"Deploy a project."}]}`)

	searchPath := filepath.Join(root, docsearch.Filename)
	if err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), searchPath); err != nil {
		t.Fatalf("generate documentation catalog: %v", err)
	}

	var catalog generatedCatalog
	decodeFixture(t, filepath.Join(root, "catalog.json"), &catalog)
	if got, want := len(catalog.Sections), 2; got != want {
		t.Fatalf("sections = %d, want %d", got, want)
	}
	if got, want := catalog.Sections[1].Groups[0].Documents[1].Slug, "cli/deploy"; got != want {
		t.Fatalf("generated CLI slug = %q, want %q", got, want)
	}
	if got, want := catalog.Sections[1].Groups[0].Documents[1].Source, "reference/cli/deploy.md"; got != want {
		t.Fatalf("generated CLI source = %q, want %q", got, want)
	}
	if got, want := catalog.Sections[0].Documents[0].NavigationTitle, "Overview"; got != want {
		t.Fatalf("navigation title = %q, want %q", got, want)
	}
	if catalog.Sections[1].Groups[0].Documents[0].Generated {
		t.Fatal("authored collection index marked as generated")
	}
	if !catalog.Sections[1].Groups[0].Documents[1].Generated {
		t.Fatal("generated collection document is not marked as generated")
	}
	llms := readFixture(t, filepath.Join(root, "llms.txt"))
	for _, want := range []string{"# LeapView", "[Documentation MCP](/mcp)", "[leapview deploy](/docs/cli/deploy)"} {
		if !strings.Contains(llms, want) {
			t.Errorf("llms.txt missing %q:\n%s", want, llms)
		}
	}

	index, err := docsearch.Open(os.DirFS(root), docsearch.Filename)
	if err != nil {
		t.Fatalf("open generated search index: %v", err)
	}
	defer index.Close()

	count, err := index.Count(context.Background())
	if err != nil {
		t.Fatalf("count generated search documents: %v", err)
	}
	if got, want := count, 3; got != want {
		t.Fatalf("search documents = %d, want %d", got, want)
	}
	results, err := index.Search(context.Background(), "leapview deploy", 1)
	if err != nil {
		t.Fatalf("query generated search index: %v", err)
	}
	if len(results) != 1 || results[0].Slug != "cli/deploy" {
		t.Fatalf("search result = %#v, want cli/deploy", results)
	}
}

func TestGenerateRejectsUnknownNavigationFields(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: concepts
    title: Core concepts
    documents:
      - {slug: projects, title: Projects, type: explanation, workspaces: and environments, source: projects.md}
`)
	writeFixture(t, root, "projects.md", "# Projects, workspaces, and environments\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "field workspaces not found") {
		t.Fatalf("generate error = %v, want strict unknown-field error", err)
	}
}

func TestGenerateRequiresSupportedDocumentationType(t *testing.T) {
	tests := []struct {
		name      string
		typeField string
		want      string
	}{
		{name: "missing", want: `documentation entry "one" requires type`},
		{name: "unsupported", typeField: "        type: recipe\n", want: `documentation entry "one" has unsupported type "recipe"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - slug: one
        title: One
`+tt.typeField+`        source: one.md
`)
			writeFixture(t, root, "one.md", "# One\n")

			err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("generate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestGenerateValidatesTutorialStructure(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - slug: first-project
        title: Build your first project
        type: tutorial
        source: first-project.md
`)
	writeFixture(t, root, "first-project.md", "# Build your first project\n\n## Make a change\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), `tutorial first-project is missing required section "Before you begin"`) {
		t.Fatalf("generate error = %v, want tutorial structure error", err)
	}
}

func TestGenerateRequiresHowToVerification(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: guides
    title: Guides
    documents:
      - slug: rotate-token
        title: Rotate a token
        type: how-to
        source: rotate-token.md
`)
	writeFixture(t, root, "rotate-token.md", "# Rotate a token\n\n## Replace the credential\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "how-to rotate-token requires a validation, verification, test, or troubleshooting section") {
		t.Fatalf("generate error = %v, want how-to verification error", err)
	}
}

func TestGenerateDoesNotTreatLatestAsATestSection(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: guides
    title: Guides
    documents:
      - slug: upgrade
        title: Upgrade LeapView
        type: how-to
        source: upgrade.md
`)
	writeFixture(t, root, "upgrade.md", "# Upgrade LeapView\n\n## Choose the latest release\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "how-to upgrade requires a validation, verification, test, or troubleshooting section") {
		t.Fatalf("generate error = %v, want how-to verification error", err)
	}
}

func TestGenerateValidatesLandingPageNavigation(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name:     "too few destinations",
			markdown: "# Start here\n\n[Install](/docs/installation)\n",
			want:     "landing start-here must link to at least two documentation destinations",
		},
		{
			name:     "procedural code fence",
			markdown: "# Start here\n\n[Install](/docs/installation) and [Tutorial](/docs/tutorial).\n\n```sh\nleapview serve\n```\n",
			want:     "landing start-here must not contain fenced code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - slug: start-here
        title: Start here
        type: landing
        source: start-here.md
`)
			writeFixture(t, root, "start-here.md", tt.markdown)

			err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("generate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestGenerateRequiresAuthoredReferenceStructure(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: reference
    title: Reference
    documents:
      - slug: api-conventions
        title: API conventions
        type: reference
        source: api-conventions.md
`)
	writeFixture(t, root, "api-conventions.md", "# API conventions\n\nThis page describes the API.\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "reference api-conventions requires a list, table, or fenced code block") {
		t.Fatalf("generate error = %v, want authored reference structure error", err)
	}
}

func TestGenerateClassifiesGeneratedCollectionsAsReference(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: reference
    title: Reference
    groups:
      - id: cli
        title: CLI
        collection:
          kind: catalog
          catalog: reference/cli/catalog.json
          sourceDir: reference/cli
          slugPrefix: cli
`)
	writeFixture(t, root, "reference/cli/deploy.md", "# leapview deploy\n")
	writeFixture(t, root, "reference/cli/catalog.json", `{"documents":[{"slug":"deploy","title":"leapview deploy","summary":"Deploy a project."}]}`)

	if err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename)); err != nil {
		t.Fatalf("generate documentation catalog: %v", err)
	}

	var catalog generatedCatalog
	decodeFixture(t, filepath.Join(root, "catalog.json"), &catalog)
	if got, want := catalog.Sections[0].Groups[0].Documents[0].Type, "reference"; got != want {
		t.Fatalf("generated document type = %q, want %q", got, want)
	}
}

func TestGenerateRejectsDocumentTitleThatDoesNotMatchHeading(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: concepts
    title: Core concepts
    documents:
      - slug: projects
        title: Projects and workspaces
        type: explanation
        navigationTitle: Projects
        source: projects.md
`)
	writeFixture(t, root, "projects.md", "# Project concepts\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), `title "Projects and workspaces" does not match h1 "Project concepts"`) {
		t.Fatalf("generate error = %v, want title/h1 mismatch", err)
	}
}

func TestGenerateRejectsDuplicateSlugs(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - {slug: duplicate, title: One, type: explanation, source: one.md}
      - {slug: duplicate, title: Two, type: explanation, source: two.md}
`)
	writeFixture(t, root, "one.md", "# One\n")
	writeFixture(t, root, "two.md", "# Two\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), `duplicate documentation slug "duplicate"`) {
		t.Fatalf("generate error = %v, want duplicate slug error", err)
	}
}

func TestGenerateRejectsMissingDocumentSource(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - {slug: missing, title: Missing, type: explanation, source: missing.md}
`)

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "missing.md") {
		t.Fatalf("generate error = %v, want missing source error", err)
	}
}

func TestGenerateRejectsOrphanedMarkdown(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - {slug: one, title: One, type: explanation, source: one.md}
`)
	writeFixture(t, root, "one.md", "# One\n")
	writeFixture(t, root, "orphan.md", "# Orphan\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "orphaned documentation source orphan.md") {
		t.Fatalf("generate error = %v, want orphaned source error", err)
	}
}

func TestGenerateRejectsBrokenInternalLink(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - {slug: one, title: One, type: explanation, source: one.md}
`)
	writeFixture(t, root, "one.md", "# One\n\n[Missing](/docs/missing)\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "broken documentation link /docs/missing") {
		t.Fatalf("generate error = %v, want broken link error", err)
	}
}

func TestGenerateRejectsInvalidYAMLExample(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - {slug: one, title: One, type: explanation, source: one.md}
`)
	writeFixture(t, root, "one.md", "# One\n\n```yaml\nitems: [unterminated\n```\n")

	err := generate(filepath.Join(root, "navigation.yaml"), filepath.Join(root, "catalog.json"), filepath.Join(root, docsearch.Filename))
	if err == nil || !strings.Contains(err.Error(), "invalid YAML example") {
		t.Fatalf("generate error = %v, want invalid YAML example error", err)
	}
}

func TestCheckGeneratedRejectsOutdatedArtifacts(t *testing.T) {
	root := t.TempDir()
	navigation := filepath.Join(root, "navigation.yaml")
	catalog := filepath.Join(root, "catalog.json")
	search := filepath.Join(root, docsearch.Filename)
	writeFixture(t, root, "navigation.yaml", `sections:
  - id: start
    title: Start
    documents:
      - {slug: one, title: One, type: explanation, source: one.md}
`)
	writeFixture(t, root, "one.md", "# One\n")
	writeFixture(t, root, "catalog.json", "{}\n")
	writeFixture(t, root, docsearch.Filename, "[]\n")

	err := checkGenerated(navigation, catalog, search)
	if err == nil || !strings.Contains(err.Error(), "catalog.json is out of date") {
		t.Fatalf("check error = %v, want outdated catalog error", err)
	}

	if err := generate(navigation, catalog, search); err != nil {
		t.Fatalf("generate current documentation artifacts: %v", err)
	}
	if err := checkGenerated(navigation, catalog, search); err != nil {
		t.Fatalf("check current documentation artifacts: %v", err)
	}
}

func writeFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func decodeFixture(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(contents, value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}
