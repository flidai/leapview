package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (r *runner) requireTrackedJavaScriptEvidence() error {
	path := filepath.Join(r.root, javascriptEvidenceRelativePath)
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect checked-in JavaScript vulnerability evidence: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("checked-in JavaScript vulnerability evidence must be a regular file")
	}
	if _, err := os.Lstat(filepath.Join(r.root, ".git")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect git checkout: %w", err)
	}
	result := r.command(r.root, "git", "ls-files", "--error-unmatch", "--", javascriptEvidenceRelativePath)
	if isCommandLifecycleError(result) {
		return fmt.Errorf("verify checked-in JavaScript vulnerability evidence tracking: %w", commandError("git ls-files", r.root, result))
	}
	if result.status != 0 {
		return fmt.Errorf("%s must be tracked in the repository", javascriptEvidenceRelativePath)
	}
	if result.err != nil {
		return fmt.Errorf("verify checked-in JavaScript vulnerability evidence tracking: %w", result.err)
	}
	if len(bytes.TrimSpace(result.stderr)) != 0 {
		return errors.New("verify checked-in JavaScript vulnerability evidence tracking: git emitted diagnostics on stderr")
	}
	return nil
}
