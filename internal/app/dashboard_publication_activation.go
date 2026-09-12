package app

import dashboardpublication "github.com/flidai/leapview/internal/app/dashboardpublication"

// dashboardPublicationServingStateReader and dashboardPublicationActivationReconciler
// are aliases to the composition-owned contracts used by runtime routing.
type dashboardPublicationServingStateReader = dashboardpublication.ServingStateReader
type dashboardPublicationActivationReconciler = dashboardpublication.ActivationReconciler

type NativeDashboardPublicationReconciler = dashboardpublication.NativeDashboardPublicationReconciler

func NewNativeDashboardPublicationReconciler() *NativeDashboardPublicationReconciler {
	return dashboardpublication.NewNativeDashboardPublicationReconciler()
}
