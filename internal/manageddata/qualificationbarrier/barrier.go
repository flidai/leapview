// Package qualificationbarrier contains the opt-in filesystem coordination
// used by the managed-data qualification recovery harness. The package is
// deliberately inert unless all of its qualification-only environment
// settings are present and identify the evaluation project exactly.
package qualificationbarrier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ArmedMarker is created by the qualification harness. A sync process
	// atomically renames it to ReachedMarker when the first partial TUS PATCH
	// has completed.
	ArmedMarker = ".qualification-managed-upload-barrier.armed"

	// ReachedMarker is held until the harness removes it, releasing the sync
	// process to continue. Its removal is the one-shot transition: later
	// chunks do not pause again because ArmedMarker has been consumed.
	ReachedMarker = ".qualification-managed-upload-barrier.reached"

	// EnabledEnv explicitly opts a single qualification command into the
	// barrier. Any value other than EnabledValue leaves the barrier inert.
	EnabledEnv   = "LEAPVIEW_QUALIFICATION_MANAGED_UPLOAD_BARRIER"
	EnabledValue = "1"

	// PathEnv identifies the writable directory containing ArmedMarker and
	// ReachedMarker. It is intentionally supplied per command rather than as
	// a process-wide default.
	PathEnv = "LEAPVIEW_QUALIFICATION_MANAGED_UPLOAD_BARRIER_PATH"

	// ProjectIDEnv prevents an accidentally inherited opt-in from pausing
	// uploads for a project other than the exact evaluation project.
	ProjectIDEnv = "LEAPVIEW_QUALIFICATION_MANAGED_UPLOAD_BARRIER_PROJECT_ID"

	// EvaluationProjectID is the only project for which this qualification
	// barrier may be active.
	EvaluationProjectID = "project:leapview-evaluation"
)

// WaitAfterPartialTusPatch performs the one-shot armed-to-reached transition
// and, once reached, waits for the harness to remove ReachedMarker.  It is a
// no-op unless the command explicitly opts in with EnabledEnv, supplies a
// writable PathEnv, and both the command project and ProjectIDEnv identify
// EvaluationProjectID exactly.
func WaitAfterPartialTusPatch(ctx context.Context, projectID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if os.Getenv(EnabledEnv) != EnabledValue {
		return nil
	}
	path := strings.TrimSpace(os.Getenv(PathEnv))
	configuredProject := strings.TrimSpace(os.Getenv(ProjectIDEnv))
	if path == "" || configuredProject != EvaluationProjectID ||
		strings.TrimSpace(projectID) != EvaluationProjectID || configuredProject != strings.TrimSpace(projectID) {
		return nil
	}
	armed := filepath.Join(path, ArmedMarker)
	reached := filepath.Join(path, ReachedMarker)
	if _, err := os.Stat(reached); err == nil {
		return fmt.Errorf("managed upload qualification barrier reached marker already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed upload qualification barrier reached marker: %w", err)
	}
	// Rename is atomic on one filesystem.  Exactly one concurrent uploader can
	// consume ArmedMarker; all subsequent invocations observe it as absent and
	// therefore proceed without pausing again.
	if err := os.Rename(armed, reached); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("claim managed upload qualification barrier: %w", err)
	}
	for {
		if _, err := os.Stat(reached); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("check managed upload qualification barrier: %w", err)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for managed upload qualification barrier release: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
