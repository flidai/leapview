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

var managedTypeSpecFixture managedTypeSpecTestFixture

func TestMain(m *testing.M) {
	code := m.Run()
	managedTypeSpecFixture.cleanup()
	os.Exit(code)
}

type managedTypeSpecTestFixture struct {
	npmCacheOnce sync.Once
	npmCachePath string
	npmCacheErr  error
	toolchain    managedTypeSpecToolchainFixture
}

type managedTypeSpecToolchainFixture struct {
	once            sync.Once
	root            string
	packageDir      string
	dependenciesDir string
	err             error
}

func (f *managedTypeSpecTestFixture) cleanup() {
	f.toolchain.cleanup()
	if f.npmCachePath != "" {
		_ = os.RemoveAll(f.npmCachePath)
	}
}

func (f *managedTypeSpecToolchainFixture) cleanup() {
	if f.root != "" {
		_ = os.RemoveAll(f.root)
	}
}

func (f *managedTypeSpecToolchainFixture) prepare() (string, error) {
	f.once.Do(func() {
		f.root, f.err = os.MkdirTemp("", "apigen-test-typespec-toolchain-")
		if f.err != nil {
			return
		}
		pkg, err := installBundledTypeSpecPackage(f.root)
		if err != nil {
			f.err = err
			return
		}
		f.packageDir = pkg.Dir
		if f.err = ensureTypeSpecToolchain(pkg); f.err != nil {
			return
		}
		f.dependenciesDir = filepath.Join(pkg.Dir, "node_modules")
	})
	return f.dependenciesDir, f.err
}

func seedManagedTypeSpecDependencies(pkg typeSpecPackage, dependenciesDir string) error {
	target := filepath.Join(pkg.Dir, "node_modules")
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("managed typespec package already has node_modules: %s", target)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat managed typespec node_modules: %w", err)
	}
	if err := os.Symlink(dependenciesDir, target); err != nil {
		return fmt.Errorf("link managed typespec dependencies: %w", err)
	}
	return nil
}

func TestCompileTypeSpec_ReusesInstalledToolchainAcrossIsolatedProjects(t *testing.T) {
	t.Helper()
	setupManagedTypeSpecCache(t)

	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	firstTypeSpecDir := filepath.Join(firstRoot, "typespec")
	secondTypeSpecDir := filepath.Join(secondRoot, "typespec")
	firstSource := `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "First Isolated API" })
@info(#{ version: "1.0.0" })
namespace FirstIsolatedAPI;

@route("/first")
@get
op first(): string;
`
	secondSource := `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Second Isolated API" })
@info(#{ version: "2.0.0" })
namespace SecondIsolatedAPI;

@route("/second")
@get
op second(): string;
`
	require.NoError(t, os.MkdirAll(firstTypeSpecDir, 0o755))
	require.NoError(t, os.MkdirAll(secondTypeSpecDir, 0o755))
	firstSourcePath := filepath.Join(firstTypeSpecDir, "main.tsp")
	secondSourcePath := filepath.Join(secondTypeSpecDir, "main.tsp")
	require.NoError(t, os.WriteFile(firstSourcePath, []byte(firstSource), 0o644))
	require.NoError(t, os.WriteFile(secondSourcePath, []byte(secondSource), 0o644))

	firstIRPath := filepath.Join(firstRoot, "json-ir.json")
	secondIRPath := filepath.Join(secondRoot, "json-ir.json")
	firstOpenAPIPath := filepath.Join(firstRoot, "openapi.yaml")
	secondOpenAPIPath := filepath.Join(secondRoot, "openapi.yaml")
	pkgBefore, err := resolveTypeSpecPackage()
	require.NoError(t, err)
	require.True(t, pkgBefore.Managed)
	require.NotEqual(t, managedTypeSpecFixture.toolchain.packageDir, pkgBefore.Dir)
	require.True(t, strings.HasPrefix(pkgBefore.Dir, filepath.Join(os.Getenv("XDG_CACHE_HOME"), "apigen", "typespec")+string(os.PathSeparator)))
	testNodeModules, err := filepath.EvalSymlinks(filepath.Join(pkgBefore.Dir, "node_modules"))
	require.NoError(t, err)
	preparedNodeModules, err := filepath.EvalSymlinks(managedTypeSpecFixture.toolchain.dependenciesDir)
	require.NoError(t, err)
	require.Equal(t, preparedNodeModules, testNodeModules)
	packageJSONBefore := mustReadString(t, filepath.Join(pkgBefore.Dir, "package.json"))

	require.NoError(t, compileTypeSpec(firstTypeSpecDir, firstIRPath, firstOpenAPIPath))
	require.NoError(t, compileTypeSpec(secondTypeSpecDir, secondIRPath, secondOpenAPIPath))

	pkgAfter, err := resolveTypeSpecPackage()
	require.NoError(t, err)
	require.Equal(t, pkgBefore.Dir, pkgAfter.Dir)
	require.Equal(t, packageJSONBefore, mustReadString(t, filepath.Join(pkgAfter.Dir, "package.json")))
	require.Equal(t, strings.TrimSpace(firstSource), mustReadString(t, firstSourcePath))
	require.Equal(t, strings.TrimSpace(secondSource), mustReadString(t, secondSourcePath))

	firstDoc, err := loadDocument(firstIRPath)
	require.NoError(t, err)
	secondDoc, err := loadDocument(secondIRPath)
	require.NoError(t, err)
	require.Equal(t, "First Isolated API", firstDoc.Info.Title)
	require.Equal(t, "Second Isolated API", secondDoc.Info.Title)
	require.NotEqual(t, mustReadString(t, firstIRPath), mustReadString(t, secondIRPath))
	require.FileExists(t, firstOpenAPIPath)
	require.FileExists(t, secondOpenAPIPath)
}

func TestEnsureTypeSpecToolchain_UsesSharedNPMCacheAcrossManagedPackages(t *testing.T) {
	t.Helper()
	setupManagedTypeSpecEnvironment(t)

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "npm-cache.log")
	npmPath := filepath.Join(binDir, "npm")
	script := `#!/bin/sh
printf '%s|%s\n' "$NPM_CONFIG_CACHE" "$npm_config_cache" >> "$APIGEN_TEST_NPM_CACHE_LOG"
mkdir -p node_modules/@typespec/compiler/cmd
touch node_modules/@typespec/compiler/cmd/tsp.js
`
	require.NoError(t, os.WriteFile(npmPath, []byte(script), 0o700))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APIGEN_TEST_NPM_CACHE_LOG", logPath)

	firstPkg, err := installBundledTypeSpecPackage(t.TempDir())
	require.NoError(t, err)
	secondPkg, err := installBundledTypeSpecPackage(t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, firstPkg.Dir, secondPkg.Dir)
	require.True(t, firstPkg.Managed)
	require.True(t, secondPkg.Managed)

	require.NoError(t, ensureTypeSpecToolchain(firstPkg))
	require.NoError(t, ensureTypeSpecToolchain(secondPkg))

	cacheLog, err := os.ReadFile(logPath)
	require.NoError(t, err)
	cachePaths := strings.Split(strings.TrimSpace(string(cacheLog)), "\n")
	require.Len(t, cachePaths, 2)
	require.Equal(t, cachePaths[0], cachePaths[1])
	cacheValues := strings.Split(cachePaths[0], "|")
	require.Equal(t, []string{os.Getenv("NPM_CONFIG_CACHE"), os.Getenv("npm_config_cache")}, cacheValues)
	require.NotEqual(t, firstPkg.Dir, cacheValues[0])
	require.NotEqual(t, secondPkg.Dir, cacheValues[0])
	require.FileExists(t, filepath.Join(firstPkg.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js"))
	require.FileExists(t, filepath.Join(secondPkg.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js"))
	firstNodeModules, err := filepath.EvalSymlinks(filepath.Join(firstPkg.Dir, "node_modules"))
	require.NoError(t, err)
	secondNodeModules, err := filepath.EvalSymlinks(filepath.Join(secondPkg.Dir, "node_modules"))
	require.NoError(t, err)
	require.NotEqual(t, firstNodeModules, secondNodeModules)
}

func TestPrepareTypeSpecToolchain_ConcurrentFirstPreparationUsesOneNPMInstall(t *testing.T) {
	t.Helper()
	setupManagedTypeSpecEnvironment(t)

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "npm-cache.log")
	npmPath := filepath.Join(binDir, "npm")
	script := `#!/bin/sh
printf '%s\n' "$NPM_CONFIG_CACHE" >> "$APIGEN_TEST_NPM_CACHE_LOG"
mkdir -p node_modules/@typespec/compiler/cmd
touch node_modules/@typespec/compiler/cmd/tsp.js
`
	require.NoError(t, os.WriteFile(npmPath, []byte(script), 0o700))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APIGEN_TEST_NPM_CACHE_LOG", logPath)

	var fixture managedTypeSpecToolchainFixture
	t.Cleanup(fixture.cleanup)
	const callerCount = 8
	results := make(chan struct {
		path string
		err  error
	}, callerCount)
	var wg sync.WaitGroup
	for range callerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := fixture.prepare()
			results <- struct {
				path string
				err  error
			}{path: path, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var preparedPath string
	for result := range results {
		require.NoError(t, result.err)
		if preparedPath == "" {
			preparedPath = result.path
		}
		require.Equal(t, preparedPath, result.path)
	}
	require.Equal(t, filepath.Join(fixture.packageDir, "node_modules"), preparedPath)
	require.FileExists(t, filepath.Join(preparedPath, "@typespec", "compiler", "cmd", "tsp.js"))

	cacheLog, err := os.ReadFile(logPath)
	require.NoError(t, err)
	cachePaths := strings.Split(strings.TrimSpace(string(cacheLog)), "\n")
	require.Len(t, cachePaths, 1)
	require.Equal(t, os.Getenv("NPM_CONFIG_CACHE"), cachePaths[0])
}

func setupManagedTypeSpecCache(t *testing.T) {
	t.Helper()

	setupManagedTypeSpecEnvironment(t)
	dependenciesDir, err := managedTypeSpecFixture.toolchain.prepare()
	require.NoError(t, err)
	pkg, err := installBundledTypeSpecPackage(os.Getenv("XDG_CACHE_HOME"))
	require.NoError(t, err)
	require.NoError(t, seedManagedTypeSpecDependencies(pkg, dependenciesDir))
}

func setupManagedTypeSpecEnvironment(t *testing.T) {
	t.Helper()

	managedTypeSpecFixture.setupEnvironment(t)
}

func (f *managedTypeSpecTestFixture) setupEnvironment(t *testing.T) {
	t.Helper()

	// These tests intentionally remain serial because t.Setenv mutates process state.
	t.Setenv(typeSpecPackageDirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	f.npmCacheOnce.Do(func() {
		f.npmCachePath, f.npmCacheErr = os.MkdirTemp("", "apigen-test-npm-cache-")
	})
	require.NoError(t, f.npmCacheErr)
	t.Setenv("NPM_CONFIG_CACHE", f.npmCachePath)
	t.Setenv("npm_config_cache", f.npmCachePath)
}
