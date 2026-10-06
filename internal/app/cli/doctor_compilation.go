package cli

import (
	"context"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
)

func compileDoctorGraph(ctx context.Context, compile func() (projectartifact.SourceBundle, error)) (projectartifact.SourceBundle, error) {
	if err := ctx.Err(); err != nil {
		return projectartifact.SourceBundle{}, err
	}
	type result struct {
		bundle projectartifact.SourceBundle
		err    error
	}
	compiled := make(chan result, 1)
	// ponytail: the compiler has no cancellation hook. Stop waiting at the
	// deadline; use compiler context cancellation when that hook exists.
	go func() {
		bundle, err := compile()
		compiled <- result{bundle: bundle, err: err}
	}()
	select {
	case <-ctx.Done():
		return projectartifact.SourceBundle{}, ctx.Err()
	case completed := <-compiled:
		if err := ctx.Err(); err != nil {
			return projectartifact.SourceBundle{}, err
		}
		return completed.bundle, completed.err
	}
}
