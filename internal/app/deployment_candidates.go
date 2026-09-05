package app

import (
	"context"
	"fmt"
	"net/http"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

type runtimeReloader interface {
	PrepareServingState(ctx context.Context, servingStateID string) (*runtimehost.Prepared, error)
	ActivatePrepared(prepared *runtimehost.Prepared, activate func() error) error
}

type servingStateRepository interface {
	refreshrun.ServingStateReader
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
