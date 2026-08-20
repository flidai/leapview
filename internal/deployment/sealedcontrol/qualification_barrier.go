package sealedcontrol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// QualificationActivationBarrierArmedMarker is a one-shot, explicit test
// marker. It is intentionally inert unless a qualification harness copies it
// into LEAPVIEW_HOME.
const QualificationActivationBarrierArmedMarker = ".qualification-activation-barrier.armed"

// QualificationActivationBarrierReachedMarker is created by consuming the
// armed marker. Its presence tells the qualification harness that publication
// has passed durable pending/approval/seal checks and is waiting immediately
// before the target activation CAS.
const QualificationActivationBarrierReachedMarker = ".qualification-activation-barrier.reached"

// QualificationActivationBarrier consumes an explicitly armed marker and
// pauses the caller before activation. It is enabled only for the exact
// evaluation environment; a marker can therefore never pause a normal
// production target, even if one is accidentally copied into its home.
// Production is unchanged when LEAPVIEW_HOME is unset or the armed marker is
// absent. Rename is used for the one-shot transition so observers cannot see a
// reached marker while the armed marker remains available for another
// activation attempt.
func QualificationActivationBarrier(ctx context.Context, environment string) error {
	if strings.TrimSpace(environment) != "evaluation" {
		return nil
	}
	home := strings.TrimSpace(os.Getenv("LEAPVIEW_HOME"))
	if home == "" {
		return nil
	}
	armed := filepath.Join(home, QualificationActivationBarrierArmedMarker)
	reached := filepath.Join(home, QualificationActivationBarrierReachedMarker)
	if _, err := os.Stat(armed); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect qualification activation barrier: %w", err)
	}
	// A stale reached marker would make the harness observe the next run before
	// this activation consumed its fresh arm. The harness removes it before
	// arming; fail closed if it is still present rather than silently replacing
	// evidence from a previous run.
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
