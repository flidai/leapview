// Package qualificationbarrier defines the inert evaluation hook shared by
// the native activation boundary and the external qualification client.
package qualificationbarrier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ArmedMarker asks an evaluation deployment to pause immediately before its
	// target activation compare-and-swap.
	ArmedMarker = ".qualification-activation-barrier.armed"

	// ReachedMarker proves that an armed evaluation deployment reached the
	// activation boundary and consumed ArmedMarker.
	ReachedMarker = ".qualification-activation-barrier.reached"
)

// WaitBeforeActivation consumes an explicitly armed marker and pauses the
// caller immediately before the native target activation compare-and-swap. It
// is inert outside the evaluation environment and when LEAPVIEW_HOME is
// unset, so it cannot pause a normal production target.
func WaitBeforeActivation(ctx context.Context, environment string) error {
	if strings.TrimSpace(environment) != "evaluation" {
		return nil
	}
	home := strings.TrimSpace(os.Getenv("LEAPVIEW_HOME"))
	if home == "" {
		return nil
	}
	armed := filepath.Join(home, ArmedMarker)
	reached := filepath.Join(home, ReachedMarker)
	if _, err := os.Stat(armed); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect qualification activation barrier: %w", err)
	}
	// Fail closed rather than replacing evidence from a previous run. Rename
	// makes the one-shot armed -> reached transition atomic to observers.
	if _, err := os.Stat(reached); err == nil {
		return fmt.Errorf("qualification activation barrier reached marker already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect qualification activation barrier reached marker: %w", err)
	}
	if err := os.Rename(armed, reached); err != nil {
		return fmt.Errorf("consume qualification activation barrier: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	<-ctx.Done()
	return ctx.Err()
}
