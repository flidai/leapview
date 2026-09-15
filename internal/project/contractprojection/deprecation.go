package contractprojection

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Contextual deprecation rules apply while creating a new sealed projection.
// Publication decoders deliberately retain the original leapview.contract/v1
// structural semantics so immutable bytes accepted by an older binary remain
// digestible and replayable under that same profile.
func validateSourceFieldDeprecations(contractVersion string, fields *map[string]Field) error {
	if fields == nil {
		return nil
	}
	deprecations := make(map[string]*Deprecation, len(*fields))
	for name, field := range *fields {
		deprecations[name] = field.Deprecation
	}
	return validateFieldDeprecationContext(contractVersion, deprecations)
}

func validateModelFieldDeprecations(contractVersion string, fields map[string]ModelField) error {
	deprecations := make(map[string]*Deprecation, len(fields))
	for name, field := range fields {
		deprecations[name] = field.Deprecation
	}
	return validateFieldDeprecationContext(contractVersion, deprecations)
}

func validateFieldDeprecationContext(contractVersion string, fields map[string]*Deprecation) error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		deprecation := fields[name]
		if deprecation == nil {
			continue
		}
		if semver.Compare("v"+deprecation.Since, "v"+contractVersion) > 0 {
			return fmt.Errorf("field %q deprecation since %q is later than contract version %q", name, deprecation.Since, contractVersion)
		}
		if deprecation.Replacement == nil {
			continue
		}
		replacement := *deprecation.Replacement
		if replacement == name {
			return fmt.Errorf("field %q cannot replace itself", name)
		}
		if _, exists := fields[replacement]; !exists {
			return fmt.Errorf("field %q replacement %q does not exist", name, replacement)
		}
	}

	complete := make(map[string]bool, len(fields))
	for _, start := range names {
		if complete[start] {
			continue
		}
		path := make([]string, 0)
		pathIndex := make(map[string]int)
		current := start
		for {
			if complete[current] {
				break
			}
			if index, seen := pathIndex[current]; seen {
				cycle := append(append([]string(nil), path[index:]...), current)
				return fmt.Errorf("field deprecation replacement cycle: %s", strings.Join(cycle, " -> "))
			}
			pathIndex[current] = len(path)
			path = append(path, current)
			deprecation := fields[current]
			if deprecation == nil || deprecation.Replacement == nil {
				break
			}
			current = *deprecation.Replacement
		}
		for _, name := range path {
			complete[name] = true
		}
	}
	return nil
}
