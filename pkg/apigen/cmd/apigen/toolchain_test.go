package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var testTypeSpecToolchain struct {
	root          string
	npmCacheOnce  sync.Once
	npmCache      string
	npmCacheErr   error
	toolchainOnce sync.Once
	cacheRoot     string
	pkg           typeSpecPackage
	err           error
}

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "apigen-test-toolchain-*")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create APIGen test toolchain root: %v\n", err)
		os.Exit(1)
	}

	testTypeSpecToolchain.root = root

	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func setupManagedTypeSpecEnvironment(t *testing.T) {
	t.Helper()

	// These tests intentionally remain serial because t.Setenv mutates process state.
	home := t.TempDir()
	t.Setenv(typeSpecPackageDirEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	testTypeSpecToolchain.npmCacheOnce.Do(func() {
		testTypeSpecToolchain.npmCache, testTypeSpecToolchain.npmCacheErr = os.MkdirTemp(testTypeSpecToolchain.root, "npm-cache-*")
	})
	require.NoError(t, testTypeSpecToolchain.npmCacheErr)
	t.Setenv("NPM_CONFIG_CACHE", testTypeSpecToolchain.npmCache)
	t.Setenv("npm_config_cache", testTypeSpecToolchain.npmCache)
}

func setupManagedTypeSpecCache(t *testing.T) {
	t.Helper()
	setupManagedTypeSpecEnvironment(t)

	testTypeSpecToolchain.toolchainOnce.Do(func() {
		testTypeSpecToolchain.cacheRoot, testTypeSpecToolchain.err = os.MkdirTemp(testTypeSpecToolchain.root, "typespec-cache-*")
		if testTypeSpecToolchain.err != nil {
			return
		}
		testTypeSpecToolchain.pkg, testTypeSpecToolchain.err = installBundledTypeSpecPackage(testTypeSpecToolchain.cacheRoot)
	})
	require.NoError(t, testTypeSpecToolchain.err)
	t.Setenv("XDG_CACHE_HOME", testTypeSpecToolchain.cacheRoot)
}

func TestEnsureTypeSpecToolchain_ColdPathUsesSharedNPMCache(t *testing.T) {
	setupManagedTypeSpecEnvironment(t)

	cacheRoot := t.TempDir()
	packageA, err := installBundledTypeSpecPackage(filepath.Join(cacheRoot, "a"))
	require.NoError(t, err)
	packageB, err := installBundledTypeSpecPackage(filepath.Join(cacheRoot, "b"))
	require.NoError(t, err)
	require.NotEqual(t, packageA.Dir, packageB.Dir)

	npmLog := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("APIGEN_TEST_NPM_LOG", npmLog)
	fakeNPM := filepath.Join(t.TempDir(), "npm")
	fakeScript := []byte(`#!/bin/sh
printf '%s|%s|%s\n' "$NPM_CONFIG_CACHE" "$npm_config_cache" "$PWD" >> "$APIGEN_TEST_NPM_LOG"
mkdir -p "$PWD/node_modules/@typespec/compiler/cmd"
printf '%s\n' "$PWD" > "$PWD/node_modules/@typespec/compiler/cmd/tsp.js"
`)
	require.NoError(t, os.WriteFile(fakeNPM, fakeScript, 0o700))
	t.Setenv("PATH", filepath.Dir(fakeNPM)+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, ensureTypeSpecToolchain(packageA))
	require.NoError(t, ensureTypeSpecToolchain(packageB))

	entries := strings.Split(strings.TrimSpace(mustReadString(t, npmLog)), "\n")
	require.Len(t, entries, 2)
	for _, entry := range entries {
		parts := strings.SplitN(entry, "|", 3)
		require.Len(t, parts, 3)
		require.Equal(t, testTypeSpecToolchain.npmCache, parts[0])
		require.Equal(t, testTypeSpecToolchain.npmCache, parts[1])
		require.Contains(t, []string{packageA.Dir, packageB.Dir}, parts[2])
	}
	require.FileExists(t, filepath.Join(packageA.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js"))
	require.FileExists(t, filepath.Join(packageB.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js"))
	require.NotEqual(t,
		mustReadString(t, filepath.Join(packageA.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js")),
		mustReadString(t, filepath.Join(packageB.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js")),
	)
}

func TestCompileTypeSpec_ReusesManagedToolchainAcrossIsolatedProjects(t *testing.T) {
	setupManagedTypeSpecCache(t)
	packageJSONPath := filepath.Join(testTypeSpecToolchain.pkg.Dir, "package.json")
	markerPath := filepath.Join(testTypeSpecToolchain.pkg.Dir, ".apigen-bundle-sha256")
	packageJSON := mustReadString(t, packageJSONPath)
	marker := mustReadString(t, markerPath)

	projects := []struct {
		prefix  string
		title   string
		version string
	}{
		{prefix: "/first", title: "First API", version: "1.0.0"},
		{prefix: "/second", title: "Second API", version: "2.0.0"},
	}

	roots := make([]string, len(projects))
	sources := make([]string, len(projects))
	irPaths := make([]string, len(projects))
	openAPIPaths := make([]string, len(projects))
	for index, project := range projects {
		roots[index] = t.TempDir()
		typeSpecDir := filepath.Join(roots[index], "typespec")
		writeMinimalTypeSpecContract(t, typeSpecDir, project.prefix, project.title, project.version)

		sourcePath := filepath.Join(typeSpecDir, "main.tsp")
		sources[index] = mustReadString(t, sourcePath)
		irPaths[index] = filepath.Join(roots[index], "json-ir.json")
		openAPIPaths[index] = filepath.Join(roots[index], "openapi.yaml")
		require.NoError(t, compileTypeSpec(typeSpecDir, irPaths[index], openAPIPaths[index]))
	}

	toolchain, err := resolveTypeSpecPackage()
	require.NoError(t, err)
	require.True(t, toolchain.Managed)
	require.Equal(t, testTypeSpecToolchain.pkg.Dir, toolchain.Dir)
	require.FileExists(t, filepath.Join(toolchain.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js"))

	for index, project := range projects {
		sourcePath := filepath.Join(roots[index], "typespec", "main.tsp")
		require.Equal(t, sources[index], mustReadString(t, sourcePath))
		require.Equal(t, packageJSON, mustReadString(t, packageJSONPath))
		require.Equal(t, marker, mustReadString(t, markerPath))

		doc, err := loadDocument(irPaths[index])
		require.NoError(t, err)
		require.Equal(t, project.title, doc.Info.Title)
		openAPI := mustReadString(t, openAPIPaths[index])
		require.Contains(t, openAPI, project.prefix+"/widgets:")
		require.NotContains(t, openAPI, projects[1-index].prefix+"/widgets:")
	}
	require.NotEqual(t, sources[0], sources[1])
	require.NotEqual(t, mustReadString(t, irPaths[0]), mustReadString(t, irPaths[1]))
	require.NotEqual(t, mustReadString(t, openAPIPaths[0]), mustReadString(t, openAPIPaths[1]))
}
