package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/brand"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

type savedExplorationWiringInputs struct {
	accessModule            *accessmodule.Module
	auth                    *accessmodule.Auth
	assets                  staticasset.Resolver
	resolveProjectID        func(context.Context) (projectgraph.ResourceID, error)
	instanceID              string
	publicURL               string
	runtime                 runtimehostmodule.Provider
	admitter                workloadmodule.Admitter
	analyticsModule         *analyticsmodule.Module
	savedExplorationService analyticsmodule.SavedExplorationService
	projectBrowser          *projecthttp.BrowserHandler
	ctx                     context.Context
	database                *sql.DB
	auditIntentRecorder     access.AuditIntentRecorder
	accessRepo              access.Repository
}

type savedExplorationWiringResult struct {
	accessModule            *accessmodule.Module
	savedExplorationService analyticsmodule.SavedExplorationService
}

func configureSavedExploration(inputs savedExplorationWiringInputs) (savedExplorationWiringResult, error) {
	ctx := inputs.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	accessModule := inputs.accessModule
	if accessModule == nil {
		var err error
		accessModule, err = accessmodule.Build(ctx, accessmodule.Config{
			Database: inputs.database, ExistingAuth: inputs.auth,
			InstanceID: inputs.instanceID, PublicURL: inputs.publicURL,
			CurrentProjectID: inputs.resolveProjectID,
			Presentation:     webpage.Presentation{ProductName: brand.Name, FaviconPath: brand.FaviconPath}, Assets: inputs.assets,
		})
		if err != nil {
			return savedExplorationWiringResult{}, fmt.Errorf("build access module: %w", err)
		}
	}
	savedService := inputs.savedExplorationService
	if inputs.database != nil {
		canonicalAuditRecorder, _ := inputs.accessRepo.(access.CanonicalAuditRecorder)
		var err error
		savedService, err = NewSavedExplorationService(SavedExplorationServiceOptions{
			Database: inputs.database, AuditIntentRecorder: inputs.auditIntentRecorder,
			AccessModule: accessModule, Runtime: inputs.runtime,
			Admitter: inputs.admitter, AuditRecorder: canonicalAuditRecorder,
		})
		if err != nil {
			return savedExplorationWiringResult{}, fmt.Errorf("build saved exploration service: %w", err)
		}
	}
	// Bind the browser only after the complete saved-exploration service has
	// been constructed. The analytics-owned UI adapter runs generated
	// Begin/Execute and CAS attestation around the browser's service callback;
	// leaving any of these fields unset would render an apparently enabled UI
	// that cannot complete a durable command.
	if inputs.projectBrowser != nil && savedService != nil && inputs.analyticsModule != nil {
		inputs.projectBrowser.SavedExplorations = savedService
		bindings := inputs.analyticsModule.SavedExplorationUICommandBindings()
		inputs.projectBrowser.SavedExplorationCommands = projecthttp.SavedExplorationCommandBindings{
			Create: bindings.Create, Update: bindings.Update, Duplicate: bindings.Duplicate, Archive: bindings.Archive,
		}
		inputs.projectBrowser.BeginSavedExplorationCommand = func(ctx context.Context, invocation projecthttp.SavedExplorationCommandInvocation) (context.Context, error) {
			return inputs.analyticsModule.BeginSavedExplorationUICommand(ctx, analyticsmodule.SavedExplorationUICommandInvocation{
				Action: invocation.Action, Project: invocation.Project, Resource: invocation.Resource,
				IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID,
				CorrelationID: invocation.CorrelationID, Revision: invocation.Revision, ConcurrencyRevision: invocation.ConcurrencyRevision,
			})
		}
		inputs.projectBrowser.ExecuteSavedExplorationCommand = func(ctx context.Context, invocation projecthttp.SavedExplorationCommandInvocation, transaction func(context.Context) error) error {
			return inputs.analyticsModule.ExecuteSavedExplorationUICommand(ctx, analyticsmodule.SavedExplorationUICommandInvocation{
				Action: invocation.Action, Project: invocation.Project, Resource: invocation.Resource,
				IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID,
				CorrelationID: invocation.CorrelationID, Revision: invocation.Revision, ConcurrencyRevision: invocation.ConcurrencyRevision,
			}, transaction)
		}
	}
	return savedExplorationWiringResult{accessModule: accessModule, savedExplorationService: savedService}, nil
}

func savedExplorationAPIGenConfig(service analyticsmodule.SavedExplorationService, accessModule *accessmodule.Module, auth *accessmodule.Auth) analyticsmodule.SavedExplorationAPIGenConfig {
	return analyticsmodule.SavedExplorationAPIGenConfig{
		Service: service,
		CurrentPrincipal: func(r *http.Request) (string, bool) {
			if accessModule == nil {
				return "", false
			}
			principal, ok := accessModule.CurrentPrincipal(r)
			if !ok && auth != nil {
				principal, _, ok = auth.Authenticate(r)
			}
			return principal.ID, ok
		},
		ReplayContext: func(ctx context.Context, r *http.Request, actor string) (context.Context, bool) {
			if auth != nil {
				principal, credential, ok := auth.Authenticate(r)
				if !ok || principal.ID != actor {
					return ctx, false
				}
				ctx = accessmodule.WithPrincipal(ctx, principal)
				if credential != nil {
					ctx = accessmodule.WithAPICredential(ctx, *credential)
				}
				return ctx, true
			}
			if accessModule == nil {
				return ctx, false
			}
			principal, ok := accessModule.CurrentPrincipal(r)
			if !ok || principal.ID != actor {
				return ctx, false
			}
			return accessmodule.WithPrincipal(ctx, principal), true
		},
	}
}
