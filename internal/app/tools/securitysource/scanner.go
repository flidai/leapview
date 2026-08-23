package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

// Run executes both gates in the same order as the former shell entrypoint:
// current-tree and candidate-history secrets first, then source scanning.
func Run(parent context.Context, cfg Config) error {
	if parent == nil {
		parent = context.Background()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	root, err := repositoryRoot(parent, cfg)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	baseRef := cfg.BaseRef
	if baseRef == "" {
		baseRef = os.Getenv("SECURITY_GITLEAKS_BASE_REF")
		if baseRef == "" {
			baseRef = defaultBaseRef
		}
	}

	contract, err := loadExceptions(cfg, root)
	if err != nil {
		return err
	}
	if err := runGitleaks(parent, cfg, root, baseRef); err != nil {
		return err
	}
	if err := runTrivy(parent, cfg, root, contract); err != nil {
		return err
	}
	return nil
}

func repositoryRoot(parent context.Context, cfg Config) (string, error) {
	root := cfg.Root
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("path is not a directory")
		}
		return "", err
	}

	// Match git rev-parse --show-toplevel from the shell gate.  An explicit
	// root still goes through Git so a typo cannot accidentally scan elsewhere.
	out, diagnostics, err := runCapture(parent, cfg, abs, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		writeBytes(cfg.Stderr, diagnostics)
		return "", commandFailure("git repository discovery", err)
	}
	resolved := strings.TrimSpace(string(out))
	if resolved == "" {
		return "", errors.New("git returned an empty repository root")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func loadExceptions(cfg Config, root string) (*securitypolicy.Exceptions, error) {
	coverage := filepath.Join(root, ".security", "coverage.yaml")
	policyMain := filepath.Join(root, filepath.FromSlash(securityPolicyGo))
	if !regularFile(coverage) || !regularFile(policyMain) {
		return nil, nil
	}

	now := cfg.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	contract, err := securitypolicy.LoadValidatedExceptions(root, now)
	if err != nil {
		return nil, fmt.Errorf("source security: validated exception contract is unavailable: %w", err)
	}
	return &contract, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func runGitleaks(parent context.Context, cfg Config, root, baseRef string) error {
	scanRoot, err := os.MkdirTemp("", "leapview-security-")
	if err != nil {
		return fmt.Errorf("create temporary scan root: %w", err)
	}
	defer os.RemoveAll(scanRoot)
	clean := filepath.Clean(scanRoot)
	if !strings.HasPrefix(clean, "/tmp/") && clean != "/tmp" && !strings.HasPrefix(clean, "/var/tmp/") && clean != "/var/tmp" {
		return fmt.Errorf("refusing unexpected temporary scan root: %s", scanRoot)
	}
	if err := materializeGitView(parent, cfg, root, scanRoot); err != nil {
		return err
	}

	args := []string{"run", "github.com/zricethezav/gitleaks/v8@" + gitleaksVersion, "dir", "--no-banner", "--no-color", "--redact=100", "--timeout=300", scanRoot}
	if err := runCommand(parent, cfg, root, cfg.Stdout, cfg.Stderr, "go", args...); err != nil {
		return commandFailure("gitleaks current-tree scan", err)
	}

	if _, diagnostics, err := runCapture(parent, cfg, root, "git", "rev-parse", "--verify", baseRef); err != nil {
		writeBytes(cfg.Stderr, diagnostics)
		return commandFailure("gitleaks history baseline", err)
	}
	historyArgs := []string{"run", "github.com/zricethezav/gitleaks/v8@" + gitleaksVersion, "git", "--no-banner", "--no-color", "--redact=100", "--timeout=300", "--log-opts=" + baseRef + "..HEAD", "."}
	if err := runCommand(parent, cfg, root, cfg.Stdout, cfg.Stderr, "go", historyArgs...); err != nil {
		return commandFailure("gitleaks candidate-history scan", err)
	}
	return nil
}

func materializeGitView(parent context.Context, cfg Config, root, scanRoot string) error {
	out, diagnostics, err := runCapture(parent, cfg, root, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		writeBytes(cfg.Stderr, diagnostics)
		return commandFailure("list files for gitleaks", err)
	}
	for _, encoded := range bytes.Split(out, []byte{0}) {
		if len(encoded) == 0 {
			continue
		}
		rel := filepath.FromSlash(string(encoded))
		if filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return fmt.Errorf("git returned unsafe path %q", string(encoded))
		}
		source := filepath.Join(root, rel)
		destination := filepath.Join(scanRoot, rel)
		if err := copyGitPath(source, destination); err != nil {
			// tar --ignore-failed-read permits a file removed between ls-files
			// and extraction.  Keep that race harmless while failing all other
			// materialization errors closed.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("materialize %q: %w", string(encoded), err)
		}
	}
	return nil
}

func copyGitPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		_ = os.Remove(destination)
		return os.Symlink(target, destination)
	case info.IsDir():
		return os.MkdirAll(destination, info.Mode().Perm())
	case info.Mode().IsRegular():
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	default:
		return nil
	}
}

func runCapture(parent context.Context, cfg Config, dir, name string, args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	err := runCommand(parent, cfg, dir, &stdout, &stderr, name, args...)
	return stdout.Bytes(), stderr.Bytes(), err
}

func runCommand(parent context.Context, cfg Config, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s: %w", cfg.Timeout, context.DeadlineExceeded)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	return err
}

func commandFailure(label string, err error) error {
	var exitErr *exec.ExitError
	if errors.Is(err, exec.ErrNotFound) || (errors.As(err, &exitErr) && exitErr.ExitCode() == 127) {
		return fmt.Errorf("%s scanner unavailable: %w", label, err)
	}
	return fmt.Errorf("%s failed: %w", label, err)
}

func writeBytes(w io.Writer, data []byte) {
	if len(data) == 0 || w == nil {
		return
	}
	_, _ = w.Write(data)
}
