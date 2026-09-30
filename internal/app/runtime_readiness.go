package app

import (
	"context"
	"errors"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

type runtimeReadinessSource interface {
	ActiveArtifact(context.Context) (servingstate.State, servingstate.Artifact, error)
	Acquire(context.Context) (runtimehost.Lease, error)
}

func checkActiveRuntimeIdentity(ctx context.Context, source runtimeReadinessSource) error {
	if ctx == nil || source == nil {
		return errors.New("runtime readiness source is unavailable")
	}
	state, _, err := source.ActiveArtifact(ctx)
	if err != nil {
		if errors.Is(err, servingstate.ErrNotFound) {
			return errNoActiveDeployment
		}
		return err
	}
	want := projectgraph.ServingIdentity{ProjectID: state.ProjectID, Environment: string(state.Environment), GenerationID: string(state.ID)}
	if err := want.Validate(); err != nil {
		return fmt.Errorf("durable active serving identity is invalid: %w", err)
	}
	lease, err := source.Acquire(ctx)
	if err != nil {
		return err
	}
	if lease == nil {
		return errors.New("runtime host returned a nil lease")
	}
	defer lease.Release()
	if got := lease.Identity(); got != want {
		return errors.New("runtime host lease does not match the durable active serving identity")
	}
	return nil
}
