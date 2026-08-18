package deployment

import (
	"errors"
	"testing"
)

func TestValidateDeliveryStartupAllowsAdministrableFreshTargetOutsideProduction(t *testing.T) {
	if err := ValidateDeliveryStartup(DeliveryStartupState{TargetID: "target", Production: false}); err != nil {
		t.Fatalf("development startup = %v, want allowed", err)
	}
}

func TestValidateDeliveryStartupReportsMissingAdmissionAndLegacyIdentity(t *testing.T) {
	err := ValidateDeliveryStartup(DeliveryStartupState{
		Production:                   true,
		TargetID:                     "target",
		ProjectID:                    "project",
		Environment:                  "prod",
		TargetRevisionExists:         true,
		MigratedRowsWithoutServingID: 2,
		ActiveServingGeneration:      true,
	})
	if !errors.Is(err, ErrDeliveryStartupNotReady) {
		t.Fatalf("startup error = %v, want not-ready", err)
	}
	diagnostics := DeliveryStartupDiagnosticsOf(err)
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, want missing pool and legacy identity", diagnostics)
	}
	if diagnostics[0].Code != DeliveryStartupMissingPoolAdmission || diagnostics[1].Code != DeliveryStartupLegacyServingIdentity {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if err.Error() != "delivery startup is not ready: missing_physical_pool_admission,migrated_serving_state_identity_missing" {
		t.Fatalf("stable diagnostic error = %q", err)
	}
}

func TestValidateDeliveryStartupRejectsMixedServingPathsAndUnadmittedPool(t *testing.T) {
	err := ValidateDeliveryStartup(DeliveryStartupState{
		Production:               true,
		TargetID:                 "target",
		ConfiguredPhysicalPoolID: "pool",
		PhysicalPoolExists:       true,
		PhysicalPoolAdmitted:     false,
		TargetRevisionExists:     true,
		LegacyServingPathEnabled: true,
	})
	if !errors.Is(err, ErrDeliveryStartupNotReady) {
		t.Fatalf("startup error = %v, want not-ready", err)
	}
	diagnostics := DeliveryStartupDiagnosticsOf(err)
	if len(diagnostics) != 2 || diagnostics[0].Code != DeliveryStartupUnadmittedPool || diagnostics[1].Code != DeliveryStartupMixedServingPaths {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestValidateDeliveryStartupRequiresTargetRevision(t *testing.T) {
	err := ValidateDeliveryStartup(DeliveryStartupState{
		Production:               true,
		TargetID:                 "target",
		ConfiguredPhysicalPoolID: "pool",
		PhysicalPoolExists:       true,
		PhysicalPoolAdmitted:     true,
	})
	if !errors.Is(err, ErrDeliveryStartupNotReady) {
		t.Fatalf("startup error = %v, want not-ready", err)
	}
	diagnostics := DeliveryStartupDiagnosticsOf(err)
	if len(diagnostics) != 1 || diagnostics[0].Code != DeliveryStartupMissingTargetRevision {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestValidateDeliveryStartupBlocksMissingAndIndeterminateRecoveryState(t *testing.T) {
	base := DeliveryStartupState{
		Production:               true,
		TargetID:                 "target",
		ConfiguredPhysicalPoolID: "pool",
		PhysicalPoolExists:       true,
		PhysicalPoolAdmitted:     true,
		TargetRevisionExists:     true,
	}

	missing := base
	missing.TargetRevisionExists = false
	err := ValidateDeliveryStartup(missing)
	if !errors.Is(err, ErrDeliveryStartupNotReady) {
		t.Fatalf("missing control state error = %v, want not-ready", err)
	}
	if diagnostics := DeliveryStartupDiagnosticsOf(err); len(diagnostics) != 1 || diagnostics[0].Code != DeliveryStartupMissingTargetRevision {
		t.Fatalf("missing control state diagnostics = %#v", diagnostics)
	}

	indeterminate := base
	indeterminate.IndeterminatePublication = true
	err = ValidateDeliveryStartup(indeterminate)
	if !errors.Is(err, ErrDeliveryStartupNotReady) {
		t.Fatalf("indeterminate control state error = %v, want not-ready", err)
	}
	if diagnostics := DeliveryStartupDiagnosticsOf(err); len(diagnostics) != 1 || diagnostics[0].Code != DeliveryStartupIndeterminatePublication {
		t.Fatalf("indeterminate control state diagnostics = %#v", diagnostics)
	}
}
