package composectl

import (
	"testing"
	"time"
)

func TestRecoveryQualificationTransitionPhaseBoundariesExcludeUnrelatedDelays(t *testing.T) {
	base := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	now := base
	type observation struct {
		operation string
		event     string
		at        time.Time
	}
	var observations []observation
	controller, err := New(Options{
		Root: t.TempDir(), Now: func() time.Time { return now },
		qualificationPhase: func(operation, event string) error {
			observations = append(observations, observation{operation: operation, event: event, at: now})
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(11 * time.Second) // preparation before upgrade
	if err := controller.runQualificationReadiness("upgrade", func() error {
		now = now.Add(5 * time.Second) // upgrade readiness
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(13 * time.Second) // checks between transitions
	if err := controller.runQualificationReadiness("rollback", func() error {
		now = now.Add(7 * time.Second) // rollback readiness
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(17 * time.Second) // post-rollback evidence work

	if len(observations) != 4 {
		t.Fatalf("phase observations = %#v", observations)
	}
	if got := observations[1].at.Sub(observations[0].at); got != 5*time.Second {
		t.Fatalf("upgrade readiness = %s, want 5s", got)
	}
	if got := observations[3].at.Sub(observations[2].at); got != 7*time.Second {
		t.Fatalf("rollback readiness = %s, want 7s", got)
	}
	if total := now.Sub(base); total != 53*time.Second {
		t.Fatalf("total qualification runtime = %s, want 53s", total)
	}
}
