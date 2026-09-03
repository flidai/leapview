// Package search builds and queries the documentation site's immutable search index.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/flidai/leapview/internal/agent/productdocs"
	"golang.org/x/text/unicode/norm"
)

const Filename = "search-index.json"

// Document is the build-time representation stored in the search index.
type Document struct {
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Section   string `json:"section"`
	Category  string `json:"category"`
	Body      string `json:"body"`
	Generated bool   `json:"generated"`
}

// Build writes a complete immutable search index to path.
func Build(path string, documents []Document) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create search index directory: %w", err)
	}
	contents, err := json.Marshal(documents)
	if err != nil {
		return fmt.Errorf("encode search index: %w", err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("write search index: %w", err)
	}
	return nil
}

// Index is an immutable in-memory documentation search index.
type Index struct {
	documents []Document
}

// Open reads an immutable search index from fsys.
func Open(fsys fs.FS, filename string) (*Index, error) {
	contents, err := fs.ReadFile(fsys, filename)
	if err != nil {
		return nil, fmt.Errorf("read embedded search index: %w", err)
	}
	var documents []Document
	if err := json.Unmarshal(contents, &documents); err != nil {
		return nil, fmt.Errorf("decode embedded search index: %w", err)
	}
	seen := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		if strings.TrimSpace(document.Slug) == "" {
			return nil, fmt.Errorf("search index contains empty slug")
		}
		if _, ok := seen[document.Slug]; ok {
			return nil, fmt.Errorf("search index contains duplicate slug %q", document.Slug)
		}
		seen[document.Slug] = struct{}{}
	}
	return &Index{documents: append([]Document(nil), documents...)}, nil
}

// Close releases the in-memory index.
func (index *Index) Close() error {
	if index != nil {
		index.documents = nil
	}
	return nil
}

// Count returns the number of indexed documents.
func (index *Index) Count(ctx context.Context) (int, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if index == nil {
		return 0, fmt.Errorf("search index is nil")
	}
	return len(index.documents), nil
}

// Slugs returns every indexed slug in source order.
func (index *Index) Slugs(ctx context.Context) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if index == nil {
		return nil, fmt.Errorf("search index is nil")
	}
	slugs := make([]string, 0, len(index.documents))
	for _, document := range index.documents {
		slugs = append(slugs, document.Slug)
	}
	return slugs, nil
}

// Search returns ranked matches. User input is compiled into safe prefix and
// phrase terms rather than being interpreted as search syntax.
func (index *Index) Search(ctx context.Context, query string, limit int) ([]productdocs.SearchMatch, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if index == nil || limit <= 0 {
		return nil, nil
	}
	segments := parseQuery(query)
	if len(segments) == 0 {
		return nil, nil
	}
	type ranked struct {
		match      productdocs.SearchMatch
		exactTitle bool
		generated  bool
		score      int
		titleLower string
	}
	rankedMatches := make([]ranked, 0, len(index.documents))
	for _, document := range index.documents {
		score, matched := scoreDocument(document, segments)
		if !matched {
			continue
		}
		rankedMatches = append(rankedMatches, ranked{
			match:      productdocs.SearchMatch{Slug: document.Slug, Excerpt: excerpt(document.Body, segments)},
			exactTitle: strings.EqualFold(strings.TrimSpace(document.Title), strings.TrimSpace(query)),
			generated:  document.Generated,
			score:      score,
			titleLower: strings.ToLower(document.Title),
		})
	}
	sort.SliceStable(rankedMatches, func(i, j int) bool {
		left, right := rankedMatches[i], rankedMatches[j]
		if left.exactTitle != right.exactTitle {
			return left.exactTitle
		}
		if left.generated != right.generated {
			return !left.generated
		}
		if left.score != right.score {
			return left.score > right.score
		}
		if left.titleLower != right.titleLower {
			return left.titleLower < right.titleLower
		}
		return left.match.Slug < right.match.Slug
	})
	if limit > len(rankedMatches) {
		limit = len(rankedMatches)
	}
	results := make([]productdocs.SearchMatch, 0, limit)
	for _, result := range rankedMatches[:limit] {
		results = append(results, result.match)
	}
	return results, nil
}

type querySegment struct {
	terms  []string
	phrase bool
}

func parseQuery(query string) []querySegment {
	query = strings.ToLower(strings.TrimSpace(query))
	segments := make([]querySegment, 0)
	var current strings.Builder
	phrase := false
	flush := func() {
		terms := searchTerms(current.String())
		if len(terms) > 0 {
			segments = append(segments, querySegment{terms: terms, phrase: phrase})
		}
		current.Reset()
	}
	for _, character := range query {
		if character == '"' {
			flush()
			phrase = !phrase
			continue
		}
		current.WriteRune(character)
	}
	flush()
	return segments
}

func scoreDocument(document Document, segments []querySegment) (int, bool) {
	fields := []struct {
		text   string
		weight int
	}{
		{document.Title, 120}, {document.Summary, 50}, {document.Section, 10},
		{document.Category, 10}, {document.Body, 1},
	}
	score := 0
	for _, segment := range segments {
		if segment.phrase && len(segment.terms) > 1 {
			matched := false
			for _, field := range fields {
				if phraseMatch(searchTerms(field.text), segment.terms) {
					score += field.weight * len(segment.terms) * 2
					matched = true
				}
			}
			if !matched {
				return 0, false
			}
			continue
		}
		for _, term := range segment.terms {
			termMatched := false
			for _, field := range fields {
				tokens := searchTerms(field.text)
				for _, token := range tokens {
					if strings.HasPrefix(token, term) {
						score += field.weight
						termMatched = true
						break
					}
				}
				if termMatched {
					break
				}
			}
			if !termMatched {
				return 0, false
			}
		}
	}
	return score, true
}

func phraseMatch(tokens, phrase []string) bool {
	if len(phrase) == 0 || len(phrase) > len(tokens) {
		return false
	}
	for start := 0; start+len(phrase) <= len(tokens); start++ {
		matched := true
		for offset, term := range phrase {
			if tokens[start+offset] != term {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func excerpt(body string, segments []querySegment) string {
	words := strings.Fields(body)
	if len(words) == 0 {
		return ""
	}
	const limit = 24
	if len(words) <= limit {
		return strings.Join(words, " ")
	}
	start := 0
	for index, word := range words {
		tokens := searchTerms(word)
		found := false
		for _, segment := range segments {
			for _, term := range segment.terms {
				for _, token := range tokens {
					if strings.HasPrefix(token, term) {
						start = index - limit/2
						if start < 0 {
							start = 0
						}
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}
	if start > len(words)-limit {
		start = len(words) - limit
	}
	end := start + limit
	result := strings.Join(words[start:end], " ")
	if start > 0 {
		result = "… " + result
	}
	if end < len(words) {
		result += " …"
	}
	return result
}

func searchTerms(value string) []string {
	value = norm.NFD.String(strings.ToLower(value))
	var normalized strings.Builder
	for _, character := range value {
		if unicode.Is(unicode.Mn, character) {
			continue
		}
		normalized.WriteRune(character)
	}
	return strings.FieldsFunc(normalized.String(), func(character rune) bool {
		return character != '_' && !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
