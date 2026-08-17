package compiler

import (
	"sort"

	"github.com/flidai/leapview/internal/dashboard"
)

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func pageItemTitle(item dashboard.PageVisual) string {
	if item.Title != "" {
		return item.Title
	}
	if item.ID != "" {
		return item.ID
	}
	return item.Kind
}

func dimensionLabel(name, label string) string {
	if label != "" {
		return label
	}
	return name
}

func metricLabel(name, label string) string {
	if label != "" {
		return label
	}
	return name
}
