package app

import (
	"context"
	"database/sql"

	"github.com/flidai/leapview/internal/deployment"
)

import dashboardpublication "github.com/flidai/leapview/internal/app/dashboardpublication"

// dashboardPublicationServingStateReader and dashboardPublicationActivationReconciler
// are aliases to the composition-owned contracts used by runtime routing.
type dashboardPublicationServingStateReader = dashboardpublication.ServingStateReader
type dashboardPublicationActivationReconciler = dashboardpublication.ActivationReconciler

type NativeDashboardPublicationTxBeginner = dashboardpublication.NativeDashboardPublicationTxBeginner
type NativeDashboardPublicationGenerationFence = dashboardpublication.NativeDashboardPublicationGenerationFence
type NativeDashboardPublicationActivationConfig = dashboardpublication.NativeDashboardPublicationActivationConfig
type NativeDashboardPublicationReconciler = dashboardpublication.NativeDashboardPublicationReconciler

func NewNativeDashboardPublicationReconciler(config NativeDashboardPublicationActivationConfig) (*NativeDashboardPublicationReconciler, error) {
	return dashboardpublication.NewNativeDashboardPublicationReconciler(config)
}

type sqliteDashboardPublicationReconciler struct {
	database *sql.DB
}

func newSQLiteDashboardPublicationReconciler(database *sql.DB) dashboardPublicationActivationReconciler {
	if database == nil {
		return nil
	}
	return &sqliteDashboardPublicationReconciler{database: database}
}

func (r *sqliteDashboardPublicationReconciler) Reconcile(ctx context.Context, states dashboardPublicationServingStateReader, activated deployment.Deployment) error {
	return reconcileActivatedDashboardPublications(ctx, r.database, states, activated)
}
