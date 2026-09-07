package app

import (
	"context"
	"errors"
	"fmt"

	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// withRuntimeHostStartupAdmission charges synchronous runtime reconstruction
// to the control workload class. Runtime construction may discover DuckLake
// snapshot schemas, so it must receive the admitted context rather than the
// process startup context.
func withRuntimeHostStartupAdmission(ctx context.Context, admitter workloadmodule.Admitter, build func(context.Context) error) error {
	if admitter == nil {
		return errors.New("runtime host startup workload admission is unavailable")
	}
	if build == nil {
		return errors.New("runtime host startup builder is unavailable")
	}
	lease, err := admitter.Acquire(ctx, workloadmodule.ControlRequest("runtimehost.startup"))
	if err != nil {
		return fmt.Errorf("admit runtime host startup: %w", err)
	}
	if lease == nil {
		return errors.New("runtime host startup workload admission returned nil lease")
	}
	defer lease.Release()
	return build(lease.Context())
}
