package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchRanksExactTitlesThenAuthoredGuidance(t *testing.T) {
	documents := []Document{
		{Slug: "generated-exact", Title: "Access policy", Generated: true},
		{Slug: "authored-title", Title: "Access policy guide"},
		{Slug: "authored-body", Title: "Authorization guide", Body: "Choose a workspace access policy."},
		{Slug: "generated-title", Title: "Workspace access policy configuration", Generated: true},
		{Slug: "generated-body", Title: "Grant configuration", Body: "Exact workspace access policy fields.", Generated: true},
	}
	index := buildTestIndex(t, documents)

	results, err := index.Search(context.Background(), "access policy", 10)
	if err != nil {
		t.Fatalf("search documentation: %v", err)
	}
	want := []string{"generated-exact", "authored-title", "authored-body", "generated-title", "generated-body"}
	if len(results) != len(want) {
		t.Fatalf("search results = %d, want %d", len(results), len(want))
	}
	for position, slug := range want {
		if results[position].Slug != slug {
			t.Errorf("search result %d = %q, want %q", position, results[position].Slug, slug)
		}
	}
}

func TestSearchCompilesUserInputToSafePrefixTerms(t *testing.T) {
	index := buildTestIndex(t, []Document{{Slug: "semantic-models", Title: "Semantic models", Body: "Define relationships between datasets."}})

	results, err := index.Search(context.Background(), `semantic relat " *`, 10)
	if err != nil {
		t.Fatalf("search syntax-like input: %v", err)
	}
	if len(results) != 1 || results[0].Slug != "semantic-models" {
		t.Fatalf("prefix results = %#v, want semantic models", results)
	}
}

func TestSearchRequiresEveryUnquotedTerm(t *testing.T) {
	index := buildTestIndex(t, []Document{{Slug: "access-only", Title: "Access guide"}})

	results, err := index.Search(context.Background(), "access policy", 10)
	if err != nil {
		t.Fatalf("search documentation: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("partial-term results = %#v, want no matches", results)
	}
}

func TestSearchPreservesQuotedPhrases(t *testing.T) {
	index := buildTestIndex(t, []Document{
		{Slug: "exact", Title: "Semantic relationships"},
		{Slug: "separate", Title: "Semantic analytical relationships"},
	})

	results, err := index.Search(context.Background(), `"semantic relationships"`, 10)
	if err != nil {
		t.Fatalf("search quoted phrase: %v", err)
	}
	if len(results) != 1 || results[0].Slug != "exact" {
		t.Fatalf("phrase results = %#v, want exact phrase only", results)
	}
}

func TestSearchNormalizesDiacriticsAndCentersExcerptOnMatch(t *testing.T) {
	words := make([]string, 0, 40)
	for index := 0; index < 30; index++ {
		words = append(words, "background")
	}
	words = append(words, "café", "guide")
	for index := 0; index < 8; index++ {
		words = append(words, "details")
	}
	index := buildTestIndex(t, []Document{{Slug: "cafe", Title: "Café", Body: strings.Join(words, " ")}})

	results, err := index.Search(context.Background(), "cafe", 1)
	if err != nil {
		t.Fatalf("search diacritic: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Excerpt, "café") || !strings.HasPrefix(results[0].Excerpt, "… ") {
		t.Fatalf("diacritic search excerpt = %#v, want centered match", results)
	}
}

func buildTestIndex(t *testing.T, documents []Document) *Index {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, Filename)
	if err := Build(path, documents); err != nil {
		t.Fatalf("build documentation search index: %v", err)
	}
	index, err := Open(os.DirFS(directory), Filename)
	if err != nil {
		t.Fatalf("open documentation search index: %v", err)
	}
	t.Cleanup(func() {
		if err := index.Close(); err != nil {
			t.Errorf("close documentation search index: %v", err)
		}
	})
	return index
}
