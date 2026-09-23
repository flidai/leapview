package testshard

import (
	"fmt"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// ParseList extracts runnable tests, examples, and fuzz seeds from Go test -list output.
func ParseList(output string) []string {
	var tests []string
	for line := range strings.Lines(output) {
		name := strings.TrimSpace(line)
		if (strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz")) && token.IsIdentifier(name) {
			tests = append(tests, name)
		}
	}
	return tests
}

// Select assigns sorted test names to stable, count-balanced shards.
func Select(tests []string, index, total int) ([]string, error) {
	if total < 1 {
		return nil, fmt.Errorf("shard count must be positive")
	}
	if index < 0 || index >= total {
		return nil, fmt.Errorf("shard index %d must be between 0 and %d", index, total-1)
	}

	sorted := append([]string(nil), tests...)
	sort.Strings(sorted)
	selected := make([]string, 0, (len(sorted)+total-1)/total)
	for position, name := range sorted {
		if position%total == index {
			selected = append(selected, name)
		}
	}
	return selected, nil
}

// Pattern returns an exact go test -run expression for the selected tests.
func Pattern(tests []string) (string, error) {
	if len(tests) == 0 {
		return "", fmt.Errorf("test shard is empty")
	}
	escaped := make([]string, len(tests))
	for index, name := range tests {
		escaped[index] = regexp.QuoteMeta(name)
	}
	return "^(?:" + strings.Join(escaped, "|") + ")$", nil
}
