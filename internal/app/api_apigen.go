package app

import (
	"net/http"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

func agentAPIGenOperations() []agentmodule.APIGenOperation {
	generated := apiaggregate.GetAPIGenOperationContracts()
	contracts := make(map[string]agentmodule.APIGenOperationContract, len(generated))
	for operationID, contract := range generated {
		contracts[operationID] = agentmodule.APIGenOperationContract{
			OperationID: contract.OperationID, Method: contract.Method, Path: contract.Path,
			Protected: contract.Protected, AuthzMode: contract.AuthzMode, Manual: contract.Manual,
			Extensions: contract.Extensions,
		}
	}
	return agentmodule.BuildAPIGenOperations(contracts, apiaggregate.GetAPIGenToolContracts())
}

func accessAPIGenOperationContracts() map[string]accessmodule.APIGenOperationContract {
	generated := apiaggregate.GetAPIGenOperationContracts()
	contracts := make(map[string]accessmodule.APIGenOperationContract, len(generated))
	for operationID, contract := range generated {
		var command *accessmodule.APIGenCommandContract
		if contract.Command != nil {
			command = &accessmodule.APIGenCommandContract{
				AuthzMode: contract.Command.AuthzMode,
				Privilege: contract.Command.Privilege,
			}
		}
		contracts[operationID] = accessmodule.APIGenOperationContract{
			OperationID: contract.OperationID, Method: contract.Method, Path: contract.Path, Protected: contract.Protected,
			AuthzMode: contract.AuthzMode, Command: command, Extensions: contract.Extensions,
		}
	}
	return contracts
}

type apiGenDispatcher struct {
	managedDataModule  *manageddatamodule.Module
	defaultEnvironment string
	instanceID         string
	canonicalOrigin    string
	buildIdentity      buildinfo.Identity
	managedDataTus     http.Handler
	arrowQueries       bool
}

func (a apiGenDispatcher) GetInstance(w http.ResponseWriter, _ *http.Request) {
	apitransport.WriteJSON(w, http.StatusOK, apigenapi.InstanceResponse{
		Id: a.instanceID, CanonicalOrigin: a.canonicalOrigin, Environment: a.defaultEnvironment,
	})
}
