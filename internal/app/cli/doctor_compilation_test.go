package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
)

func TestDoctorCompilationBudgetDoesNotWaitForUncooperativeSourceReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	result := make(chan error, 1)
	go func() {
		_, err := compileDoctorGraph(ctx, func() (projectartifact.SourceBundle, error) {
			close(entered)
			<-release
			close(finished)
			return projectartifact.SourceBundle{}, nil
		})
		result <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("compilation error %v, want context cancellation", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("doctor continued waiting for compilation after its time budget expired")
	}
	// Cleanup releases the source reader even when the regression fails.
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("source reader did not drain")
		}
	})
}
