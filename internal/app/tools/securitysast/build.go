package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

func moduleDirectories(root string) ([]string, error) {
	coverage, err := securitypolicy.LoadValidatedCoverage(root)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var modules []string
	for _, surface := range coverage.Surfaces {
		if surface.Kind != "go-module" {
			continue
		}
		manifest := filepath.Join(root, filepath.FromSlash(surface.Path))
		realManifest, err := filepath.EvalSymlinks(manifest)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", surface.Path, err)
		}
		rel, err := filepath.Rel(realRoot, realManifest)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("module %s escapes repository", surface.Path)
		}
		modules = append(modules, filepath.Dir(manifest))
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("SAST coverage contains no Go modules")
	}
	sort.Strings(modules)
	return modules, nil
}
