package app

import (
	"net/http"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
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
				Owner:       contract.Command.Owner,
				AuthzMode:   contract.Command.AuthzMode,
				Privilege:   contract.Command.Privilege,
				Idempotency: contract.Command.Idempotency,
				Concurrency: contract.Command.Concurrency,
			}
			if contract.Command.Target != nil {
				command.Target = &accessmodule.APIGenCommandTarget{Parameter: contract.Command.Target.Parameter, Type: contract.Command.Target.Type}
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
	productAPI         *adminmodule.Module
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

func (a apiGenDispatcher) GetProductSettings(w http.ResponseWriter, r *http.Request) {
	a.productAPI.GetProductSettings(w, r)
}

func (a apiGenDispatcher) UpdateProductSettings(w http.ResponseWriter, r *http.Request, headers apigenapi.GenUpdateProductSettingsHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	a.productAPI.UpdateProductSettings(w, r)
}

func (a apiGenDispatcher) UploadProductLogo(w http.ResponseWriter, r *http.Request, headers apigenapi.GenUploadProductLogoHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	r.Header.Set("Content-Type", headers.ContentType)
	a.productAPI.UploadProductLogo(w, r)
}

func (a apiGenDispatcher) DeleteProductLogo(w http.ResponseWriter, r *http.Request, headers apigenapi.GenDeleteProductLogoHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	a.productAPI.DeleteProductLogo(w, r)
}

func (a apiGenDispatcher) GetProductLogo(w http.ResponseWriter, r *http.Request, _ string) {
	a.productAPI.GetProductLogo(w, r)
}

func (a apiGenDispatcher) GetProductAuthenticationStatus(w http.ResponseWriter, r *http.Request) {
	a.productAPI.GetProductAuthenticationStatus(w, r)
}

func (a apiGenDispatcher) GetProductSystemStatus(w http.ResponseWriter, r *http.Request) {
	a.productAPI.GetProductSystemStatus(w, r)
}

func (a apiGenDispatcher) GetProductAPIStatus(w http.ResponseWriter, r *http.Request) {
	a.productAPI.GetProductAPIStatus(w, r)
}
