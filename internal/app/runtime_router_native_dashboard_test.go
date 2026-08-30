package app

import (
	"context"
	"database/sql"
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

func TestValidateDashboardAssemblyInputsPreservesLegacyOnlyWhenNativeIsAbsent(t *testing.T) {
	legacy := dataAssemblyInputs{Database: &sql.DB{}, AdminDatabase: &sql.DB{}}
	if dashboardNativeInputsPresent(legacy) {
		t.Fatal("legacy database inputs were classified as native dashboard authorities")
	}
	if err := validateDashboardAssemblyInputs(legacy, capabilityAssemblyInputs{}); err != nil {
		t.Fatalf("explicit legacy-only dashboard inputs were rejected: %v", err)
	}

	native := dataAssemblyInputs{DashboardPublicationReconciler: nativeDashboardReconcilerStub{}}
	if !dashboardNativeInputsPresent(native) {
		t.Fatal("native publication reconciler did not select native dashboard admission")
	}
}

func TestNativePersistenceInputsPresenceIncludesOpaqueCapabilityBundles(t *testing.T) {
	if nativePersistenceInputsPresent(dataAssemblyInputs{}, capabilityAssemblyInputs{}) {
		t.Fatal("empty assembly was classified as native persistence")
	}
	if !nativePersistenceInputsPresent(dataAssemblyInputs{RefreshPersistence: &refreshmodule.Persistence{}}, capabilityAssemblyInputs{}) {
		t.Fatal("refresh persistence did not select native persistence admission")
	}
	if !nativePersistenceInputsPresent(dataAssemblyInputs{}, capabilityAssemblyInputs{AgentPersistence: &agentmodule.Persistence{}}) {
		t.Fatal("agent persistence did not select native persistence admission")
	}
	if nativePersistenceInputsPresent(dataAssemblyInputs{}, capabilityAssemblyInputs{JobModule: &jobsmodule.Module{}}) {
		t.Fatal("opaque jobs module was incorrectly assumed to be native persistence")
	}
}

func TestProductionRuntimeInputsRequireNativeDurableAuthorities(t *testing.T) {
	production := runtimeAssemblyInputs{Production: true}
	if err := validateProductionRuntimeInputs(dataAssemblyInputs{}, capabilityAssemblyInputs{}, production); err == nil || !strings.Contains(err.Error(), "native dashboard") {
		t.Fatalf("missing dashboard admission error = %v", err)
	}
	data := dataAssemblyInputs{DashboardPersistence: &dashboardmodule.NativePersistence{}, RequireExplicitAPIProtocol: true}
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
	if err := validateProductionRuntimeInputs(data, capabilities, production); err != nil {
		t.Fatalf("complete native production authorities rejected: %v", err)
	}
}

func TestNativeRuntimeInputsRejectMixedSQLiteAuthorities(t *testing.T) {
	capabilities := capabilityAssemblyInputs{AgentPersistence: &agentmodule.Persistence{}}
	for _, data := range []dataAssemblyInputs{
		{Database: &sql.DB{}},
		{AdminDatabase: &sql.DB{}},
		{AuditRuntime: &auditRuntime{}},
	} {
		if err := validateProductionRuntimeInputs(data, capabilities, runtimeAssemblyInputs{}); err == nil || !strings.Contains(err.Error(), "rejects SQLite") {
			t.Fatalf("mixed native/SQLite inputs error = %v", err)
		}
	}
}

func TestValidateDashboardAssemblyInputsRejectsPartialOrMixedNativeAuthorities(t *testing.T) {
	tests := []struct {
		name string
		data dataAssemblyInputs
		want string
	}{
		{
			name: "sqlite mixing",
			data: dataAssemblyInputs{Database: &sql.DB{}, DashboardPublicationReconciler: nativeDashboardReconcilerStub{}},
			want: "rejects database/sql",
		},
		{
			name: "legacy audit mixing",
			data: dataAssemblyInputs{AuditRuntime: &auditRuntime{}, DashboardPublicationReconciler: nativeDashboardReconcilerStub{}},
			want: "legacy SQLite audit runtime",
		},
		{
			name: "missing bundle",
			data: dataAssemblyInputs{RequireNativeDashboard: true},
			want: "persistence bundle",
		},
		{
			name: "missing reconciler",
			data: dataAssemblyInputs{DashboardPersistence: &dashboardmodule.NativePersistence{}},
			want: "publication reconciler",
		},
		{
			name: "forged bundle",
			data: dataAssemblyInputs{DashboardPersistence: &dashboardmodule.NativePersistence{}, DashboardPublicationReconciler: nativeDashboardReconcilerStub{}},
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
