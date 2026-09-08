package app

import (
	"context"
	"strings"
	"testing"

	agentmodule "github.com/flidai/leapview/internal/agent/module"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

type nativeDashboardReconcilerStub struct{}

func (nativeDashboardReconcilerStub) Reconcile(context.Context, dashboardPublicationServingStateReader, deployment.Deployment) error {
	return nil
}

func TestProductionRuntimeInputsRequireNativeDurableAuthorities(t *testing.T) {
	production := runtimeAssemblyInputs{Production: true}
	if err := validateProductionRuntimeInputs(dataAssemblyInputs{}, capabilityAssemblyInputs{}, production); err == nil || !strings.Contains(err.Error(), "native dashboard") {
		t.Fatalf("missing dashboard admission error = %v", err)
	}
	data := dataAssemblyInputs{DashboardPersistence: &dashboardmodule.NativePersistence{}, RequireNativeDashboard: true, RequireExplicitAPIProtocol: true}
	if err := validateProductionRuntimeInputs(data, capabilityAssemblyInputs{}, production); err == nil || !strings.Contains(err.Error(), "jobs module") {
		t.Fatalf("missing jobs admission error = %v", err)
	}
	jobs := &jobsmodule.Module{}
	capabilities := capabilityAssemblyInputs{JobModule: jobs}
	if err := validateProductionRuntimeInputs(data, capabilities, production); err == nil || !strings.Contains(err.Error(), "agent persistence") {
		t.Fatalf("missing agent persistence error = %v", err)
	}
	capabilities.AgentPersistence = &agentmodule.Persistence{}
	if err := validateProductionRuntimeInputs(data, capabilities, production); err == nil || !strings.Contains(err.Error(), "refresh persistence") {
		t.Fatalf("missing refresh persistence error = %v", err)
	}
	data.RefreshPersistence = &refreshmodule.Persistence{}
	if err := validateProductionRuntimeInputs(data, capabilities, production); err == nil || !strings.Contains(err.Error(), "delivery target reader") {
		t.Fatalf("missing delivery target reader error = %v", err)
	}
	production.DeliveryTargetReader = bootstrapTargetReaderFake{}
	if err := validateProductionRuntimeInputs(data, capabilities, production); err != nil {
		t.Fatalf("complete native production authorities rejected: %v", err)
	}
}

func TestValidateDashboardAssemblyInputsRejectsPartialNativeAuthorities(t *testing.T) {
	tests := []struct {
		name string
		data dataAssemblyInputs
		want string
	}{
		{
			name: "missing bundle",
			data: dataAssemblyInputs{RequireNativeDashboard: true},
			want: "persistence bundle",
		},
		{
			name: "missing reconciler",
			data: dataAssemblyInputs{RequireNativeDashboard: true, DashboardPersistence: &dashboardmodule.NativePersistence{}},
			want: "publication reconciler",
		},
		{
			name: "forged bundle",
			data: dataAssemblyInputs{RequireNativeDashboard: true, DashboardPersistence: &dashboardmodule.NativePersistence{}, DashboardPublicationReconciler: nativeDashboardReconcilerStub{}},
			want: "authoring application",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDashboardAssemblyInputs(test.data, capabilityAssemblyInputs{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}
