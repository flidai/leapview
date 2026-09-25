package authoring

import (
	"fmt"
	"strings"
)

func canonicalSlicerTargetPolicy(targets []string) (*[]string, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: slicer requires at least one compatible visual target", ErrInvalidPayload)
	}
	copy := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			return nil, fmt.Errorf("%w: slicer target cannot be empty", ErrInvalidPayload)
		}
		if _, ok := seen[target]; ok {
			return nil, fmt.Errorf("%w: duplicate slicer target %q", ErrInvalidPayload, target)
		}
		seen[target] = struct{}{}
		copy = append(copy, target)
	}
	return &copy, nil
}
