package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

func buildModules(ctx context.Context, root string, execute func(context.Context, string, ...string) error) error {
	coverage, err := securitypolicy.LoadValidatedCoverage(root)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	var modules []string
	for _, surface := range coverage.Surfaces {
		if surface.Kind != "go-module" {
			continue
		}
		manifest := filepath.Join(root, filepath.FromSlash(surface.Path))
		realManifest, err := filepath.EvalSymlinks(manifest)
		if err != nil {
			return fmt.Errorf("module %s: %w", surface.Path, err)
		}
		rel, err := filepath.Rel(realRoot, realManifest)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("module %s escapes repository", surface.Path)
		}
		modules = append(modules, filepath.Dir(manifest))
	}
	if len(modules) == 0 {
		return fmt.Errorf("SAST coverage contains no Go modules")
	}
	sort.Strings(modules)
	for _, dir := range modules {
		for _, args := range [][]string{
			{"list", "-mod=readonly", "-deps", "-tags=duckdb_arrow", "./..."},
			{"build", "-a", "-p=2", "-mod=readonly", "-tags=duckdb_arrow", "./..."},
		} {
			if err := execute(ctx, dir, args...); err != nil {
				return fmt.Errorf("Go %s in %s: %w", args[0], dir, err)
			}
		}
	}
	return nil
}
