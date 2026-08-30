package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
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
