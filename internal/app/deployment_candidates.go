package app

import (
	"context"
	"fmt"
	"net/http"

	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

type runtimeReloader interface {
	PrepareServingState(ctx context.Context, servingStateID string) (servingstatemodule.PreparedRuntime, error)
	ActivatePrepared(prepared servingstatemodule.PreparedRuntime, activate func() error) error
}

type servingStateRepository interface {
	refreshmodule.ServingStateRepository
	ListActiveScopes(context.Context) ([]servingstatemodule.ActiveScope, error)
}

func resolveServingStateRepository(persistence persistenceInputs) (servingStateRepository, error) {
	if persistence.servingStateRepo != nil {
		return persistence.servingStateRepo, nil
	}
	return nil, fmt.Errorf("serving state repository is not configured")
}

func defaultServingEnvironment(environment string) servingstatemodule.Environment {
	return servingstatemodule.NormalizeEnvironment(servingstatemodule.Environment(environment))
}

func requestServingEnvironment(environment string, _ *http.Request) servingstatemodule.Environment {
	return defaultServingEnvironment(environment)
}
