package app

import (
	"database/sql"
	"fmt"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// SavedExplorationServiceOptions contains the complete process-composition
// boundary for the saved-exploration application service. The service must
// use the same runtime provider and workload admission controller as the rest
// of the application, while persistence and audit remain platform-owned.
type SavedExplorationServiceOptions struct {
	Database            *sql.DB
	AuditIntentRecorder accessmodule.AuditIntentRecorder
	AccessModule        SavedExplorationAccessModule
	Runtime             runtimehostmodule.Provider
	Admitter            workloadmodule.Admitter
	AuditRecorder       accessmodule.CanonicalAuditRecorder
}

// NewSavedExplorationService wires the process-owned cross-capability ports
// and delegates durable service construction to analytics/module.
func NewSavedExplorationService(options SavedExplorationServiceOptions) (analyticsmodule.SavedExplorationService, error) {
	adapters, err := NewSavedExplorationAdapters(SavedExplorationExecutorOptions{
		AccessModule:  options.AccessModule,
		Admitter:      options.Admitter,
		AuditRecorder: options.AuditRecorder,
	})
	if err != nil {
		return nil, fmt.Errorf("build saved exploration adapters: %w", err)
	}
	return analyticsmodule.BuildSavedExplorationService(analyticsmodule.SavedExplorationServiceOptions{
		Database:            options.Database,
		AuditIntentRecorder: options.AuditIntentRecorder,
		Authorizer:          adapters.Authorizer,
		Runtime:             options.Runtime,
		Executor:            adapters.Executor,
	})
}
