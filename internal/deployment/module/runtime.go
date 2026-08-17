package module

import (
	"context"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/runtimehost"
)

type RuntimeRegistry interface {
	PrepareServingStateCandidate(context.Context, runtimehost.ServingStateCandidate) (*runtimehost.Prepared, error)
	VerifyPrepared(context.Context, *runtimehost.Prepared) (runtimehost.PreparedVerification, error)
	ActivatePrepared(*runtimehost.Prepared, func() error) error
}

func NewRuntime(registry RuntimeRegistry) (deployment.Runtime, error) {
	return deployment.NewRegistryRuntime(registry)
}
