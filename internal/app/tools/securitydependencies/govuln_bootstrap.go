package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func (r *runner) commandWithEnv(dir, name string, overrides map[string]string, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	if len(overrides) > 0 {
		environment := os.Environ()
		for key, value := range overrides {
			prefix := key + "="
			found := false
			for index, entry := range environment {
				if strings.HasPrefix(entry, prefix) {
					environment[index] = prefix + value
					found = true
					break
				}
			}
			if !found {
				environment = append(environment, prefix+value)
			}
		}
		command.Env = environment
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	status := 0
	var timedOut, canceled, signaled bool
	if err != nil {
		status = 1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			status = exitError.ExitCode()
			if status < 0 {
				signaled = true
				status = 1
			}
		}
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (command timed out after %s)", ctx.Err(), r.timeout)
			timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
			canceled = errors.Is(ctx.Err(), context.Canceled)
		}
	}
	return commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: status, err: err, timedOut: timedOut, canceled: canceled, signaled: signaled}
}

func (r *runner) prepareGovulncheck() (string, func(), error) {
	gobin, err := os.MkdirTemp("", "leapview-govulncheck-")
	if err != nil {
		return "", func() {}, fmt.Errorf("govulncheck bootstrap: create private binary directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(gobin) }
	target := "golang.org/x/vuln/cmd/govulncheck@" + govulncheckVersion
	install := r.runGoInstallCommand(r.root, gobin, "install", target)
	if err := r.validateGovulncheckBootstrapResult("install", install); err != nil {
		cleanup()
		return "", func() {}, err
	}
	binaryName := "govulncheck"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(gobin, binaryName)
	version := r.runGovulnCommand(r.root, binary, "-version")
	if err := r.validateGovulncheckBootstrapResult("version", version); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if !hasExactGovulncheckIdentity(version.stdout) {
		r.emitFailure(version)
		cleanup()
		return "", func() {}, fmt.Errorf("govulncheck bootstrap version identity is not exactly %q", "Scanner: govulncheck@"+govulncheckVersion)
	}
	return binary, cleanup, nil
}

func hasExactGovulncheckIdentity(data []byte) bool {
	want := "Scanner: govulncheck@" + govulncheckVersion
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "Go: ") || strings.TrimSpace(strings.TrimPrefix(lines[0], "Go: ")) == "" {
		return false
	}
	if lines[1] != want {
		return false
	}
	if !strings.HasPrefix(lines[2], "DB: ") || strings.TrimSpace(strings.TrimPrefix(lines[2], "DB: ")) == "" {
		return false
	}
	return strings.HasPrefix(lines[3], "DB updated: ") && strings.TrimSpace(strings.TrimPrefix(lines[3], "DB updated: ")) != ""
}

func (r *runner) validateGovulncheckBootstrapResult(phase string, result commandResult) error {
	if isCommandLifecycleError(result) {
		r.emitFailure(result)
		return commandError("govulncheck bootstrap "+phase, r.root, result)
	}
	if result.status != 0 {
		r.emitFailure(result)
		return statusError("govulncheck bootstrap "+phase, r.root, result.status)
	}
	if result.err != nil {
		r.emitFailure(result)
		return commandError("govulncheck bootstrap "+phase, r.root, result)
	}
	if phase == "install" && len(result.stdout) != 0 {
		r.emitFailure(result)
		return fmt.Errorf("govulncheck bootstrap install emitted unexpected stdout")
	}
	if phase == "version" && len(result.stderr) != 0 {
		r.emitFailure(result)
		return fmt.Errorf("govulncheck bootstrap version emitted diagnostics on stderr")
	}
	if phase == "version" {
		return nil
	}
	if !validGoDownloadDiagnostics(result.stderr) {
		r.emitFailure(result)
		return fmt.Errorf("govulncheck bootstrap %s emitted unknown diagnostics on stderr", phase)
	}
	return nil
}

var (
	goDownloadModulePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*(?:/[A-Za-z0-9][A-Za-z0-9._~-]*)+$`)
	goDownloadVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

func validGoDownloadDiagnostics(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, " ")
		if len(fields) != 4 || fields[0] != "go:" || fields[1] != "downloading" || fields[2] == "" || fields[3] == "" {
			return false
		}
		if !goDownloadModulePattern.MatchString(fields[2]) || !goDownloadVersionPattern.MatchString(fields[3]) {
			return false
		}
	}
	return true
}

func (r *runner) runGoInstallCommand(dir, gobin string, args ...string) commandResult {
	if r.goInstallCommand != nil {
		return r.goInstallCommand(dir, gobin, args...)
	}
	// Keep existing command fakes useful for focused tests that do not need to
	// inspect the environment passed to the real go process.
	if r.goCommand != nil {
		return r.goCommand(dir, args...)
	}
	return r.commandWithEnv(dir, "go", map[string]string{"GOBIN": gobin}, args...)
}

func (r *runner) runGovulnCommand(dir, binary string, args ...string) commandResult {
	if r.govulnCommand != nil {
		return r.govulnCommand(dir, binary, args...)
	}
	// Existing scanGo tests inject the go command. Preserve that seam while
	// making the production path execute the provisioned absolute binary.
	if r.goCommand != nil {
		return r.goCommand(dir, args...)
	}
	return r.command(dir, binary, args...)
}
