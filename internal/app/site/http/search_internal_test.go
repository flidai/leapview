package http

import (
	"context"
	"testing"
)

func TestEmbeddedDocumentationSearchCoversCatalog(t *testing.T) {
	count, err := documentationSearchIndex.Count(context.Background())
	if err != nil {
		t.Fatalf("count embedded documentation search index: %v", err)
	}
	if count != len(siteDocuments) {
		t.Fatalf("indexed documents = %d, want %d catalog documents", count, len(siteDocuments))
	}
}

func TestDocumentationSearchSupportsSafePrefixQueries(t *testing.T) {
	results := searchSiteDocuments(`semantic relat`)
	if !containsDocument(results, "concepts/semantic-models") {
		t.Fatal("prefix search does not include semantic models")
	}

	// Search input is data, not raw search syntax.
	results = searchSiteDocuments(`semantic "relationships" *`)
	if !containsDocument(results, "concepts/semantic-models") {
		t.Fatal("syntax-like search does not include semantic models")
	}
}

func containsDocument(documents []siteDocument, slug string) bool {
	for _, document := range documents {
		if document.slug == slug {
			return true
		}
	}
	return false
}
